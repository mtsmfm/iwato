//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"errors"
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

// refusingGateway refuses to approve one chunk.
type refusingGateway struct {
	*fakePolicy
	refuse string
}

func (r refusingGateway) ApproveDraftChunk(ctx context.Context, ws, sandbox, id, token string) (*v1.ApproveResult, error) {
	if id == r.refuse {
		return nil, errors.New("proposal is not applicable: refused")
	}
	return r.fakePolicy.ApproveDraftChunk(ctx, ws, sandbox, id, token)
}

// A batch is one commit and one whole policy, with a rule per proposal; a
// changed proposal and one OpenShell refuses stay waiting, the others go.
func TestApproveManyIsOneCommit(t *testing.T) {
	rule := func(host string) *v1.NetworkPolicyRule {
		return &v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: host, Port: 443, Ports: []uint32{443}}}}
	}
	f := &fakePolicy{}
	for _, id := range []string{"a", "changed", "refused", "b"} {
		f.chunks = append(f.chunks, v1.PolicyChunk{ID: id, Status: "pending", ReviewToken: "t", RuleName: "allow_" + id, Binary: "/usr/bin/curl", ProposedRule: rule(id + ".example.com")})
	}
	o, w, ctx := &OpenShell{pol: refusingGateway{f, "refused"}, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}, newSandbox("dev"), context.Background()
	var items []ProposalApproval
	for _, c := range f.chunks {
		items = append(items, ProposalApproval{ID: requestID(c.ID), Digest: proposalDigest(c)})
	}
	f.chunks[1].Binary = "/usr/bin/wget"
	batch, err := o.ApproveProposals(ctx, w, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Changes) != 2 || batch.Changes[0].Rule != "allow_a" || batch.Changes[1].Rule != "allow_b" || batch.Changes[0].Version == 0 || batch.Changes[0].Version != batch.Changes[1].Version {
		t.Fatalf("%+v", batch.Changes)
	}
	if len(batch.Skipped) != 2 || batch.Skipped[0].Code != "proposal_changed" || batch.Skipped[1].Code != "approve_many_refused" || batch.Skipped[1].ID != requestID("refused") {
		t.Fatalf("%+v", batch.Skipped)
	}
	if len(f.updates) != 1 || len(f.updates[0].Policy.NetworkPolicies) != 3 {
		t.Fatalf("applied %d times: %+v", len(f.updates), f.updates)
	}
	commits, err := o.PolicyCommits(ctx, w, 10)
	if err != nil || len(commits) != 2 || commits[0].Subject != "Approve 2 proposals" {
		t.Fatalf("%v %+v", err, commits)
	}
	if got := []string{f.chunks[0].Status, f.chunks[1].Status, f.chunks[2].Status, f.chunks[3].Status}; !slices.Equal(got, []string{"approved", "pending", "pending", "approved"}) {
		t.Fatal(got)
	}
}

// OpenShell rejects a waiting proposal an approval just covered: the batch
// approves it all the same, so that its /wait answers approved. One the
// owner rejected stays rejected.
func TestApproveManyCoveredProposal(t *testing.T) {
	rule := &v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "a.example.com", Port: 443, Ports: []uint32{443}}}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		{ID: "covered", Status: "rejected", RejectionReason: coveredRejection + "7", ReviewToken: "t", RuleName: "allow_a", Binary: "/usr/bin/curl", ProposedRule: rule},
		{ID: "owner", Status: "rejected", RejectionReason: rejectReason(1, ""), ReviewToken: "t", RuleName: "allow_b", Binary: "/usr/bin/curl", ProposedRule: rule},
	}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}
	batch, err := o.ApproveProposals(context.Background(), newSandbox("dev"), []ProposalApproval{
		{ID: requestID("covered"), Digest: proposalDigest(f.chunks[0])}, {ID: requestID("owner"), Digest: proposalDigest(f.chunks[1])},
	}, nil)
	if err != nil || len(batch.Changes) != 1 || len(batch.Skipped) != 1 || batch.Skipped[0].Code != "request_decided" {
		t.Fatalf("%v %+v", err, batch)
	}
	if f.chunks[0].Status != "approved" || f.chunks[1].Status != "rejected" {
		t.Fatal(f.chunks[0].Status, f.chunks[1].Status)
	}
}

