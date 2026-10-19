package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	gql := &v1.NetworkPolicyRule{Name: "gql", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "g.example", Port: 443, Protocol: "rest", GraphqlMaxBodyBytes: 1024}}}
	f := &fakePolicy{chunks: []v1.PolicyChunk{
		{ID: "many", RuleName: "many", Status: "pending", ReviewToken: "t", ProposedRule: many},
		{ID: "wide", RuleName: "wide", Status: "pending", ReviewToken: "t", ProposedRule: wide},
		{ID: "gql", RuleName: "gql", Status: "pending", ReviewToken: "t", ProposedRule: gql},
	}}
	o, w, ctx := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs}, newSandbox("dev"), context.Background()
	v, _ := o.Proposals(ctx, w)
	unshown := map[string][]string{}
	for _, p := range v.Pending {
		unshown[p.Rule] = p.Unshown
	}
	if !slices.Equal(unshown["many"], []string{"binaries > 8"}) || !slices.Equal(unshown["wide"], []string{"endpoints > 20"}) || !slices.Equal(unshown["gql"], []string{"graphql"}) {
		t.Fatal("unshown:", unshown)
	}
	for _, c := range []struct {
		id    string
		scope *ProposalScope
	}{{"many", nil}, {"wide", nil}, {"wide", &ProposalScope{ReadOnly: true, Binaries: []string{"/usr/bin/curl"}}}, {"gql", &ProposalScope{Audit: true, Binaries: []string{"/usr/bin/curl"}}}} {
		if _, err := o.DecideProposal(ctx, w, "approve", requestID(c.id), testDigest(o, ctx, w, requestID(c.id)), c.scope); codeOf(err) != "proposal_unshown" {
			t.Errorf("%s %+v: %v", c.id, c.scope, err)
		}
	}
	// Narrowed to programs the page shows, the many-program one goes.
	if _, err := o.DecideProposal(ctx, w, "approve", requestID("many"), testDigest(o, ctx, w, requestID("many")), &ProposalScope{Binaries: []string{"/bin/p0"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.approved) != 1 {
		t.Fatal(f.approved)
	}

	s := &Server{host: "127.0.0.1:17600"}
	if err := s.iwatoPortRefusal(map[string]v1.NetworkPolicyRule{"r": {Endpoints: []v1.PolicyNetworkEndpoint{{Host: "host.docker.internal", Ports: []uint32{443, 17600}}}}}); codeOf(err) != "iwato_port" {
		t.Fatal("Iwato's port:", err)
	}
	if got := gatewayRules(map[string]v1.NetworkPolicyRule{"a": {Endpoints: []v1.PolicyNetworkEndpoint{{Host: "HOST.wslc.internal", Port: 80}, {Host: "", Port: 80, AllowedIPs: []string{"192.168.65.0/24"}}, {Host: "example.com", Port: 443}}}}); len(got) != 2 || got[0].Gateway != "host.wslc.internal" || got[1].Gateway != "192.168.65.254" {
		t.Fatal("gateways:", got)
	}
}
