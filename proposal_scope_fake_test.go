//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// Widening from the approve dialog: the scope gains the repository, no
// policy rule is added, and once the sandbox applied the profile the
// proposal is closed with the reason the agent reads on /wait.
func TestWidenProposalClosesAfterTheScopeApplies(t *testing.T) {
	oldPoll, oldDelay := scopeApplyPoll, fakeApplyDelay
	scopeApplyPoll, fakeApplyDelay = 10*time.Millisecond, 50*time.Millisecond
	defer func() { scopeApplyPoll, fakeApplyDelay = oldPoll, oldDelay }()
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-work"}`); code != 200 {
		t.Fatal(code, body)
	}
	view := func() ProposalView {
		code, body := do("GET", "/api/sandboxes/dev/proposals", "")
		var v ProposalView
		if code != 200 || json.Unmarshal([]byte(body), &v) != nil {
			t.Fatal(code, body)
		}
		return v
	}
	pending := func(v ProposalView, id string) *PendingProposal {
		i := slices.IndexFunc(v.Pending, func(p PendingProposal) bool { return p.ID == requestID(id) })
		if i < 0 {
			return nil
		}
		return &v.Pending[i]
	}
	v := view()
	if p := pending(v, "fake-git"); p == nil || p.ProviderScope == nil || p.ProviderScope.Provider != "github-work" || !slices.Equal(p.ProviderScope.Repos, []string{"mtsmfm/iwato"}) {
		t.Fatalf("%+v", p)
	}
	// Uninspected: no scope, the reason says so.
	if p := pending(v, "fake-uploads"); p == nil || p.ProviderScope == nil || !strings.Contains(p.ProviderScope.Reason, "検査せずに") {
		t.Fatalf("%+v", p)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/scope", `{"requestId":"`+requestID("fake-uploads")+`"`+digestField(s.rt, requestID("fake-uploads"))+`}`); code != 409 || !strings.Contains(body, "プロバイダーの範囲にできません") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/scope", `{"requestId":"`+requestID("fake-git")+`"`+digestField(s.rt, requestID("fake-git"))+`}`); code != 200 || !strings.Contains(body, `"closed":false`) {
		t.Fatal(code, body)
	}
	if sc, _ := s.providerScope("github-work"); sc.Mode != scopeRepos || !slices.Equal(sc.Repos, []string{"mtsmfm/iwato"}) {
		t.Fatalf("%+v", sc)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/scope", `{"requestId":"`+requestID("fake-git")+`"`+digestField(s.rt, requestID("fake-git"))+`}`); code != 409 {
		t.Fatal("twice:", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v = view()
		if pending(v, "fake-git") == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proposal was not closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	i := slices.IndexFunc(v.Rejected, func(r RejectedProposal) bool { return r.ID == requestID("fake-git") })
	if i < 0 || v.Rejected[i].ScopeProvider != "github-work" {
		t.Fatalf("%+v", v.Rejected)
	}
	c, _ := d.openShell().pol.GetDraft(context.Background(), openshellWorkspace, "dev")
	for _, ch := range c.Chunks {
		if ch.ID == "fake-git" && (!strings.Contains(ch.RejectionReason, "github-work's scope (added writes to mtsmfm/iwato)") || !strings.Contains(ch.RejectionReason, "retry the original request now") || !strings.HasSuffix(ch.RejectionReason, "(hits: 3)")) {
			t.Fatal(ch.RejectionReason)
		}
	}
	sb, _ := s.lookupSandbox("dev")
	rules, _ := d.PolicyRules(context.Background(), sb)
	if _, ok := rules["allow_github_com_443"]; ok {
		t.Fatal("the push became a policy rule")
	}
}

// Rules already in the policy on a provider's host are marked.
func TestPolicyMarksProviderHosts(t *testing.T) {
	s, _, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-git")+`"`+digestField(s.rt, requestID("fake-git"))+`}`); code != 200 {
		t.Fatal(code, body)
	}
	code, body := do("GET", "/api/sandboxes/dev/proposals", "")
	var v ProposalView
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil || v.Policy == nil {
		t.Fatal(code, body)
	}
	for _, r := range v.Policy.Rules {
		want := r.Name == "allow_github_com_443"
		if got := len(r.ProviderHosts) > 0; got != want || want && (r.ProviderHosts[0].Provider != "github-personal" || r.ProviderHosts[0].Host != "github.com") {
			t.Errorf("%s: %+v", r.Name, r.ProviderHosts)
		}
	}
}