// Through the server: a refusal is a code, and an apply that fails after
// OpenShell approved leaves a drift, as a single approval does.
func TestApproveManyThroughTheServer(t *testing.T) {
	s, d, do := providerServer(t)
	for _, step := range [][2]string{{"/api/sandboxes/dev/start", `{}`}, {"/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`}} {
		if code, body := do("POST", step[0], step[1]); code != 200 {
			t.Fatal(step[0], code, body)
		}
	}
	sb := newSandbox("dev")
	digest := func(id string) string { return testDigest(d, context.Background(), sb, requestID(id)) }
	entry := func(id, scope string) string {
		return `{"id":"` + requestID(id) + `","digest":"` + digest(id) + `"` + scope + `}`
	}
	reads := `,"scope":{"rules":[{"method":"GET","path":"/**"},{"method":"HEAD","path":"/**"}]}`
	// Uninspected where the provider's token goes: refused on its own, and
	// the others of the batch go on.
	d.gatewayCalls()
	code, body := do("POST", "/api/sandboxes/dev/proposals/approve-many", `{"approvals":[`+entry("fake-downloads", "")+`,`+entry("fake-uploads", "")+`,`+entry("fake-datadog", reads)+`]}`)
	if code != 200 {
		t.Fatal(code, body)
	}
	calls := d.gatewayCalls()
	if calls["UpdateConfig"] != 1 || calls["ApproveDraftChunk"] != 2 {
		t.Fatal(calls)
	}
	var out struct {
		Version  uint32
		Approved []struct{ ID, Rule string }
		Skipped  []ProposalSkip
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Version == 0 || len(out.Approved) != 2 || len(out.Skipped) != 1 || out.Skipped[0].ID != requestID("fake-uploads") || out.Skipped[0].Code != "credential_uninspected" {
		t.Fatal(body)
	}
	d.mu.Lock()
	var approved []string
	for _, c := range d.fakeDraft("dev") {
		if c.ID == "fake-downloads" || c.ID == "fake-datadog" {
			approved = append(approved, c.Status)
		}
	}
	d.mu.Unlock()
	if !slices.Equal(approved, []string{"approved", "approved"}) {
		t.Fatal(approved)
	}
	changes := slices.DeleteFunc(slices.Clone(s.policyChanges["dev"]), func(c PolicyChange) bool { return c.Kind != "approve" })
	if len(changes) != 2 || changes[0].Version != changes[1].Version {
		t.Fatalf("%+v", changes)
	}

	// Two for one rule name: OpenShell renames the second when the first
	// is approved, which is no change to it. Each keeps its own rule.
	code, body = do("POST", "/api/sandboxes/dev/proposals/approve-many", `{"approvals":[`+entry("fake-pr", "")+`,`+entry("fake-mise-gh", reads)+`]}`)
	if code != 200 || !strings.Contains(body, `"rule":"allow_api_github_com_443"`) || !strings.Contains(body, `"rule":"allow_api_github_com_443_2"`) || !strings.Contains(body, `"skipped":[]`) {
		t.Fatal(code, body)
	}

	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve-many", `{"approvals":[]}`); code != 400 {
		t.Fatal(code, body)
	}
	dup := entry("fake-git", "")
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve-many", `{"approvals":[`+dup+`,`+dup+`]}`); code != 400 {
		t.Fatal(code, body)
	}

	o := d.openShell()
	applied := o.updateConfig
	o.updateConfig = func(context.Context, *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
		return nil, errors.New("gateway down")
	}
	code, body = do("POST", "/api/sandboxes/dev/proposals/approve-many", `{"approvals":[`+entry("fake-git", "")+`]}`)
	o.updateConfig = applied
	if code != 409 || !strings.Contains(body, `"code":"approve_many_apply_failed"`) || !strings.Contains(body, requestID("fake-git")) {
		t.Fatal(code, body)
	}
	if st, err := d.PolicyStore(context.Background(), sb); err != nil || st.State != "drift" {
		t.Fatal(err, st)
	}
}
