//go:build uitest

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The route reads the fake's draft and policy, with no host or path.
func TestProposalStatsRoute(t *testing.T) {
	d := NewFake()
	s := newServer(testConfig(), d, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, cookie, csrf)
	if r := reviewRequest(s, "GET", "/api/sandboxes/dev/proposals/stats", "", "", "", s.host, nil, ""); r.Code != 401 {
		t.Fatal("without a session:", r.Code)
	}
	r := reviewRequest(s, "GET", "/api/sandboxes/dev/proposals/stats", "", "", "", s.host, cookie, "")
	var st ProposalStats
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &st) != nil || st.Chunks == 0 || st.Policy == nil || st.Policy.Rules == 0 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, leak := range []string{"github.com", "example.com", "/usr/", "/sandbox/", "/v1/", "push"} {
		if strings.Contains(r.Body.String(), leak) {
			t.Errorf("%q in %s", leak, r.Body)
		}
	}
}
