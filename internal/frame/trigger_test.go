package frame

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type testEnvelope struct {
	Head    map[string]any `json:"head"`
	Payload string         `json:"payload"`
}

func buildFixed(t *testing.T, timeout int) (testEnvelope, []byte, time.Time) {
	t.Helper()
	now := time.Unix(1756813256, 0)
	raw, err := BuildTrigger("TESTSN0001", "user-1", "android-anker_power-user-1-cert-1", timeout, now)
	if err != nil {
		t.Fatalf("BuildTrigger: %v", err)
	}
	var env testEnvelope
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	var pl struct {
		SN   string `json:"device_sn"`
		Acct string `json:"account_id"`
		Data string `json:"data"`
	}
	if err = json.Unmarshal([]byte(env.Payload), &pl); err != nil {
		t.Fatalf("payload is not a JSON string: %v", err)
	}
	if pl.SN != "TESTSN0001" || pl.Acct != "user-1" {
		t.Errorf("payload ids = %q %q", pl.SN, pl.Acct)
	}
	b, err := base64.StdEncoding.DecodeString(pl.Data)
	if err != nil {
		t.Fatalf("data is not base64: %v", err)
	}
	return env, b, now
}

func TestBuildTrigger_Envelope(t *testing.T) {
	env, _, now := buildFixed(t, 60)
	want := map[string]any{
		"version": "1.0.0.1", "client_id": "android-anker_power-user-1-cert-1",
		"sess_id": "1234-5678", "msg_seq": 1.0, "seed": 1.0,
		"timestamp": float64(now.Unix()), "cmd_status": 2.0, "cmd": 17.0,
		"sign_code": 1.0, "device_pn": "A1761", "device_sn": "TESTSN0001",
	}
	for k, v := range want {
		if env.Head[k] != v {
			t.Errorf("head[%s] = %v, want %v", k, env.Head[k], v)
		}
	}
	if len(env.Head) != len(want) {
		t.Errorf("head has %d keys, want %d", len(env.Head), len(want))
	}
}

func TestBuildTrigger_Frame(t *testing.T) {
	_, b, now := buildFixed(t, 300)
	if got := hex.EncodeToString(b[:2]); got != "ff09" {
		t.Errorf("prefix = %s", got)
	}
	if n := int(binary.LittleEndian.Uint16(b[2:4])); n != len(b) || n != 31 {
		t.Errorf("length field = %d, frame len %d, want 31", n, len(b))
	}
	if got := hex.EncodeToString(b[4:7]); got != "03000f" {
		t.Errorf("pattern = %s", got)
	}
	if got := hex.EncodeToString(b[7:9]); got != "0057" {
		t.Errorf("message type = %s", got)
	}
	ts := make([]byte, 4)
	binary.LittleEndian.PutUint32(ts, uint32(now.Unix())) //nolint:gosec // fixed test time
	want := "a10122" + "a2020101" + "a30503" + "2c010000" + "fe0503" + hex.EncodeToString(ts)
	if got := hex.EncodeToString(b[9 : len(b)-1]); got != want {
		t.Errorf("fields = %s\nwant     %s", got, want)
	}
	var x byte
	for _, c := range b[:len(b)-1] {
		x ^= c
	}
	if b[len(b)-1] != x {
		t.Errorf("checksum = %02x, want %02x", b[len(b)-1], x)
	}
	var all byte
	for _, c := range b {
		all ^= c
	}
	if all != 0 {
		t.Errorf("xor of whole frame = %02x, want 00", all)
	}
}

func TestBuildTrigger_Errors(t *testing.T) {
	now := time.Unix(1, 0)
	cases := map[string]struct {
		sn, user string
		timeout  int
	}{
		"empty sn":     {"", "u", 60},
		"empty user":   {"S", "", 60},
		"zero timeout": {"S", "u", 0},
		"negative":     {"S", "u", -5},
		"huge timeout": {"S", "u", 1 << 31},
		"below min":    {"S", "u", MinTriggerTimeout - 1},
		"above max":    {"S", "u", MaxTriggerTimeout + 1},
	}
	for name, c := range cases {
		if _, err := BuildTrigger(c.sn, c.user, "cid", c.timeout, now); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestHeadClientID(t *testing.T) {
	got := HeadClientID("anker_power", "u", "c")
	if got != "android-anker_power-u-c" || strings.Contains(got, " ") {
		t.Errorf("HeadClientID = %q", got)
	}
}

func TestBuildTrigger_AcceptsBounds(t *testing.T) {
	for _, timeout := range []int{MinTriggerTimeout, 300, MaxTriggerTimeout} {
		if _, err := BuildTrigger("S", "u", "cid", timeout, time.Unix(1, 0)); err != nil {
			t.Errorf("timeout %d: %v", timeout, err)
		}
	}
}
