package main

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// testDigest is the digest the page sends for a proposal (proposalDigest).
func testDigest(rt proposedRuleRuntime, ctx context.Context, sb Sandbox, request string) string {
	_, d, _ := rt.ProposedRule(ctx, sb, request)
	return d
}

// digestField is the digest a decision carries, as JSON to append to the
// body: the runtime's, or any well-formed one for runtimes without it.
func digestField(rt Runtime, request string) string {
	d := strings.Repeat("0", 64)
	if pr, ok := rt.(proposedRuleRuntime); ok {
		d = testDigest(pr, context.Background(), newSandbox("dev"), request)
	}
	return `,"digest":"` + d + `"`
}

// fakePolicy is a sandbox's draft as the gateway keeps it.
type fakePolicy struct {
	v1.PolicyInterface
	chunks   []v1.PolicyChunk
	approved []string // chunk ID + review token
	rejected []string
	// status, when set, is the latest policy revision; revisions its history.
	status    *v1.PolicyStatusResult
	revisions []v1.SandboxPolicyRevision
	// approveVersion is the revision an approval answers (0: none).
	approveVersion uint32
	// providerRules are the attached providers' rules the effective
	// policy adds (GetSandboxConfig); configErr fails it.
	providerRules map[string]v1.NetworkPolicyRule
	configErr     error
	// draftVersion is the draft's version; reads count GetDraft by status.
	draftVersion uint64
	reads        map[string]int
	// updates are the whole policies applied (update).
	updates []*v1.ConfigUpdate
}

// sandboxConfig is the effective policy: the latest revision's rules with
// the providers'.
func (f *fakePolicy) sandboxConfig(ctx context.Context, w Sandbox) (*v1.SandboxConfig, error) {
	if f.configErr != nil {
		return nil, f.configErr
	}
	st, _ := f.GetStatus(ctx, "", w.Name)
	rules := map[string]v1.NetworkPolicyRule{}
	if st.Revision.Policy != nil {
		maps.Copy(rules, st.Revision.Policy.NetworkPolicies)
	}
	maps.Copy(rules, f.providerRules)
	return &v1.SandboxConfig{PolicyVersion: st.Revision.Version, Policy: &v1.SandboxPolicy{NetworkPolicies: rules}}, nil
}

// GetDraft answers the chunks of the status asked for, as the gateway;
// reads counts the calls by status ("" for all).
func (f *fakePolicy) GetDraft(_ context.Context, _, _ string, opts ...v1.GetDraftOption) (*v1.DraftPolicy, error) {
	status := draftStatusFilter(opts)
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	f.reads[status]++
	var chunks []v1.PolicyChunk
	for _, c := range f.chunks {
		if status == "" || c.Status == status {
			chunks = append(chunks, c)
		}
	}
	return &v1.DraftPolicy{Chunks: chunks, DraftVersion: f.draftVersion}, nil
}

func (f *fakePolicy) decide(id, status string) {
	for i := range f.chunks {
		if f.chunks[i].ID == id {
			f.chunks[i].Status, f.chunks[i].DecidedAt = status, time.Unix(300, 0)
		}
	}
}

func (f *fakePolicy) ApproveDraftChunk(_ context.Context, _, _, id, token string) (*v1.ApproveResult, error) {
	f.approved = append(f.approved, id+" "+token)
	f.decide(id, "approved")
	return &v1.ApproveResult{PolicyVersion: f.approveVersion}, nil
}

// EditDraftChunk replaces the rule; the chunk gets a new review token.
func (f *fakePolicy) EditDraftChunk(_ context.Context, _, _, id string, rule *v1.NetworkPolicyRule) error {
	for i := range f.chunks {
		if f.chunks[i].ID == id {
			f.chunks[i].ProposedRule, f.chunks[i].ReviewToken = rule, f.chunks[i].ReviewToken+"-edited"
		}
	}
	return nil
}

func (f *fakePolicy) RejectDraftChunk(_ context.Context, _, _, id, reason string) error {
	f.rejected = append(f.rejected, id)
	f.decide(id, "rejected")
	for i := range f.chunks {
		if f.chunks[i].ID == id {
			f.chunks[i].RejectionReason = reason
		}
	}
	return nil
}

