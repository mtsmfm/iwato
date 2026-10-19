package main

import (
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// The cases of OpenShell v0.1.4's (unchanged since v0.1.2) crates/openshell-policy/src/ambiguity.rs
// tests, in the same order and with the same names.

func ambEndpoint(host string, port uint32) v1.PolicyNetworkEndpoint {
	return v1.PolicyNetworkEndpoint{Host: host, Port: port}
}

func ambPolicy(left, right v1.PolicyNetworkEndpoint) map[string]v1.NetworkPolicyRule {
	return map[string]v1.NetworkPolicyRule{
		"left":  {Name: "left", Endpoints: []v1.PolicyNetworkEndpoint{left}, Binaries: policyBinaries("/usr/bin/curl")},
		"right": {Name: "right", Endpoints: []v1.PolicyNetworkEndpoint{right}, Binaries: policyBinaries("/usr/bin/bash")},
	}
}

func hasConflict(a []EndpointAmbiguity, field string) bool {
	for _, x := range a {
		for _, c := range x.Conflicts {
			if strings.Contains(c, field) {
				return true
			}
		}
	}
	return false
}

func TestAmbiguityExactAndWildcardHostsOverlap(t *testing.T) {
	if !hostPatternsOverlap("api.example.com", "*.example.com") || !hostPatternsOverlap("us-aiplatform.googleapis.com", "*-aiplatform.googleapis.com") || hostPatternsOverlap("api.example.com", "*.other.com") {
		t.Fatal("exact and wildcard hosts")
	}
}

func TestAmbiguityIntersectingWildcardsAreDetected(t *testing.T) {
	if !hostPatternsOverlap("*.example.com", "api.*.com") || !hostPatternsOverlap("**.example.com", "api.example.com") || hostPatternsOverlap("*.example.com", "*.example.org") {
		t.Fatal("intersecting wildcards")
	}
	// * stays within a label; ** crosses them.
	if hostPatternsOverlap("*.example.com", "a.b.example.com") || !hostPatternsOverlap("**.example.com", "a.b.example.com") {
		t.Fatal("label delimiter")
	}
}

func TestAmbiguityDisjointPortsDoNotOverlap(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.TLS = v1.NetworkTLSModeSkip
	if a := findEndpointAmbiguities(ambPolicy(left, ambEndpoint("api.example.com", 8443))); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityCompatibleRequestRulesMayOverlap(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Protocol, left.TLS = "rest", v1.NetworkTLSModeSkip
	right := left
	left.Access, right.Access = v1.NetworkAccessPresetReadOnly, v1.NetworkAccessPresetReadWrite
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityAdvisorProvenanceDoesNotMakeEndpointsAmbiguous(t *testing.T) {
	explicit := ambEndpoint("api.example.com", 443)
	proposed := explicit
	proposed.AdvisorProposed = true
	if a := findEndpointAmbiguities(ambPolicy(explicit, proposed)); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityPlainL4EndpointDoesNotCompeteWithL7(t *testing.T) {
	right := ambEndpoint("api.example.com", 443)
	right.Protocol, right.Enforcement = "rest", v1.NetworkEnforcementModeEnforce
	if a := findEndpointAmbiguities(ambPolicy(ambEndpoint("api.example.com", 443), right)); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityDisjointPathSpecificProtocolsMayOverlap(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Path, left.Protocol = "/graphql", "graphql"
	right := ambEndpoint("api.example.com", 443)
	right.Path, right.Protocol = "/repos/**", "rest"
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityQuestionMarkPathOverlapIsDetected(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Path, left.Protocol = "/v?", "rest"
	right := ambEndpoint("api.example.com", 443)
	right.Path, right.Protocol = "/v1", "graphql"
	a := findEndpointAmbiguities(ambPolicy(left, right))
	if len(a) != 1 || !hasConflict(a, "protocol") {
		t.Fatal(a)
	}
}

func TestAmbiguityCharacterClassPaths(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Path, left.Protocol = "/v[12]", "rest"
	right := ambEndpoint("api.example.com", 443)
	right.Path, right.Protocol = "/v[23]", "graphql"
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 1 {
		t.Fatal("overlapping classes", a)
	}
	right.Path = "/v[34]"
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 0 {
		t.Fatal("disjoint classes", a)
	}
}

func TestAmbiguityNegatedCharacterClassPaths(t *testing.T) {
	if !pathPatternsOverlap("/v[!0]", "/v1") || pathPatternsOverlap("/v[!0]", "/v0") {
		t.Fatal("negated class")
	}
}

func TestAmbiguityMoreSpecificPathMayOverrideRequestMetadata(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Protocol, left.Enforcement = "rest", v1.NetworkEnforcementModeEnforce
	right := ambEndpoint("api.example.com", 443)
	right.Path, right.Protocol, right.Enforcement = "/graphql", "graphql", v1.NetworkEnforcementModeEnforce
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 0 {
		t.Fatal(a)
	}
}

func TestAmbiguityExactWildcardTLSConflictIsRejected(t *testing.T) {
	left := ambEndpoint("*.example.com", 443)
	left.TLS = v1.NetworkTLSModeSkip
	a := findEndpointAmbiguities(ambPolicy(left, ambEndpoint("api.example.com", 443)))
	if len(a) != 1 || !strings.Contains(a[0].Conflicts[0], "tls") || !strings.Contains(a[0].String(), "left") || !strings.Contains(a[0].String(), "right") {
		t.Fatal(a)
	}
}

func TestAmbiguityAllowedIPConflictIsRejectedRegardlessOfOrder(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.AllowedIPs = []string{"10.0.1.0/24", "10.0.0.0/24"}
	compatible := ambEndpoint("api.example.com", 443)
	compatible.AllowedIPs = []string{"10.0.0.0/24", "10.0.1.0/24"}
	if a := findEndpointAmbiguities(ambPolicy(left, compatible)); len(a) != 0 {
		t.Fatal(a)
	}
	conflicting := ambEndpoint("api.example.com", 443)
	conflicting.AllowedIPs = []string{"10.0.2.0/24"}
	if a := findEndpointAmbiguities(ambPolicy(left, conflicting)); !hasConflict(a, "allowed_ips") {
		t.Fatal(a)
	}
}

func TestAmbiguityCredentialAndParserConflictsOnSamePath(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.Protocol, left.CredentialSigning, left.SigningService = "rest", "sigv4", "execute-api"
	right := left
	right.SigningService, right.AllowEncodedSlash = "bedrock", true
	a := findEndpointAmbiguities(ambPolicy(left, right))
	if !hasConflict(a, "signing_service") || !hasConflict(a, "allow_encoded_slash") {
		t.Fatal(a)
	}
}

func TestAmbiguityCredentialBindingConflicts(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.CredentialBinding = &types.NetworkCredentialBinding{Provider: "provider-a"}
	right := ambEndpoint("api.example.com", 443)
	right.CredentialBinding = &types.NetworkCredentialBinding{Provider: "provider-b"}
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 1 || !hasConflict(a, "credential_binding.provider") {
		t.Fatal(a)
	}
}

func TestAmbiguityJSONRPCBodyLimitWithinTheSameProtocol(t *testing.T) {
	jsonRPC := ambEndpoint("api.example.com", 443)
	jsonRPC.Protocol, jsonRPC.JSONRPCMaxBodyBytes = "json-rpc", 1024
	mcp := ambEndpoint("api.example.com", 443)
	mcp.Protocol, mcp.JSONRPCMaxBodyBytes = "mcp", 2048
	a := findEndpointAmbiguities(ambPolicy(jsonRPC, mcp))
	if len(a) != 1 || !hasConflict(a, "protocol") || hasConflict(a, "json_rpc_max_body_bytes") {
		t.Fatal(a)
	}
	other := mcp
	other.JSONRPCMaxBodyBytes = 4096
	if a := findEndpointAmbiguities(ambPolicy(mcp, other)); len(a) != 1 || !hasConflict(a, "json_rpc_max_body_bytes") {
		t.Fatal(a)
	}
}

func TestAmbiguityWebsocketGraphQLClassification(t *testing.T) {
	graphql := ambEndpoint("api.example.com", 443)
	graphql.Protocol = "websocket"
	graphql.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{OperationType: "subscription"}}}
	transport := ambEndpoint("api.example.com", 443)
	transport.Protocol = "websocket"
	transport.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "WEBSOCKET_TEXT"}}}
	if a := findEndpointAmbiguities(ambPolicy(graphql, transport)); len(a) != 1 || !hasConflict(a, "websocket_graphql_policy") {
		t.Fatal(a)
	}
	left := ambEndpoint("api.example.com", 443)
	left.Protocol, left.PersistedQueries = "websocket", "allow_registered"
	right := left
	right.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{OperationName: "Events"}}}
	if a := findEndpointAmbiguities(ambPolicy(left, right)); len(a) != 0 {
		t.Fatal("matching classification", a)
	}
}

