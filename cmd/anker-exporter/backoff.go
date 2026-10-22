package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
)

// backoffPolicy sets the retry delays for setup. A server rejection backs off slowly
// because repeated bad logins can lock the account.
type backoffPolicy struct {
	rejectBase    time.Duration
	rejectMax     time.Duration
	netBase       time.Duration
	netMax        time.Duration
	maxRejections int
}

var defaultBackoff = backoffPolicy{
	rejectBase:    5 * time.Minute,
	rejectMax:     time.Hour,
	netBase:       30 * time.Second,
	netMax:        5 * time.Minute,
	maxRejections: 5,
}

type backoff struct {
	p          backoffPolicy
	rejections int
	failures   int
}

// next records a failed attempt and returns the delay before the next one.
// It returns a fatalError once the server has rejected the login too many times in a row.
func (b *backoff) next(err error) (time.Duration, error) {
	var ae *anker.APIError
	if errors.As(err, &ae) {
		b.failures = 0
		b.rejections++
		if b.rejections >= b.p.maxRejections {
			return 0, fatalError{fmt.Errorf("login rejected %d times in a row, giving up: %w", b.rejections, err)}
		}
		return grow(b.p.rejectBase, b.p.rejectMax, b.rejections), nil
	}
	b.rejections = 0
	b.failures++
	return grow(b.p.netBase, b.p.netMax, b.failures), nil
}

func (b *backoff) reset() { b.rejections, b.failures = 0, 0 }

// grow returns base doubled n-1 times, capped at limit.
func grow(base, limit time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// fatalError marks a setup failure that retrying cannot fix.
type fatalError struct{ error }

func (e fatalError) Unwrap() error { return e.error }

// Fatal tells mqttsession.Runner to stop retrying.
func (fatalError) Fatal() bool { return true }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// acquire calls step until it returns a serial, backing off between failures.
func acquire(ctx context.Context, log *slog.Logger, step func(context.Context) (string, error), p backoffPolicy, sleep func(context.Context, time.Duration) error) (string, error) {
	b := backoff{p: p}
	for {
		sn, err := step(ctx)
		if err == nil {
			b.reset()
			return sn, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var fatal fatalError
		if errors.As(err, &fatal) {
			return "", err
		}
		delay, ferr := b.next(err)
		if ferr != nil {
			return "", ferr
		}
		log.Error("setup failed, retrying", "err", err, "retry_in", delay.String())
		if err = sleep(ctx, delay); err != nil {
			return "", err
		}
	}
}
