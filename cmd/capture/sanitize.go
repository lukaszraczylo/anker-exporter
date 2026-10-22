package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	placeholderToken  = "TESTTOKEN"
	placeholderUser   = "TESTUSER0001"
	placeholderEmail  = "user@example.invalid"
	placeholderNick   = "TestUser"
	placeholderSN     = "TESTSN0001"
	placeholderThing  = "TESTTHING0001"
	placeholderCertID = "TESTCERTID0001"
	placeholderKey    = "TESTKEY"
	placeholderText   = "TESTVALUE"
	fakePEMBody       = "VEVTVEZBS0U="
	minLearnedLen     = 4
)

// exactKeyPlaceholders maps a lowercase JSON key to the value that replaces its string content.
var exactKeyPlaceholders = map[string]string{
	"auth_token":     placeholderToken,
	"user_id":        placeholderUser,
	"email":          placeholderEmail,
	"nick_name":      placeholderNick,
	"nickname":       placeholderNick,
	"user_name":      placeholderNick,
	"device_sn":      placeholderSN,
	"sn":             placeholderSN,
	"thing_name":     placeholderThing,
	"certificate_id": placeholderCertID,
	"geo_key":        "TESTGEOKEY",
	"trace_id":       "TESTTRACE0001",
	"bt_ble_id":      "TESTBLEID0001",
	"img_url":        "https://example.invalid/device.png",
	"pkcs12":         placeholderKey,
}

// pemKeys hold key or certificate material.
var pemKeys = map[string]bool{
	"certificate_pem": true, "private_key": true, "public_key": true, "aws_root_ca1_pem": true,
}

// ambiguousKeyParts mark free-form personal or network values; they are replaced and listed in the report.
var ambiguousKeyParts = []string{"token", "secret", "password", "avatar", "ssid", "mac", "phone", "address_", "device_name", "wifi_name", "alias", "ip_addr"}

var (
	pemRe   = regexp.MustCompile(`(?s)-----BEGIN ([A-Z0-9 ]+)-----.*?-----END ([A-Z0-9 ]+)-----(\n?)`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	jwtRe   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	runRe   = regexp.MustCompile(`[A-Za-z0-9+/_=-]{40,}`)
)

type sanitizer struct {
	learned map[string]string
}

// sanitizeBodies replaces every account-specific value in the JSON bodies and keeps keys, nesting and value types.
// Identifiers learned from any body are also replaced inside the others, so the fixtures stay consistent.
func sanitizeBodies(bodies ...[]byte) ([][]byte, error) {
	s := &sanitizer{learned: map[string]string{}}
	docs := make([]any, len(bodies))
	for i, b := range bodies {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&docs[i]); err != nil {
			return nil, fmt.Errorf("body %d: %w", i, err)
		}
		s.learn("", docs[i])
	}
	out := make([][]byte, len(docs))
	for i, d := range docs {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s.apply("", d)); err != nil {
			return nil, err
		}
		out[i] = buf.Bytes()
	}
	return out, nil
}

func (s *sanitizer) learn(key string, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			s.learn(strings.ToLower(k), e)
		}
	case []any:
		for _, e := range x {
			s.learn(key, e)
		}
	case string:
		if ph, ok := exactKeyPlaceholders[key]; ok && len(x) >= minLearnedLen && x != ph {
			s.learned[x] = ph
		}
		if ph, ok := keyPlaceholder(key); ok && !pemKeys[key] && len(x) >= minLearnedLen && x != ph && !strings.Contains(x, "-----BEGIN") {
			s.learned[x] = ph
		}
	}
}

// keyPlaceholder returns the replacement for a string held under key, if the key is sensitive.
func keyPlaceholder(key string) (string, bool) {
	if ph, ok := exactKeyPlaceholders[key]; ok {
		return ph, true
	}
	if pemKeys[key] {
		return placeholderKey, true
	}
	for _, part := range ambiguousKeyParts {
		if strings.Contains(key, part) {
			if strings.Contains(part, "token") {
				return placeholderToken, true
			}
			return placeholderText, true
		}
	}
	return "", false
}

func (s *sanitizer) apply(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = s.apply(strings.ToLower(k), e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = s.apply(key, e)
		}
		return out
	case string:
		return s.scrubString(key, x)
	case json.Number:
		if key == "token_id" {
			return json.Number("1")
		}
		return v
	default:
		return v
	}
}

func (s *sanitizer) scrubString(key, v string) string {
	if v == "" {
		return v
	}
	if strings.Contains(v, "-----BEGIN") {
		return pemRe.ReplaceAllStringFunc(v, fakePEMBlock)
	}
	if ph, ok := keyPlaceholder(key); ok {
		return ph
	}
	reals := make([]string, 0, len(s.learned))
	for r := range s.learned {
		reals = append(reals, r)
	}
	sort.Slice(reals, func(i, j int) bool { return len(reals[i]) > len(reals[j]) })
	for _, r := range reals {
		v = strings.ReplaceAll(v, r, s.learned[r])
	}
	v = emailRe.ReplaceAllString(v, placeholderEmail)
	v = jwtRe.ReplaceAllString(v, placeholderToken)
	return runRe.ReplaceAllString(v, placeholderToken)
}

func fakePEMBlock(block string) string {
	m := pemRe.FindStringSubmatch(block)
	return "-----BEGIN " + m[1] + "-----\n" + fakePEMBody + "\n-----END " + m[2] + "-----" + m[3]
}
