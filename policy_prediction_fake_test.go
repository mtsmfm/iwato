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
// attached: audited, it would disagree with the provider's enforced /**
// endpoint (a path-less endpoint becomes /** there), which the server
// refuses naming the provider's rule, in the preview too; enforced, it
// goes through.
func TestApprovalRefusedWhereTheProviderEnforces(t *testing.T) {
	s, _, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	id := `{"requestId":"` + requestID("fake-mise-gh") + `"` + digestField(s.rt, requestID("fake-mise-gh"))
	for _, op := range []string{"preview", "approve"} {
		code, body := do("POST", "/api/sandboxes/dev/proposals/"+op, id+`,"scope":{"audit":true,"binaries":["/**"]}}`)
		var resp struct {
			Error       string
			Code        string
			Ambiguities []AmbiguityView
		}
		if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Ambiguities) != 1 || resp.Ambiguities[0].Rule != "provider_github_personal" ||
			resp.Code != "ambiguous_approve" || !slices.Equal(resp.Ambiguities[0].Diffs, []AmbiguityDiff{{"enforcement", "enforce", "audit"}}) ||
			!strings.Contains(resp.Error, "この承認は OpenShell に拒否されます") {
			t.Fatal(op, code, body)
		}
	}
	scope := `,"scope":{"rules":[{"method":"GET","path":"/**"}],"binaries":["/**"]}}`
	code, body := do("POST", "/api/sandboxes/dev/proposals/preview", id+scope)
	var preview struct{ YAML string }
	if code != 200 || json.Unmarshal([]byte(body), &preview) != nil || !strings.Contains(preview.YAML, "path: /**") || !strings.Contains(preview.YAML, "provider: github-personal") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/proposals/approve", id+scope); code != 200 {
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
	predicted, _ := predictAmbiguities(base, effective, approvalPlanOps(base, effective, rule.Name, (&ProposalScope{Audit: true, Binaries: []string{fakeMise}}).apply(*rule), nil))
	if len(predicted) != 1 || !strings.Contains(err.Error(), predicted[0].String()) {
		t.Fatal(predicted, err)
	}
	if msg, ok := translateGatewayError(err.Error(), nil); !ok || !strings.Contains(msg.text, "規則 allow_api_github_com_443 と 規則 provider_github_personal") {
		t.Fatal(msg)
	}
}

// Hand edits: a rule auditing api.openai.com while the provider's profile
// (endpoints of its own: OpenShell composes its rule) enforces is refused
// naming the provider; enforced it goes through.
func TestHandEditsAreCheckedForAmbiguity(t *testing.T) {
	_, _, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"openai"}`)
	code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/curl"],"host":"api.openai.com","ports":[443],"access":"full","audit":true}`)
	if code != 409 || !strings.Contains(body, "この変更は OpenShell に拒否されます") || !strings.Contains(body, "プロバイダー openai") || !strings.Contains(body, "プロバイダーの規則は変えられないので") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/curl"],"host":"api.openai.com","ports":[443],"access":"full"}`); code != 200 {
		t.Fatal(code, body)
	}
	// Uninspected (L4) does not compete for requests.
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", `{"binaries":["/usr/bin/wget"],"host":"api.openai.com","ports":[8443]}`); code != 200 {
		t.Fatal(code, body)
	}
}

// Attaching a provider whose profile enforces api.openai.com to a sandbox
// whose policy audits it: refused with the endpoint named and the fixes;
// switching it to enforce (keeping its rules) lets the provider on.
func TestAttachChecksAmbiguityAndFixes(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	// A rule from before (hand edits on a provider's host are refused now).
	if _, err := d.EditPolicy(context.Background(), newSandbox("dev"), PolicyEdit{Op: "add", Rule: "gh", Binaries: []string{"/usr/bin/gh"}, Host: "api.openai.com", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/repos/**"}}, Audit: true}); err != nil {
		t.Fatal(err)
	}
	code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"openai"}`)
	var resp struct {
		Error       string
		Ambiguities []AmbiguityView
	}
	if code != 409 || json.Unmarshal([]byte(body), &resp) != nil || len(resp.Ambiguities) != 1 {
		t.Fatal(code, body)
	}
	a := resp.Ambiguities[0]
	if a.Rule != "gh" || a.With != "_provider_openai" || a.WithProvider.Name != "openai" || a.Fix != "enforce" || !a.Audited || a.Host != "api.openai.com" ||
		!slices.Equal(a.Binaries, []string{"/usr/bin/gh"}) || len(a.Allows) != 1 || a.Allows[0].Path != "/repos/**" || !strings.Contains(resp.Error, "遮断に切り替える") {
		t.Fatalf("%s\n%+v", resp.Error, a)
	}
	if _, attached, _ := d.ListProviders(context.Background()); slices.Contains(attached["dev"], "openai") {
		t.Fatal("attached anyway")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"openai","mode":"enforce"}`); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(context.Background(), newSandbox("dev"))
	if e := rules["gh"].Endpoints[0]; e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) != 1 || e.Rules[0].Allow.Path != "/repos/**" {
		t.Fatalf("%+v", e)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"openai"}`); code != 200 {
		t.Fatal(code, body)
	}
}

func TestAttachFixRemovesTheEndpoint(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	if _, err := d.EditPolicy(context.Background(), newSandbox("dev"), PolicyEdit{Op: "add", Rule: "gh", Binaries: []string{"/usr/bin/gh"}, Host: "api.openai.com", Ports: []uint32{443}, Access: "read-only", Audit: true}); err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"openai"}`); code != 200 || !strings.Contains(body, `"fixed":[{"rule":"gh"`) {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(context.Background(), newSandbox("dev"))
	if _, ok := rules["gh"]; ok {
		t.Fatal("the endpoint (the rule's only one) is still there")
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"openai"}`); code != 200 {
		t.Fatal(code, body)
	}
}
