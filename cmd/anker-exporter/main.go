// Command anker-exporter exposes Anker SOLIX C1000 (A1761) telemetry as Prometheus metrics.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/collector"
	"github.com/lukaszraczylo/anker-exporter/internal/config"
	"github.com/lukaszraczylo/anker-exporter/internal/frame"
	"github.com/lukaszraczylo/anker-exporter/internal/mqttsession"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	hexPrefixBytes    = 16
)

var errHelp = errors.New("help requested")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout)
	stop()
	switch {
	case err == nil, errors.Is(err, errHelp):
	default:
		slog.New(slog.NewJSONHandler(os.Stdout, nil)).Error("exporter stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("anker-exporter", flag.ContinueOnError)
	listen := fs.String("listen", ":9876", "HTTP listen address")
	trigger := fs.Duration("trigger", 300*time.Second, "realtime trigger timeout; the exporter resends it before it expires")
	stale := fs.Duration("stale-after", 30*time.Second, "anker_c1000_up goes 0 when no telemetry frame arrived for this long")
	printMetrics := fs.Bool("print-metrics", false, "print the metric names and help text, then exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelp
		}
		return err
	}
	if *printMetrics {
		for _, line := range collector.New("SN", *stale, time.Now).MetricList() {
			_, _ = fmt.Fprintln(stdout, line)
		}
		return nil
	}
	if err := validateTrigger(*trigger); err != nil {
		return err
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return serve(ctx, log, *listen, reg, func(ctx context.Context) error {
		return work(ctx, log, cfg, reg, *trigger, *stale)
	})
}

// validateTrigger applies the bounds frame.BuildTrigger enforces, before the slow login.
func validateTrigger(d time.Duration) error {
	if _, err := frame.BuildTrigger("sn", "uid", "cid", int(d/time.Second), time.Now()); err != nil {
		return fmt.Errorf("invalid -trigger %v: %w", d, err)
	}
	return nil
}

// serve runs the HTTP endpoints and the worker. It returns the worker error, so the process exits
// non-zero, unless the worker ended because ctx was cancelled.
func serve(ctx context.Context, log *slog.Logger, listen string, reg *prometheus.Registry, worker func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := &http.Server{Addr: listen, Handler: newMux(reg), ReadHeaderTimeout: readHeaderTimeout}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()
	log.Info("listening", "addr", listen)

	workerDone := make(chan error, 1)
	go func() { workerDone <- worker(ctx) }()

	var result error
	select {
	case err := <-srvErr:
		cancel()
		<-workerDone
		return fmt.Errorf("http server: %w", err)
	case err := <-workerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			result = fmt.Errorf("worker: %w", err)
		}
	case <-ctx.Done():
		<-workerDone
	}
	log.Info("shutting down")
	shutCtx, stopShut := context.WithTimeout(context.Background(), shutdownTimeout)
	defer stopShut()
	if err := srv.Shutdown(shutCtx); err != nil {
		return errors.Join(result, fmt.Errorf("shutdown: %w", err))
	}
	return result
}

func newMux(reg *prometheus.Registry) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// work logs in, then runs the MQTT runner until ctx ends. Setup failures back off, so a slow or
// rejecting cloud API never takes the HTTP endpoint away.
func work(ctx context.Context, log *slog.Logger, cfg config.Config, reg prometheus.Registerer, trigger, stale time.Duration) error {
	cl, err := anker.NewClient(cfg.BaseURL, cfg.Email, cfg.Password, cfg.Country, nil)
	if err != nil {
		return err
	}
	sn, err := acquire(ctx, log, func(ctx context.Context) (string, error) { return setup(ctx, cl, cfg) }, defaultBackoff, sleepCtx)
	if err != nil {
		return err
	}
	log.Info("device selected", "serial", config.MaskSN(sn))

	col := collector.New(sn, stale, time.Now)
	if err = col.Register(reg); err != nil {
		return err
	}
	r := mqttsession.NewRunner(cl, sn, trigger, col.Update)
	r.OnBadFrame = func(err error) {
		col.CountBadFrame()
		log.Warn("bad frame", append([]any{"err", err}, badFrameAttrs(err)...)...)
	}
	r.OnConnect = func() { log.Info("mqtt subscribed") }
	r.OnError = func(err error) { log.Warn("mqtt session error", "err", err) }
	return r.Run(ctx)
}

// badFrameAttrs describes a frame that failed to decode: no field values, only framing facts.
func badFrameAttrs(err error) []any {
	var fe *frame.FrameError
	if !errors.As(err, &fe) {
		return nil
	}
	raw := fe.Raw
	attrs := []any{"payload_len", len(raw), "hex_prefix", hex.EncodeToString(raw[:min(len(raw), hexPrefixBytes)])}
	if len(raw) >= 4 {
		attrs = append(attrs, "length_field", binary.LittleEndian.Uint16(raw[2:4]))
	}
	if len(raw) >= 9 {
		attrs = append(attrs, "msg_type", hex.EncodeToString(raw[7:9]))
	}
	return attrs
}

func setup(ctx context.Context, cl *anker.Client, cfg config.Config) (string, error) {
	if err := cl.Login(ctx); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	devs, err := cl.Devices(ctx)
	if err != nil {
		// %v drops the chain: only a login rejection may count towards the lockout cap.
		return "", fmt.Errorf("devices: %v", err) //nolint:errorlint // deliberate
	}
	sn, err := pickDevice(devs, cfg.DeviceSN)
	if err != nil {
		return "", fatalError{err}
	}
	return sn, nil
}

func pickDevice(devs []anker.Device, want string) (string, error) {
	if want != "" {
		for _, d := range devs {
			if d.SN == want {
				return d.SN, nil
			}
		}
		return "", fmt.Errorf("ANKER_DEVICE_SN %s is not an A1761 on the account", config.MaskSN(want))
	}
	switch len(devs) {
	case 0:
		return "", errors.New("no A1761 device on the account")
	case 1:
		return devs[0].SN, nil
	default:
		return "", fmt.Errorf("%d A1761 devices on the account, set ANKER_DEVICE_SN", len(devs))
	}
}
