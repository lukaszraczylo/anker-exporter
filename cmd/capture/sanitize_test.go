package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Fake values only: nothing here is a real credential.
const (
	fakeToken  = "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5zA7bC9dE1" //nolint:gosec // synthetic
	fakeUser   = "0123456789abcdef0123456789abcdef"
	fakeSN     = "AZVWXYZ123456789"
	fakeEmail  = "someone@corp.example.org"
	fakeThing  = "fake-thing-name-00112233"
	fakeCertID = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	fakePEM    = "-----BEGIN CERTIFICATE-----\nMIIDWTCCAkGgAwIBAgIUQmFzZTY0RmFrZUJvZHlGb3JUZXN0\nZXN0IQ==\n-----END CERTIFICATE-----\n"
	fakeKey    = "-----BEGIN RSA PRIVATE KEY-----\nTWFkZSB1cCBrZXkgbWF0ZXJpYWwgZm9yIHRoZSB0ZXN0\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // synthetic
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func syntheticBodies(t *testing.T) (login, devices, mqtt []byte) {
	t.Helper()
	login = mustJSON(t, map[string]any{
		"code": 0, "msg": "success!",
		"data": map[string]any{
			"auth_token": fakeToken, "user_id": fakeUser, "email": fakeEmail,
			"nick_name": "Real Person", "token_expires_at": 4102444800, "ab": "GB",
			"profile": map[string]any{"avatar": "https://cdn.example.org/" + fakeUser + ".png", "gender": 1},
		},
	})
	devices = mustJSON(t, map[string]any{
		"code": 0, "msg": "success!",
		"data": map[string]any{"data": []any{
			map[string]any{"device_sn": fakeSN, "product_code": "A1761", "device_name": "Garage " + fakeSN, "wifi_online": true, "bind_time": 1700000000},
		}},
	})
	mqtt = mustJSON(t, map[string]any{
		"code": 0, "msg": "success!",
		"data": map[string]any{
			"endpoint_addr": "iot.example-region.amazonaws.com", "thing_name": fakeThing,
			"user_id": fakeUser, "certificate_id": fakeCertID, "app_name": "anker_power",
			"certificate_pem": fakePEM, "private_key": fakeKey, "aws_root_ca1_pem": fakePEM,
			"public_key": fakeToken, "pkcs12": fakeToken + "==",
			"topics": []any{"dt/anker_power/A1761/" + fakeSN + "/#", "cmd/" + fakeThing},
			"note":   "reach " + fakeEmail + " about " + fakeToken,
		},
	})
	return login, devices, mqtt
}

func TestSanitize_RemovesEverySensitiveValue(t *testing.T) {
	login, devices, mqtt := syntheticBodies(t)
	out, err := sanitizeBodies(login, devices, mqtt)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range out {
		s := string(b)
		for _, secret := range []string{fakeToken, fakeUser, fakeSN, fakeEmail, fakeThing, fakeCertID, "MIIDWTCC", "TWFkZSB1", "Real Person"} {
			if strings.Contains(s, secret) {
				t.Errorf("body %d still contains a sensitive value (%d chars starting %q)", i, len(secret), secret[:4])
			}
		}
	}
}

func TestSanitize_UsesFixedPlaceholders(t *testing.T) {
	login, devices, mqtt := syntheticBodies(t)
	out, err := sanitizeBodies(login, devices, mqtt)
	if err != nil {
		t.Fatal(err)
	}
	var l, d, m map[string]any
	for i, dst := range []*map[string]any{&l, &d, &m} {
		if err := json.Unmarshal(out[i], dst); err != nil {
			t.Fatal(err)
		}
	}
	ld := l["data"].(map[string]any)
	if ld["auth_token"] != "TESTTOKEN" || ld["user_id"] != "TESTUSER0001" || ld["email"] != "user@example.invalid" || ld["nick_name"] != "TestUser" {
		t.Errorf("login data = %v", ld)
	}
	dev := d["data"].(map[string]any)["data"].([]any)[0].(map[string]any)
	if dev["device_sn"] != "TESTSN0001" || dev["product_code"] != "A1761" {
		t.Errorf("device = %v", dev)
	}
	if dev["device_name"] == "Garage "+fakeSN || strings.Contains(dev["device_name"].(string), fakeSN) {
		t.Errorf("device_name leaks the serial: %v", dev["device_name"])
	}
	md := m["data"].(map[string]any)
	if md["endpoint_addr"] != "iot.example-region.amazonaws.com" || md["app_name"] != "anker_power" {
		t.Errorf("kept values changed: %v", md)
	}
	if md["user_id"] != "TESTUSER0001" || md["thing_name"] == fakeThing || md["certificate_id"] == fakeCertID {
		t.Errorf("mqtt ids = %v", md)
	}
	topics := md["topics"].([]any)
	if topics[0] != "dt/anker_power/A1761/TESTSN0001/#" {
		t.Errorf("topic = %v", topics[0])
	}
	for _, k := range []string{"certificate_pem", "private_key", "aws_root_ca1_pem"} {
		s := md[k].(string)
		if !strings.HasPrefix(s, "-----BEGIN ") || !strings.Contains(s, "-----END ") {
			t.Errorf("%s is not PEM shaped: %q", k, s)
		}
	}
	if !strings.HasPrefix(md["private_key"].(string), "-----BEGIN RSA PRIVATE KEY-----") {
		t.Errorf("PEM label not preserved: %q", md["private_key"])
	}
}

func shape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, e := range x {
			m[k] = shape(e)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = shape(e)
		}
		return out
	default:
		return reflect.TypeOf(v).String()
	}
}

