package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/mqttsession"
)

// lockoutSource applies the setup rejection policy to the logins the runner triggers on reconnect.
type lockoutSource struct {
	inner mqttsession.TokenSource
	log   *slog.Logger
	sleep func(context.Context, time.Duration) error
	b     backoff
}

func newLockoutSource(inner mqttsession.TokenSource, log *slog.Logger, p backoffPolicy, sleep func(context.Context, time.Duration) error) *lockoutSource {
	return &lockoutSource{inner: inner, log: log, sleep: sleep, b: backoff{p: p}}
}

// MQTTInfo delays after a login rejection and returns a fatalError after too many in a row.
func (l *lockoutSource) MQTTInfo(ctx context.Context) (anker.MQTTInfo, error) {
	info, err := l.inner.MQTTInfo(ctx)
	if err == nil {
		l.b.reset()
		return info, nil
	}
	var le *anker.LoginError
	var ae *anker.APIError
	if !errors.As(err, &le) || !errors.As(err, &ae) {
		return anker.MQTTInfo{}, err
	}
	delay, ferr := l.b.next(err)
	if ferr != nil {
		return anker.MQTTInfo{}, ferr
	}
	l.log.Error("login rejected, waiting", "err", err, "retry_in", delay.String())
	if serr := l.sleep(ctx, delay); serr != nil {
		return anker.MQTTInfo{}, serr
	}
	return anker.MQTTInfo{}, err
}
