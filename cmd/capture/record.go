package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
)

// apiFixtures lists the recorded endpoints in the order their bodies are sanitised.
var apiFixtures = []struct{ file, path string }{
	{"login.json", "passport/login"},
	{"devices.json", "power_service/v1/app/get_relate_and_bind_devices"},
	{"mqtt_info.json", "app/devicemanage/get_user_mqtt_info"},
}

// bodyRecorder keeps the last response body per URL path so the client code stays untouched.
type bodyRecorder struct {
	next   http.RoundTripper
	bodies map[string][]byte
	mu     sync.Mutex
}

func (r *bodyRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.bodies[strings.TrimPrefix(req.URL.Path, "/")] = data
	r.mu.Unlock()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

// recordAPI makes one live login, fetches devices and MQTT info, and writes sanitised bodies to outDir.
// Raw bodies go to rawDir when it is set; keep that directory outside the repository.
func recordAPI(ctx context.Context, base, email, password, country, outDir, rawDir string) error {
	rec := &bodyRecorder{next: http.DefaultTransport, bodies: map[string][]byte{}}
	cl, err := anker.NewClient(base, email, password, country, &http.Client{Timeout: apiTimeout, Transport: rec})
	if err != nil {
		return err
	}
	if err = cl.Login(ctx); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if _, err = cl.Devices(ctx); err != nil {
		return fmt.Errorf("devices: %w", err)
	}
	if _, err = cl.MQTTInfo(ctx); err != nil {
		return fmt.Errorf("mqtt info: %w", err)
	}
	if rawDir != "" {
		if err = os.MkdirAll(rawDir, 0o700); err != nil {
			return err
		}
	}
	raws := make([][]byte, len(apiFixtures))
	for i, f := range apiFixtures {
		raws[i] = rec.bodies[f.path]
		if len(raws[i]) == 0 {
			return fmt.Errorf("no response recorded for %s", f.file)
		}
		if rawDir != "" {
			if err = os.WriteFile(filepath.Join(rawDir, f.file), raws[i], 0o600); err != nil {
				return err
			}
		}
	}
	return writeSanitised(raws, outDir)
}

// sanitiseRaw re-sanitises raw bodies saved by an earlier recording, with no network access.
func sanitiseRaw(rawDir, outDir string) error {
	raws := make([][]byte, len(apiFixtures))
	for i, f := range apiFixtures {
		b, err := os.ReadFile(filepath.Join(rawDir, f.file)) //nolint:gosec // path is an operator flag
		if err != nil {
			return err
		}
		raws[i] = b
	}
	return writeSanitised(raws, outDir)
}

func writeSanitised(raws [][]byte, outDir string) error {
	clean, err := sanitizeBodies(raws...)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	for i, f := range apiFixtures {
		if err = os.WriteFile(filepath.Join(outDir, f.file), clean[i], 0o600); err != nil {
			return err
		}
		log.Printf("wrote %s", filepath.Join(outDir, f.file))
	}
	return nil
}
