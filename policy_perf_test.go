package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// syntheticPolicy is a large hand-grown policy: rules of one to three
// inspected endpoints (some read-only, the rest with method and path
// rules), hosts shared between rules, a few host globs and paths, ports
// 443 and 80, and programs (globs too) from a small set.
func syntheticPolicy(seed uint64, rules int) map[string]v1.NetworkPolicyRule {
	rnd := rand.New(rand.NewPCG(seed, 1))
	binaries := []string{"/usr/bin/curl", "/usr/bin/wget", "/usr/bin/tool-a*", "/usr/bin/tool-b.*", "/opt/tool-c/bin/c", "/usr/bin/git",
		"/opt/runtimes/lang-a/*/bin/a", "/opt/runtimes/lang-b/*/bin/b", "/opt/agents/agent-a/versions/*",
		"/opt/browsers/browser-*/bin/browser", "/opt/toolchains/*/bin/build", "/opt/bin/app-*"}
	hosts := rules * 3 / 4
	host := func() string {
		n := rnd.IntN(hosts)
		if n%40 == 7 {
			return fmt.Sprintf("*.cdn%d.example.test", n)
		}
		return fmt.Sprintf("svc%d.example.test", n)
	}
	out := map[string]v1.NetworkPolicyRule{}
	for i := range rules {
		name := fmt.Sprintf("rule_%03d", i)
		r := v1.NetworkPolicyRule{Name: name}
		n := 1
		if i%3 == 0 {
			n = 2
		}
		if i%17 == 0 {
			n = 3
		}
		for range n {
			e := v1.PolicyNetworkEndpoint{Host: host(), Port: 443, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce}
			if rnd.IntN(20) == 0 {
				e.Port = 80
			}
			if rnd.IntN(60) == 0 {
				e.Path = fmt.Sprintf("/v%d/**", rnd.IntN(3))
			}
			if rnd.IntN(4) == 0 {
				e.Access = v1.NetworkAccessPresetReadOnly
			} else {
				for k := range []int{1, 2, 3, 3, 3, 3, 3, 3, 3, 4, 5, 6, 6, 6, 9, 9, 9, 12, 15, 24}[rnd.IntN(20)] {
					m := []string{"GET", "HEAD", "POST", "PUT"}[rnd.IntN(4)]
					e.Rules = append(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: fmt.Sprintf("/api/v%d/items/%d/**", k%3, k)}})
				}
			}
			r.Endpoints = append(r.Endpoints, e)
		}
		for range 1 + rnd.IntN(rnd.IntN(2)+1) {
			b := binaries[rnd.IntN(len(binaries))]
			if !containsBinary(r.Binaries, b) {
				r.Binaries = append(r.Binaries, v1.PolicyNetworkBinary{Path: b})
			}
		}
		out[name] = r
	}
	return out
}

func containsBinary(list []v1.PolicyNetworkBinary, path string) bool {
	for _, b := range list {
		if b.Path == path {
			return true
		}
	}
	return false
}

// benchPolicy is the synthetic policy of the owner's size, or the policy
// in the YAML file IWATO_BENCH_POLICY names (to compare with a real one,
// kept outside the repository).
func benchPolicy(tb testing.TB) map[string]v1.NetworkPolicyRule {
	if path := os.Getenv("IWATO_BENCH_POLICY"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			tb.Fatal(err)
		}
		rules, err := parsePolicyYAML(b)
		if err != nil {
			tb.Fatal(err)
		}
		return rules
	}
	return syntheticPolicy(1, 96)
}

func TestSyntheticPolicyShape(t *testing.T) {
	rules := syntheticPolicy(1, 96)
	eps, allows := 0, 0
	for _, r := range rules {
		eps += len(r.Endpoints)
		for _, e := range r.Endpoints {
			allows += len(e.Rules)
		}
	}
	if len(rules) != 96 || eps < 120 || eps > 150 || allows < 450 || allows > 750 {
		t.Fatal(len(rules), eps, allows)
	}
	if list := findEndpointAmbiguities(rules); len(list) > 0 {
		t.Fatal(list[0].String())
	}
}
