package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/frame"
)

var errNet = errors.New("dial tcp: i/o timeout")

func reject() error { return fmt.Errorf("login: %w", &anker.APIError{Code: 26108, Msg: "bad"}) }

func TestSetupBackoff_RejectionDoublesFromFiveMinutes(t *testing.T) {
	p := defaultBackoff
	p.maxRejections = 10
	b := backoff{p: p}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		got, err := b.next(reject())
		if err != nil || got != w {
			t.Fatalf("step %d: %v %v, want %v", i, got, err, w)
		}
	}
}

func TestSetupBackoff_NetworkDoublesFromThirtySeconds(t *testing.T) {
	b := backoff{p: defaultBackoff}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		got, err := b.next(errNet)
		if err != nil || got != w {
			t.Fatalf("step %d: %v %v, want %v", i, got, err, w)
		}
	}
}

func TestSetupBackoff_FiveRejectionsAreFatal(t *testing.T) {
	b := backoff{p: defaultBackoff}
	for i := 1; i <= 4; i++ {
		if _, err := b.next(reject()); err != nil {
			t.Fatalf("rejection %d fatal: %v", i, err)
		}
	}
	_, err := b.next(reject())
	var fatal fatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("5th rejection err = %v, want fatalError", err)
	}
}

func TestSetupBackoff_SuccessAndNetworkErrorResetCounters(t *testing.T) {
	b := backoff{p: defaultBackoff}
	for range 4 {
		_, _ = b.next(reject())
	}
	b.reset()
	if d, err := b.next(reject()); err != nil || d != 5*time.Minute {
		t.Fatalf("after reset: %v %v", d, err)
	}
	_, _ = b.next(errNet)
	if d, err := b.next(reject()); err != nil || d != 5*time.Minute {
		t.Fatalf("after network error: %v %v", d, err)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAcquire_RetriesThenSucceeds(t *testing.T) {
	var delays []time.Duration
	calls := 0
	step := func(context.Context) (string, error) {
		calls++
		if calls < 3 {
			return "", errNet
		}
		return "SN", nil
	}
	sleep := func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	sn, err := acquire(context.Background(), discard(), step, defaultBackoff, sleep)
	if err != nil || sn != "SN" || len(delays) != 2 || delays[0] != 30*time.Second || delays[1] != time.Minute {
		t.Fatalf("sn=%q err=%v delays=%v", sn, err, delays)
	}
}

func TestAcquire_FatalAfterFiveRejections(t *testing.T) {
	calls := 0
	step := func(context.Context) (string, error) { calls++; return "", reject() }
	sleep := func(context.Context, time.Duration) error { return nil }
	_, err := acquire(context.Background(), discard(), step, defaultBackoff, sleep)
	var ae *anker.APIError
	if err == nil || calls != 5 || !errors.As(err, &ae) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestAcquire_PickDeviceErrorIsFatalImmediately(t *testing.T) {
	step := func(context.Context) (string, error) { return "", fatalError{errors.New("no device")} }
	_, err := acquire(context.Background(), discard(), step, defaultBackoff, func(context.Context, time.Duration) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no device") {
		t.Fatalf("err = %v", err)
	}
}

func TestServe_WorkerErrorIsReturned(t *testing.T) {
	boom := errors.New("boom")
	err := serve(context.Background(), discard(), "127.0.0.1:0", prometheus.NewRegistry(), func(context.Context) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

func TestServe_CancelledIsCleanExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	work := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if err := serve(ctx, discard(), "127.0.0.1:0", prometheus.NewRegistry(), work); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestServe_WorkerCanceledErrorIsClean(t *testing.T) {
	err := serve(context.Background(), discard(), "127.0.0.1:0", prometheus.NewRegistry(), func(context.Context) error { return context.Canceled })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRun_InvalidTriggerFailsBeforeLogin(t *testing.T) {
	creds := env(map[string]string{"ANKER_EMAIL": "a@b.c", "ANKER_PASSWORD": "pw"})
	for _, v := range []string{"0s", "-5s", "500ms", "29s", "601s", "1h"} {
		err := run(context.Background(), []string{"-trigger", v, "-listen", "127.0.0.1:0"}, creds, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "-trigger") {
			t.Errorf("trigger %s: err = %v", v, err)
		}
	}
}

func TestRun_MissingConfigFails(t *testing.T) {
	if err := run(context.Background(), nil, env(nil), io.Discard); err == nil {
		t.Fatal("want error")
	}
}

func TestRun_HelpAndPrintMetricsNeedNoCredentials(t *testing.T) {
	var out strings.Builder
	if err := run(context.Background(), []string{"-print-metrics"}, env(nil), &out); err != nil || !strings.Contains(out.String(), "anker_c1000_up") {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
	if err := run(context.Background(), []string{"-h"}, env(nil), io.Discard); !errors.Is(err, errHelp) {
		t.Fatalf("-h err = %v", err)
	}
}

func TestBadFrameAttrs(t *testing.T) {
	raw := []byte{0xff, 0x09, 0x40, 0x00, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
	attrs := badFrameAttrs(&frame.FrameError{Err: frame.ErrLength, Raw: raw})
	got := fmt.Sprint(attrs)
	for _, want := range []string{"22", "64", "ff09"} {
		if !strings.Contains(got, want) {
			t.Errorf("attrs %s lack %s", got, want)
		}
	}
	if strings.Contains(got, "0d0e0f1011") {
		t.Errorf("hex prefix longer than 16 bytes: %s", got)
	}
}

func TestRun_PrintMetricsListsCostOnlyWhenPriced(t *testing.T) {
	var off, on strings.Builder
	if err := run(context.Background(), []string{"-print-metrics"}, env(nil), &off); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.String(), "projected_monthly_cost") {
		t.Error("cost metrics listed with feature off")
	}
	if err := run(context.Background(), []string{"-print-metrics"}, env(map[string]string{"ANKER_PRICE_PER_KWH": "0.25"}), &on); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"anker_c1000_projected_monthly_cost", "anker_c1000_energy_price_per_kwh"} {
		if !strings.Contains(on.String(), want) {
			t.Errorf("env price: %s not listed", want)
		}
	}
	on.Reset()
	if err := run(context.Background(), []string{"-print-metrics", "-price-per-kwh", "0.3", "-currency", "usd"}, env(nil), &on); err != nil || !strings.Contains(on.String(), "projected_monthly_cost") {
		t.Fatalf("flags: err=%v out=%q", err, on.String())
	}
}

func TestRun_InvalidPricingFailsBeforeLogin(t *testing.T) {
	e := map[string]string{"ANKER_EMAIL": "a@b.c", "ANKER_PASSWORD": "pw"}
	for _, args := range [][]string{
		{"-price-per-kwh", "-1"},
		{"-price-per-kwh", "abc"},
		{"-price-per-kwh", "0.25", "-currency", "GB"},
	} {
		err := run(context.Background(), args, env(e), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "ANKER_") {
			t.Errorf("args %v: err = %v", args, err)
		}
	}
	envBad := map[string]string{"ANKER_EMAIL": "a@b.c", "ANKER_PASSWORD": "pw", "ANKER_PRICE_PER_KWH": "NaN"}
	if err := run(context.Background(), nil, env(envBad), io.Discard); err == nil {
		t.Error("NaN env price accepted")
	}
}
