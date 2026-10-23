package main

import (
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// The editor's YAML holds every field a rule can have: a policy read,
// rendered and read back is the same, so a save changes only what the
// owner changed.
func TestPolicyYAMLRoundTrip(t *testing.T) {
	strict := true
	rich := v1.PolicyNetworkEndpoint{Host: "mcp.example.com", Port: 443, Ports: []uint32{443, 8443}, Path: "/v1/**", Protocol: "mcp", TLS: v1.NetworkTLSModeTerminate,
		Enforcement: v1.NetworkEnforcementModeAudit, AllowedIPs: []string{"10.0.0.0/8"}, AllowEncodedSlash: true, JSONRPCMaxBodyBytes: 4096,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/v1/mcp", Params: map[string]v1.L7QueryMatcher{"name": {Any: []string{"read_*", "list_*"}}}}},
			{Allow: &v1.L7Allow{Method: "GET", Path: "/q", Query: map[string]v1.L7QueryMatcher{"a": {Glob: "x*"}}}}},
		DenyRules: []v1.L7DenyRule{{Method: "POST", Path: "/v1/admin", Command: "drop"}},
		Mcp:       &types.McpOptions{Versions: []string{"2025-06-18"}, StrictToolNames: &strict}}
	gql := v1.PolicyNetworkEndpoint{Host: "api.example.org", Port: 443, Protocol: "graphql", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules:                   []v1.L7Rule{{Allow: &v1.L7Allow{OperationType: "query", Fields: []string{"viewer"}}}},
		GraphqlPersistedQueries: map[string]v1.GraphqlOperation{"abc": {OperationType: "query", OperationName: "Q", Fields: []string{"viewer"}}}}
	rules := presetRules(allBuiltinPresets())
	rules["rich"] = v1.NetworkPolicyRule{Name: "rich", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/node"}}, Endpoints: []v1.PolicyNetworkEndpoint{rich, gql, {Host: "pypi.org", Port: 443, TLS: v1.NetworkTLSModeSkip}}}
	bound := rich
	bound.Host, bound.CredentialBinding = "bound.example.com", &types.NetworkCredentialBinding{Provider: "p"}
	rules["bound"] = v1.NetworkPolicyRule{Name: "bound", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/node"}}, Endpoints: []v1.PolicyNetworkEndpoint{bound}}
	doc, err := policyRulesYAML(rules)
	if err != nil {
		t.Fatal(err)
	}
	back, err := parsePolicyYAML([]byte(doc))
	if err != nil {
		t.Fatal(err, doc)
	}
	if _, diff := policyYAMLPlan(rules, back); !diff.empty() {
		b, _ := json.Marshal(diff)
		t.Fatalf("a read-back policy differs: %s\n%s", b, doc)
	}
}

func TestPolicyYAMLPlan(t *testing.T) {
	ep := func(host string) v1.PolicyNetworkEndpoint {
		return v1.PolicyNetworkEndpoint{Host: host, Port: 443, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Access: v1.NetworkAccessPresetReadOnly}
	}
	advised := ep("a.example.com")
	advised.AdvisorProposed = true
	bin := []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}
	old := map[string]v1.NetworkPolicyRule{
		"keep":   {Name: "keep", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{advised}},
		"change": {Name: "change", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{advised, ep("b.example.com")}},
		"drop":   {Name: "drop", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{ep("c.example.com")}},
	}
	edited := map[string]v1.NetworkPolicyRule{
		"keep":   {Name: "keep", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{ep("a.example.com")}},
		"change": {Name: "change", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{ep("a.example.com")}},
		"new":    {Name: "new", Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{ep("d.example.com")}},
	}
	rules, diff := policyYAMLPlan(old, edited)
	names := func(l []PolicyYAMLRule) string {
		var s []string
		for _, r := range l {
			s = append(s, r.Name)
		}
		return strings.Join(s, ",")
	}
	if names(diff.Added) != "new" || names(diff.Removed) != "drop" || names(diff.Changed) != "change" {
		t.Fatalf("diff: %+v", diff)
	}
	// Unchanged rules are the gateway's; a changed one keeps the marker
	// on the endpoint it kept.
	if !rules["keep"].Endpoints[0].AdvisorProposed || !rules["change"].Endpoints[0].AdvisorProposed || len(rules["change"].Endpoints) != 1 {
		t.Fatalf("markers: %+v", rules)
	}
	// MCP's strict_tool_names: false is not the same as unset (true).
	strict, loose := ep("m.example.com"), ep("m.example.com")
	loose.Mcp = &types.McpOptions{StrictToolNames: new(bool)}
	if _, d := policyYAMLPlan(map[string]v1.NetworkPolicyRule{"m": {Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{strict}}}, map[string]v1.NetworkPolicyRule{"m": {Binaries: bin, Endpoints: []v1.PolicyNetworkEndpoint{loose}}}); len(d.Changed) != 1 {
		t.Fatalf("turning strict tool names off is no change: %+v", d)
	}
	if _, ok := rules["drop"]; ok {
		t.Fatal("a removed rule stayed")
	}
}

// ADR 0006: no rule written as YAML may reach an attached provider's
// credential, inspected or not; one already there may stay as it was
// (for the same or fewer programs).
func TestPolicyYAMLProviderHosts(t *testing.T) {
	targets := []scopeTarget{{name: "github-personal", label: "GitHub", creds: []credScope{{Provider: "github-personal", Host: "api.github.com", Port: 443}}}}
	gh := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/repos/other/repo/**"}}}}
	curl := []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}
	old := map[string]v1.NetworkPolicyRule{"legacy": {Name: "legacy", Binaries: append(curl, v1.PolicyNetworkBinary{Path: "/usr/bin/git"}), Endpoints: []v1.PolicyNetworkEndpoint{gh}}}
	other := v1.PolicyNetworkEndpoint{Host: "example.com", Port: 443}
	cases := []struct {
		what string
		rule v1.NetworkPolicyRule
		hit  bool
	}{
		{"a new rule on the provider's host", v1.NetworkPolicyRule{Binaries: curl, Endpoints: []v1.PolicyNetworkEndpoint{gh}}, true},
		{"a new uninspected rule on it", v1.NetworkPolicyRule{Binaries: curl, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.github.com", Port: 443}}}, true},
		{"another host", v1.NetworkPolicyRule{Binaries: curl, Endpoints: []v1.PolicyNetworkEndpoint{other}}, false},
	}
	for _, c := range cases {
		rules := map[string]v1.NetworkPolicyRule{"r": c.rule}
		if hits := policyYAMLProviderHits(old, rules, []string{"r"}, targets); (len(hits) > 0) != c.hit {
			t.Errorf("%s: %+v", c.what, hits)
		}
	}
	legacy := func(bins []v1.PolicyNetworkBinary, eps ...v1.PolicyNetworkEndpoint) []ProviderHostHit {
		return policyYAMLProviderHits(old, map[string]v1.NetworkPolicyRule{"legacy": {Binaries: bins, Endpoints: eps}}, []string{"legacy"}, targets)
	}
	if hits := legacy(curl, gh, other); len(hits) != 0 {
		t.Error("an endpoint kept as it was, for fewer programs:", hits)
	}
	if hits := legacy(append(curl, v1.PolicyNetworkBinary{Path: "/usr/bin/python3"}), gh); len(hits) == 0 {
		t.Error("a program added to a provider's host passed")
	}
	wider := gh
	wider.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "*", Path: "/**"}}}
	if hits := legacy(curl, wider); len(hits) == 0 {
		t.Error("a widened provider's endpoint passed")
	}
}

// Iwato's presets with rules on GitHub's hosts (packages) fit the
// GitHub provider's scope; wider access does not.
func TestBeyondScope(t *testing.T) {
	for _, prof := range allProfiles() {
		if b := beyondScope(presetRules(allBuiltinPresets()), prof.ID, prof.proto()); len(b) != 0 {
			t.Errorf("Iwato's presets with %s: %+v", prof.ID, b)
		}
	}
	gh, _ := profileByID("iwato-github")
	post := v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/git"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "github.com", Port: 443, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/o/r.git/git-receive-pack"}}}}}}
	mutation := v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.github.com", Port: 443, Path: "/graphql", Protocol: "graphql", Enforcement: v1.NetworkEnforcementModeEnforce, Access: v1.NetworkAccessPresetReadWrite}}}
	audited := v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: "uploads.github.com", Port: 443, Protocol: "rest", Access: v1.NetworkAccessPresetReadOnly, Enforcement: v1.NetworkEnforcementModeAudit}}}
	if b := beyondScope(map[string]v1.NetworkPolicyRule{"push": post, "mutation": mutation, "audited": audited}, "github", gh.proto()); len(b) != 3 {
		t.Fatalf("wider rules: %+v", b)
	}
}

// A rule bound to a provider's credential keeps its programs.
func TestBoundBinaries(t *testing.T) {
	bound := v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "x.example.com", Port: 443, CredentialBinding: &types.NetworkCredentialBinding{Provider: "p"}}}}
	old := map[string]v1.NetworkPolicyRule{"b": bound}
	more := cloneRule(bound)
	more.Binaries = append(more.Binaries, v1.PolicyNetworkBinary{Path: "/usr/bin/curl"})
	if checkBoundBinaries(old, map[string]v1.NetworkPolicyRule{"b": more}, []string{"b"}) == nil {
		t.Fatal("a program added to a bound rule passed")
	}
	other := cloneRule(bound)
	other.Endpoints[0].Port = 8443
	if err := checkBoundBinaries(old, map[string]v1.NetworkPolicyRule{"b": other}, []string{"b"}); err != nil {
		t.Fatal("same programs:", err)
	}
}

// Several sandboxes' locks are taken in one order, whatever the order asked.
func TestLockPolicyOrder(t *testing.T) {
	s := &Server{}
	done := make(chan bool)
	for i := 0; i < 2; i++ {
		go func(i int) {
			for j := 0; j < 200; j++ {
				if i == 0 {
					s.lockPolicy("a", "b")()
				} else {
					s.lockPolicy("b", "a", "b")()
				}
			}
			done <- true
		}(i)
	}
	<-done
	<-done
}
