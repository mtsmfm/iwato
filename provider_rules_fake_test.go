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

// boundTo are the rules' endpoints bound to provider, as rule/host.
func boundTo(rules map[string]v1.NetworkPolicyRule, provider string) []string {
	var out []string
	for name, r := range rules {
		for _, e := range r.Endpoints {
			if e.CredentialBinding != nil && e.CredentialBinding.Provider == provider {
				out = append(out, name+"/"+e.Host)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Attaching commits the provider's initial rules, bound to it, after the
// attach; detaching takes every endpoint bound to it out before the
// detach (the gateway refuses it while the policy binds the provider).
func TestProviderAttachCommitsItsRules(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	sb, _ := s.lookupSandbox("dev")
	ctx := context.Background()
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"runpod"}`); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(ctx, sb)
	if got := boundTo(rules, "runpod"); len(got) != 5 || !strings.HasPrefix(got[0], "provider_runpod/") {
		t.Fatal(got)
	}
	commits, _ := d.PolicyCommits(ctx, sb, 1)
	if len(commits) != 1 || commits[0].Subject != "Attach provider runpod" {
		t.Fatalf("%+v", commits)
	}
	// One endpoint unbound: it stays when the provider goes.
	if _, err := d.SetBinding(ctx, sb, PolicyBinding{Rule: "provider_runpod", Host: "rest.runpod.io", Ports: []uint32{443}, Path: "/v1/**"}); err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/detach", `{"provider":"runpod"}`); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ = d.PolicyRules(ctx, sb)
	if got := boundTo(rules, "runpod"); len(got) != 0 {
		t.Fatal("still bound:", got)
	}
	if r, ok := rules["provider_runpod"]; !ok || len(r.Endpoints) != 1 || r.Endpoints[0].Host != "rest.runpod.io" {
		t.Fatalf("the unbound endpoint goes with the rest: %+v", r)
	}
	if _, attached, _ := d.ListProviders(ctx); slices.Contains(attached["dev"], "runpod") {
		t.Fatal("still attached")
	}
}

// The binding API attaches an attached provider's credential to an
// inspected endpoint of the policy and removes it.
func TestPolicyBindingAPI(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	sb, _ := s.lookupSandbox("dev")
	ctx := context.Background()
	bind := func(rule, host, provider string) (int, string) {
		return do("POST", "/api/sandboxes/dev/policy/binding", `{"rule":"`+rule+`","host":"`+host+`","ports":[443],"path":"","provider":"`+provider+`"}`)
	}
	if code, body := bind("allow_registry_npmjs_org_443", "registry.npmjs.org", "huggingface"); code != 409 || !strings.Contains(body, "binding_not_attached") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"huggingface"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := bind("allow_registry_npmjs_org_443", "registry.npmjs.org", "huggingface"); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ := d.PolicyRules(ctx, sb)
	if got := boundTo(rules, "huggingface"); !slices.Contains(got, "allow_registry_npmjs_org_443/registry.npmjs.org") {
		t.Fatal(got)
	}
	if code, body := bind("allow_registry_npmjs_org_443", "registry.npmjs.org", ""); code != 200 {
		t.Fatal(code, body)
	}
	rules, _ = d.PolicyRules(ctx, sb)
	if got := boundTo(rules, "huggingface"); slices.Contains(got, "allow_registry_npmjs_org_443/registry.npmjs.org") {
		t.Fatal(got)
	}
	// Not inspected: OpenShell binds no credential there.
	if code, body := bind("allow_pypi_org_443", "pypi.org", "huggingface"); code != 409 || !strings.Contains(body, "policy_endpoint_not_inspected") {
		t.Fatal(code, body)
	}
}

// An approval on a host an attached provider's credential is meant for
// offers the credential: off by default, bound with attach true. A path
// under the provider's endpoint carries it by default.
func TestApprovalOffersTheCredential(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	code, body := do("GET", "/api/sandboxes/dev/proposals", "")
	var v ProposalView
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil {
		t.Fatal(code, body)
	}
	i := slices.IndexFunc(v.Pending, func(p PendingProposal) bool { return p.ID == requestID("fake-pr") })
	if i < 0 || len(v.Pending[i].Attach) != 1 || v.Pending[i].Attach[0].Provider != "github-personal" || len(v.Pending[i].Offer) != 0 {
		t.Fatalf("%+v", v.Pending[i])
	}
	plan := v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{restEndpoint("uploads.github.com", 443), l4Endpoint("github.com", 443), restEndpoint("example.com", 443)}}
	list, attached, _ := d.ListProviders(context.Background())
	if offers := credentialOffers(plan, list, attached["dev"]); len(offers) != 1 || offers[0].Host != "uploads.github.com" || offers[0].Provider != "github-personal" {
		t.Fatalf("%+v", offers)
	}
	sb, _ := s.lookupSandbox("dev")
	// Without the provider's endpoint on uploads.github.com (/**, which a
	// path-less approval would have to match), the credential is offered.
	if _, err := d.EditPolicy(context.Background(), sb, PolicyEdit{Op: "remove-endpoint", Rule: "provider_github_personal", Host: "uploads.github.com", Ports: []uint32{443}}); err != nil {
		t.Fatal(err)
	}
	for _, attach := range []string{"false", "true"} {
		id := requestID("fake-uploads")
		if attach == "true" {
			if code, body := do("POST", "/api/sandboxes/dev/proposals/undo", `{"requestId":"`+id+`"}`); code != 200 {
				t.Fatal(code, body)
			}
		}
		code, body := do("POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+id+`"`+digestField(s.rt, id)+`,"scope":{"readOnly":true,"binaries":["/usr/bin/gh"],"attach":`+attach+`}}`)
		if code != 200 {
			t.Fatal(attach, code, body)
		}
		rules, _ := d.PolicyRules(context.Background(), sb)
		bound := slices.ContainsFunc(boundTo(rules, "github-personal"), func(x string) bool { return strings.HasPrefix(x, "allow_uploads_github_com_443") })
		if bound != (attach == "true") {
			t.Fatal(attach, boundTo(rules, "github-personal"))
		}
	}
}
