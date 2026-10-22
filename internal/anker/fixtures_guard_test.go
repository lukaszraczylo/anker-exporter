package anker

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	allowedEmail   = "user@example.invalid"
	allowedPEMBody = "VEVTVEZBS0U="
)

// runAllowlist names fixtures whose payloads are base64 frames, not credentials.
var runAllowlist = map[string]bool{"frames.jsonl": true}

var (
	guardEmailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	guardPEMRe   = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----(.*?)-----END [A-Z0-9 ]+-----`)
	guardRunRe   = regexp.MustCompile(`[A-Za-z0-9+/_=-]{40,}`)
)

// scanSecrets names the rules a fixture breaks. It never returns the matching text, so a failure cannot leak it.
func scanSecrets(name string, content []byte) []string {
	var broken []string
	s := string(content)
	for _, m := range guardEmailRe.FindAllString(s, -1) {
		if m != allowedEmail {
			broken = append(broken, "email address")
			break
		}
	}
	for _, m := range guardPEMRe.FindAllStringSubmatch(s, -1) {
		body := strings.Join(strings.Fields(strings.ReplaceAll(m[1], `\n`, "")), "")
		if body != allowedPEMBody {
			broken = append(broken, "PEM block with a real body")
			break
		}
	}
	if !runAllowlist[filepath.Base(name)] && guardRunRe.MatchString(guardPEMRe.ReplaceAllString(s, "")) {
		broken = append(broken, "base64 or hex run of 40 or more characters")
	}
	return broken
}

func TestFixtures_ContainNoSecrets(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	scanned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // walks the repository's own testdata
		if err != nil {
			return err
		}
		scanned++
		for _, rule := range scanSecrets(p, b) {
			t.Errorf("%s: %s", p, rule)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 4 {
		t.Fatalf("scanned %d files, want the API fixtures and frames.jsonl", scanned)
	}
}

func TestScanSecrets_FlagsEachRuleWithoutQuotingTheValue(t *testing.T) {
	long := strings.Repeat("aB3dE5fG", 6)
	cases := map[string]string{
		"email address":              `{"mail":"someone@corp.example.org"}`,
		"PEM block with a real body": "-----BEGIN CERTIFICATE-----\\nTWFkZSB1cCBib2R5\\n-----END CERTIFICATE-----",
		"base64 or hex run":          `{"k":"` + long + `"}`,
	}
	for want, in := range cases {
		got := scanSecrets("x.json", []byte(in))
		if len(got) != 1 || !strings.HasPrefix(got[0], want) {
			t.Errorf("%q: got %v", want, got)
		}
		for _, g := range got {
			if strings.Contains(g, "corp") || strings.Contains(g, long) {
				t.Errorf("rule text quotes the value: %q", g)
			}
		}
	}
	clean := `{"mail":"` + allowedEmail + `","pem":"-----BEGIN CERTIFICATE-----\n` + allowedPEMBody + `\n-----END CERTIFICATE-----\n"}`
	if got := scanSecrets("x.json", []byte(clean)); len(got) != 0 {
		t.Errorf("clean fixture flagged: %v", got)
	}
	if got := scanSecrets("frames.jsonl", []byte(long)); len(got) != 0 {
		t.Errorf("allow-listed frames flagged: %v", got)
	}
}

const fixtureSerial = "TESTSN0001"

var (
	serialLikeRe = regexp.MustCompile(`[A-Z0-9]{8,}`)
	letterRe     = regexp.MustCompile(`[A-Z]`)
	printableRe  = regexp.MustCompile(`[\x20-\x7e]{8,}`)
)

// serialTokens returns every serial-like token in a recorded frame: the topic, the head, the inner sn and the decoded binary body.
// The base64 data field itself is skipped, since its uppercase runs look like serials.
func serialTokens(t *testing.T, line []byte) (tokens []string, innerSN string) {
	t.Helper()
	var rec struct{ Topic, Payload string }
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("frame record: %v", err)
	}
	var outer struct {
		Payload string
		Head    json.RawMessage
	}
	if err := json.Unmarshal([]byte(rec.Payload), &outer); err != nil {
		t.Fatalf("frame envelope: %v", err)
	}
	var inner struct{ Data, SN string }
	if err := json.Unmarshal([]byte(outer.Payload), &inner); err != nil {
		t.Fatalf("frame inner payload: %v", err)
	}
	body, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("frame data is not base64: %v", err)
	}
	texts := []string{rec.Topic, string(outer.Head), inner.SN}
	for _, run := range printableRe.FindAll(body, -1) {
		texts = append(texts, string(run))
	}
	for _, txt := range texts {
		for _, tok := range serialLikeRe.FindAllString(txt, -1) {
			if letterRe.MatchString(tok) { // all-digit runs are timestamps
				tokens = append(tokens, tok)
			}
		}
	}
	return tokens, inner.SN
}

func TestFixtures_FramesCarryOnlyTheTestSerial(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	frames, seen := 0, 0
	for sc.Scan() {
		frames++
		tokens, innerSN := serialTokens(t, sc.Bytes())
		if innerSN != fixtureSerial {
			t.Errorf("frame %d: inner sn is not the test serial", frames)
		}
		for _, tok := range tokens {
			seen++
			if tok != fixtureSerial {
				t.Errorf("frame %d: serial-like token other than the test serial", frames)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if frames == 0 || seen < frames {
		t.Fatalf("scanned %d frames and %d serial-like tokens, want at least one per frame", frames, seen)
	}
}

func TestSerialTokens_FindsASerialHiddenInTheBinaryBody(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("\x01\x02REALSERIAL12345\x03"))
	inner, _ := json.Marshal(map[string]string{"data": body, "sn": fixtureSerial})
	outer, _ := json.Marshal(map[string]string{"payload": string(inner)})
	line, _ := json.Marshal(map[string]string{"topic": "dt/anker_power/A1761/" + fixtureSerial + "/param_info", "payload": string(outer)})
	tokens, _ := serialTokens(t, line)
	found := false
	for _, tok := range tokens {
		found = found || tok == "REALSERIAL12345"
	}
	if !found {
		t.Errorf("tokens = %v, want the hidden serial", tokens)
	}
}
