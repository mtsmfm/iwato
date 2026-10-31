package main

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// storeGateway keeps a sandbox's policy revisions as the gateway does: a
// whole policy makes a revision with its annotations as provenance.
type storeGateway struct {
	v1.PolicyInterface
	revs []v1.SandboxPolicyRevision
	fail error
	sent []*v1.ConfigUpdate
}

func (g *storeGateway) add(rules map[string]v1.NetworkPolicyRule, provenance map[string]string) {
	g.revs = append(g.revs, v1.SandboxPolicyRevision{Version: uint32(len(g.revs) + 1), Provenance: provenance,
		Policy: &v1.SandboxPolicy{Version: 1, NetworkPolicies: cloneRules(rules)}})
}

func (g *storeGateway) GetStatus(_ context.Context, _, _ string, opts ...v1.GetStatusOption) (*v1.PolicyStatusResult, error) {
	cfg := types.ApplyGetStatusOptions(opts)
	v := cfg.Version()
	if v == 0 {
		v = uint32(len(g.revs))
	}
	if v == 0 || int(v) > len(g.revs) {
		return nil, errors.New("not found")
	}
	return &v1.PolicyStatusResult{Revision: g.revs[v-1]}, nil
}

func (g *storeGateway) update(_ context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
	g.sent = append(g.sent, u)
	if g.fail != nil {
		return nil, g.fail
	}
	g.add(u.Policy.NetworkPolicies, maps.Clone(u.Annotations))
	return &v1.ConfigUpdateResult{Version: uint32(len(g.revs))}, nil
}

func (g *storeGateway) config(context.Context, Sandbox) (*v1.SandboxConfig, error) {
	return &v1.SandboxConfig{Policy: g.revs[len(g.revs)-1].Policy}, nil
}

func storeRule(host string) v1.NetworkPolicyRule {
	return v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: host, Port: 443, Ports: []uint32{443}}}}
}

func addRule(name, host string) []v1.PolicyMergeOperation {
	r := storeRule(host)
	r.Name = name
	return []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: r}}}
}

