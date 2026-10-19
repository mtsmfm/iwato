package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFailCode(t *testing.T) {
	w := httptest.NewRecorder()
	failCode(w, 400, "name_invalid", map[string]any{"max": 32}, "名前が正しくありません")
	var body struct {
		Error  string         `json:"error"`
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 400 {
		t.Fatalf("status %d, body %q: %v", w.Code, w.Body, err)
	}
	if body.Error != "名前が正しくありません" || body.Code != "name_invalid" || body.Params["max"] != float64(32) {
		t.Fatalf("body %+v", body)
	}
	w = httptest.NewRecorder()
	failCode(w, 401, "session_missing", nil, "x")
	if strings.Contains(w.Body.String(), "params") {
		t.Fatalf("empty params are left out: %s", w.Body)
	}
}

// Every code the server sends has a Japanese string in the page (the page
// falls back to the server's text otherwise, so a missing one goes unseen).
func TestErrorCodesHaveStrings(t *testing.T) {
	page, err := webFiles.ReadFile("web/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	ja := string(page)
	if i := strings.Index(ja, "const en = {"); i > 0 {
		ja = ja[:i]
	}
	// Each pattern's first group is a code; its string is prefix+code.
	uses := []struct {
		glob   string
		re     *regexp.Regexp
		prefix string
	}{
		{"*.go", regexp.MustCompile(`(?:(?:failCode(?:With)?\(w, \d+|rejectCode\(w, r, \d+), |(?:errCode(?:En)?|errRefused)\()"([^"]*)"`), "error."},
		{"*.go", regexp.MustCompile(`"(ambiguous_[a-z0-9_]+)"`), "error."},
		{"*.go", regexp.MustCompile(`(?:resetWarning|failedTo|warnTo)\("([^"]*)"`), "reset.warning."},
		{"*.go", regexp.MustCompile(`(?:prepState\("[a-z]*", "[a-z]*", |prepFailed\("[a-z]*", |report\()"([^"]*)"`), "images.state."},
		{"internal/openshell/gateway/*.go", regexp.MustCompile(`report\("([^"]*)"`), "images.state."},
		{"internal/ingress/*/*.go", regexp.MustCompile(`Code: "([^"]*)"`), "ingress.status."},
		{"internal/ingress/*/*.go", regexp.MustCompile(`fail\("([^"]*)"`), "ingress.status."},
	}
	found := 0
	for _, u := range uses {
		files, _ := filepath.Glob(u.glob)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range u.re.FindAllStringSubmatch(string(src), -1) {
				found++
				if !regexp.MustCompile(`^[a-z0-9_]{1,64}$`).MatchString(m[1]) {
					t.Errorf("%s: code %q is not lowercase snake_case", f, m[1])
				}
				if !strings.Contains(ja, "'"+u.prefix+m[1]+"':") {
					t.Errorf("%s: code %q has no '%s%s' in web/i18n.js (ja)", f, m[1], u.prefix, m[1])
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no failCode, rejectCode or errCode calls found")
	}
}

// The console's text of an error: coded errors in English, or as their
// code and params; the cause stays reachable.
func TestEnglish(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	inner := errCodeEn("openshell_unavailable", nil, "OpenShell を使えません: "+cause.Error(), "OpenShell is not usable: "+cause.Error(), cause)
	err := fmt.Errorf("sandbox dev: %w", inner)
	if got := english(err); got != "sandbox dev: OpenShell is not usable: dial tcp: refused" {
		t.Errorf("wrapped: %q", got)
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is lost")
	}
	if got := english(errCode("name_invalid", map[string]any{"max": 32, "a": "b"}, "名前が正しくありません")); got != "name_invalid (a=b, max=32)" {
		t.Errorf("without English: %q", got)
	}
	if english(nil) != "" || english(errors.New("plain")) != "plain" {
		t.Error("plain errors change")
	}
}
