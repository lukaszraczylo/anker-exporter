package mqttsession

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/frame"
)

const testWait = 2 * time.Second

type fakeSession struct {
	handler func(string, []byte)
	lost    chan struct{}
	subs    chan string
	pubs    chan string
	closed  chan struct{}
	once    sync.Once
}

func newFakeSession(h func(string, []byte)) *fakeSession {
	return &fakeSession{handler: h, lost: make(chan struct{}), subs: make(chan string, 8), pubs: make(chan string, 8), closed: make(chan struct{})}
}

func (f *fakeSession) Subscribe(t string) error { f.subs <- t; return nil }
func (f *fakeSession) Publish(t string, _ []byte) error {
	f.pubs <- t
	return nil
}
func (f *fakeSession) Close()                { f.once.Do(func() { close(f.closed) }) }
func (f *fakeSession) Lost() <-chan struct{} { return f.lost }

type fakeAPI struct {
	mu    sync.Mutex
	calls int
}

func (a *fakeAPI) MQTTInfo(context.Context) (anker.MQTTInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return anker.MQTTInfo{UserID: "u1", AppName: "anker_power", CertificateID: "c1"}, nil
}

func (a *fakeAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

type harness struct {
	r        *Runner
	api      *fakeAPI
	sessions chan *fakeSession
}

func newHarness(onMessage func(frame.Message)) *harness {
	h := &harness{api: &fakeAPI{}, sessions: make(chan *fakeSession, 8)}
	h.r = NewRunner(h.api, "SN1", 60*time.Second, onMessage)
	h.r.connect = func(_ context.Context, _ anker.MQTTInfo, handler func(string, []byte)) (Link, error) {
		s := newFakeSession(handler)
		h.sessions <- s
		return s, nil
	}
	h.r.baseDelay = time.Millisecond
	return h
}

func (h *harness) start(t *testing.T) (cancel func(), done chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- h.r.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel, done
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(testWait):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func goodPayloads(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open("../../testdata/frames.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec struct{ Payload string }
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if _, err := frame.Decode([]byte(rec.Payload)); err == nil {
			out = append(out, []byte(rec.Payload))
		}
	}
	if len(out) == 0 {
		t.Fatal("no decodable frames in testdata")
	}
	return out
}

// corrupt flips one body byte of the frame so only the checksum fails.
func corrupt(t *testing.T, envelope []byte) []byte {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatal(err)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(env["payload"].(string)), &inner); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(inner["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-3] ^= 0xff
	inner["data"] = base64.StdEncoding.EncodeToString(raw)
	ib, _ := json.Marshal(inner)
	env["payload"] = string(ib)
	out, _ := json.Marshal(env)
	if _, err := frame.Decode(out); !errors.Is(err, frame.ErrChecksum) {
		t.Fatalf("corrupted frame error = %v, want ErrChecksum", err)
	}
	return out
}

func TestRunner_ResendsTriggerBeforeTimeout(t *testing.T) {
	h := newHarness(func(frame.Message) {})
	h.r.resendAfter = 10 * time.Millisecond
	h.start(t)
	s := <-h.sessions
	if got := recv(t, s.subs, "subscribe"); got != "dt/anker_power/A1761/SN1/#" {
		t.Errorf("subscribed to %q", got)
	}
	for i := 0; i < 3; i++ {
		if got := recv(t, s.pubs, "trigger publish"); got != "cmd/anker_power/A1761/SN1/req" {
			t.Errorf("publish %d to %q", i, got)
		}
	}
}

func TestRunner_ResendIsEightyPercentOfTimeout(t *testing.T) {
	r := NewRunner(&fakeAPI{}, "SN1", 60*time.Second, nil)
	if r.resendAfter != 48*time.Second {
		t.Errorf("resendAfter = %v, want 48s", r.resendAfter)
	}
}

func TestRunner_ReconnectsAfterDrop(t *testing.T) {
	h := newHarness(func(frame.Message) {})
	h.start(t)
	first := <-h.sessions
	recv(t, first.subs, "first subscribe")
	close(first.lost)
	recv(t, first.closed, "old session closed")
	second := <-h.sessions
	recv(t, second.subs, "second subscribe")
	recv(t, second.pubs, "trigger on new session")
	if n := h.api.count(); n != 2 {
		t.Errorf("MQTTInfo fetched %d times, want 2", n)
	}
}

func TestRunner_BackoffDoublesAndCaps(t *testing.T) {
	r := NewRunner(&fakeAPI{}, "SN1", 60*time.Second, nil)
	d := time.Second
	var got []time.Duration
	for i := 0; i < 8; i++ {
		got = append(got, d)
		d = r.nextDelay(d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("delays = %v", got)
		}
	}
}

func TestRunner_DropsBadFrames(t *testing.T) {
	good := goodPayloads(t)
	got := make(chan frame.Message, 4)
	h := newHarness(func(m frame.Message) { got <- m })
	badErrs := make(chan error, 4)
	h.r.OnBadFrame = func(err error) { badErrs <- err }
	_, done := h.start(t)
	s := <-h.sessions
	recv(t, s.pubs, "trigger")

	s.handler("t", corrupt(t, good[0]))
	if err := recv(t, badErrs, "bad frame callback"); !errors.Is(err, frame.ErrChecksum) {
		t.Errorf("bad frame err = %v", err)
	}
	s.handler("t", []byte("not json"))
	_ = recv(t, badErrs, "garbage callback")
	s.handler("t", good[0])
	if m := recv(t, got, "good frame"); m.Type == "" {
		t.Errorf("good frame delivered empty: %+v", m)
	}
	select {
	case m := <-got:
		t.Errorf("bad frame reached onMessage: %+v", m)
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("Run stopped: %v", err)
	default:
	}
}

func TestRunner_StopsOnCancelAndClosesSession(t *testing.T) {
	h := newHarness(func(frame.Message) {})
	cancel, done := h.start(t)
	s := <-h.sessions
	recv(t, s.pubs, "trigger")
	cancel()
	if err := recv(t, done, "Run return"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	recv(t, s.closed, "session closed")
}

func TestRunner_RejectsOutOfRangeTrigger(t *testing.T) {
	r := NewRunner(&fakeAPI{}, "SN1", 500*time.Millisecond, nil)
	if err := r.Run(context.Background()); err == nil {
		t.Error("expected error for sub-second trigger")
	}
}

type fatalErr struct{}

func (fatalErr) Error() string { return "fatal" }
func (fatalErr) Fatal() bool   { return true }

type failingAPI struct {
	err   error
	calls int
}

func (a *failingAPI) MQTTInfo(context.Context) (anker.MQTTInfo, error) {
	a.calls++
	return anker.MQTTInfo{}, a.err
}

func TestRunner_ReturnsFatalTokenError(t *testing.T) {
	api := &failingAPI{err: fatalErr{}}
	r := NewRunner(api, "SN1", 60*time.Second, nil)
	r.baseDelay = time.Millisecond
	if err := r.Run(context.Background()); !errors.As(err, new(fatalErr)) {
		t.Fatalf("Run = %v, want the fatal error", err)
	}
	if api.calls != 1 {
		t.Errorf("MQTTInfo calls = %d, want 1", api.calls)
	}
}

func TestRunner_RetriesPlainTokenError(t *testing.T) {
	api := &failingAPI{err: errors.New("boom")}
	r := NewRunner(api, "SN1", 60*time.Second, nil)
	r.baseDelay = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v", err)
	}
	if api.calls < 2 {
		t.Errorf("MQTTInfo calls = %d, want retries", api.calls)
	}
}

func TestRunner_DelayGrowsWhenBrokerDropsRightAfterSubscribe(t *testing.T) {
	h := newHarness(func(frame.Message) {})
	h.r.baseDelay = time.Second
	delays := make(chan time.Duration, 16)
	h.r.sleep = func(ctx context.Context, d time.Duration) bool {
		delays <- d
		return ctx.Err() == nil
	}
	h.r.connect = func(_ context.Context, _ anker.MQTTInfo, handler func(string, []byte)) (Link, error) {
		s := newFakeSession(handler)
		close(s.lost)
		return s, nil
	}
	cancel, _ := h.start(t)
	var got []time.Duration
	for i := 0; i < 5; i++ {
		got = append(got, recv(t, delays, "reconnect delay"))
	}
	cancel()
	want := []time.Duration{1, 2, 4, 8, 16}
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("delays = %v, want doubling from 1s", got)
		}
	}
}

func TestRunner_DelayResetsAfterSessionDeliveredFrame(t *testing.T) {
	good := goodPayloads(t)
	h := newHarness(func(frame.Message) {})
	h.r.baseDelay = time.Second
	delays := make(chan time.Duration, 16)
	h.r.sleep = func(ctx context.Context, d time.Duration) bool {
		delays <- d
		return ctx.Err() == nil
	}
	n := 0
	h.r.connect = func(_ context.Context, _ anker.MQTTInfo, handler func(string, []byte)) (Link, error) {
		n++
		s := newFakeSession(handler)
		if n == 3 {
			handler("t", good[0])
		}
		close(s.lost)
		return s, nil
	}
	cancel, _ := h.start(t)
	var got []time.Duration
	for i := 0; i < 3; i++ {
		got = append(got, recv(t, delays, "reconnect delay"))
	}
	cancel()
	want := []time.Duration{1, 2, 1}
	for i, w := range want {
		if got[i] != w*time.Second {
			t.Fatalf("delays = %v, want 1s,2s then reset to 1s", got)
		}
	}
}