// The repository follows what OpenShell took: a sandbox without one
// imports its policy first; a change is committed only once applied, with
// the commit in the revision's provenance; a change made outside Iwato is
// imported before the next change, or refused on the policy screen; a
// commit can be reverted.
func TestPolicyStoreFollowsOpenShell(t *testing.T) {
	g := &storeGateway{}
	g.add(map[string]v1.NetworkPolicyRule{"a": storeRule("a.example")}, nil)
	o := &OpenShell{pol: g, updateConfig: g.update, sandboxConfig: g.config}
	w, ctx := newSandbox("dev"), context.Background()
	if st, err := o.PolicyStore(ctx, w); err != nil || st.State != "none" {
		t.Fatal(err, st)
	}

	if _, err := o.applyOps(withPolicyNote(ctx, "preset", "Apply preset x"), w, addRule("b", "b.example")); err != nil {
		t.Fatal(err)
	}
	log, _ := o.PolicyCommits(ctx, w, 10)
	if len(log) != 2 || log[1].Kind != "import" || log[0].Kind != "preset" || log[0].Subject != "Apply preset x" || !strings.Contains(log[0].Message, "Add rule b (b.example:443)") {
		t.Fatalf("%+v", log)
	}
	u := g.sent[0]
	if u.Policy == nil || len(u.MergeOperations) > 0 || u.Annotations[provenanceCommit] != log[0].SHA || u.Annotations[provenanceChange] != "preset" || len(u.Policy.NetworkPolicies) != 2 {
		t.Fatalf("%+v", u)
	}
	if st, _ := o.PolicyStore(ctx, w); st.State != "synced" || st.Head.SHA != log[0].SHA {
		t.Fatalf("%+v", st)
	}

	// Refused by OpenShell: the branch stays.
	g.fail = errors.New("refused")
	if _, err := o.applyOps(ctx, w, addRule("c", "c.example")); err == nil {
		t.Fatal("no error")
	}
	g.fail = nil
	if again, _ := o.PolicyCommits(ctx, w, 10); len(again) != 2 || again[0].SHA != log[0].SHA {
		t.Fatalf("%+v", again)
	}

	// A revision without Iwato's provenance but the same rules (the
	// supervisor adding paths) is no drift; other rules are.
	g.add(g.revs[len(g.revs)-1].Policy.NetworkPolicies, nil)
	if st, _ := o.PolicyStore(ctx, w); st.State != "synced" {
		t.Fatalf("%+v", st)
	}
	outside := cloneRules(g.revs[len(g.revs)-1].Policy.NetworkPolicies)
	outside["cli"] = storeRule("cli.example")
	g.add(outside, nil)
	if st, _ := o.PolicyStore(ctx, w); st.State != "drift" {
		t.Fatalf("%+v", st)
	}
	// The policy screen's writes wait for the owner to import or overwrite it.
	if _, err := o.applyOps(refuseDrift(ctx), w, []v1.PolicyMergeOperation{{RemoveRule: &v1.RemoveNetworkRule{RuleName: "a"}}}); codeOf(err) != "policy_drift" {
		t.Fatal("a drift under the policy screen:", err)
	}
	if _, err := o.applyOps(ctx, w, []v1.PolicyMergeOperation{{RemoveRule: &v1.RemoveNetworkRule{RuleName: "a"}}}); err != nil {
		t.Fatal(err)
	}
	log, _ = o.PolicyCommits(ctx, w, 10)
	if len(log) != 4 || log[1].Kind != "import" || log[0].Subject != "Remove rule a" {
		t.Fatalf("%+v", log)
	}
	if rules := g.revs[len(g.revs)-1].Policy.NetworkPolicies; len(rules) != 2 || len(rules["cli"].Endpoints) != 1 {
		t.Fatalf("%+v", rules)
	}

	// Reverting the preset's commit takes b out again; the rule a commit
	// changed and a later one changed again cannot be reverted.
	if _, err := o.RevertPolicyCommit(ctx, w, log[2].SHA); err != nil {
		t.Fatal(err)
	}
	if rules := g.revs[len(g.revs)-1].Policy.NetworkPolicies; len(rules) != 1 || len(rules["cli"].Endpoints) != 1 {
		t.Fatalf("%+v", rules)
	}
	if _, err := o.RevertPolicyCommit(ctx, w, log[3].SHA); codeOf(err) != "policy_revert_root" {
		t.Fatal("reverted the first commit:", err)
	}
	if _, err := revertRules(map[string]v1.NetworkPolicyRule{"a": storeRule("x.example")}, map[string]v1.NetworkPolicyRule{"a": storeRule("a.example")}, map[string]v1.NetworkPolicyRule{}); codeOf(err) != "policy_revert_conflict" {
		t.Fatal("reverted a rule changed since:", err)
	}
	_, diff, err := o.PolicyCommitDiff(ctx, w, log[0].SHA)
	if err != nil || !strings.Contains(diff, "-  a:") {
		t.Fatal(err, diff)
	}
}

