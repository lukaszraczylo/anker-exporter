// Package collector exposes decoded A1761 telemetry as Prometheus metrics.
package collector

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lukaszraczylo/anker-exporter/internal/frame"
)

const (
	namespace     = "anker_c1000"
	deviceLabel   = "device"
	telemetryType = "0405"

	// remainingSentinel is 0xFFFF after the 0.1 scale; the device uses it for "unknown".
	remainingSentinel  = 6553.5
	remainingTimeField = "remaining_time_hours"
	remainingTimeHelp  = "Device estimate of remaining runtime in hours; unverified and unreliable (raw values up to 6634 at 100% SOC)"
	timestampField     = "msg_timestamp"
	timestampHelp      = "Device message timestamp as unix time in seconds, not a duration"
)

type gaugeSpec struct {
	name  string
	help  string
	desc  *prometheus.Desc
	field string
}

// Collector turns the latest telemetry frame into metrics at scrape time.
type Collector struct {
	last       time.Time
	now        func() time.Time
	up         *prometheus.Desc
	age        *prometheus.Desc
	bad        *prometheus.Desc
	values     map[string]float64
	strings    map[string]string
	device     string
	gauges     []gaugeSpec
	staleAfter time.Duration
	badFrames  float64
	mu         sync.Mutex
	hasFrame   bool
}

// New builds a collector for one device serial.
func New(device string, staleAfter time.Duration, now func() time.Time) *Collector {
	c := &Collector{
		device:     device,
		staleAfter: staleAfter,
		now:        now,
		up:         prometheus.NewDesc(namespace+"_up", "1 if a telemetry frame arrived within the staleness window, else 0", []string{deviceLabel}, nil),
		age:        prometheus.NewDesc(namespace+"_last_message_age_seconds", "Seconds since the last accepted telemetry frame", []string{deviceLabel}, nil),
		bad:        prometheus.NewDesc(namespace+"_bad_frames_total", "Frames that failed to decode", []string{deviceLabel}, nil),
	}
	for _, f := range frame.A1761Fields() {
		help := f.Help
		switch f.Name {
		case remainingTimeField:
			help = remainingTimeHelp
		case timestampField:
			help = timestampHelp
		}
		c.gauges = append(c.gauges, gaugeSpec{
			field: f.Name,
			name:  metricName(f),
			help:  help,
			desc:  prometheus.NewDesc(metricName(f), help, []string{deviceLabel}, nil),
		})
	}
	return c
}

func metricName(f frame.FieldInfo) string {
	name := namespace + "_" + f.Name
	if f.Unit != "" && !strings.HasSuffix(f.Name, "_"+f.Unit) {
		name += "_" + f.Unit
	}
	return name
}

// Register adds the collector to r.
func (c *Collector) Register(r prometheus.Registerer) error { return r.Register(c) }

// Update records a decoded frame; only telemetry frames count.
func (c *Collector) Update(m frame.Message) {
	if m.Type != telemetryType {
		return
	}
	vals := make(map[string]float64, len(m.Values))
	for k, v := range m.Values {
		vals[k] = v
	}
	strs := make(map[string]string, len(m.Strings))
	for k, v := range m.Strings {
		strs[k] = v
	}
	c.mu.Lock()
	c.values, c.strings = vals, strs
	c.last, c.hasFrame = c.now(), true
	c.mu.Unlock()
}

// CountBadFrame increments the undecodable-frame counter.
func (c *Collector) CountBadFrame() {
	c.mu.Lock()
	c.badFrames++
	c.mu.Unlock()
}

// Describe sends nothing: the info metric has dynamic labels, so the collector is unchecked.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ch <- prometheus.MustNewConstMetric(c.bad, prometheus.CounterValue, c.badFrames, c.device)
	if !c.hasFrame {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0, c.device)
		return
	}
	age := max(c.now().Sub(c.last), 0)
	ch <- prometheus.MustNewConstMetric(c.age, prometheus.GaugeValue, age.Seconds(), c.device)
	if age > c.staleAfter {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0, c.device)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1, c.device)

	for _, g := range c.gauges {
		v, ok := c.values[g.field]
		if !ok || (g.field == remainingTimeField && v == remainingSentinel) {
			continue
		}
		ch <- prometheus.MustNewConstMetric(g.desc, prometheus.GaugeValue, v, c.device)
	}
	c.collectInfo(ch)
}

func (c *Collector) collectInfo(ch chan<- prometheus.Metric) {
	keys := make([]string, 0, len(c.strings))
	for k := range c.strings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	labels := append([]string{deviceLabel}, keys...)
	vals := []string{c.device}
	for _, k := range keys {
		vals = append(vals, c.strings[k])
	}
	desc := prometheus.NewDesc(namespace+"_info", "Device identity and firmware versions", labels, nil)
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1, vals...)
}

// MetricList returns "name<TAB>help" lines for every metric the collector can expose.
func (c *Collector) MetricList() []string {
	out := []string{
		namespace + "_up\t1 if a telemetry frame arrived within the staleness window, else 0",
		namespace + "_last_message_age_seconds\tSeconds since the last accepted telemetry frame",
		namespace + "_bad_frames_total\tFrames that failed to decode",
		namespace + "_info\tDevice identity and firmware versions",
	}
	for _, g := range c.gauges {
		out = append(out, g.name+"\t"+g.help)
	}
	return out
}
