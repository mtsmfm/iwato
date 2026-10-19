package main

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"google.golang.org/protobuf/proto"
)

// Atlassian's scope: new providers get mode tools, which inspects the MCP
// endpoint and calls only the read tools and the named ones; providers at
// read-only render the shipped profile unchanged and show as every tool.
func TestAtlassianToolScope(t *testing.T) {
	base, _ := builtinProfile(atlassianProfileID)
	if got := defaultScopeMode(atlassianProfileID); got != scopeTools {
		t.Fatalf("default mode %s", got)
	}
	// The execute meta-tools run any tool named in their arguments.
	for _, w := range []string{"createJiraIssue", "execute", "executeRead", "executeWrite", "executeDestructive", "updateJiraScreen", "deleteJiraIssue"} {
		if slices.Contains(atlassianReadTools, w) {
			t.Errorf("%s is a read tool", w)
		}
	}
	for i, n := range atlassianReadTools {
		if !mcpToolPattern.MatchString(n) || slices.Contains(atlassianReadTools[:i], n) {
			t.Errorf("read tool %q", n)
		}
	}
	m := mustRender(t, "a", ProviderScope{Base: atlassianProfileID, Mode: scopeTools, Tools: []string{"createJiraIssue", "getJiraIssue", " createJiraIssue"}})
	if len(m.Endpoints) != 1 {
		t.Fatalf("%d endpoints", len(m.Endpoints))
	}
	e, b := m.Endpoints[0], base.proto().Endpoints[0]
	want := append(slices.Clone(atlassianReadTools), "createJiraIssue")
	if e.Protocol != "mcp" || e.Access != sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED || e.Host != b.Host || e.Path != b.Path || !slices.Equal(profileEndpointPorts(e), profileEndpointPorts(b)) ||
		len(e.Rules) != 1 || e.Rules[0].Allow.Method != "tools/call" || !slices.Equal(e.Rules[0].Allow.Params["name"].Any, want) ||
		!e.Mcp.GetAllowAllKnownMcpMethods() || !e.Mcp.GetStrictToolNames() {
		t.Fatalf("tools: %v", e)
	}
	if err := withinBase(m, base.proto()); err != nil {
		t.Fatal(err)
	}
	y, err := profileYAML(m)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := parseProfileYAML([]byte(y)); err != nil || profileHash(back) != profileHash(m) {
		t.Fatalf("does not read back: %v\n%s", err, y)
	}
	// Legacy read-only: the shipped profile, shown as every tool.
	legacy := mustRender(t, "a", ProviderScope{Base: atlassianProfileID, Mode: scopeReadOnly})
	if !proto.Equal(legacy.Endpoints[0], b) {
		t.Fatalf("legacy read-only changed: %v", legacy.Endpoints[0])
	}
	if v := scopeView(atlassianProfileID, ProviderScope{Base: atlassianProfileID, Mode: scopeReadOnly}, true); v.Mode != scopeFull || !slices.Equal(v.Modes, []string{scopeTools, scopeFull}) {
		t.Fatalf("legacy view %+v", v)
	}
	if err := validScopes(map[string]ProviderScope{"old": {Base: atlassianProfileID, Mode: scopeReadOnly}, "new": {Base: atlassianProfileID, Mode: scopeTools, Tools: []string{"createJiraIssue"}}}); err != nil {
		t.Fatal(err)
	}
	for _, sc := range []ProviderScope{
		{Base: atlassianProfileID, Mode: scopeTools, Tools: []string{"create*"}},
		{Base: atlassianProfileID, Mode: scopeTools, Tools: []string{"a b"}},
		{Base: "iwato-claude", Mode: scopeTools},
	} {
		if _, err := sc.normalize(); err == nil {
			t.Errorf("%+v accepted", sc)
		}
	}
	if sc, err := (ProviderScope{Base: atlassianProfileID, Mode: scopeFull, Tools: []string{"x"}}).normalize(); err != nil || len(sc.Tools) > 0 {
		t.Fatal(sc, err)
	}
}

// A proposal for MCP tools on an Atlassian provider in mode tools adds the
// write tools it names; tools/call of every tool means every tool (full).
func TestWidenScopeToTools(t *testing.T) {
	sc := ProviderScope{Base: atlassianProfileID, Mode: scopeTools, Tools: []string{"editJiraIssue"}}
	m := mustRender(t, "at", sc)
	bins := []string{"/home/dev/.local/share/claude/versions/*"}
	at := scopeTarget{name: "at", label: "Atlassian", base: atlassianProfileID, scope: sc, creds: profileScopes("at", "Atlassian", m), binaries: bins}
	mcp := func(rules ...v1.L7Allow) v1.NetworkPolicyRule {
		e := v1.PolicyNetworkEndpoint{Host: "mcp.atlassian.com", Port: 443, Path: "/v2/mcp/**", Protocol: "mcp", Enforcement: v1.NetworkEnforcementModeEnforce}
		for _, r := range rules {
			e.Rules = append(e.Rules, v1.L7Rule{Allow: &r})
		}
		return v1.NetworkPolicyRule{Binaries: policyBinaries(bins...), Endpoints: []v1.PolicyNetworkEndpoint{e}}
	}
	tool := func(names ...string) v1.L7Allow {
		return v1.L7Allow{Method: "tools/call", Params: map[string]v1.L7QueryMatcher{"name": {Any: names}}}
	}
	for _, c := range []struct {
		name   string
		rule   v1.NetworkPolicyRule
		tools  []string
		write  bool
		reason string
	}{
		{"a write tool", mcp(v1.L7Allow{Method: "initialize"}, tool("createJiraIssue", "getJiraIssue", "editJiraIssue")), []string{"createJiraIssue"}, false, ""},
		{"read tools", mcp(tool("getJiraIssue")), nil, false, ""},
		{"every tool", mcp(v1.L7Allow{Method: "tools/call"}), nil, true, ""},
		{"a glob", mcp(tool("create*")), nil, false, "ツールの名前ではありません"},
		{"another method", mcp(v1.L7Allow{Method: "x/admin"}), nil, false, "MCP のメソッド"},
	} {
		w := widenScope(c.rule, []scopeTarget{at})
		switch {
		case w == nil:
			t.Errorf("%s: no widening", c.name)
		case c.reason != "" && !strings.Contains(w.Reason, c.reason):
			t.Errorf("%s: reason %q", c.name, w.Reason)
		case c.reason == "" && (w.Reason != "" || w.Write != c.write || !slices.Equal(w.Tools, c.tools)):
			t.Errorf("%s: %+v", c.name, w)
		}
	}
	got, err := addToScope(sc, scopeAdd{tools: []string{"createJiraIssue"}})
	if err != nil || got.Mode != scopeTools || !slices.Equal(got.Tools, []string{"editJiraIssue", "createJiraIssue"}) {
		t.Fatal(got, err)
	}
	// A REST proposal on the endpoint is judged as for the other profiles.
	rest := restEndpoint("mcp.atlassian.com", 443)
	rest.Path = "/v2/mcp/**"
	rest.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/v2/mcp/**"}}}
	if w := widenScope(v1.NetworkPolicyRule{Binaries: policyBinaries(bins...), Endpoints: []v1.PolicyNetworkEndpoint{rest}}, []scopeTarget{at}); w == nil || w.Reason != "" || !w.Write {
		t.Fatalf("rest: %+v", w)
	}
}
