package main

import (
	"context"
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// refFindAmbiguities is the plain all-pairs check, the reference for the
// indexed one.
func refFindAmbiguities(rules map[string]v1.NetworkPolicyRule) []EndpointAmbiguity {
	type ref struct {
		rule  string
		index int
		e     v1.PolicyNetworkEndpoint
	}
	var eps []ref
	for _, key := range slices.Sorted(maps.Keys(rules)) {
		r := rules[key]
		name := r.Name
		if name == "" {
			name = key
		}
		for i, e := range r.Endpoints {
			eps = append(eps, ref{name, i, e})
		}
	}
	var out []EndpointAmbiguity
	for i := range eps {
		for j := i + 1; j < len(eps); j++ {
			l, r := eps[i], eps[j]
			ports := overlappingPorts(l.e, r.e)
			if len(ports) == 0 || !hostPatternsOverlap(l.e.Host, r.e.Host) {
				continue
			}
			conflicts := connectionConflicts(l.e, r.e)
			if contributesRequestMetadata(l.e) && contributesRequestMetadata(r.e) &&
				pathPatternsOverlap(l.e.Path, r.e.Path) && pathSpecificity(l.e.Path) == pathSpecificity(r.e.Path) {
				conflicts = append(conflicts, requestConflicts(l.e, r.e)...)
			}
			if len(conflicts) == 0 {
				continue
			}
			out = append(out, EndpointAmbiguity{LeftRule: l.rule, LeftIndex: l.index, LeftSelector: endpointSelector(l.e),
				RightRule: r.rule, RightIndex: r.index, RightSelector: endpointSelector(r.e), Ports: ports, Conflicts: conflicts, left: l.e, right: r.e})
		}
	}
	return out
}

func refNewAmbiguities(before, after map[string]v1.NetworkPolicyRule) []EndpointAmbiguity {
	old := map[string]bool{}
	for _, a := range refFindAmbiguities(before) {
		old[a.key()] = true
	}
	var out []EndpointAmbiguity
	for _, a := range refFindAmbiguities(after) {
		if !old[a.key()] {
			out = append(out, a)
		}
	}
	return out
}

func randomEndpoint(rnd *rand.Rand) v1.PolicyNetworkEndpoint {
	pick := func(xs ...string) string { return xs[rnd.Intn(len(xs))] }
	e := v1.PolicyNetworkEndpoint{
		Host:     pick("", "api.example.com", "API.example.com", "*.example.com", "**.example.com", "api.*.com", "a.b.example.com", "other.org", "x.other.org"),
		Port:     []uint32{0, 443, 80, 8443}[rnd.Intn(4)],
		Protocol: pick("", "", "rest", "tcp", "websocket", "graphql", "mcp", "json-rpc"),
		Path:     pick("", "", "/**", "/v1/*", "/v1/x", "/v2/[ab]", "/**/y"),
	}
	if rnd.Intn(3) == 0 {
		e.Ports = []uint32{443, 8443}
	}
	e.TLS = []v1.NetworkTLSMode{v1.NetworkTLSModeUnspecified, v1.NetworkTLSModeSkip}[rnd.Intn(2)]
	e.Enforcement = []v1.NetworkEnforcementMode{v1.NetworkEnforcementModeUnspecified, v1.NetworkEnforcementModeAudit, v1.NetworkEnforcementModeEnforce}[rnd.Intn(3)]
	if rnd.Intn(5) == 0 {
		e.AllowedIPs = []string{pick("10.0.0.0/8", "192.168.0.0/16")}
	}
	if rnd.Intn(6) == 0 {
		e.CredentialBinding = &types.NetworkCredentialBinding{Provider: pick("github", "gitlab")}
	}
	if rnd.Intn(4) == 0 {
		e.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: pick("GET", "POST"), Path: pick("/**", "/v1/x")}}}
	}
	e.AllowEncodedSlash = rnd.Intn(5) == 0
	e.JSONRPCMaxBodyBytes = []uint32{0, 65536, 1024}[rnd.Intn(3)]
	return e
}

func randomRule(rnd *rand.Rand, name string) v1.NetworkPolicyRule {
	r := v1.NetworkPolicyRule{Binaries: policyBinaries("/usr/bin/curl")}
	if rnd.Intn(4) != 0 {
		r.Name = name
	}
	for range 1 + rnd.Intn(3) {
		r.Endpoints = append(r.Endpoints, randomEndpoint(rnd))
	}
	return r
}

func randomPolicy(rnd *rand.Rand) map[string]v1.NetworkPolicyRule {
	rules := map[string]v1.NetworkPolicyRule{}
	for i := range rnd.Intn(12) {
		name := fmt.Sprintf("rule%d", i)
		if rnd.Intn(4) == 0 {
			name = providerRulePrefix + name
		}
		rules[name] = randomRule(rnd, name)
	}
	return rules
}

// The indexed check finds the same ambiguities, in the same order, as the
// plain all-pairs one: for policies and changes to them.
func TestAmbiguityIndexMatchesAllPairs(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for n := range 500 {
		base := randomPolicy(rnd)
		if got, want := findEndpointAmbiguities(base), refFindAmbiguities(base); !reflect.DeepEqual(got, want) {
			t.Fatalf("%d: find\ngot  %v\nwant %v", n, got, want)
		}
		after := maps.Clone(base)
		for _, key := range slices.Sorted(maps.Keys(base)) {
			switch rnd.Intn(5) {
			case 0:
				delete(after, key)
			case 1:
				r := cloneRule(after[key])
				r.Endpoints[rnd.Intn(len(r.Endpoints))] = randomEndpoint(rnd)
				after[key] = r
			case 2:
				r := cloneRule(after[key])
				r.Name = "renamed"
				after[key] = r
			}
		}
		for i := range rnd.Intn(3) {
			after[fmt.Sprintf("added%d", i)] = randomRule(rnd, fmt.Sprintf("added%d", i))
		}
		if got, want := newAmbiguities(base, after), refNewAmbiguities(base, after); !reflect.DeepEqual(got, want) {
			t.Fatalf("%d: new\ngot  %v\nwant %v", n, got, want)
		}
	}
}

func TestAmbiguityIndexStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := ambPolicy(ambEndpoint("api.example.com", 443), ambEndpoint("api.example.com", 443))
	if _, err := indexAmbiguities(ctx, base); err == nil {
		t.Fatal("index ran on")
	}
}
