package anker

import (
	"context"
	"crypto/md5" //nolint:gosec // gtoken is defined as md5(user_id)
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const testPassword = "s3cret-pw-value"

func loginOK(w http.ResponseWriter, token string) {
	_, _ = w.Write([]byte(`{"code":0,"msg":"success!","data":{"auth_token":"` + token + `","user_id":"uid-1","token_expires_at":4102444800}}`))
}

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "user@example.com", testPassword, "GB", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLogin_SendsExpectedBody(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/passport/login" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&body)
		loginOK(w, "tok")
	}))
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"Content-Type": "application/json", "Model-Type": "DESKTOP", "App-Name": "anker_power", "Os-Type": "android", "Country": "GB"} {
		if hdr.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, hdr.Get(k), v)
		}
	}
	if !strings.HasPrefix(hdr.Get("Timezone"), "GMT") {
		t.Errorf("timezone header = %q", hdr.Get("Timezone"))
	}
	if body["ab"] != "GB" || body["enc"] != float64(0) || body["email"] != "user@example.com" {
		t.Errorf("body = %v", body)
	}
	csi, _ := body["client_secret_info"].(map[string]any)
	if pk, _ := csi["public_key"].(string); !strings.HasPrefix(pk, "04") || len(pk) != 130 {
		t.Errorf("public_key = %v", csi["public_key"])
	}
	pw, _ := body["password"].(string)
	if pw == "" || pw == testPassword {
		t.Errorf("password not encrypted: %q", pw)
	}
	if _, ok := body["time_zone"].(float64); !ok {
		t.Errorf("time_zone = %v", body["time_zone"])
	}
	if tx, _ := body["transaction"].(string); tx == "" {
		t.Errorf("transaction = %v", body["transaction"])
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":26006,"msg":"Password error"}`))
	}))
	err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "26006") || !strings.Contains(err.Error(), "Password error") {
		t.Errorf("error lacks server code/msg: %v", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestMQTTInfo_ParsesFields(t *testing.T) {
	const wantGTok = "f3cf54d8f237d5d289f7ccd70d42cf31" //nolint:gosec // md5 of the test user id, not a credential
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/login":
			loginOK(w, "tok")
		case "/app/devicemanage/get_user_mqtt_info":
			if r.Header.Get("x-auth-token") != "tok" || r.Header.Get("gtoken") != wantGTok {
				t.Errorf("auth headers: %v", r.Header)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"success!","data":{"endpoint_addr":"ep.example","thing_name":"thing","certificate_pem":"CERT","private_key":"KEY","aws_root_ca1_pem":"CA","user_id":"u","certificate_id":"cid","app_name":"anker_power"}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	got, err := c.MQTTInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := MQTTInfo{Endpoint: "ep.example", ThingName: "thing", UserID: "u", CertificateID: "cid", AppName: "anker_power", CertPEM: "CERT", KeyPEM: "KEY", RootCAPEM: "CA"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestRequest_ReloginOnExpiredToken(t *testing.T) {
	var logins, calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/login":
			n := logins.Add(1)
			loginOK(w, map[int32]string{1: "old", 2: "new"}[n])
		default:
			calls.Add(1)
			if r.Header.Get("x-auth-token") != "new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"msg":"expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"endpoint_addr":"ep"}}`))
		}
	}))
	got, err := c.MQTTInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "ep" || logins.Load() != 2 || calls.Load() != 2 {
		t.Errorf("endpoint=%q logins=%d calls=%d", got.Endpoint, logins.Load(), calls.Load())
	}
}

func TestDevices_FiltersA1761(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/passport/login":
			loginOK(w, "tok")
		case "/power_service/v1/app/get_relate_and_bind_devices":
			_, _ = w.Write([]byte(`{"code":0,"data":{"data":[{"device_sn":"SN1","product_code":"A17C0"},{"device_sn":"SN2","product_code":"A1761"}]}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	got, err := c.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Device{SN: "SN2", PN: "A1761"}) {
		t.Errorf("got %+v", got)
	}
}

func TestNewClient_DefaultTimeout(t *testing.T) {
	c, err := NewClient("https://x.invalid", "e", "p", "GB", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.httpc.Timeout != DefaultTimeout {
		t.Fatalf("timeout %v", c.httpc.Timeout)
	}
}

func TestLogin_ServerRejectionIsAPIError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":26108,"msg":"Email address or password incorrect."}`))
	}))
	err := c.Login(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.Code != 26108 || ae.Msg == "" {
		t.Errorf("got %+v", ae)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Error("error leaks password")
	}
}

func TestLogin_TransportAndHTTP5xxAreNotAPIErrors(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	var ae *APIError
	if err := c.Login(context.Background()); err == nil || errors.As(err, &ae) {
		t.Fatalf("5xx err = %v", err)
	}
	c.base = "http://127.0.0.1:1"
	if err := c.Login(context.Background()); err == nil || errors.As(err, &ae) {
		t.Fatalf("transport err = %v", err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "api", name)) //nolint:gosec // fixed test path
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// replayClient serves each API path from its recorded fixture and checks the session headers on authed calls.
func replayClient(t *testing.T) *Client {
	t.Helper()
	files := map[string]string{
		"/passport/login": "login.json",
		"/power_service/v1/app/get_relate_and_bind_devices": "devices.json",
		"/app/devicemanage/get_user_mqtt_info":              "mqtt_info.json",
	}
	return newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/passport/login" {
			sum := md5.Sum([]byte("TESTUSER0001")) //nolint:gosec // gtoken is defined as md5(user_id)
			if r.Header.Get("X-Auth-Token") != "TESTTOKEN" || r.Header.Get("Gtoken") != hex.EncodeToString(sum[:]) {
				t.Errorf("%s: session headers do not match the recorded login", r.URL.Path)
			}
		}
		_, _ = w.Write(fixture(t, name))
	}))
}

func TestLogin_ReplaysRecordedResponse(t *testing.T) {
	c := replayClient(t)
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("TESTUSER0001")) //nolint:gosec // see replayClient
	if c.token != "TESTTOKEN" || c.gtoken != hex.EncodeToString(sum[:]) {
		t.Errorf("session = %q / %q", c.token, c.gtoken)
	}
}

func TestDevices_ReplaysRecordedResponse(t *testing.T) {
	got, err := replayClient(t).Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []Device{{SN: "TESTSN0001", PN: "A1761"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("devices = %+v, want %+v", got, want)
	}
}

func TestMQTTInfo_ReplaysRecordedResponse(t *testing.T) {
	got, err := replayClient(t).MQTTInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint == "" || got.AppName != "anker_power" || got.UserID != "TESTUSER0001" ||
		got.ThingName != "TESTTHING0001" || got.CertificateID != "TESTCERTID0001" {
		t.Errorf("info = %+v", got)
	}
	for name, pem := range map[string]string{"cert": got.CertPEM, "key": got.KeyPEM, "root": got.RootCAPEM} {
		if !strings.HasPrefix(pem, "-----BEGIN ") {
			t.Errorf("%s is not PEM shaped", name)
		}
	}
}
