package anker

import (
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
