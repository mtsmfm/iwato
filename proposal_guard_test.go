package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func codeOf(err error) string {
	var c *codedError
	if errors.As(err, &c) {
		return c.code
	}
	return ""
}

// The programs approved are exactly the page's: a proposal naming several
// gets the one sent, a scope without programs is refused, and the
// proposal's own programs alone approve it unchanged.
func TestApprovalAppliesThePagesPrograms(t *testing.T) {
	rule := func() *v1.NetworkPolicyRule {
		return &v1.NetworkPolicyRule{Name: "gh", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}, {Path: "/**"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.github.com", Port: 443}}}
	}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		{ID: "c1", Status: "pending", ReviewToken: "t1", Binary: "/usr/bin/gh", ProposedRule: rule()},
		{ID: "c2", Status: "pending", ReviewToken: "t2", Binary: "/usr/bin/gh", ProposedRule: rule()},
		{ID: "c3", Status: "pending", ReviewToken: "t3", Binary: "/usr/bin/gh", ProposedRule: &v1.NetworkPolicyRule{Name: "one", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}}}},
	}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs}, newSandbox("dev"), context.Background()
	decide := func(id string, scope *ProposalScope) error {
		_, err := o.DecideProposal(ctx, w, "approve", requestID(id), testDigest(o, ctx, w, requestID(id)), scope)
		return err
	}
	if err := decide("c1", &ProposalScope{ReadOnly: true}); codeOf(err) != "scope_binaries_missing" {
		t.Fatal("a scope without programs:", err)
	}
	if err := decide("c1", &ProposalScope{Binaries: []string{"/usr/bin/gh"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.chunks[0].ProposedRule.Binaries; len(got) != 1 || got[0].Path != "/usr/bin/gh" {
		t.Fatal("the proposal's other programs were kept:", got)
	}
	if err := decide("c2", &ProposalScope{ReadOnly: true, Binaries: []string{"/usr/bin/*"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.chunks[1].ProposedRule.Binaries; len(got) != 1 || got[0].Path != "/usr/bin/*" {
		t.Fatal("narrowed:", got)
	}
	// A single program sent as it is proposed: approved unchanged, no edit.
	if err := decide("c3", &ProposalScope{Binaries: []string{"/usr/bin/gh"}}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"c1 t1-edited", "c2 t2-edited", "c3 t3"}; !slices.Equal(f.approved, want) {
		t.Fatal(f.approved)
	}
}

// What the page cannot show is never approved: more than the limits, or
// settings it does not render, as proposed or narrowed. Iwato's own port
// is refused outright.
func TestApprovalRefusesWhatThePageCannotShow(t *testing.T) {
	many := &v1.NetworkPolicyRule{Name: "many"}
	for i := range proposalLimits.Binaries + 1 {
		many.Binaries = append(many.Binaries, v1.PolicyNetworkBinary{Path: fmt.Sprintf("/bin/p%d", i)})
	}
	many.Endpoints = []v1.PolicyNetworkEndpoint{{Host: "a.example", Port: 443}}
	var endpoints []v1.PolicyNetworkEndpoint
	for i := range proposalLimits.Endpoints + 1 {
		endpoints = append(endpoints, v1.PolicyNetworkEndpoint{Host: fmt.Sprintf("h%d.example", i), Port: 443})
	}
	wide := &v1.NetworkPolicyRule{Name: "wide", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: endpoints}
	// Rules count across endpoints: the limit is approvable, one more is not.
	rules := func(name string, n int) *v1.NetworkPolicyRule {
		r := &v1.NetworkPolicyRule{Name: name, Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}}
		for e := range 4 {
			ep := v1.PolicyNetworkEndpoint{Host: fmt.Sprintf("r%d.example", e), Port: 443, Protocol: "rest"}
			for i := e; i < n; i += 4 {
				ep.Rules = append(ep.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: "GET", Path: fmt.Sprintf("/p%d", i)}})
			}
			r.Endpoints = append(r.Endpoints, ep)
		}
		return r
	}
	gql := &v1.NetworkPolicyRule{Name: "gql", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "g.example", Port: 443, Protocol: "rest", GraphqlMaxBodyBytes: 1024}}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		{ID: "many", RuleName: "many", Status: "pending", ReviewToken: "t", ProposedRule: many},
		{ID: "wide", RuleName: "wide", Status: "pending", ReviewToken: "t", ProposedRule: wide},
		{ID: "gql", RuleName: "gql", Status: "pending", ReviewToken: "t", ProposedRule: gql},
		{ID: "rules", RuleName: "rules", Status: "pending", ReviewToken: "t", ProposedRule: rules("rules", proposalLimits.Rules)},
		{ID: "rules1", RuleName: "rules1", Status: "pending", ReviewToken: "t", ProposedRule: rules("rules1", proposalLimits.Rules+1)},
	}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs}, newSandbox("dev"), context.Background()
	v, _ := o.Proposals(ctx, w)
	unshown := map[string][]string{}
	for _, p := range v.Pending {
		unshown[p.Rule] = p.Unshown
	}
	if !slices.Equal(unshown["many"], []string{"binaries > 8"}) || !slices.Equal(unshown["wide"], []string{"endpoints > 20"}) || !slices.Equal(unshown["gql"], []string{"graphql"}) || unshown["rules"] != nil || !slices.Equal(unshown["rules1"], []string{"rules > 128"}) {
		t.Fatal("unshown:", unshown)
	}
	for _, c := range []struct {
		id    string
		scope *ProposalScope
	}{{"many", nil}, {"wide", nil}, {"wide", &ProposalScope{ReadOnly: true, Binaries: []string{"/usr/bin/curl"}}}, {"gql", &ProposalScope{Audit: true, Binaries: []string{"/usr/bin/curl"}}}, {"rules1", nil}} {
		if _, err := o.DecideProposal(ctx, w, "approve", requestID(c.id), testDigest(o, ctx, w, requestID(c.id)), c.scope); codeOf(err) != "proposal_unshown" {
			t.Errorf("%s %+v: %v", c.id, c.scope, err)
		}
	}
	// Narrowed to programs the page shows, the many-program one goes.
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("many"), testDigest(o, ctx, w, requestID("many")), &ProposalScope{Binaries: []string{"/bin/p0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("rules"), testDigest(o, ctx, w, requestID("rules")), nil); err != nil {
		t.Fatal("rules at the limit:", err)
	}
	if len(f.approved) != 2 {
		t.Fatal(f.approved)
	}
	// Rejecting one over a count tells the agent how to propose it again.
	for _, id := range []string{"rules1", "gql"} {
		if _, err := o.DecideProposal(ctx, w, "reject", requestID(id), testDigest(o, ctx, w, requestID(id)), nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range f.chunks {
		if hint := strings.Contains(c.RejectionReason, "combine paths with * or **"); c.Status == "rejected" && hint != (c.ID == "rules1") {
			t.Errorf("%s: %q", c.ID, c.RejectionReason)
		}
	}

	s := &Server{host: "127.0.0.1:17600"}
	if err := s.iwatoPortRefusal(map[string]v1.NetworkPolicyRule{"r": {Endpoints: []v1.PolicyNetworkEndpoint{{Host: "host.docker.internal", Ports: []uint32{443, 17600}}}}}); codeOf(err) != "iwato_port" {
		t.Fatal("Iwato's port:", err)
	}
	if got := gatewayRules(map[string]v1.NetworkPolicyRule{"a": {Endpoints: []v1.PolicyNetworkEndpoint{{Host: "HOST.wslc.internal", Port: 80}, {Host: "", Port: 80, AllowedIPs: []string{"192.168.65.0/24"}}, {Host: "example.com", Port: 443}}}}); len(got) != 2 || got[0].Gateway != "host.wslc.internal" || got[1].Gateway != "192.168.65.254" {
		t.Fatal("gateways:", got)
	}
}