func TestProposalDecisionsFollowTheDraft(t *testing.T) {
	rule := func(host string) *v1.NetworkPolicyRule {
		return &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: host, Port: 443, Ports: []uint32{443}}}}
	}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		{ID: "c1", Status: "pending", ProposedRule: rule("example.com"), Binary: "/usr/bin/curl", Rationale: "fetch docs", ReviewToken: "t1", CreatedAt: time.Unix(100, 0)},
		{ID: "c2", Status: "pending", ProposedRule: rule("pypi.org"), ReviewToken: "t2", CreatedAt: time.Unix(200, 0)},
	}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}
	w := newSandbox("dev")
	ctx := context.Background()

	v, err := o.Proposals(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Available || len(v.Pending) != 2 || len(v.Rejected) != 0 || len(v.Unlinked) != 0 {
		t.Fatalf("view: %+v", v)
	}
	p := v.Pending[0]
	if p.ID != requestID("c1") || p.State != "pending-approval" || p.Summary != "/usr/bin/curl → example.com:443: fetch docs" {
		t.Fatalf("first pending: %+v", p)
	}

	// The request ID alone finds the proposal (a push notification may
	// approve it before any view of this process).
	// It answers the revision it made (Iwato's whole policy after
	// OpenShell's approval) and what the recent list shows for it.
	fresh := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}
	change, err := fresh.DecideProposal(ctx, w, "approve", p.ID, testDigest(fresh, ctx, w, p.ID), nil)
	if err != nil || change.Version != 4 || change.Kind != "approve" || change.Summary != p.Summary || change.Host != "example.com" {
		t.Fatalf("%v %+v", err, change)
	}
	if change, err := o.DecideProposal(ctx, w, "reject", v.Pending[1].ID, testDigest(o, ctx, w, v.Pending[1].ID), nil); err != nil || change.Version != 0 {
		t.Fatal(err, change)
	}
	if strings.Join(f.approved, ",") != "c1 t1-edited" || strings.Join(f.rejected, ",") != "c2" {
		t.Fatalf("approved %v, rejected %v", f.approved, f.rejected)
	}
	if _, err := o.DecideProposal(ctx, w, "approve", p.ID, testDigest(o, ctx, w, p.ID), nil); err == nil {
		t.Fatal("a decided proposal was approved again")
	}
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("gone"), testDigest(o, ctx, w, requestID("gone")), nil); err == nil {
		t.Fatal("an unknown request was approved")
	}

	// The rejected one lists as rejected; the approved one, whose rule
	// this fake gateway never added, as approved without its rule.
	v, _ = o.Proposals(ctx, w)
	if len(v.Pending) != 0 || len(v.Rejected) != 1 || len(v.Unlinked) != 1 {
		t.Fatalf("view after decisions: %+v", v)
	}
	if r := v.Rejected[0]; r.ID != requestID("c2") || r.State != "rejected" || r.Endpoints[0].Host != "pypi.org" || r.HitsAfter != 0 || r.SeenAfter {
		t.Fatalf("rejected: %+v", r)
	}
	if u := v.Unlinked[0]; u.ID != requestID("c1") || u.Endpoints[0].Host != "example.com" || u.RuleExists {
		t.Fatalf("unlinked: %+v", u)
	}
	// A rejected proposal can be approved, as proposed only (OpenShell
	// edits pending ones only).
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("c2"), testDigest(o, ctx, w, requestID("c2")), &ProposalScope{ReadOnly: true}); err == nil || !strings.Contains(err.Error(), "提案どおり") {
		t.Fatal("a rejected proposal was narrowed:", err)
	}
	if _, err := o.DecideProposal(ctx, w, "reject", requestID("c2"), testDigest(o, ctx, w, requestID("c2")), nil); err == nil {
		t.Fatal("a rejected proposal was rejected again")
	}
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("c2"), testDigest(o, ctx, w, requestID("c2")), nil); err != nil || strings.Join(f.approved, ",") != "c1 t1-edited,c2 t2" {
		t.Fatal(err, f.approved)
	}
}

