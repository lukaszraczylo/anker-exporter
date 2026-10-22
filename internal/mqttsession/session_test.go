package mqttsession

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

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
