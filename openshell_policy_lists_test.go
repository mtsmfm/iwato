package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func binaryRule(name string, binaries []string, hosts ...string) v1.NetworkPolicyRule {
	r := v1.NetworkPolicyRule{Name: name, Binaries: policyBinaries(binaries...)}
	for _, h := range hosts {
		r.Endpoints = append(r.Endpoints, l4Endpoint(h, 443))
	}
	return r
}

func approvedChunk(id, rule, binary string, binaries []string, host string, at int64) v1.PolicyChunk {
	r := binaryRule(rule, binaries, host)
	return v1.PolicyChunk{ID: id, Status: "approved", RuleName: rule, Binary: binary, ProposedRule: &r, DecidedAt: time.Unix(at, 0), Rationale: "intent of " + id}
}

// A rule comes from the approved proposals of its name whose program it
// holds; a provider's rule and one by hand from none. An approved proposal
// whose rule is gone (or holds none of its programs) is listed apart.
func TestRulesLinkToTheirApprovedProposals(t *testing.T) {
	p := policyView(&v1.PolicyStatusResult{}, map[string]v1.NetworkPolicyRule{
		"allow_api":       binaryRule("allow_api", []string{"/usr/bin/gh", "/usr/bin/git"}, "api.github.com"),
		"allow_npm":       binaryRule("allow_npm", []string{"/n/*/bin/node"}, "registry.npmjs.org", "npm.pkg.github.com"),
		"allow_pypi":      binaryRule("allow_pypi", []string{"/usr/bin/python3"}, "pypi.org"),
		"allow_other":     binaryRule("allow_other", []string{"/usr/bin/wget"}, "other.example"),
		"_provider_gh":    binaryRule("_provider_gh", []string{"/usr/bin/gh"}, "github.com"),
		"_provider_agent": binaryRule("_provider_agent", nil, "example.com"),
	})
	chunks := []v1.PolicyChunk{
		approvedChunk("gh", "allow_api", "/usr/bin/gh", []string{"/usr/bin/gh"}, "api.github.com", 100),
		approvedChunk("git", "allow_api", "/usr/bin/git", []string{"/usr/bin/git"}, "api.github.com", 200),
		// Widened to a glob in the approve dialog.
		approvedChunk("node", "allow_npm", "/n/22.11.0/bin/node", []string{"/n/*/bin/node"}, "registry.npmjs.org", 300),
		// Its rule was taken out; another's rule of its name lacks its program.
		approvedChunk("mise", "allow_mise", "/usr/bin/mise", []string{"/usr/bin/mise"}, "mise.jdx.dev", 50),
		approvedChunk("curl", "allow_other", "/usr/bin/curl", []string{"/usr/bin/curl"}, "other.example", 60),
		// A provider's rule is never a proposal's.
		approvedChunk("prov", "_provider_gh", "/usr/bin/gh", []string{"/usr/bin/gh"}, "github.com", 70),
		{ID: "wait", Status: "pending", RuleName: "allow_pypi", Binary: "/usr/bin/python3"},
		{ID: "no", Status: "rejected", RuleName: "allow_pypi", Binary: "/usr/bin/python3"},
	}
	unlinked := linkChunks(p, chunks)
	byName := map[string]PolicyRuleView{}
	var order []string
	for _, r := range p.Rules {
		byName[r.Name] = r
		order = append(order, r.Name)
	}
	if strings.Join(order, " ") != "allow_api allow_npm allow_other allow_pypi _provider_agent _provider_gh" {
		t.Fatal("the sandbox's rules first, then the providers':", order)
	}
	api := byName["allow_api"]
	if api.Source != "proposal" || len(api.Chunks) != 2 || api.Chunks[0].ID != requestID("gh") || api.Chunks[1].Binary != "/usr/bin/git" || api.Chunks[0].Intent != "intent of gh" {
		t.Fatalf("merged rule: %+v", api)
	}
	// Undoing one of two leaves the rule (git keeps it).
	if api.Chunks[0].RemovesRule || api.Chunks[0].Widened {
		t.Fatalf("%+v", api.Chunks[0])
	}
	npm := byName["allow_npm"].Chunks
	if len(npm) != 1 || !npm[0].Widened || !npm[0].RemovesRule || !slices.Equal(npm[0].Drops, []string{"npm.pkg.github.com:443"}) || !slices.Equal(npm[0].Binaries, []string{"/n/*/bin/node"}) {
		t.Fatalf("widened rule: %+v", npm)
	}
	for _, name := range []string{"allow_pypi", "allow_other"} {
		if r := byName[name]; r.Source != "hand" || len(r.Chunks) != 0 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	for _, name := range []string{"_provider_gh", "_provider_agent"} {
		if r := byName[name]; r.Source != "provider" || len(r.Chunks) != 0 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if len(unlinked) != 3 || unlinked[0].ID != requestID("mise") || unlinked[0].RuleExists || unlinked[1].ID != requestID("curl") || !unlinked[1].RuleExists || unlinked[2].Rule != "_provider_gh" || unlinked[0].State != "approved" || unlinked[0].DecidedAt != 50 {
		t.Fatalf("unlinked: %+v", unlinked)
	}
}

// Undoing takes out what OpenShell's UndoDraftChunk does not: the glob the
// approval widened the program to, unless another approved proposal of the
// rule holds it.
func TestUndoPlanTakesTheWidenedGlob(t *testing.T) {
	rule := binaryRule("r", []string{"/n/*/bin/node", "/usr/bin/curl"}, "a.example")
	node := approvedChunk("node", "r", "/n/22/bin/node", []string{"/n/*/bin/node"}, "a.example", 1)
	curl := approvedChunk("curl", "r", "/usr/bin/curl", []string{"/usr/bin/curl"}, "a.example", 2)
	left, removes := undoPlan(rule, node, []v1.PolicyChunk{node, curl})
	if !slices.Equal(left, []string{"/n/*/bin/node"}) || removes {
		t.Fatal(left, removes)
	}
	// Another proposal approved with the same glob keeps it.
	node2 := approvedChunk("node2", "r", "/n/23/bin/node", []string{"/n/*/bin/node"}, "a.example", 3)
	if left, _ := undoPlan(rule, node, []v1.PolicyChunk{node, curl, node2}); len(left) != 0 {
		t.Fatal(left)
	}
	// The last program: the rule goes.
	if left, removes := undoPlan(binaryRule("r", []string{"/usr/bin/curl"}, "a.example"), curl, []v1.PolicyChunk{curl}); len(left) != 0 || !removes {
		t.Fatal(left, removes)
	}
	// A rule for any program is never removed by taking programs out.
	if _, removes := undoPlan(binaryRule("r", nil, "a.example"), curl, nil); removes {
		t.Fatal("a rule without programs went")
	}
}

func chunkStatus(t *testing.T, o *OpenShell, w Sandbox, id string) string {
	t.Helper()
	draft, _ := o.pol.GetDraft(context.Background(), "", w.Name)
	for _, c := range draft.Chunks {
		if c.ID == id {
			return c.Status
		}
	}
	t.Fatal("no chunk", id)
	return ""
}

// The policy shown is the effective one (GetSandboxConfig): with the
// attached providers' rules, which no revision holds. When the gateway
// cannot compose it, the revision's rules stand in, with the reason.
func TestEffectivePolicyHasTheProviderRules(t *testing.T) {
	f := &fakePolicy{providerRules: map[string]v1.NetworkPolicyRule{"_provider_github_personal": binaryRule("_provider_github_personal", []string{"/usr/bin/gh"}, "github.com")}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs}
	ctx, w := context.Background(), newSandbox("dev")
	v, err := o.Proposals(ctx, w)
	if err != nil || v.Error != "" || len(v.Policy.Rules) != 2 || v.Policy.Rules[1].Name != "_provider_github_personal" || v.Policy.Rules[1].Source != "provider" || v.Policy.Version != 3 {
		t.Fatalf("%v %+v", err, v.Policy)
	}
	f.configErr = errors.New("provider composition produced an invalid effective policy")
	v, _ = o.Proposals(ctx, w)
	if len(v.Policy.Rules) != 1 || !strings.Contains(v.Error, "プロバイダーの規則を含むポリシーを読めません") {
		t.Fatalf("%q %+v", v.Error, v.Policy)
	}
}

// The rejected list: newest first; the hits since, from the count Iwato
// put in the reason; OpenShell's own rejections named.
func TestRejectedProposalsTellWhatCameAfter(t *testing.T) {
	at := time.Unix(1000, 0)
	r := func(id, reason string, hits int32, decided, last time.Time) v1.PolicyChunk {
		return v1.PolicyChunk{ID: id, Status: "rejected", RejectionReason: reason, HitCount: hits, DecidedAt: decided, LastSeen: last, Rationale: "Allow curl to connect to x.example:443 (HTTPS)."}
	}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		r("owner", rejectReason(3), 5, at, at.Add(time.Hour)),
		r("quiet", rejectReason(3), 3, at.Add(time.Minute), at.Add(time.Minute+time.Second)),
		r("old", "no", 2, at.Add(-time.Hour), at),
		r("covered", "already covered by approved chunk c1 (agent_authored or prior auto-approval)", 1, at.Add(2*time.Minute), at.Add(2*time.Minute)),
		r("superseded", "superseded by chunk c9", 1, at.Add(3*time.Minute), at.Add(3*time.Minute)),
	}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs}
	v, err := o.Proposals(context.Background(), newSandbox("dev"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range v.Rejected {
		got = append(got, x.ID)
	}
	want := []string{requestID("superseded"), requestID("covered"), requestID("quiet"), requestID("owner"), requestID("old")}
	if !slices.Equal(got, want) {
		t.Fatal("not newest first")
	}
	x := v.Rejected
	if x[3].HitsAfter != 2 || !x[3].SeenAfter || x[3].Auto != "" || x[3].Source != "auto" || x[3].DecidedAt != 1000 {
		t.Fatalf("owner: %+v", x[3])
	}
	if x[2].HitsAfter != 0 || x[2].SeenAfter {
		t.Fatalf("quiet: %+v", x[2])
	}
	// Without Iwato's count: only whether it came again.
	if x[4].HitsAfter != -1 || !x[4].SeenAfter {
		t.Fatalf("old: %+v", x[4])
	}
	if x[1].Auto != "covered" || x[0].Auto != "superseded" || x[0].SeenAfter {
		t.Fatalf("auto: %+v %+v", x[1], x[0])
	}
	// Iwato's rejection carries the count so far.
	f.chunks = append(f.chunks, v1.PolicyChunk{ID: "p", Status: "pending", HitCount: 7, ReviewToken: "t"})
	if _, err := o.DecideProposal(context.Background(), newSandbox("dev"), "reject", requestID("p"), testDigest(o, context.Background(), newSandbox("dev"), requestID("p")), nil); err != nil || f.chunks[len(f.chunks)-1].RejectionReason != "the owner rejected this rule in Iwato (hits: 7)" {
		t.Fatal(err, f.chunks[len(f.chunks)-1].RejectionReason)
	}
}
