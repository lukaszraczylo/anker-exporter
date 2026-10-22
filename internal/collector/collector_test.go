package collector

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lukaszraczylo/anker-exporter/internal/frame"
)

const stale = 30 * time.Second

type clock struct {
	t  time.Time
	mu sync.Mutex
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestCollector(t *testing.T) (*Collector, *clock, *prometheus.Registry) {
	t.Helper()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := New("TESTSN0001", stale, clk.now)
	reg := prometheus.NewRegistry()
	if err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	return c, clk, reg
}

func realMessage(t *testing.T) frame.Message {
	t.Helper()
	f, err := os.Open("../../testdata/frames.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		m, err := frame.Decode([]byte(rec.Payload))
		if err != nil || m.Type != "0405" {
			continue
		}
		if _, ok := m.Values["main_battery_soc"]; ok && m.Strings["device_sn"] != "" {
			return m
		}
	}
	t.Fatal("no usable frame in testdata")
	return frame.Message{}
}

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		m := mf.GetMetric()[0]
		for _, l := range m.GetLabel() {
			if l.GetName() == "device" && l.GetValue() != "TESTSN0001" {
				t.Fatalf("%s device label = %q", name, l.GetValue())
			}
		}
		switch {
		case m.GetGauge() != nil:
			return m.GetGauge().GetValue(), true
		case m.GetCounter() != nil:
			return m.GetCounter().GetValue(), true
		}
	}
	return 0, false
}

func names(t *testing.T, reg *prometheus.Registry) map[string]bool {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, mf := range mfs {
		out[mf.GetName()] = true
	}
	return out
}