// An approval is a rule of its own for every program, one endpoint per
// approved path, marked as the advisor's.
func TestApprovedRuleSplitsPaths(t *testing.T) {
	allow := func(m, p string) v1.L7Rule { return v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: p}} }
	e := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules: []v1.L7Rule{allow("GET", "/repos/o/r/pulls"), allow("POST", "/repos/o/r/pulls"), allow("POST", "/repos/o/r/issues")}}
	l4 := v1.PolicyNetworkEndpoint{Host: "pypi.org", Port: 443}
	r := approvedRule("allow_api", v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}}, Endpoints: []v1.PolicyNetworkEndpoint{e, l4}})
	if len(r.Binaries) != 1 || r.Binaries[0].Path != "/**" || len(r.Endpoints) != 3 {
		t.Fatalf("%+v", r)
	}
	if p := r.Endpoints[0]; p.Path != "/repos/o/r/pulls" || len(p.Rules) != 2 || !p.AdvisorProposed {
		t.Fatalf("%+v", p)
	}
	if p := r.Endpoints[1]; p.Path != "/repos/o/r/issues" || len(p.Rules) != 1 {
		t.Fatalf("%+v", p)
	}
	if p := r.Endpoints[2]; p.Path != "" || p.Host != "pypi.org" || !p.AdvisorProposed {
		t.Fatalf("%+v", p)
	}
	// A path's endpoint keeps the approved rules that can match its
	// requests: GET /api/** reaches /api/upload, POST /api/upload does not
	// widen /api/**'s endpoint beyond its own path.
	nested := approvedRule("n", v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.example.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules: []v1.L7Rule{allow("GET", "/api/**"), allow("POST", "/api/upload"), allow("GET", "/other")}}}})
	if len(nested.Endpoints) != 3 {
		t.Fatalf("%+v", nested)
	}
	if p := nested.Endpoints[1]; p.Path != "/api/upload" || len(p.Rules) != 2 || p.Rules[0].Allow.Method != "GET" || p.Rules[1].Allow.Method != "POST" {
		t.Fatalf("%+v", p)
	}
	if p := nested.Endpoints[2]; p.Path != "/other" || len(p.Rules) != 1 {
		t.Fatalf("%+v", p)
	}
	// Any path, or an access preset: the endpoint stays whole.
	e.Rules = append(e.Rules, allow("GET", "**"))
	if r := approvedRule("x", v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{e}}); len(r.Endpoints) != 1 || r.Endpoints[0].Path != "" {
		t.Fatalf("%+v", r)
	}

	// Approved paths that overlap a credential-bound endpoint with the same
	// specificity are refused; a more specific path wins and is not.
	bound := func(path, provider string) v1.PolicyNetworkEndpoint {
		x := v1.PolicyNetworkEndpoint{Host: "api.example.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Path: path}
		if provider != "" {
			x.CredentialBinding = &types.NetworkCredentialBinding{Provider: provider}
		}
		return x
	}
	rules := func(a, b v1.PolicyNetworkEndpoint) map[string]v1.NetworkPolicyRule {
		return map[string]v1.NetworkPolicyRule{"a": {Endpoints: []v1.PolicyNetworkEndpoint{a}}, "b": {Endpoints: []v1.PolicyNetworkEndpoint{b}}}
	}
	if a := findEndpointAmbiguities(rules(bound("/a/*/c", "p"), bound("/a/b/*", ""))); len(a) != 1 || !hasConflict(a, "credential_binding.provider") {
		t.Fatal(a)
	}
	if a := findEndpointAmbiguities(rules(bound("/a/*/c", "p"), bound("/a/b/c", ""))); len(a) != 0 {
		t.Fatal(a)
	}
}

