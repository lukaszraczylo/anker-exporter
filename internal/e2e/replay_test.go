package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/collector"
	"github.com/lukaszraczylo/anker-exporter/internal/mqttsession"
)

const (
	serial     = "TESTSN0001"
	staleAfter = 2 * time.Minute
	testWait   = 5 * time.Second
)

type fakeAPI struct{}

func (fakeAPI) MQTTInfo(context.Context) (anker.MQTTInfo, error) {
	return anker.MQTTInfo{UserID: "TESTUSER0001", AppName: "anker_power", CertificateID: "TESTCERTID0001"}, nil
}

// fakeLink delivers nothing by itself; the test pushes recorded frames through the handler.
type fakeLink struct {
	lost    chan struct{}
	trigger chan struct{}
	once    sync.Once
}

func (l *fakeLink) Subscribe(string) error { return nil }
func (l *fakeLink) Publish(string, []byte) error {
	l.once.Do(func() { close(l.trigger) })
	return nil
}
func (l *fakeLink) Close()                {}
func (l *fakeLink) Lost() <-chan struct{} { return l.lost }

func recordedFrames(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var rec struct{ Payload string }
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, []byte(rec.Payload))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no recorded frames")
	}
	return out
}

type pipeline struct {
	now *atomic.Int64
	url string
}

// replay pushes every recorded frame through Runner and Collector and serves the result over promhttp.
func replay(t *testing.T) *pipeline {
	t.Helper()
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC).UnixNano())
	col := collector.New(serial, staleAfter, func() time.Time { return time.Unix(0, now.Load()).UTC() })
	reg := prometheus.NewRegistry()
	if err := col.Register(reg); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	link := &fakeLink{lost: make(chan struct{}), trigger: make(chan struct{})}
	handlers := make(chan func(string, []byte), 1)
	r := mqttsession.NewRunner(fakeAPI{}, serial, time.Minute, col.Update)
	r.OnBadFrame = func(error) { col.CountBadFrame() }
	r.UseConnector(func(_ context.Context, _ anker.MQTTInfo, h func(string, []byte)) (mqttsession.Link, error) {
		handlers <- h
		return link, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	var handler func(string, []byte)
	select {
	case handler = <-handlers:
	case <-time.After(testWait):
		t.Fatal("runner never connected")
	}
	select {
	case <-link.trigger:
	case <-time.After(testWait):
		t.Fatal("runner never sent the trigger")
	}
	for _, f := range recordedFrames(t) {
		handler("dt/anker_power/A1761/"+serial+"/param_info", f)
	}
	return &pipeline{url: srv.URL, now: &now}
}

func (p *pipeline) scrape(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(p.url) //nolint:noctx,gosec // local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplay_FramesToMetrics(t *testing.T) {
	body := replay(t).scrape(t)
	for _, want := range []string{
		`anker_c1000_up{device="TESTSN0001"} 1`,
		`anker_c1000_last_message_age_seconds{device="TESTSN0001"} 0`,
		`anker_c1000_bad_frames_total{device="TESTSN0001"} 0`,
		`anker_c1000_main_battery_soc_percent{device="TESTSN0001"} 100`,
		`anker_c1000_ac_output_power_watts{device="TESTSN0001"} 444`,
		`anker_c1000_info{device="TESTSN0001",device_sn="TESTSN0001",exp_1_type="A1761_30Ah",sw_controller="1.5.4",sw_expansion="0",sw_version="1.5.4"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition lacks %q", want)
		}
	}
}

func TestReplay_StaleKeepsOnlyHealthSeries(t *testing.T) {
	p := replay(t)
	p.now.Add(int64(staleAfter + 2*time.Second))
	body := p.scrape(t)
	var series []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "anker_c1000_") {
			series = append(series, line)
		}
	}
	want := []string{
		`anker_c1000_bad_frames_total{device="TESTSN0001"} 0`,
		`anker_c1000_last_message_age_seconds{device="TESTSN0001"} 122`,
		`anker_c1000_up{device="TESTSN0001"} 0`,
	}
	if strings.Join(series, "\n") != strings.Join(want, "\n") {
		t.Errorf("series = %q, want %q", series, want)
	}
}