func TestCollector_ExposesGauges(t *testing.T) {
	c, _, reg := newTestCollector(t)
	m := realMessage(t)
	c.Update(m)

	for name, field := range map[string]string{
		"anker_c1000_main_battery_soc_percent":  "main_battery_soc",
		"anker_c1000_ac_output_power_watts":     "ac_output_power",
		"anker_c1000_temperature_celsius":       "temperature",
		"anker_c1000_ac_output_timeout_seconds": "ac_output_timeout_seconds",
		"anker_c1000_ac_frequency_hertz":        "ac_frequency",
		"anker_c1000_device_timeout_minutes":    "device_timeout_minutes",
		"anker_c1000_charging_status":           "charging_status",
	} {
		want, ok := m.Values[field]
		if !ok {
			t.Fatalf("test frame lacks %s", field)
		}
		got, ok := gaugeValue(t, reg, name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if v, ok := gaugeValue(t, reg, "anker_c1000_up"); !ok || v != 1 {
		t.Errorf("up = %v,%v", v, ok)
	}
	if v, _ := gaugeValue(t, reg, "anker_c1000_msg_timestamp_seconds"); v != m.Values["msg_timestamp"] {
		t.Errorf("msg_timestamp_seconds = %v, want %v", v, m.Values["msg_timestamp"])
	}
	n := names(t, reg)
	for _, bad := range []string{"anker_c1000_msg_timestamp", "anker_c1000_ac_output_timeout_seconds_seconds"} {
		if n[bad] {
			t.Errorf("unexpected metric %s", bad)
		}
	}
	if v, ok := gaugeValue(t, reg, "anker_c1000_last_message_age_seconds"); !ok || v != 0 {
		t.Errorf("age = %v,%v", v, ok)
	}
}

func TestCollector_UpGoesZeroWhenStale(t *testing.T) {
	c, clk, reg := newTestCollector(t)
	c.Update(realMessage(t))
	if !names(t, reg)["anker_c1000_info"] {
		t.Fatal("info missing while fresh")
	}
	clk.advance(stale + time.Second)

	n := names(t, reg)
	if v, ok := gaugeValue(t, reg, "anker_c1000_up"); !ok || v != 0 {
		t.Errorf("up = %v,%v want 0", v, ok)
	}
	if !n["anker_c1000_bad_frames_total"] {
		t.Error("bad_frames_total must stay present")
	}
	for name := range n {
		if name == "anker_c1000_up" || name == "anker_c1000_bad_frames_total" || name == "anker_c1000_last_message_age_seconds" {
			continue
		}
		if strings.HasPrefix(name, "anker_c1000_") {
			t.Errorf("stale scrape still exposes %s", name)
		}
	}
	if v, ok := gaugeValue(t, reg, "anker_c1000_last_message_age_seconds"); !ok || v <= stale.Seconds() {
		t.Errorf("age = %v,%v", v, ok)
	}
}

func TestCollector_BeforeFirstFrame(t *testing.T) {
	_, _, reg := newTestCollector(t)
	n := names(t, reg)
	if len(n) != 2 || !n["anker_c1000_up"] || !n["anker_c1000_bad_frames_total"] {
		t.Fatalf("before first frame got %v", n)
	}
	if v, _ := gaugeValue(t, reg, "anker_c1000_up"); v != 0 {
		t.Errorf("up = %v", v)
	}
}

func TestCollector_CountsBadFrames(t *testing.T) {
	c, _, reg := newTestCollector(t)
	c.CountBadFrame()
	c.CountBadFrame()
	if v, _ := gaugeValue(t, reg, "anker_c1000_bad_frames_total"); v != 2 {
		t.Errorf("bad frames = %v", v)
	}
	if n, err := testutil.GatherAndCount(reg, "anker_c1000_bad_frames_total"); err != nil || n != 1 {
		t.Errorf("count %d err %v", n, err)
	}
}

func TestCollector_IgnoresOtherTypes(t *testing.T) {
	c, _, reg := newTestCollector(t)
	m := realMessage(t)
	m.Type = "0408"
	c.Update(m)
	if n := names(t, reg); len(n) != 2 {
		t.Fatalf("non-0405 frame changed output: %v", n)
	}
}

func TestCollector_DropsRemainingTimeSentinel(t *testing.T) {
	c, _, reg := newTestCollector(t)
	m := realMessage(t)
	m.Values = copyValues(m.Values)

	m.Values["remaining_time_hours"] = 12.5
	c.Update(m)
	if v, ok := gaugeValue(t, reg, "anker_c1000_remaining_time_hours"); !ok || v != 12.5 {
		t.Fatalf("remaining = %v,%v", v, ok)
	}
	m.Values["remaining_time_hours"] = 6553.5
	c.Update(m)
	if _, ok := gaugeValue(t, reg, "anker_c1000_remaining_time_hours"); ok {
		t.Error("sentinel exported")
	}
	if !names(t, reg)["anker_c1000_main_battery_soc_percent"] {
		t.Error("other gauges must remain")
	}
}

func copyValues(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestCollector_InfoSeriesReplaced(t *testing.T) {
	c, _, reg := newTestCollector(t)
	m := realMessage(t)
	m.Strings = map[string]string{"device_sn": "TESTSN0001", "sw_version": "1.0.0"}
	c.Update(m)
	m.Strings = map[string]string{"device_sn": "TESTSN0001", "sw_version": "2.0.0"}
	c.Update(m)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var series int
	for _, mf := range mfs {
		if mf.GetName() != "anker_c1000_info" {
			continue
		}
		for _, mt := range mf.GetMetric() {
			series++
			labels := map[string]string{}
			for _, l := range mt.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["sw_version"] != "2.0.0" || labels["device"] != "TESTSN0001" || labels["device_sn"] != "TESTSN0001" {
				t.Errorf("labels = %v", labels)
			}
			if mt.GetGauge().GetValue() != 1 {
				t.Error("info value must be 1")
			}
		}
	}
	if series != 1 {
		t.Fatalf("info series = %d, want 1", series)
	}
}

func TestCollector_ConcurrentUpdateCollect(t *testing.T) {
	c, clk, reg := newTestCollector(t)
	m := realMessage(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Update(m)
				c.CountBadFrame()
				clk.advance(time.Millisecond)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := reg.Gather(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
