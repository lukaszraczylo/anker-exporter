package anker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
