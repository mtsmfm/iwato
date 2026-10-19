//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// The fake's mise proposal for api.github.com with the GitHub provider
// attached: audit is refused (the dialog's choice disabled, with the
// provider named), the others go through.
func TestApproveDialogKnowsTheProviderEnforces(t *testing.T) {
	s, _, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	_ = s
	code, body := do("GET", "/api/sandboxes/dev/proposals", "")
	var v ProposalView
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil {
		t.Fatal(code, body)
	}
	i := slices.IndexFunc(v.Pending, func(p PendingProposal) bool { return p.ID == requestID("fake-mise-gh") })
	if i < 0 {
		t.Fatal(body)
	}
	b := v.Pending[i].Blocked
	if len(b) != 1 || !strings.Contains(b["audit"].Reason, "「監査」は選べません") || !strings.Contains(b["audit"].Reason, "プロバイダー github-personal (GitHub) の規則 _provider_github_personal") ||
		!strings.Contains(b["audit"].Reason, "遮断 (enforce)") || b["audit"].Ambiguities[0].Rule != "_provider_github_personal" {
		t.Fatalf("%+v", b)
	}
	// The push to github.com (the provider enforces there too) cannot be
	// audited either; the others are free.
	for _, p := range v.Pending {
		want := 0
		if strings.HasSuffix(p.Rule, "github_com_443") {
			want = 1
		}
		if _, audit := p.Blocked["audit"]; len(p.Blocked) != want || want == 1 && !audit {
			t.Fatalf("%s blocked: %+v", p.Rule, p.Blocked)
		}
	}
	// The server refuses the same, saying what works.
	code, body = do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-mise-gh")+`"`+digestField(s.rt, requestID("fake-mise-gh"))+`,"scope":{"audit":true,"binaries":["`+fakeMise+`"]}}`)
	var resp struct {
		Error       string
		Code        string
		Params      struct{ Choices []string }
		Ambiguities []AmbiguityView
	}
	// The page words it from the code, the choices that work and the
	// pairs' diffs.
	if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Ambiguities) != 1 || resp.Ambiguities[0].Provider.Name != "github-personal" ||
		resp.Code != "ambiguous_approve" || !slices.Equal(resp.Params.Choices, []string{"read", "rules", "all"}) ||
		!slices.Equal(resp.Ambiguities[0].Diffs, []AmbiguityDiff{{"enforcement", "enforce", "audit"}}) ||
		!strings.Contains(resp.Error, "この承認は OpenShell に拒否されます") || !strings.Contains(resp.Error, "「読み取りだけ」「メソッドとパスを指定」「提案どおり」なら承認できます") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+requestID("fake-mise-gh")+`"`+digestField(s.rt, requestID("fake-mise-gh"))+`,"scope":{"readOnly":true,"binaries":["`+fakeMise+`"]}}`); code != 200 {
		t.Fatal(code, body)
	}
}

// Without Iwato's check, the fake's gateway refuses as OpenShell does; the
// prediction and the refusal name the same pair, and Iwato translates it.
func TestFakeGatewayRefusesWhatIwatoPredicts(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`)
	sb := newSandbox("dev")
	_, err := d.DecideProposal(context.Background(), sb, "approve", requestID("fake-mise-gh"), testDigest(d, context.Background(), sb, requestID("fake-mise-gh")), &ProposalScope{Audit: true, Binaries: []string{fakeMise}})
	if err == nil || !strings.Contains(err.Error(), "proposal is not applicable: candidate invalid: network endpoint ambiguity validation failed:") {
		t.Fatal(err)
	}
	base, effective, _ := d.PolicyLayers(context.Background(), sb)
	rule, _, _ := d.ProposedRule(context.Background(), sb, requestID("fake-mise-gh"))
	predicted, _ := predictAmbiguities(base, effective, approvalOps(rule.Name, (&ProposalScope{Audit: true, Binaries: []string{fakeMise}}).apply(*rule)))
	if len(predicted) != 1 || !strings.Contains(err.Error(), predicted[0].String()) {
		t.Fatal(predicted, err)
	}
	if msg, ok := translateGatewayError(err.Error(), nil); !ok || !strings.Contains(msg.text, "規則 _provider_github_personal と 規則 allow_api_github_com_443") {
		t.Fatal(msg)
	}
}

// Hand edits: a rule auditing api.github.com while the provider enforces
// is refused naming the provider; enforced it goes through.
func TestHandEditsAreCheckedForAmbiguity(t *testing.T) {
	_, _, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`)
	code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/curl"],"host":"api.github.com","ports":[443],"access":"full","audit":true}`)
	if code != 409 || !strings.Contains(body, "この変更は OpenShell に拒否されます") || !strings.Contains(body, "プロバイダー github-personal (GitHub)") || !strings.Contains(body, "プロバイダーの規則は変えられないので") {
		t.Fatal(code, body)
	}
	// Enforced, it is no longer ambiguous, but the provider's host is the
	// provider's (ADR 0006).
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/curl"],"host":"api.github.com","ports":[443],"access":"full"}`); code != 409 || !strings.Contains(body, `"code":"policy_provider_host"`) {
		t.Fatal(code, body)
	}
	// Uninspected (L4) does not compete for requests.
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/wget"],"host":"api.github.com","ports":[8443]}`); code != 200 {
		t.Fatal(code, body)
	}
}

// Attaching the GitHub provider to a sandbox whose policy audits
// api.github.com: refused with the endpoint named and the fixes; switching
// it to enforce (keeping its rules) lets the provider on.
func TestAttachChecksAmbiguityAndFixes(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	// A rule from before (hand edits on a provider's host are refused now).
	if _, err := d.EditPolicy(context.Background(), newSandbox("dev"), PolicyEdit{Op: "add", Rule: "gh", Binaries: []string{"/usr/bin/gh"}, Host: "api.github.com", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/repos/**"}}, Audit: true}); err != nil {
		t.Fatal(err)
	}
	code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`)
	var resp struct {
		Error       string
		Ambiguities []AmbiguityView
	}
	if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Ambiguities) != 1 {
		t.Fatal(code, body)
	}
	a := resp.Ambiguities[0]
	if a.Rule != "gh" || a.With != "_provider_github_personal" || a.WithProvider.Name != "github-personal" || a.Fix != "enforce" || !a.Audited || a.Host != "api.github.com" ||
		!slices.Equal(a.Binaries, []string{"/usr/bin/gh"}) || len(a.Allows) != 1 || a.Allows[0].Path != "/repos/**" || !strings.Contains(resp.Error, "遮断に切り替える") {
		t.Fatalf("%s\n%+v", resp.Error, a)
	}
	if _, attached, _ := d.ListProviders(context.Background()); slices.Contains(attached["dev"], "github-personal") {
		t.Fatal("attached anyway")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"github-personal","mode":"enforce"}`); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(context.Background(), newSandbox("dev"))
	if e := rules["gh"].Endpoints[0]; e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) != 1 || e.Rules[0].Allow.Path != "/repos/**" {
		t.Fatalf("%+v", e)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
}

func TestAttachFixRemovesTheEndpoint(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	if _, err := d.EditPolicy(context.Background(), newSandbox("dev"), PolicyEdit{Op: "add", Rule: "gh", Binaries: []string{"/usr/bin/gh"}, Host: "api.github.com", Ports: []uint32{443}, Access: "read-only", Audit: true}); err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"github-personal"}`); code != 200 || !strings.Contains(body, `"fixed":[{"rule":"gh"`) {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(context.Background(), newSandbox("dev"))
	if _, ok := rules["gh"]; ok {
		t.Fatal("the endpoint (the rule's only one) is still there")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
}
