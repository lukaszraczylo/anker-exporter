// Command anker-exporter exposes Anker SOLIX C1000 (A1761) telemetry as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/lukaszraczylo/anker-exporter/internal/mqttsession"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 10 * time.Second
	setupRetryDelay   = 30 * time.Second
)

func main() {
	listen := flag.String("listen", ":9876", "HTTP listen address")
	trigger := flag.Duration("trigger", 300*time.Second, "realtime trigger timeout; the exporter resends it before it expires")
	stale := flag.Duration("stale-after", 30*time.Second, "anker_c1000_up goes 0 when no telemetry frame arrived for this long")
	printMetrics := flag.Bool("print-metrics", false, "print the metric names and help text, then exit")
	flag.Parse()

	if *printMetrics {
		printMetricList(*stale)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, *listen, *trigger, *stale); err != nil {
		log.Error("exporter stopped", "err", err)
		os.Exit(1)
	}
}

func printMetricList(stale time.Duration) {
	for _, line := range collector.New("SN", stale, time.Now).MetricList() {
		fmt.Println(line)
	}
}

func run(log *slog.Logger, listen string, trigger, stale time.Duration) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	srv := &http.Server{Addr: listen, Handler: newMux(reg), ReadHeaderTimeout: readHeaderTimeout}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()
	log.Info("listening", "addr", listen)

	workerDone := make(chan error, 1)
	go func() { workerDone <- work(ctx, log, cfg, reg, trigger, stale) }()

	select {
	case err = <-srvErr:
		stop()
		<-workerDone
		return fmt.Errorf("http server: %w", err)
	case err = <-workerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Error("worker stopped", "err", err)
		}
	case <-ctx.Done():
		<-workerDone
	}
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err = srv.Shutdown(shutCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
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

// work logs in, then runs the MQTT runner until ctx ends. Setup failures retry, so a slow or
// down cloud API never takes the HTTP endpoint away.
func work(ctx context.Context, log *slog.Logger, cfg config.Config, reg prometheus.Registerer, trigger, stale time.Duration) error {
	cl, err := anker.NewClient(cfg.BaseURL, cfg.Email, cfg.Password, cfg.Country, nil)
	if err != nil {
		return err
	}
	var sn string
	for sn == "" {
		if sn, err = setup(ctx, cl, cfg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var fatal fatalError
			if errors.As(err, &fatal) {
				return err
			}
			log.Error("setup failed, retrying", "err", err, "retry_in", setupRetryDelay.String())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(setupRetryDelay):
			}
		}
	}
	log.Info("device selected", "serial", config.MaskSN(sn))

	col := collector.New(sn, stale, time.Now)
	if err = col.Register(reg); err != nil {
		return err
	}
	r := mqttsession.NewRunner(cl, sn, trigger, col.Update)
	r.OnBadFrame = func(err error) {
		col.CountBadFrame()
		log.Warn("bad frame", "err", err)
	}
	r.OnConnect = func() { log.Info("mqtt subscribed") }
	r.OnError = func(err error) { log.Warn("mqtt session error", "err", err) }
	return r.Run(ctx)
}

// fatalError marks a setup failure that retrying cannot fix.
type fatalError struct{ error }

func (e fatalError) Unwrap() error { return e.error }

func setup(ctx context.Context, cl *anker.Client, cfg config.Config) (string, error) {
	if err := cl.Login(ctx); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	devs, err := cl.Devices(ctx)
	if err != nil {
		return "", fmt.Errorf("devices: %w", err)
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