func TestAmbiguityDifferentBinaryListsDoNotHideIt(t *testing.T) {
	left := ambEndpoint("api.example.com", 443)
	left.TLS = v1.NetworkTLSModeSkip
	if a := findEndpointAmbiguities(ambPolicy(left, ambEndpoint("api.example.com", 443))); len(a) != 1 {
		t.Fatal(a)
	}
}

func TestAmbiguityExplicitTCPAndOmittedProtocol(t *testing.T) {
	tcp := ambEndpoint("api.example.com", 443)
	tcp.Protocol, tcp.TLS = "tcp", v1.NetworkTLSModeSkip
	omitted := ambEndpoint("api.example.com", 443)
	omitted.TLS = v1.NetworkTLSModeSkip
	if a := findEndpointAmbiguities(ambPolicy(tcp, omitted)); len(a) != 1 || !hasConflict(a, "transparent_tcp_eligible") {
		t.Fatal(a)
	}
}

// The refusal seen on a real gateway: the GitHub provider's rule enforces
// api.github.com, an approval audits it. The message is OpenShell's,
// word for word (but for the rules' order, which is the map's there).
func TestAmbiguityGitHubProviderAndAudit(t *testing.T) {
	provider := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Protocol: "rest", Access: v1.NetworkAccessPresetReadOnly, Enforcement: v1.NetworkEnforcementModeEnforce}
	graphql := provider
	graphql.Path, graphql.Protocol = "/graphql", "graphql"
	audited := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeAudit,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}}
	rules := map[string]v1.NetworkPolicyRule{
		"_provider_github":         {Name: "_provider_github", Endpoints: []v1.PolicyNetworkEndpoint{provider, graphql}},
		"allow_api_github_com_443": {Name: "allow_api_github_com_443", Endpoints: []v1.PolicyNetworkEndpoint{audited}},
	}
	a := findEndpointAmbiguities(rules)
	want := "network policies '_provider_github' endpoint[0] (api.github.com:443) and 'allow_api_github_com_443' endpoint[0] (api.github.com:443) overlap on port(s) 443 with conflicting metadata: enforcement=1 vs 2"
	if len(a) != 1 || a[0].String() != want {
		t.Fatal(a)
	}
	// Enforced, it agrees (the rules may differ).
	audited.Enforcement = v1.NetworkEnforcementModeEnforce
	rules["allow_api_github_com_443"] = v1.NetworkPolicyRule{Name: "allow_api_github_com_443", Endpoints: []v1.PolicyNetworkEndpoint{audited}}
	if a := findEndpointAmbiguities(rules); len(a) != 0 {
		t.Fatal(a)
	}
}