func TestSanitize_PreservesKeysNestingAndTypes(t *testing.T) {
	login, devices, mqtt := syntheticBodies(t)
	out, err := sanitizeBodies(login, devices, mqtt)
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range [][]byte{login, devices, mqtt} {
		var a, b any
		if err := json.Unmarshal(in, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(out[i], &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(shape(a), shape(b)) {
			t.Errorf("body %d: shape changed\n in: %v\nout: %v", i, shape(a), shape(b))
		}
	}
}

func TestSanitize_CatchesUnlistedTokenLookingValues(t *testing.T) {
	in := mustJSON(t, map[string]any{"data": map[string]any{
		"mystery": fakeToken, "hexish": fakeCertID, "mail": "a.b@c.example.org",
		"jwt": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJl", "count": 7, "ok": true, "none": nil,
	}})
	out, err := sanitizeBodies(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out[0])
	for _, secret := range []string{fakeToken, fakeCertID, "a.b@c.example.org", "eyJhbGci"} {
		if strings.Contains(s, secret) {
			t.Errorf("unlisted secret survived: %.6s...", secret)
		}
	}
	var got struct{ Data map[string]any }
	if err := json.Unmarshal(out[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.Data["count"] != float64(7) || got.Data["ok"] != true || got.Data["none"] != nil {
		t.Errorf("non-string values changed: %v", got.Data)
	}
}

func TestSanitize_RejectsInvalidJSON(t *testing.T) {
	if _, err := sanitizeBodies([]byte("not json")); err == nil {
		t.Fatal("expected error")
	}
}

func TestSanitize_ReplacesShortIdentifiersAndURLs(t *testing.T) {
	in := mustJSON(t, map[string]any{"data": map[string]any{
		"geo_key": "0123456789abcdef0123456789abcdef", "trace_id": "fedcba9876543210fedcba9876543210",
		"bt_ble_id": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEE", "img_url": "https://cdn.example.org/p/9f8e7d.png",
		"mac_addr": "aa:bb:cc:dd:ee:ff", "wifi_name": "HomeNet", "msg": "success!", "token_id": 257019648,
	}})
	out, err := sanitizeBodies(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out[0]), "257019648") {
		t.Error("numeric token_id survived")
	}
	var got struct{ Data map[string]any }
	if err := json.Unmarshal(out[0], &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"geo_key", "trace_id", "bt_ble_id", "img_url", "mac_addr", "wifi_name"} {
		v, _ := got.Data[k].(string)
		if !strings.HasPrefix(v, "TEST") && !strings.HasPrefix(v, "https://example.invalid") {
			t.Errorf("%s = %q, want a placeholder", k, v)
		}
	}
}
