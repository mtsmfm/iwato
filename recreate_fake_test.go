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

// TestRecreateFake recreates through the endpoint on the fake runtime:
// the providers and the rules (one added by hand) come back, the
// configuration keeps its settings, and a rule OpenShell would refuse is
// reported while the others go in.
func TestRecreateFake(t *testing.T) {
	s, _ := newTestServer(t)
	d := s.rt.(*Fake)
	d.seedProviders()
	cookie, csrf := login(t, s)
	ctx := context.Background()
	if res, b := request(t, s, "/api/sandboxes/dev/recreate", "POST", `{"confirm":"dev"}`, cookie, csrf); res.StatusCode != 500 || !strings.Contains(string(b), "まだ OpenShell にありません") {
		t.Fatalf("recreate before a start: %d %s", res.StatusCode, b)
	}
	if res, b := request(t, s, "/api/sandboxes/dev/start", "POST", `{}`, cookie, csrf); res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, b)
	}
	if res, b := request(t, s, "/api/sandboxes/dev/providers/attach", "POST", `{"provider":"github-personal"}`, cookie, csrf); res.StatusCode != 200 {
		t.Fatalf("attach: %d %s", res.StatusCode, b)
	}
	sb, _ := s.lookupSandbox("dev")
	if _, err := d.EditPolicy(ctx, sb, PolicyEdit{Op: "add", Rule: "hand_rule", Binaries: []string{"/usr/bin/curl"}, Host: "hand.example", Ports: []uint32{443}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := d.PolicyLayers(ctx, sb)
	if res, b := request(t, s, "/api/sandboxes/dev/recreate", "POST", `{"confirm":"de"}`, cookie, csrf); res.StatusCode != 400 {
		t.Fatalf("recreate without the name: %d %s", res.StatusCode, b)
	}
	res, b := request(t, s, "/api/sandboxes/dev/recreate", "POST", `{"confirm":"dev"}`, cookie, csrf)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"failed":[]`) {
		t.Fatalf("recreate: %d %s", res.StatusCode, b)
	}
	if st := d.Status(ctx, sb); st.State != "running" {
		t.Fatalf("after recreate: %+v", st)
	}
	_, attached, _ := d.ListProviders(ctx)
	if !slices.Equal(attached["dev"], []string{"github-personal"}) {
		t.Fatalf("providers after recreate: %v", attached["dev"])
	}
	after, _, _ := d.PolicyLayers(ctx, sb)
	if len(after) != len(before) {
		t.Fatalf("rules after recreate: %d, before %d", len(after), len(before))
	}
	for name, r := range before {
		if !sameRule(after[name], r) {
			t.Errorf("rule %s changed: %+v, was %+v", name, after[name], r)
		}
	}
	if sb, _ = s.lookupSandbox("dev"); sb.Carry != nil || !slices.Equal(sb.Providers, []string{"github-personal"}) {
		t.Fatalf("configuration after recreate: %+v", sb)
	}

	// An earlier recreate stopped after the delete: the carried rules
	// include one that the GitHub provider's enforced endpoint makes
	// ambiguous. It is reported; the others go in.
	if err := d.Delete(ctx, sb, false); err != nil {
		t.Fatal(err)
	}
	bad := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeAudit, Access: v1.NetworkAccessPresetReadOnly}
	carry, err := newCarriedPolicy(map[string]v1.NetworkPolicyRule{
		"audit_github": {Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{bad}},
		"kept":         {Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "kept.example", Port: 443, Ports: []uint32{443}}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) { list[0].Carry = carry; return list, nil }); err != nil {
		t.Fatal(err)
	}
	res, b = request(t, s, "/api/sandboxes/dev/recreate", "POST", `{"confirm":"dev"}`, cookie, csrf)
	var out struct {
		Failed []CarryFailure `json:"failed"`
	}
	if json.Unmarshal(b, &out) != nil || res.StatusCode != 200 || len(out.Failed) != 1 || out.Failed[0].Rule != "audit_github" ||
		!strings.Contains(out.Failed[0].Error, "_provider_github_personal") || out.Failed[0].Code != "ambiguous_carry" || len(out.Failed[0].Ambiguities) != 1 {
		t.Fatalf("recreate with a refused rule: %d %s", res.StatusCode, b)
	}
	after, _, _ = d.PolicyLayers(ctx, sb)
	if _, ok := after["kept"]; !ok {
		t.Fatalf("the other rule did not go in: %v", after)
	}
	if _, ok := after["audit_github"]; ok {
		t.Fatal("the refused rule went in")
	}
	if sb, _ = s.lookupSandbox("dev"); sb.Carry != nil {
		t.Fatal("the carried rules stayed after they were applied")
	}

	// A carry-over that did not finish, resumed by a later start: a rule
	// approved meanwhile, and the others the sandbox has, stay.
	if _, err := d.EditPolicy(ctx, sb, PolicyEdit{Op: "remove", Rule: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EditPolicy(ctx, sb, PolicyEdit{Op: "add", Rule: "approved_later", Binaries: []string{"/usr/bin/curl"}, Host: "later.example", Ports: []uint32{443}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ = d.PolicyLayers(ctx, sb)
	if err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) { list[0].Carry = carry; return list, nil }); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(ctx, sb); err != nil {
		t.Fatal(err)
	}
	if res, b := request(t, s, "/api/sandboxes/dev/start", "POST", `{}`, cookie, csrf); res.StatusCode != 200 || !strings.Contains(string(b), "audit_github") {
		t.Fatalf("start resuming the carry-over: %d %s", res.StatusCode, b)
	}
	after, _, _ = d.PolicyLayers(ctx, sb)
	for name := range before {
		if _, ok := after[name]; !ok {
			t.Errorf("rule %s was removed by the resumed carry-over", name)
		}
	}
	if _, ok := after["kept"]; !ok {
		t.Error("the missing carried rule was not added")
	}
}
