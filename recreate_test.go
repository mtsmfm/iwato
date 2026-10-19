package main

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// TestCarrySteps: the new sandbox's rules become the old one's, one rule per
// update, leaving equal rules (an empty list is no list), the providers'
// rules and those not named to be removed alone; a resumed carry-over only
// adds what is missing.
func TestCarrySteps(t *testing.T) {
	rest := func(host string) v1.PolicyNetworkEndpoint {
		return v1.PolicyNetworkEndpoint{Host: host, Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Access: v1.NetworkAccessPresetReadOnly}
	}
	rule := func(name string, eps ...v1.PolicyNetworkEndpoint) v1.NetworkPolicyRule {
		return v1.NetworkPolicyRule{Name: name, Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: eps}
	}
	same := rule("preset_same", rest("a.example"))
	sameAgain := cloneRule(same)
	sameAgain.Endpoints[0].Rules, sameAgain.Endpoints[0].AllowedIPs = []v1.L7Rule{}, []string{}
	current := map[string]v1.NetworkPolicyRule{
		"preset_same":    same,
		"preset_removed": rule("preset_removed", rest("b.example")),
		"changed":        rule("changed", rest("c.example")),
		"_provider_gh":   rule("_provider_gh", rest("api.github.com")),
		"approved_since": rule("approved_since", rest("f.example")),
	}
	changed := rule("changed", rest("c.example"), rest("d.example"))
	carried := map[string]v1.NetworkPolicyRule{
		"preset_same":  sameAgain,
		"changed":      changed,
		"added":        rule("", rest("e.example")),
		"_provider_xx": rule("_provider_xx", rest("x.example")),
	}
	describe := func(steps []carryStep) []string {
		var got []string
		for _, s := range steps {
			var ops []string
			for _, op := range s.Ops {
				switch {
				case op.RemoveRule != nil:
					ops = append(ops, "remove "+op.RemoveRule.RuleName)
				case op.AddRule != nil:
					ops = append(ops, "add "+op.AddRule.RuleName+"/"+op.AddRule.Rule.Name)
				}
			}
			got = append(got, s.Rule+": "+strings.Join(ops, ", "))
		}
		return got
	}
	remove := []string{"preset_removed", "not_there"}
	steps := carrySteps(current, carried, remove, true)
	want := []string{"preset_removed: remove preset_removed", "added: add added/added", "changed: remove changed, add changed/changed"}
	if got := describe(steps); !slices.Equal(got, want) {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := describe(carrySteps(current, carried, remove, false)); !slices.Equal(got, []string{"added: add added/added"}) {
		t.Fatalf("resumed steps: %v", got)
	}
	// Applied as the gateway merges, they give the carried rules.
	rules := current
	for _, s := range steps {
		var err error
		if rules, err = simulateMerge(rules, s.Ops); err != nil {
			t.Fatal(err)
		}
	}
	for name, r := range carried {
		if !isProviderRule(name) && !sameRule(rules[name], r) {
			t.Errorf("rule %s: %+v, want %+v", name, rules[name], r)
		}
	}
	if _, ok := rules["preset_removed"]; ok {
		t.Error("preset_removed stayed")
	}
	if _, ok := rules["approved_since"]; !ok {
		t.Error("a rule the old sandbox did not have was removed")
	}
}
