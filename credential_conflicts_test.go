package main

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// The match follows OpenShell's: host and port overlap, the provider's
// path covers the endpoint's, and the endpoint is not inspected.
func TestCredentialConflicts(t *testing.T) {
	claude := providerScopes("claude", "iwato-claude")
	atl := providerScopes("atl", atlassianProfileID)
	gh := providerScopes("gh", "iwato-github")
	if len(claude) != 1 || claude[0].Host != "api.anthropic.com" || len(gh) != 4 || len(atl) != 1 || atl[0].Path != "/v2/mcp/**" {
		t.Fatal(claude, gh, atl)
	}
	ep := func(host string, ports []uint32, protocol string) v1.PolicyNetworkEndpoint {
		return v1.PolicyNetworkEndpoint{Host: host, Ports: ports, Protocol: protocol}
	}
	rule := func(e v1.PolicyNetworkEndpoint) map[string]v1.NetworkPolicyRule {
		return map[string]v1.NetworkPolicyRule{"r": {Name: "r", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{e}}}
	}
	skip := ep("api.anthropic.com", []uint32{443}, "rest")
	skip.TLS = v1.NetworkTLSModeSkip
	optIn := ep("api.anthropic.com", []uint32{443}, "")
	optIn.AllowUninspectedCredentials = true
	mcpPath := ep("mcp.atlassian.com", []uint32{443}, "")
	mcpPath.Path = "/v2/mcp"
	for _, c := range []struct {
		name   string
		rules  map[string]v1.NetworkPolicyRule
		scopes []credScope
		mode   string
	}{
		{"L4 on the provider's host and port", rule(ep("api.anthropic.com", []uint32{443}, "")), claude, "L4-only"},
		{"uppercase host, several ports", rule(ep("API.anthropic.com", []uint32{80, 443}, "")), claude, "L4-only"},
		{"a wildcard host", rule(ep("*.anthropic.com", []uint32{443}, "")), claude, "L4-only"},
		{"tls: skip", rule(skip), claude, "tls: skip"},
		{"inspected", rule(ep("api.anthropic.com", []uint32{443}, "rest")), claude, ""},
		{"another port", rule(ep("api.anthropic.com", []uint32{8443}, "")), claude, ""},
		{"another host", rule(ep("anthropic.com", []uint32{443}, "")), claude, ""},
		{"explicit opt-in", rule(optIn), claude, ""},
		{"git over L4 with a GitHub provider", rule(ep("github.com", []uint32{443}, "")), gh, "L4-only"},
		// The Atlassian credential is bound to /v2/mcp/**: a host-wide
		// endpoint is not covered (OpenShell's path_pattern_covers).
		{"Atlassian, host-wide", rule(ep("mcp.atlassian.com", []uint32{443}, "")), atl, ""},
		{"Atlassian, the MCP path", rule(mcpPath), atl, "L4-only"},
		{"a provider's own rule", map[string]v1.NetworkPolicyRule{"_provider_claude": {Endpoints: []v1.PolicyNetworkEndpoint{ep("api.anthropic.com", []uint32{443}, "")}}}, claude, ""},
	} {
		got := credentialConflicts(c.rules, c.scopes)
		if c.mode == "" && len(got) != 0 || c.mode != "" && (len(got) != 1 || got[0].Mode != c.mode || got[0].Rule == "") {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	got := credentialConflicts(rule(ep("api.anthropic.com", []uint32{443}, "")), claude)
	if !slices.Equal(got[0].Binaries, []string{"/usr/bin/curl"}) || got[0].Provider != "claude" {
		t.Fatalf("%+v", got)
	}
	if m := conflictMessage(got, true); !strings.Contains(m, "規則 r の api.anthropic.com:443 (L4-only)") || !strings.Contains(m, "規則から外して") {
		t.Fatal(m)
	}
}
