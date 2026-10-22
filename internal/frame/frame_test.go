package frame

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

const capturePath = "../../testdata/frames.jsonl"

type captureRecord struct {
	Payload string `json:"payload"`
}

func loadCapture(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open(capturePath)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec captureRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("capture line: %v", err)
		}
		out = append(out, []byte(rec.Payload))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("capture is empty")
	}
	return out
}

// rawFrame extracts the binary frame from a captured envelope.
func rawFrame(t *testing.T, envelope []byte) []byte {
	t.Helper()
	var env struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatal(err)
	}
	var inner struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(env.Payload), &inner); err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wrap(t *testing.T, frame []byte) []byte {
	t.Helper()
	inner, err := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString(frame), "sn": "TESTSN0001", "pn": "A1761",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(map[string]any{"head": map[string]any{"cmd": 16}, "payload": string(inner)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func xor(b []byte) byte {
	var c byte
	for _, v := range b {
		c ^= v
	}
	return c
}

// buildFrame assembles a valid frame: header, fields, XOR checksum.
func buildFrame(msgType string, fields ...[]byte) []byte {
	body := []byte{0xff, 0x09, 0, 0, 0x03, 0x01, 0x0f}
	mt, err := hex.DecodeString(msgType)
	if err != nil {
		panic(err)
	}
	body = append(body, mt...)
	for _, f := range fields {
		body = append(body, f...)
	}
	n := len(body) + 1
	binary.LittleEndian.PutUint16(body[2:4], uint16(n)) //nolint:gosec // test frames are tiny
	return append(body, xor(body))
}

// pat is the a1 pattern field every real telemetry frame starts with.
var pat = []byte{0xa1, 0x01, 0x34}

func fld(tag byte, typ byte, val ...byte) []byte {
	n := byte(len(val) + 1) //nolint:gosec // test values are tiny
	return append([]byte{tag, n, typ}, val...)
}

func TestDecode_0405GoldenVectors(t *testing.T) {
	powers := []string{
		"ac_input_power", "ac_output_power", "usbc_1_power", "usbc_2_power",
		"usba_1_power", "usba_2_power", "dc_input_power", "photovoltaic_power", "output_power_total",
	}
	for i, env := range loadCapture(t) {
		m, err := Decode(env)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if m.Type != "0405" {
			t.Fatalf("line %d: type %q", i, m.Type)
		}
		soc, ok := m.Values["main_battery_soc"]
		if !ok || soc < 0 || soc > 100 {
			t.Errorf("line %d: main_battery_soc = %v (present %v)", i, soc, ok)
		}
		for _, name := range powers {
			v, ok := m.Values[name]
			if !ok || v < 0 {
				t.Errorf("line %d: %s = %v (present %v)", i, name, v, ok)
			}
		}
		if ac := m.Values["ac_output_power"]; ac > 2000 {
			t.Errorf("line %d: implausible ac_output_power %v", i, ac)
		}
		if got := m.Strings["device_sn"]; got != "TESTSN0001" {
			t.Errorf("line %d: device_sn = %q", i, got)
		}
	}
}

func TestDecode_FirstCapturedFrameValues(t *testing.T) {
	m, err := Decode(loadCapture(t)[0])
	if err != nil {
		t.Fatal(err)
	}
	wantNum := map[string]float64{
		"remaining_time_hours": 663.4, "ac_output_power": 449, "output_power_total": 449,
		"main_battery_soc": 100, "battery_soh": 100, "temperature": 30, "exp_1_temperature": 0,
		"ac_frequency": 50, "ac_input_limit": 600, "display_timeout_seconds": 300,
		"ac_output_mode": 2, "dc_12v_output_mode": 1, "temp_unit_fahrenheit": 0,
	}
	for k, want := range wantNum {
		if got, ok := m.Values[k]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, want)
		}
	}
	wantStr := map[string]string{"sw_version": "1.5.4", "sw_controller": "1.5.4", "exp_1_type": "A1761_30Ah"}
	for k, want := range wantStr {
		if got := m.Strings[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(m.Unknown) == 0 {
		t.Error("real frames carry tags outside the table; Unknown is empty")
	}
}

func TestDecode_BadChecksum(t *testing.T) {
	b := rawFrame(t, loadCapture(t)[0])
	b[40] ^= 0x01
	if _, err := Decode(wrap(t, b)); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
}

func TestDecode_Truncated(t *testing.T) {
	b := rawFrame(t, loadCapture(t)[0])
	for _, cut := range []int{0, 5, 9, 100, len(b) - 1} {
		if _, err := Decode(wrap(t, b[:cut])); !errors.Is(err, ErrLength) {
			t.Errorf("cut %d: err = %v, want ErrLength", cut, err)
		}
	}
}

func TestDecode_FieldOverrunsFrame(t *testing.T) {
	body := buildFrame("0405", pat, []byte{0xc1, 0x20, 0x01, 0x64})
	if _, err := Decode(wrap(t, body)); !errors.Is(err, ErrLength) {
		t.Fatalf("err = %v, want ErrLength", err)
	}
}

func TestDecode_UnknownTag(t *testing.T) {
	f := buildFrame("0405", pat, fld(0xc1, 0x01, 55), fld(0xee, 0x01, 7), fld(0xa6, 0x02, 0x2c, 0x01))
	m, err := Decode(wrap(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Unknown, "ee") || slices.Contains(m.Unknown, "c1") {
		t.Errorf("Unknown = %v, want ee listed and c1 not", m.Unknown)
	}
	if m.Values["main_battery_soc"] != 55 || m.Values["ac_output_power"] != 300 {
		t.Errorf("known values damaged: %v", m.Values)
	}
}

func TestDecode_NegativeTemperature(t *testing.T) {
	f := buildFrame("0405", pat, fld(0xbd, 0x01, 0xf6), fld(0xbe, 0x01, 0xfb))
	m, err := Decode(wrap(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if m.Values["temperature"] != -10 || m.Values["exp_1_temperature"] != -5 {
		t.Errorf("temps = %v / %v, want -10 / -5", m.Values["temperature"], m.Values["exp_1_temperature"])
	}
}

// The capture only has dd=0 with temperature 30, and the reference decoder
// labels the value degrees C without converting, so dd=1 must not change it.
func TestDecode_FahrenheitFlagLeavesTemperatureUnchanged(t *testing.T) {
	f := buildFrame("0405", pat, fld(0xbd, 0x01, 70), fld(0xdd, 0x01, 1))
	m, err := Decode(wrap(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if m.Values["temperature"] != 70 {
		t.Errorf("temperature = %v, want 70 unchanged", m.Values["temperature"])
	}
	if m.Values["temp_unit_fahrenheit"] != 1 {
		t.Errorf("temp_unit_fahrenheit = %v, want 1", m.Values["temp_unit_fahrenheit"])
	}
}

func TestDecode_OtherMessageTypeHasNoValues(t *testing.T) {
	f := buildFrame("0407", pat, fld(0xc1, 0x01, 55))
	m, err := Decode(wrap(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "0407" || len(m.Values) != 0 || len(m.Strings) != 0 {
		t.Errorf("got %+v", m)
	}
}

func TestDecode_IncrementByteAfterType(t *testing.T) {
	f := buildFrame("0405", []byte{0x07}, fld(0xc1, 0x01, 42))
	m, err := Decode(wrap(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if m.Values["main_battery_soc"] != 42 {
		t.Errorf("values = %v", m.Values)
	}
}

func TestDecode_MalformedEnvelope(t *testing.T) {
	cases := map[string]string{
		"not json":       "{",
		"inner not json": `{"payload":"{"}`,
		"bad base64":     `{"payload":"{\"data\":\"!!!\"}"}`,
		"no payload":     `{"head":{}}`,
	}
	for name, in := range cases {
		_, err := Decode([]byte(in))
		if err == nil || errors.Is(err, ErrChecksum) || errors.Is(err, ErrLength) {
			t.Errorf("%s: err = %v, want wrapped decode error", name, err)
		}
	}
}

func TestA1761Fields_OrderedAndUnique(t *testing.T) {
	fields := A1761Fields()
	if len(fields) == 0 {
		t.Fatal("no fields")
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Name == "" || f.Help == "" || seen[f.Name] {
			t.Errorf("bad or duplicate field %+v", f)
		}
		seen[f.Name] = true
	}
	for _, want := range []string{"main_battery_soc", "ac_output_power", "temperature", "ac_output_mode"} {
		if !seen[want] {
			t.Errorf("missing %s", want)
		}
	}
	if seen["device_sn"] || seen["sw_version"] {
		t.Error("text fields must not be metric fields")
	}
}

func TestDecode_ErrorDetailAndRaw(t *testing.T) {
	b := rawFrame(t, loadCapture(t)[0])
	_, err := Decode(wrap(t, b[:len(b)-1]))
	if !errors.Is(err, ErrLength) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "length field") {
		t.Errorf("no detail in %q", err)
	}
	var fe *FrameError
	if !errors.As(err, &fe) || len(fe.Raw) != len(b)-1 {
		t.Fatalf("FrameError = %+v", fe)
	}
}

// Known limitation: a counter byte in 0xa0-0xa9 is read as a field tag, so the frame fails with ErrLength.
func TestDecode_CounterByteInTagRange(t *testing.T) {
	f := buildFrame("0405", []byte{0xa3}, fld(0xc1, 0x01, 42))
	if _, err := Decode(wrap(t, f)); !errors.Is(err, ErrLength) {
		t.Fatalf("err = %v, want ErrLength from the mis-sliced counter byte", err)
	}
}
