package main

import (
	"maps"
	"slices"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Builders of policy rules, for the fake backend's policies (fake_policy.go)
// and the tests.

func restEndpoint(host string, ports ...uint32) v1.PolicyNetworkEndpoint {
	return v1.PolicyNetworkEndpoint{Host: host, Port: ports[0], Ports: ports, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce}
}

func l4Endpoint(host string, port uint32) v1.PolicyNetworkEndpoint {
	return v1.PolicyNetworkEndpoint{Host: host, Port: port, Ports: []uint32{port}}
}

func policyBinaries(paths ...string) []v1.PolicyNetworkBinary {
	out := make([]v1.PolicyNetworkBinary, len(paths))
	for i, p := range paths {
		out[i] = v1.PolicyNetworkBinary{Path: p}
	}
	return out
}

// cloneRules copies rules deeply enough to change one without the other.
func cloneRules(rules map[string]v1.NetworkPolicyRule) map[string]v1.NetworkPolicyRule {
	out := maps.Clone(rules)
	for k, r := range out {
		r.Binaries = slices.Clone(r.Binaries)
		r.Endpoints = slices.Clone(r.Endpoints)
		for i := range r.Endpoints {
			r.Endpoints[i].Ports = slices.Clone(r.Endpoints[i].Ports)
			r.Endpoints[i].Rules = slices.Clone(r.Endpoints[i].Rules)
			r.Endpoints[i].AllowedIPs = slices.Clone(r.Endpoints[i].AllowedIPs)
		}
		out[k] = r
	}
	return out
}
