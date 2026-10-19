package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"
)

// fakeClaude is Claude's token endpoint for the code exchange as Claude
// Code sends it (JSON, with state).
type fakeClaude struct {
	mu     sync.Mutex
	bodies []map[string]string
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var b map[string]string
	if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&b) != nil {
		http.Error(w, "bad", 400)
		return
	}
	f.mu.Lock()
	f.bodies = append(f.bodies, b)
	f.mu.Unlock()
	if b["grant_type"] != "authorization_code" || b["client_id"] != claudeClientID || b["code"] != "good-code" || b["code_verifier"] == "" || b["state"] == "" {
		jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	jsonResponse(w, 200, map[string]any{"access_token": "at-SECRET", "refresh_token": "rt-SECRET", "expires_in": 28800, "token_type": "Bearer", "scope": claudeScopes})
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