// policy.yaml keeps what OpenShell's YAML has and the two markers it does
// not author.
func TestStorePolicyRoundTrip(t *testing.T) {
	e := v1.PolicyNetworkEndpoint{Host: "api.example.com", Port: 443, Ports: []uint32{443, 8443}, Path: "/v1/**", Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/v1/**"}}}, DenyRules: []v1.L7DenyRule{{Method: "DELETE", Path: "/v1/**"}},
		AllowedIPs: []string{"10.0.0.0/8"}, AdvisorProposed: true, CredentialBinding: &types.NetworkCredentialBinding{Provider: "work"}}
	rules := map[string]v1.NetworkPolicyRule{"api": {Name: "api", Binaries: []v1.PolicyNetworkBinary{{Path: "/**"}}, Endpoints: []v1.PolicyNetworkEndpoint{e}}}
	p := &v1.SandboxPolicy{Version: 1, Landlock: &v1.LandlockPolicy{Compatibility: "best_effort"}, NetworkPolicies: rules}
	b, err := encodeStorePolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeStoreRules(b)
	if err != nil {
		t.Fatal(err, string(b))
	}
	if rulesKey(back) != rulesKey(rules) || !back["api"].Endpoints[0].AdvisorProposed || back["api"].Endpoints[0].CredentialBinding.Provider != "work" || !strings.Contains(string(b), "landlock:") {
		t.Fatal(string(b))
	}
}

// A path-specific approved endpoint keeps what the endpoint it shadows
// allowed on its path (the most specific of those), and that endpoint's
// credential binding by default.
func TestApprovalInheritsTheShadowedEndpoint(t *testing.T) {
	allow := func(m, p string) v1.L7Rule { return v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: p}} }
	ep := func(path string, rules ...v1.L7Rule) v1.PolicyNetworkEndpoint {
		return v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Path: path, Rules: rules}
	}
	broad := ep("", allow("GET", "/**"), allow("POST", "/gists"))
	broad.CredentialBinding = &types.NetworkCredentialBinding{Provider: "github"}
	repos := ep("/repos/**", allow("GET", "/repos/**"), allow("DELETE", "/repos/**"))
	repos.DenyRules = []v1.L7DenyRule{{Method: "DELETE", Path: "/repos/**"}}
	repos.CredentialBinding = &types.NetworkCredentialBinding{Provider: "github"}
	other := ep("/other/x/y/z/long", allow("GET", "/other/**"))
	rules := map[string]v1.NetworkPolicyRule{"broad": {Endpoints: []v1.PolicyNetworkEndpoint{broad}}, "repos": {Endpoints: []v1.PolicyNetworkEndpoint{repos}}, "other": {Endpoints: []v1.PolicyNetworkEndpoint{other}}}
	proposed := v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{ep("", allow("POST", "/repos/o/r/pulls"))}}

	plan := inheritShadowed(approvedRule("pr", proposed), rules, nil)
	e := plan.Endpoints[0]
	var got []string
	for _, r := range e.Rules {
		got = append(got, r.Allow.Method+" "+r.Allow.Path)
	}
	// From /repos/** (more specific than the whole host), not from broad.
	if strings.Join(got, ",") != "POST /repos/o/r/pulls,GET /repos/**,DELETE /repos/**" || len(e.DenyRules) != 1 || e.CredentialBinding == nil || e.CredentialBinding.Provider != "github" {
		t.Fatalf("%v %+v", got, e)
	}
	if a := approvalAttach(plan, rules); len(a) != 1 || a[0].Required || a[0].Path != "/repos/o/r/pulls" || a[0].Provider != "github" {
		t.Fatalf("%+v", a)
	}
	off := false
	if e := inheritShadowed(approvedRule("pr", proposed), rules, &off).Endpoints[0]; e.CredentialBinding != nil || len(e.Rules) != 3 {
		t.Fatalf("%+v", e)
	}
	// Without a shadowed endpoint: nothing copied, no credential.
	delete(rules, "repos")
	delete(rules, "broad")
	if e := inheritShadowed(approvedRule("pr", proposed), rules, nil).Endpoints[0]; e.CredentialBinding != nil || len(e.Rules) != 1 {
		t.Fatalf("%+v", e)
	}
	// An uninspected source let everything through on the path.
	rules["l4"] = v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.github.com", Port: 443}}}
	if e := inheritShadowed(approvedRule("pr", proposed), rules, nil).Endpoints[0]; len(e.Rules) != 2 || e.Rules[1].Allow.Method != "*" || e.Rules[1].Allow.Path != "/repos/o/r/pulls" {
		t.Fatalf("%+v", e)
	}
}

// A read of the store between OpenShell taking a write and the write
// moving the branch neither moves the branch nor fails the write, and
// reports the commit OpenShell took as the head.
func TestPolicyStoreReadDuringWrite(t *testing.T) {
	g := &storeGateway{}
	g.add(map[string]v1.NetworkPolicyRule{"a": storeRule("a.example")}, nil)
	o := &OpenShell{pol: g, sandboxConfig: g.config}
	w, ctx := newSandbox("dev"), context.Background()
	var seen PolicyStoreState
	o.updateConfig = func(ctx context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
		res, err := g.update(ctx, u)
		if err == nil {
			seen, err = o.PolicyStore(ctx, w)
		}
		return res, err
	}
	for _, name := range []string{"b", "c"} {
		if _, err := o.applyOps(ctx, w, addRule(name, name+".example")); err != nil {
			t.Fatal(err)
		}
		log, _ := o.PolicyCommits(ctx, w, 10)
		if seen.State != "synced" || seen.Head == nil || seen.Head.SHA != log[0].SHA {
			t.Fatalf("%+v %+v", seen, log)
		}
	}
}
