//go:build uitest

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// GET /api/provider-profiles/ID: Iwato's file verbatim, or a gateway
// profile rendered; with a session only.
func TestProviderProfileAPI(t *testing.T) {
	s, _, do, _ := providerServerCookie(t)
	var got struct{ ID, Source, File, Scope, YAML string }
	code, body := do("GET", "/api/provider-profiles/iwato-github", "")
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatal(code, body)
	}
	file, _ := profileFiles.ReadFile("profiles/iwato-github.yaml")
	if got.Source != "iwato" || got.File != "profiles/iwato-github.yaml" || got.YAML != string(file) || !strings.Contains(got.YAML, "# Iwato's GitHub provider profile") {
		t.Fatalf("%+v", got)
	}
	got = struct{ ID, Source, File, Scope, YAML string }{}
	code, body = do("GET", "/api/provider-profiles/openai", "")
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatal(code, body)
	}
	p, err := parseProfileYAML([]byte(got.YAML))
	if got.Source != "gateway" || got.Scope != "platform" || got.File != "" || err != nil || p.Id != "openai" || !p.InferenceCapable || strings.Contains(got.YAML, "source:") {
		t.Fatalf("%+v %v", got, err)
	}
	for path, want := range map[string]int{"/api/provider-profiles/nope": 404, "/api/provider-profiles/-x": 400, "/api/provider-profiles/a/b": 400, "/api/provider-profiles/": 400, "/api/provider-profiles/a%20b": 400} {
		if code, body := do("GET", path, ""); code != want {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	if code, _ := do("POST", "/api/provider-profiles/openai", "{}"); code != 404 {
		t.Error("POST:", code)
	}
	if w := reviewRequest(s, "GET", "/api/provider-profiles/iwato-github", "", s.origin, "same-origin", s.host, nil, ""); w.Code != 401 {
		t.Error("without a session:", w.Code)
	}
}
