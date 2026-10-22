package mqttsession

import (
	"context"
	"fmt"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/frame"
)

const (
	defaultBaseDelay = time.Second
	maxDelay         = time.Minute
	resendNum        = 4 // resend after 4/5 of the trigger timeout
	resendDen        = 5
)

// TokenSource supplies fresh MQTT connection details for each (re)connect.
type TokenSource interface {
	MQTTInfo(ctx context.Context) (anker.MQTTInfo, error)
}

// Link is the part of a live MQTT session the Runner uses.
type Link interface {
	Subscribe(topic string) error
	Publish(topic string, payload []byte) error
	Close()
	// Lost is closed when the broker connection drops.
	Lost() <-chan struct{}
}

type connectFunc func(ctx context.Context, info anker.MQTTInfo, handler func(string, []byte)) (Link, error)

// Runner keeps one MQTT session alive: it subscribes, sends the realtime trigger,
// resends it before it expires and reconnects with fresh credentials after a drop.
type Runner struct {
	api       TokenSource
	onMessage func(frame.Message)
	connect   connectFunc

	// Optional hooks; set before Run.
	OnBadFrame func(error)
	OnConnect  func()
	OnError    func(error)

	sn          string
	trigger     time.Duration
	baseDelay   time.Duration
	resendAfter time.Duration
}

// NewRunner returns a Runner that triggers realtime telemetry with the given timeout.
func NewRunner(api TokenSource, sn string, trigger time.Duration, onMessage func(frame.Message)) *Runner {
	r := &Runner{
		api: api, sn: sn, trigger: trigger, onMessage: onMessage,
		baseDelay:   defaultBaseDelay,
		resendAfter: trigger * resendNum / resendDen,
	}
	r.connect = func(ctx context.Context, info anker.MQTTInfo, h func(string, []byte)) (Link, error) {
		sess, err := Connect(ctx, info, h, WithOnError(func(err error) { reportTo(r.OnError, err) }))
		if err != nil {
			return nil, err // a nil *Session must not become a non-nil Link
		}
		return sess, nil
	}
	return r
}

func reportTo(f func(error), err error) {
	if f != nil {
		f(err)
	}
}

func (r *Runner) triggerSeconds() int { return int(r.trigger / time.Second) }

func (r *Runner) nextDelay(d time.Duration) time.Duration { return min(d*2, maxDelay) }

// Run blocks until ctx ends, returning ctx.Err(), or until the trigger setting is invalid.
func (r *Runner) Run(ctx context.Context) error {
	if _, err := frame.BuildTrigger(r.sn, "check", "check", r.triggerSeconds(), time.Now()); err != nil {
		return fmt.Errorf("runner: trigger %v: %w", r.trigger, err)
	}
	delay := r.baseDelay
	for ctx.Err() == nil {
		established, err := r.runSession(ctx)
		if ctx.Err() != nil {
			break
		}
		if established {
			delay = r.baseDelay
		}
		reportTo(r.OnError, err)
		if !sleep(ctx, delay) {
			break
		}
		if !established {
			delay = r.nextDelay(delay)
		}
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// runSession returns once the session ends; established reports whether it got as far as subscribing.
func (r *Runner) runSession(ctx context.Context) (established bool, err error) {
	info, err := r.api.MQTTInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("mqtt info: %w", err)
	}
	sess, err := r.connect(ctx, info, r.handle)
	if err != nil {
		return false, err
	}
	defer sess.Close()
	if err = sess.Subscribe(TelemetryTopic(info.AppName, r.sn)); err != nil {
		return false, err
	}
	if r.OnConnect != nil {
		r.OnConnect()
	}
	cmdTopic := CommandTopic(info.AppName, r.sn)
	clientID := frame.HeadClientID(info.AppName, info.UserID, info.CertificateID)
	send := func() error {
		msg, berr := frame.BuildTrigger(r.sn, info.UserID, clientID, r.triggerSeconds(), time.Now())
		if berr != nil {
			return berr
		}
		return sess.Publish(cmdTopic, msg)
	}
	if err = send(); err != nil {
		return true, err
	}
	tick := time.NewTicker(r.resendAfter)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-sess.Lost():
			return true, fmt.Errorf("mqtt connection lost")
		case <-tick.C:
			if err = send(); err != nil {
				return true, err
			}
		}
	}
}

func (r *Runner) handle(_ string, payload []byte) {
	msg, err := frame.Decode(payload)
	if err != nil {
		reportTo(r.OnBadFrame, err)
		return
	}
	r.onMessage(msg)
}
