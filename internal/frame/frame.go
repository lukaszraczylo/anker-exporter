package frame

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var (
	ErrChecksum = errors.New("frame: checksum mismatch")
	ErrLength   = errors.New("frame: truncated or inconsistent length")
	ErrPrefix   = errors.New("frame: bad prefix")
)

const (
	headerLen    = 9
	msgTelemetry = "0405"

	wireStr  = 0x00
	wireUI   = 0x01
	wireSile = 0x02
	wireVar  = 0x03
	wireBin  = 0x04
)

// Message is one decoded frame.
type Message struct {
	Type    string
	Values  map[string]float64
	Strings map[string]string
	// Unknown holds hex tags absent from the field table.
	Unknown []string
}

// Decode parses an MQTT envelope carrying a base64 hex frame.
func Decode(envelope []byte) (Message, error) {
	raw, err := unwrap(envelope)
	if err != nil {
		return Message{}, err
	}
	return decodeFrame(raw)
}

func unwrap(envelope []byte) ([]byte, error) {
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(envelope, &env); err != nil {
		return nil, fmt.Errorf("frame: envelope: %w", err)
	}
	inner := []byte(env.Payload)
	if len(inner) == 0 {
		return nil, errors.New("frame: envelope has no payload")
	}
	if inner[0] == '"' {
		var s string
		if err := json.Unmarshal(inner, &s); err != nil {
			return nil, fmt.Errorf("frame: payload string: %w", err)
		}
		inner = []byte(s)
	}
	var pl struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(inner, &pl); err != nil {
		return nil, fmt.Errorf("frame: payload: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(pl.Data)
	if err != nil {
		return nil, fmt.Errorf("frame: data base64: %w", err)
	}
	return raw, nil
}

func decodeFrame(b []byte) (Message, error) {
	if len(b) < headerLen+1 {
		return Message{}, ErrLength
	}
	if b[0] != 0xff || b[1] != 0x09 {
		return Message{}, ErrPrefix
	}
	if int(binary.LittleEndian.Uint16(b[2:4])) != len(b) {
		return Message{}, ErrLength
	}
	var sum byte
	for _, v := range b[:len(b)-1] {
		sum ^= v
	}
	if sum != b[len(b)-1] {
		return Message{}, ErrChecksum
	}
	msg := Message{
		Type:    hex.EncodeToString(b[7:9]),
		Values:  map[string]float64{},
		Strings: map[string]string{},
	}
	if msg.Type != msgTelemetry {
		return msg, nil
	}
	body := b[headerLen : len(b)-1]
	// An optional counter byte follows the type unless it already looks like a field tag (a0-a9).
	if len(body) > 0 && (body[0] < 0xa0 || body[0] > 0xa9) {
		body = body[1:]
	}
	return msg, decodeFields(body, &msg)
}

func decodeFields(body []byte, msg *Message) error {
	for len(body) > 0 {
		if len(body) < 2 {
			return ErrLength
		}
		tag, n := body[0], int(body[1])
		if len(body) < 2+n {
			return ErrLength
		}
		data := body[2 : 2+n]
		body = body[2+n:]
		def, ok := a1761Index[tag]
		if !ok {
			msg.Unknown = append(msg.Unknown, hex.EncodeToString([]byte{tag}))
			continue
		}
		if len(data) < 2 {
			continue // single raw byte without a type marker, not a value field
		}
		applyField(def, data[0], data[1:], msg)
	}
	return nil
}

func applyField(def *fieldDef, wire byte, val []byte, msg *Message) {
	switch wire {
	case wireStr:
		if def.kind == kindText {
			msg.Strings[def.name] = printable(val)
		}
	case wireBin:
		for _, bf := range def.bytes {
			if bf.offset < len(val) {
				msg.Values[bf.name] = float64(val[bf.offset])
			}
		}
	case wireUI, wireSile, wireVar:
		n, ok := integer(def, wire, val)
		if !ok {
			return
		}
		switch def.kind {
		case kindVersion:
			msg.Strings[def.name] = strings.Join(strings.Split(strconv.FormatInt(n, 10), ""), ".")
		case kindNumber:
			msg.Values[def.name] = scale(n, def.factor)
		}
	}
}

// integer follows the reference decoder: ui is unsigned unless flagged, sile
// and var are signed little-endian unless flagged unsigned.
func integer(def *fieldDef, wire byte, val []byte) (int64, bool) {
	size := len(val)
	switch wire {
	case wireUI:
		size = 1
	case wireSile:
		size = 2
	case wireVar:
		size = 4
		if def.values == 1 {
			size = 1
		}
	}
	if len(val) < size {
		return 0, false
	}
	signed := def.sign != signNo
	if wire == wireUI || (wire == wireVar && def.values == 1) {
		signed = def.sign == signYes
	}
	var u uint64
	for i := size - 1; i >= 0; i-- {
		u = u<<8 | uint64(val[i])
	}
	if signed {
		shift := 64 - 8*uint(size)
		return int64(u<<shift) >> shift, true //nolint:gosec // sign extension of a size-byte value
	}
	return int64(u), true //nolint:gosec // at most 4 bytes
}

func scale(n int64, factor float64) float64 {
	if factor == 0 {
		return float64(n)
	}
	// Round to the factor's decimal places to avoid 663.4000000000001.
	return math.Round(float64(n)*factor*1e6) / 1e6
}

func printable(b []byte) string {
	b = bytes.TrimSpace(b)
	var sb strings.Builder
	for _, r := range string(b) {
		if r >= 0x20 && r != 0x7f {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
