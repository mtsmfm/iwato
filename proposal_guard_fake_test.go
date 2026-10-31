//go:build uitest

package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// A decision carries the digest of the proposal the owner was shown; one
// that changed since is refused, approve and reject alike.
func TestDecisionRefusesAChangedProposal(t *testing.T) {
	rule := &v1.NetworkPolicyRule{Name: "gh", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.github.com", Port: 443}}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{{ID: "c1", Status: "pending", ReviewToken: "t1", Binary: "/usr/bin/gh", ProposedRule: rule}}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}, newSandbox("dev"), context.Background()
	v, _ := o.Proposals(ctx, w)
	shown := v.Pending[0].Digest
	if shown != testDigest(o, ctx, w, requestID("c1")) || len(shown) != 64 {
		t.Fatal("the list and the rule disagree on the digest:", shown)
	}
	// The proposal gains a program after the owner saw it.
	changed := *rule
	changed.Binaries = append(slices.Clone(rule.Binaries), v1.PolicyNetworkBinary{Path: "/**"})
	f.chunks[0].ProposedRule = &changed
	for _, action := range []string{"approve", "reject"} {
		if _, err := o.DecideProposal(ctx, w, action, requestID("c1"), shown, nil); codeOf(err) != "proposal_changed" {
			t.Fatalf("%s of a changed proposal: %v", action, err)
		}
	}
	if len(f.approved) != 0 || len(f.rejected) != 0 {
		t.Fatal("decided:", f.approved, f.rejected)
	}
	// Through the server: a coded 409, and a missing digest is a 400.
	s, _, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	id := requestID("fake-uploads")
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+id+`","digest":"`+strings.Repeat("0", 64)+`"}`); code != 409 || !strings.Contains(body, `"code":"proposal_changed"`) {
		t.Fatal(code, body)
	}
	for _, op := range []string{"approve", "reject"} {
		if code, body := do("POST", "/api/sandboxes/dev/proposals/"+op, `{"requestId":"`+id+`"}`); code != 400 || !strings.Contains(body, `"code":"proposal_digest_missing"`) {
			t.Fatal(op, code, body)
		}
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/reject", `{"requestId":"`+id+`"`+digestField(s.rt, id)+`}`); code != 200 {
		t.Fatal(code, body)
	}
}
