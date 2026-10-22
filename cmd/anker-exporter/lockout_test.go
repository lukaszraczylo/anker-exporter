package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
)

type scriptedSource struct {
	errs  []error
	calls int
}

func (s *scriptedSource) MQTTInfo(context.Context) (anker.MQTTInfo, error) {
	err := s.errs[min(s.calls, len(s.errs)-1)]
	s.calls++
	return anker.MQTTInfo{UserID: "u"}, err
}

func rejected() error {
	return &anker.LoginError{Err: &anker.APIError{Code: 100053, Msg: "bad password"}}
}

func newTestLockout(src *scriptedSource, slept *[]time.Duration) *lockoutSource {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newLockoutSource(src, log, defaultBackoff, func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	})
}

func isFatalErr(err error) bool {
	var f fatalError
	return errors.As(err, &f)
}

func TestLockout_RunnerLoginRejectionsEndInFatalError(t *testing.T) {
	src := &scriptedSource{errs: []error{rejected()}}
	var slept []time.Duration
	l := newTestLockout(src, &slept)
	var err error
	for i := 0; i < defaultBackoff.maxRejections && !isFatalErr(err); i++ {
		_, err = l.MQTTInfo(context.Background())
	}
	if !isFatalErr(err) {
		t.Fatalf("err after %d rejections = %v, want fatalError", src.calls, err)
	}
	if src.calls != defaultBackoff.maxRejections {
		t.Errorf("calls = %d, want %d", src.calls, defaultBackoff.maxRejections)
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute}
	if len(slept) != len(want) {
		t.Fatalf("delays = %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("delay %d = %v, want %v", i, slept[i], want[i])
		}
	}
}

func TestLockout_SuccessResetsCount(t *testing.T) {
	src := &scriptedSource{errs: []error{rejected(), rejected(), nil, rejected()}}
	var slept []time.Duration
	l := newTestLockout(src, &slept)
	for i := 0; i < 4; i++ {
		_, _ = l.MQTTInfo(context.Background())
	}
	if len(slept) != 3 || slept[2] != defaultBackoff.rejectBase {
		t.Errorf("delays = %v, want the third rejection to restart at %v", slept, defaultBackoff.rejectBase)
	}
}

func TestLockout_OtherErrorsPassThroughWithoutDelay(t *testing.T) {
	cases := map[string]error{
		"network":                 errors.New("dial tcp: timeout"),
		"api error outside login": &anker.APIError{Code: 5, Msg: "mqtt info refused"},
	}
	for name, e := range cases {
		src := &scriptedSource{errs: []error{e}}
		var slept []time.Duration
		l := newTestLockout(src, &slept)
		for i := 0; i < 10; i++ {
			if _, err := l.MQTTInfo(context.Background()); !errors.Is(err, e) || isFatalErr(err) {
				t.Fatalf("%s: err = %v", name, err)
			}
		}
		if len(slept) != 0 {
			t.Errorf("%s: slept %v", name, slept)
		}
	}
}
