package mqttsession

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/lukaszraczylo/anker-exporter/internal/anker"
)

func selfSigned(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func TestClientID_Format(t *testing.T) {
	re := regexp.MustCompile(`^thing-1_\d{5}$`)
	for range 20 {
		id, err := ClientID("thing-1")
		if err != nil || !re.MatchString(id) {
			t.Fatalf("ClientID = %q, %v", id, err)
		}
	}
}

func TestTLSConfig_Valid(t *testing.T) {
	c, k := selfSigned(t)
	cfg, err := TLSConfig(anker.MQTTInfo{Endpoint: "broker.example", CertPEM: c, KeyPEM: k, RootCAPEM: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 || cfg.RootCAs == nil || cfg.ServerName != "broker.example" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestTLSConfig_Errors(t *testing.T) {
	c, k := selfSigned(t)
	const secret = "SECRETKEYMATERIAL"
	cases := map[string]anker.MQTTInfo{
		"bad pair": {CertPEM: c, KeyPEM: secret, RootCAPEM: c},
		"bad root": {CertPEM: c, KeyPEM: k, RootCAPEM: "nope"},
	}
	for name, info := range cases {
		_, err := TLSConfig(info)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks key material", name)
		}
	}
}

func TestConnect_EmptyEndpoint(t *testing.T) {
	if _, err := Connect(t.Context(), anker.MQTTInfo{}, nil); err == nil {
		t.Error("expected error")
	}
}

func TestTopics(t *testing.T) {
	if got := TelemetryTopic("anker_power", "SN1"); got != "dt/anker_power/A1761/SN1/#" {
		t.Errorf("telemetry = %q", got)
	}
	if got := CommandTopic("anker_power", "SN1"); got != "cmd/anker_power/A1761/SN1/req" {
		t.Errorf("command = %q", got)
	}
}

func TestSubscribeDedupesResubscribeList(t *testing.T) {
	s := &Session{}
	s.addTopic("a")
	s.addTopic("a")
	s.addTopic("b")
	if len(s.topics) != 2 {
		t.Errorf("topics = %v, want 2 unique", s.topics)
	}
}

func TestClientOptions_NoAutoReconnectAndKeepalive(t *testing.T) {
	s := &Session{lost: make(chan struct{})}
	o := s.clientOptions("example.invalid", "id", &tls.Config{MinVersion: tls.VersionTLS12})
	if o.AutoReconnect {
		t.Error("auto reconnect must be off")
	}
	if o.KeepAlive != 30 || o.PingTimeout != 10*time.Second {
		t.Errorf("keepalive %d ping timeout %v", o.KeepAlive, o.PingTimeout)
	}
	if len(o.Servers) != 1 || o.Servers[0].Host != "example.invalid:8883" {
		t.Errorf("servers %v", o.Servers)
	}
}

func TestMaskTopic(t *testing.T) {
	cases := map[string]string{
		TelemetryTopic("anker_power", "AZSN123456789"): "dt/anker_power/A1761/.../#",
		CommandTopic("anker_power", "AZSN123456789"):   "cmd/anker_power/A1761/.../req",
		"short": "short",
	}
	for in, want := range cases {
		got := maskTopic(in)
		want = strings.Replace(want, "...", "...6789", 1)
		if got != want {
			t.Errorf("maskTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionErrors_DoNotLeakSerial(t *testing.T) {
	const sn = "AZSN123456789"
	s := &Session{client: mqtt.NewClient(mqtt.NewClientOptions())}
	errs := []error{
		s.Subscribe(TelemetryTopic("anker_power", sn)),
		s.Publish(CommandTopic("anker_power", sn), nil),
	}
	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error from an unconnected client")
		}
		if strings.Contains(err.Error(), sn) {
			t.Errorf("error leaks the serial: %v", err)
		}
	}
}
