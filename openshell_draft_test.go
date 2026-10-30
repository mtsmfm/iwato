package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Each view reads only the pending proposals while the decided ones are
// kept; a new draft version, Iwato's own decision or their age reads them
// again, and the lists stay as a full read gives them.
func TestProposalsReadDecidedChunksOnlyWhenTheyMayHaveChanged(t *testing.T) {
	rule := func(host string) *v1.NetworkPolicyRule {
		return &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{{Host: host, Port: 443, Ports: []uint32{443}}}}
	}
	f := &fakePolicy{draftVersion: 1, chunks: []v1.PolicyChunk{
		{ID: "p1", Status: "pending", ProposedRule: rule("a.example"), ReviewToken: "t1", CreatedAt: time.Unix(100, 0)},
		{ID: "r1", Status: "rejected", ProposedRule: rule("b.example"), DecidedAt: time.Unix(150, 0)},
	}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: f.update}
	w := newSandbox("dev")
	ctx := context.Background()
	view := func() ProposalView {
		t.Helper()
		v, err := o.Proposals(ctx, w)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	reads := func(want string) {
		t.Helper()
		if got := fmt.Sprint(f.reads[""], " ", f.reads["pending"]); got != want {
			t.Fatalf("reads (all pending) = %s, want %s", got, want)
		}
	}
	lists := func(v ProposalView, pending, rejected int) {
		t.Helper()
		if len(v.Pending) != pending || len(v.Rejected) != rejected {
			t.Fatalf("pending %d rejected %d: %+v", len(v.Pending), len(v.Rejected), v)
		}
	}

	lists(view(), 1, 1)
	reads("1 0")
	lists(view(), 1, 1)
	reads("1 1")

	// A new proposal reads the pending ones only: the version moves.
	f.chunks = append(f.chunks, v1.PolicyChunk{ID: "p2", Status: "pending", ProposedRule: rule("c.example"), CreatedAt: time.Unix(200, 0)})
	f.draftVersion = 2
	lists(view(), 2, 1)
	reads("2 2")

	// Rejecting in Iwato reads them again at the next view.
	if _, err := o.DecideProposal(ctx, w, "reject", requestID("p1"), testDigest(o, ctx, w, requestID("p1")), nil); err != nil {
		t.Fatal(err)
	}
	f.reads = nil
	lists(view(), 1, 2)
	reads("1 0")

	// A decision elsewhere (the CLI) shows once the kept ones are old.
	f.decide("p2", "rejected")
	lists(view(), 0, 2)
	o.drafts.m["dev"] = decidedDraft{version: o.drafts.m["dev"].version, at: time.Now().Add(-decidedFor), chunks: o.drafts.m["dev"].chunks}
	lists(view(), 0, 3)
	reads("2 1")
}
