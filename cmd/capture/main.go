// Command capture records raw MQTT messages from an A1761 into capture/frames.jsonl.
// With -trigger it publishes one realtime trigger (message 0057), the only write it can make.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
	"github.com/lukaszraczylo/anker-exporter/internal/frame"
	"github.com/lukaszraczylo/anker-exporter/internal/mqttsession"
)

// The login endpoint answered in ~22 s in testing, so the default 30 s client timeout is too tight.
const apiTimeout = 120 * time.Second

var apiHosts = map[string]string{
	"eu":  "https://ankerpower-api-eu.anker.com",
	"com": "https://ankerpower-api.anker.com",
}

type record struct {
	Topic   string `json:"topic"`
	Payload string `json:"payload"`
	TS      string `json:"ts"`
}

func main() {
	dur := flag.Duration("duration", 60*time.Second, "capture duration")
	out := flag.String("out", "capture/frames.jsonl", "output file (keep it out of git)")
	trigger := flag.Bool("trigger", false, "publish one realtime trigger (0057) after subscribing")
	timeout := flag.Int("timeout", 60, "trigger timeout in seconds")
	recordDir := flag.String("record-api", "", "write sanitised login, device and MQTT-info responses to this directory and exit")
	rawDir := flag.String("raw-dir", "", "with -record-api, also save the raw bodies here (keep it out of the repository)")
	fromRaw := flag.String("from-raw", "", "with -record-api, sanitise raw bodies saved in this directory instead of logging in")
	flag.Parse()
	if *recordDir != "" {
		if err := runRecord(*recordDir, *rawDir, *fromRaw); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(*dur, *out, *trigger, *timeout); err != nil {
		log.Fatal(err)
	}
}

func runRecord(outDir, rawDir, fromRaw string) error {
	if fromRaw != "" {
		return sanitiseRaw(fromRaw, outDir)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	region := strings.ToLower(envOr("ANKER_REGION", "eu"))
	base, ok := apiHosts[region]
	if !ok {
		return fmt.Errorf("unknown ANKER_REGION %q", region)
	}
	return recordAPI(ctx, base, os.Getenv("ANKER_EMAIL"), os.Getenv("ANKER_PASSWORD"), envOr("ANKER_COUNTRY", "GB"), outDir, rawDir)
}

func run(dur time.Duration, out string, trigger bool, timeoutSec int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	region := strings.ToLower(envOr("ANKER_REGION", "eu"))
	base, ok := apiHosts[region]
	if !ok {
		return fmt.Errorf("unknown ANKER_REGION %q", region)
	}
	cl, err := anker.NewClient(base, os.Getenv("ANKER_EMAIL"), os.Getenv("ANKER_PASSWORD"), envOr("ANKER_COUNTRY", "GB"), &http.Client{Timeout: apiTimeout})
	if err != nil {
		return err
	}
	if err = cl.Login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	devs, err := cl.Devices(ctx)
	if err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	sn := os.Getenv("ANKER_DEVICE_SN")
	if sn == "" {
		if len(devs) == 0 {
			return fmt.Errorf("no A1761 device on the account")
		}
		sn = devs[0].SN
	}
	fmt.Fprintf(os.Stderr, "devices found: %d\n", len(devs))
	info, err := cl.MQTTInfo(ctx)
	if err != nil {
		return fmt.Errorf("mqtt info: %w", err)
	}

	if err = os.MkdirAll(dirOf(out), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is an operator flag
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var (
		mu     sync.Mutex
		enc    = json.NewEncoder(f)
		counts = map[string]int{}
		werr   error
	)
	sess, err := mqttsession.Connect(ctx, info, func(topic string, payload []byte) {
		mu.Lock()
		defer mu.Unlock()
		counts[topicTail(topic)]++
		if encErr := enc.Encode(record{Topic: topic, Payload: string(payload), TS: time.Now().UTC().Format(time.RFC3339Nano)}); encErr != nil {
			werr = encErr
		}
	})
	if err != nil {
		return err
	}
	defer sess.Close()

	if err := sess.Subscribe(mqttsession.TelemetryTopic(info.AppName, sn)); err != nil {
		return err
	}
	log.Printf("subscribed to dt/%s/A1761/<sn>/#", info.AppName)

	if trigger {
		msg, err := frame.BuildTrigger(sn, info.UserID, frame.HeadClientID(info.AppName, info.UserID, info.CertificateID), timeoutSec, time.Now())
		if err != nil {
			return err
		}
		if err := sess.Publish(mqttsession.CommandTopic(info.AppName, sn), msg); err != nil {
			return err
		}
		log.Printf("published realtime trigger, timeout %ds", timeoutSec)
	}

	select {
	case <-time.After(dur):
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for k, n := range counts {
		total += n
		log.Printf("messages on %s: %d", k, n)
	}
	log.Printf("total messages: %d", total)
	return werr
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

// topicTail drops the serial so logs and counts carry no device identifier.
func topicTail(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) > 4 {
		return parts[0] + "/" + strings.Join(parts[4:], "/")
	}
	return topic
}