func TestNarrowedApprovalInspectsRequests(t *testing.T) {
	f := &fakePolicy{chunks: []v1.PolicyChunk{{ID: "c1", Status: "pending", ReviewToken: "t1", Binary: "/usr/lib/git-core/git-remote-http",
		ProposedRule: &v1.NetworkPolicyRule{Name: "git", Endpoints: []v1.PolicyNetworkEndpoint{{Host: "github.com", Port: 443}}}}}}
	f.approveVersion = 9
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}, newSandbox("dev"), context.Background()
	scope := &ProposalScope{Rules: []ProposalRule{{Method: "*", Path: "/owner/repo.git/**"}}}
	if change, err := o.DecideProposal(ctx, w, "approve", requestID("c1"), testDigest(o, ctx, w, requestID("c1")), scope); err != nil || change.Version != 4 {
		t.Fatal(err, change)
	}
	e := f.chunks[0].ProposedRule.Endpoints[0]
	if e.Protocol != "rest" || e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) != 1 || e.Rules[0].Allow.Path != "/owner/repo.git/**" || e.Host != "github.com" {
		t.Fatalf("%+v", e)
	}
	if strings.Join(f.approved, ",") != "c1 t1-edited" {
		t.Fatal("the approval must bind to the edited rule:", f.approved)
	}
	if got := (ProposalScope{ReadOnly: true}).apply(*f.chunks[0].ProposedRule).Endpoints[0]; got.Access != v1.NetworkAccessPresetReadOnly || len(got.Rules) != 0 {
		t.Fatalf("read-only: %+v", got)
	}
	if got := (ProposalScope{Audit: true}).apply(*f.chunks[0].ProposedRule).Endpoints[0]; got.Enforcement != v1.NetworkEnforcementModeAudit || got.Protocol != "rest" || len(got.Rules) != 1 {
		t.Fatalf("audit: %+v", got)
	}
	if rules := proposalRules(f.chunks[0]); len(rules) != 1 || rules[0] != (ProposalRule{Method: "*", Path: "/owner/repo.git/**"}) {
		t.Fatal("a proposal's rules:", rules)
	}
	for _, bad := range []ProposalScope{{}, {ReadOnly: true, Audit: true}, {ReadOnly: true, Rules: []ProposalRule{{Method: "GET", Path: "/"}}}, {Rules: []ProposalRule{{Method: "TRACE", Path: "/"}}}, {Rules: []ProposalRule{{Method: "GET", Path: "a"}}}, {Rules: []ProposalRule{{Method: "GET", Path: "/a b"}}}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// update takes a whole policy as the gateway does: a new latest revision
// with the annotations as its provenance.
func (f *fakePolicy) update(ctx context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
	f.updates = append(f.updates, u)
	st, _ := f.GetStatus(ctx, "", u.Name)
	next := *st
	next.Revision.Version++
	next.Revision.Policy, next.Revision.Provenance = u.Policy, maps.Clone(u.Annotations)
	f.status = &next
	return &v1.ConfigUpdateResult{Version: next.Revision.Version}, nil
}

func (f *fakePolicy) GetStatus(context.Context, string, string, ...v1.GetStatusOption) (*v1.PolicyStatusResult, error) {
	if f.status != nil {
		return f.status, nil
	}
	return &v1.PolicyStatusResult{ActiveVersion: 2, Revision: v1.SandboxPolicyRevision{Version: 3, Status: v1.PolicyLoadStatusPending, Policy: &v1.SandboxPolicy{NetworkPolicies: map[string]v1.NetworkPolicyRule{
		"git": {Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/git"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
			Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/o/r.git/**"}}}}}},
	}}}}, nil
}

func TestCurrentPolicyListsRules(t *testing.T) {
	f := &fakePolicy{}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}
	p, err := o.currentPolicy(context.Background(), newSandbox("dev"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 3 || p.Loaded || len(p.Rules) != 1 {
		t.Fatalf("%+v", p)
	}
	e := p.Rules[0].Endpoints[0]
	if p.Rules[0].Name != "git" || p.Rules[0].Binaries[0] != "/usr/bin/git" || e.Host != "github.com" || len(e.Ports) != 1 || e.Inspect != "rest" || e.Enforcement != "enforce" || len(e.Rules) != 1 || e.Rules[0].Path != "/o/r.git/**" {
		t.Fatalf("%+v", p.Rules[0])
	}
}

// An approval can open internal addresses (allowed_ips); the owner sees
// them, and narrowing the approval keeps them as proposed.
func TestProposalAllowedIPsShowAndSurviveNarrowing(t *testing.T) {
	rule := &v1.NetworkPolicyRule{Name: "ci", Endpoints: []v1.PolicyNetworkEndpoint{
		{Host: "ci.tailnet.example", Port: 443, AllowedIPs: []string{"100.64.0.0/10"}},
		{Host: "", Port: 8080, AllowedIPs: []string{"10.0.0.0/8", "fd00::/8"}},
		{Host: "plain.example", Port: 443},
	}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{{ID: "c1", Status: "pending", ReviewToken: "t1", Binary: "/usr/bin/curl", ProposedRule: rule}}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}, newSandbox("dev"), context.Background()
	v, err := o.Proposals(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	p := v.Pending[0]
	want := "/usr/bin/curl → ci.tailnet.example:443 · 解決先 IP を 100.64.0.0/10 に限定 (内部アドレスも許可), すべてのホスト:8080 · ホストを問わず、解決先 IP が 10.0.0.0/8, fd00::/8 なら許可 (内部アドレスも), plain.example:443"
	if p.Summary != want {
		t.Errorf("summary:\n got %s\nwant %s", p.Summary, want)
	}
	if len(p.AllowedIPs) != 2 || p.AllowedIPs[0].Host != "ci.tailnet.example" || strings.Join(p.AllowedIPs[0].IPs, ",") != "100.64.0.0/10" || p.AllowedIPs[1].Host != "" || len(p.AllowedIPs[1].IPs) != 2 {
		t.Errorf("allowed IPs: %+v", p.AllowedIPs)
	}
	for _, scope := range []ProposalScope{{ReadOnly: true}, {Audit: true}, {Rules: []ProposalRule{{Method: "GET", Path: "/**"}}}} {
		got := scope.apply(*rule)
		for i, e := range got.Endpoints {
			if !slices.Equal(e.AllowedIPs, rule.Endpoints[i].AllowedIPs) || e.Host != rule.Endpoints[i].Host {
				t.Errorf("%+v changed endpoint %d: %+v", scope, i, e)
			}
		}
	}
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("c1"), testDigest(o, ctx, w, requestID("c1")), &ProposalScope{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.chunks[0].ProposedRule.Endpoints[0].AllowedIPs; strings.Join(got, ",") != "100.64.0.0/10" {
		t.Fatal("the edited chunk lost allowed_ips:", got)
	}
}

// The common approval: a versioned program widened to a glob, reads only,
// on the paths seen.
func TestApprovalSetsProgramsAndReadOnlyPaths(t *testing.T) {
	bin := "/sandbox/.claude/downloads/claude-2.1.293-linux-x64"
	rule := &v1.NetworkPolicyRule{Name: "raw", Binaries: []v1.PolicyNetworkBinary{{Path: bin}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "raw.githubusercontent.com", Port: 443, Ports: []uint32{443}, AllowedIPs: []string{"10.0.0.0/8"},
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/a/b/install.sh"}}}}}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{{ID: "c1", Status: "pending", ReviewToken: "t1", Binary: bin, ProposedRule: rule}}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}, newSandbox("dev"), context.Background()
	v, err := o.Proposals(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	p := v.Pending[0]
	if !slices.Equal(p.Binaries, []string{bin}) || len(p.Endpoints) != 1 || p.Endpoints[0].Host != "raw.githubusercontent.com" || !slices.Equal(p.Endpoints[0].Ports, []uint32{443}) {
		t.Fatalf("pending: %+v", p)
	}
	scope := &ProposalScope{ReadOnly: true, Binaries: []string{"/sandbox/.claude/downloads/claude-*-linux-x64"}, Rules: []ProposalRule{{Path: "/a/b/install.sh"}, {Path: "/a/**"}, {Path: "/a/**"}}}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideProposal(ctx, w, "approve", p.ID, testDigest(o, ctx, w, p.ID), scope); err != nil {
		t.Fatal(err)
	}
	// Every program (ADR 0022), one endpoint per path.
	got := f.chunks[0].ProposedRule
	if len(got.Binaries) != 1 || got.Binaries[0].Path != "/**" || len(got.Endpoints) != 2 {
		t.Fatalf("rule: %+v", got)
	}
	for i, path := range []string{"/a/b/install.sh", "/a/**"} {
		e := got.Endpoints[i]
		var rules []string
		for _, r := range e.Rules {
			rules = append(rules, r.Allow.Method+" "+r.Allow.Path)
		}
		want := "GET " + path + ",HEAD " + path + ",OPTIONS " + path
		if i == 0 {
			// The wider path's rules reach the narrower one's endpoint too.
			want += ",GET /a/**,HEAD /a/**,OPTIONS /a/**"
		}
		if e.Path != path || strings.Join(rules, ",") != want || e.Access != v1.NetworkAccessPresetUnspecified || e.Protocol != "rest" || e.Enforcement != v1.NetworkEnforcementModeEnforce || strings.Join(e.AllowedIPs, ",") != "10.0.0.0/8" {
			t.Fatalf("endpoint: %v %+v", rules, e)
		}
	}
	if applied := f.updates[len(f.updates)-1].Policy.NetworkPolicies["allow_raw_githubusercontent_com_443"]; len(applied.Endpoints) != 2 || !applied.Endpoints[0].AdvisorProposed {
		t.Fatalf("applied: %+v", applied)
	}
	if strings.Join(f.approved, ",") != "c1 t1-edited" {
		t.Fatal(f.approved)
	}

	// Programs alone keep the endpoints as proposed (no inspection added).
	only := ProposalScope{Binaries: []string{"/**"}}
	if err := only.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := only.apply(*rule); got.Binaries[0].Path != "/**" || got.Endpoints[0].Protocol != "" || len(got.Endpoints[0].Rules) != 1 || len(rule.Binaries) != 1 || rule.Binaries[0].Path != bin {
		t.Fatalf("programs only: %+v (proposed %+v)", got, rule)
	}
	for _, bad := range []ProposalScope{
		{Binaries: []string{"claude-*"}}, {Binaries: []string{"/"}}, {Binaries: []string{"/a//b"}}, {Binaries: []string{"/a/"}}, {Binaries: []string{"/a b"}}, {Binaries: []string{"/a\u200bb"}}, {Binaries: []string{""}},
		{ReadOnly: true, Rules: []ProposalRule{{Method: "POST", Path: "/a"}}}, {ReadOnly: true, Rules: []ProposalRule{{Path: "a"}}},
		{Audit: true, Binaries: []string{"/x"}, Rules: []ProposalRule{{Method: "GET", Path: "/"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for _, good := range []string{"/usr/bin/python3.*", "/**", "/sandbox/.claude/downloads/*", "/opt/日本語/x"} {
		if err := validBinary(good); err != nil {
			t.Error(err)
		}
	}
}

// OpenShell's own proposal with a lone GET / and no samples carries a
// placeholder, not a request seen; with samples, or from the agent, it is
// one.
func TestPlaceholderRoot(t *testing.T) {
	root := restEndpoint("api.github.com", 443)
	root.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}
	chunk := func(rationale string, e v1.PolicyNetworkEndpoint) v1.PolicyChunk {
		return v1.PolicyChunk{Rationale: rationale, ProposedRule: &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{e}}}
	}
	auto := "Allow mise to connect to api.github.com:443 (HTTPS)."
	if !placeholderRoot(chunk(auto, root)) {
		t.Fatal("the mise case")
	}
	if placeholderRoot(chunk("Allow mise to connect to api.github.com:443 (HTTPS) with L7 inspection. Allowed paths: GET /.", root)) {
		t.Fatal("a sampled GET /")
	}
	if placeholderRoot(chunk("mise の最新版を調べる", root)) {
		t.Fatal("the agent's /")
	}
	other := root
	other.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/repos/**"}}}
	if placeholderRoot(chunk(auto, other)) {
		t.Fatal("another path")
	}
	two := root
	two.Rules = append(slices.Clone(root.Rules), other.Rules...)
	if placeholderRoot(chunk(auto, two)) {
		t.Fatal("two rules")
	}
	p := pendingProposal("x", v1.PolicyChunk{Binary: "/usr/local/lib/mise/mise", Rationale: auto, ProposedRule: &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{root}}})
	if !p.Placeholder || len(p.Rules) != 0 || strings.Contains(p.Summary, "GET /") {
		t.Fatalf("%+v", p)
	}
}

// The page tells what approving as proposed allows from these: an access
// preset widens past the rules, and an inspected endpoint not set to
// enforce is audited (OpenShell's default), letting everything through.
func TestProposalEndpointsReportWhatWidens(t *testing.T) {
	c := v1.PolicyChunk{ProposedRule: &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{
		{Host: "a.example", Port: 443, Protocol: "rest", Access: v1.NetworkAccessPresetFull, Enforcement: v1.NetworkEnforcementModeEnforce},
		{Host: "b.example", Port: 443, Protocol: "rest"},
		{Host: "c.example", Port: 22},
		{Host: "d.example", Port: 443, Protocol: "rest", Access: v1.NetworkAccessPreset(9), Enforcement: v1.NetworkEnforcementModeAudit},
	}}}
	var got []string
	for _, e := range proposalEndpoints(c) {
		got = append(got, e.Host+" "+e.Access+" "+e.Enforcement)
	}
	want := []string{"a.example full enforce", "b.example  audit", "c.example  ", "d.example unknown audit"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
