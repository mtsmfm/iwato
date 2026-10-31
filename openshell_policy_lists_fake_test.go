//go:build uitest

package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// fakeOpenShell is the fake's gateway behind OpenShell's code, with the
// updates it was sent: a whole policy as its change's kind, merge
// operations as they are.
func fakeOpenShell(t *testing.T) (*Fake, *OpenShell, Sandbox, *[]string) {
	d := NewFake()
	w := newSandbox("dev")
	if err := d.Start(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	o := d.openShell()
	var sent []string
	update := o.updateConfig
	o.updateConfig = func(ctx context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
		if u.Policy != nil {
			sent = append(sent, "policy "+u.Annotations[provenanceChange])
		} else {
			sent = append(sent, opsString(u.MergeOperations))
		}
		return update(ctx, u)
	}
	return d, o, w, &sent
}

func TestUndoTakesTheWidenedRuleWithTheProposal(t *testing.T) {
	d, o, w, sent := fakeOpenShell(t)
	ctx := context.Background()
	v, err := o.Proposals(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	var npm PolicyRuleView
	for _, r := range v.Policy.Rules {
		if r.Name == "allow_registry_npmjs_org_443" {
			npm = r
		}
	}
	if len(npm.Chunks) != 1 || !npm.Chunks[0].Widened || !npm.Chunks[0].RemovesRule || !slices.Equal(npm.Chunks[0].Drops, []string{"npm.pkg.github.com:443"}) {
		t.Fatalf("%+v", npm)
	}
	// The rule's own program is the glob: Iwato takes it out (the rule goes
	// with it), and OpenShell's undo then finds nothing to take out.
	before := len(d.policies[w.Name])
	change, err := o.UndoProposal(ctx, w, npm.Chunks[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	rules, _ := d.PolicyRules(ctx, w)
	if _, ok := rules["allow_registry_npmjs_org_443"]; ok {
		t.Fatalf("the rule stayed: %+v", rules["allow_registry_npmjs_org_443"])
	}
	if strings.Join(*sent, "|") != "policy undo" || chunkStatus(t, o, w, "fake-npm") != "pending" {
		t.Fatal(*sent, chunkStatus(t, o, w, "fake-npm"))
	}
	if change.Kind != "edit" || change.Rule != "allow_registry_npmjs_org_443" || change.Version != uint32(before+1) {
		t.Fatalf("%+v (from %d)", change, before)
	}
	v, _ = o.Proposals(ctx, w)
	if !slices.ContainsFunc(v.Pending, func(p PendingProposal) bool { return p.ID == npm.Chunks[0].ID }) {
		t.Fatal("the undone proposal is not pending again")
	}
	// One of two proposals of a rule: the rule stays for the other's
	// program.
	*sent = nil
	if _, err := o.UndoProposal(ctx, w, requestID("fake-gh"), false); err != nil {
		t.Fatal(err)
	}
	rules, _ = d.PolicyRules(ctx, w)
	if r := rules[fakeGHERule]; len(r.Binaries) != 1 || r.Binaries[0].Path != "/usr/bin/git" || strings.Join(*sent, "|") != "policy undo" {
		t.Fatalf("%+v %v", r, *sent)
	}
	// Undo is for an approved proposal with its rule.
	if _, err := o.UndoProposal(ctx, w, requestID("fake-gh"), false); err == nil {
		t.Fatal("a pending proposal was undone")
	}
	if _, err := o.UndoProposal(ctx, w, requestID("fake-mise"), false); err == nil || !strings.Contains(err.Error(), "規則を戻して取り消す") {
		t.Fatal("a proposal without its rule was undone:", err)
	}
}

// The routes: undo and restore take a request ID (CSRF as every action),
// and answer the revision for the badges.
func TestUndoRoutes(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, _ := do("POST", "/api/sandboxes/dev/proposals/undo", `{"requestId":"bad"}`); code != 400 {
		t.Fatal("a bad request ID:", code)
	}
	if code, _ := do("POST", "/api/sandboxes/dev/proposals/undo", `{"requestId":"`+requestID("fake-gh")+`","scope":{"readOnly":true}}`); code != 400 {
		t.Fatal("a scope on undo:", code)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/undo", `{"requestId":"`+requestID("fake-gh")+`"}`); code != 200 || !strings.Contains(body, `"version":3`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/restore", `{"requestId":"`+requestID("fake-mise")+`"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/undo", `{"requestId":"`+requestID("fake-gh")+`"}`); code != 409 {
		t.Fatal("undone twice:", code, body)
	}
	sb, _ := s.lookupSandbox("dev")
	v, _ := d.Proposals(context.Background(), sb)
	if len(v.Pending) != 8 || len(v.Unlinked) != 0 {
		t.Fatalf("%d pending, %d unlinked", len(v.Pending), len(v.Unlinked))
	}
	if cs := s.policyChangesFor("dev", nil); len(cs) != 2 || cs[0].Rule != fakeGHERule {
		t.Fatalf("changes: %+v", cs)
	}
}

func TestRestorePutsTheProposalBack(t *testing.T) {
	d, o, w, sent := fakeOpenShell(t)
	ctx := context.Background()
	v, _ := o.Proposals(ctx, w)
	if len(v.Unlinked) != 1 || v.Unlinked[0].ID != requestID("fake-mise") || v.Unlinked[0].RuleExists {
		t.Fatalf("%+v", v.Unlinked)
	}
	if _, err := o.UndoProposal(ctx, w, requestID("fake-gh"), true); err == nil {
		t.Fatal("a proposal with its rule was restored")
	}
	if _, err := o.UndoProposal(ctx, w, requestID("fake-mise"), true); err != nil {
		t.Fatal(err)
	}
	// The rule is gone already: the policy stays, the proposal waits again.
	rules, _ := d.PolicyRules(ctx, w)
	if _, ok := rules["allow_mise_versions_jdx_dev_443"]; ok || chunkStatus(t, o, w, "fake-mise") != "pending" {
		t.Fatal(rules, chunkStatus(t, o, w, "fake-mise"))
	}
	if len(*sent) != 0 {
		t.Fatal(*sent)
	}
	v, _ = o.Proposals(ctx, w)
	if len(v.Unlinked) != 0 || !slices.ContainsFunc(v.Pending, func(p PendingProposal) bool { return p.ID == requestID("fake-mise") }) {
		t.Fatalf("%+v", v)
	}
}

// The server names each provider rule's provider from the sandbox's
// attached providers, as OpenShell names the rules.
func TestProviderRulesNameTheirProvider(t *testing.T) {
	s, _, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	// Iwato's profiles have no endpoints (ADR 0022): a CLI-imported one's.
	for _, p := range []string{"github-personal", "openai"} {
		if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"`+p+`"}`); code != 200 {
			t.Fatal(code, body)
		}
	}
	sb, _ := s.lookupSandbox("dev")
	v, err := s.rt.(proposalRuntime).Proposals(context.Background(), sb)
	if err != nil {
		t.Fatal(err)
	}
	nameProviderRules(v.Policy, s.ruleProviders(context.Background(), sb))
	if slices.ContainsFunc(v.Policy.Rules, func(r PolicyRuleView) bool { return r.Name == "_provider_github_personal" }) {
		t.Fatal("an endpointless profile's rule")
	}
	i := slices.IndexFunc(v.Policy.Rules, func(r PolicyRuleView) bool { return r.Name == "_provider_openai" })
	if i < 0 {
		t.Fatalf("%+v", v.Policy.Rules)
	}
	r := v.Policy.Rules[i]
	if r.Source != "provider" || r.Provider == nil || *r.Provider != (RuleProvider{Name: "openai", Profile: "openai"}) || !slices.Contains(r.Binaries, "/usr/bin/curl") {
		t.Fatalf("%+v %+v", r, r.Provider)
	}
	for in, want := range map[string]string{"github-personal": "_provider_github_personal", "Claude.Work": "_provider_claude_work", "--": "_provider_unnamed", "_x_": "_provider_x"} {
		if got := providerRuleName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
