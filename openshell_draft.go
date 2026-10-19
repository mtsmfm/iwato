package main

import (
	"context"
	"slices"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// decidedFor is how long the decided proposals (approved and rejected
// chunks) are reused without reading them again. OpenShell keeps every
// chunk it ever drafted, so the whole draft grows without bound while the
// pending ones stay few: each view reads the pending ones and takes the
// decided ones from here. They change through decisions, which Iwato makes
// itself (forgetDraft) or, rarely, someone else does (the OpenShell CLI);
// this bounds how late the page sees those. A rejected proposal's hit count
// also grows without a new draft version (the gateway's dedup updates only
// the count), so it is as late.
const decidedFor = time.Minute

// draftCache keeps each sandbox's decided chunks, with the draft version
// they were read at (GetDraftPolicy's draft_version: the highest version of
// any chunk, whatever the status filter; it moves when the sandbox submits
// new proposals, which may also reject older ones as superseded).
type draftCache struct {
	mu sync.Mutex
	m  map[string]decidedDraft
	// gen counts each sandbox's forgets, so that a read that began before
	// one does not keep what it read.
	gen map[string]uint64
}

type decidedDraft struct {
	version uint64
	at      time.Time
	chunks  []v1.PolicyChunk
}

// forgetDraft drops the sandbox's decided chunks: Iwato changed them (or
// the sandbox went), so the next view reads them again.
func (o *OpenShell) forgetDraft(sandbox string) {
	c := &o.drafts
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, sandbox)
	if c.gen == nil {
		c.gen = map[string]uint64{}
	}
	c.gen[sandbox]++
}

// draftChunks are the sandbox's chunks, as GetDraft answers them without a
// filter: the pending ones read now, the decided ones from the cache while
// it holds them for the same draft version, else read again with the rest.
func (o *OpenShell) draftChunks(ctx context.Context, pol v1.PolicyInterface, sandbox string) ([]v1.PolicyChunk, error) {
	c := &o.drafts
	c.mu.Lock()
	gen := c.gen[sandbox]
	cached, ok := c.m[sandbox]
	c.mu.Unlock()
	if ok && time.Since(cached.at) < decidedFor {
		pending, err := pol.GetDraft(ctx, openshellWorkspace, sandbox, v1.WithStatusFilter("pending"))
		if err != nil {
			return nil, err
		}
		if pending.DraftVersion == cached.version {
			// A chunk decided elsewhere and put back to pending (an undo)
			// is the fresh one.
			out := make([]v1.PolicyChunk, 0, len(pending.Chunks)+len(cached.chunks))
			ids := map[string]bool{}
			for _, ch := range pending.Chunks {
				if ch.Status == "pending" {
					ids[ch.ID] = true
					out = append(out, ch)
				}
			}
			for _, ch := range cached.chunks {
				if !ids[ch.ID] {
					out = append(out, ch)
				}
			}
			return out, nil
		}
	}
	draft, err := pol.GetDraft(ctx, openshellWorkspace, sandbox)
	if err != nil {
		return nil, err
	}
	var decided []v1.PolicyChunk
	for _, ch := range draft.Chunks {
		if ch.Status != "pending" {
			decided = append(decided, ch)
		}
	}
	c.mu.Lock()
	if c.gen[sandbox] == gen {
		if c.m == nil {
			c.m = map[string]decidedDraft{}
		}
		c.m[sandbox] = decidedDraft{version: draft.DraftVersion, at: time.Now(), chunks: decided}
	}
	c.mu.Unlock()
	return draft.Chunks, nil
}

// draftChunk finds the chunk of a request ID among the given statuses,
// read now (one filtered read each, in order), for the owner's decisions;
// with it the chunks of its status.
func draftChunk(ctx context.Context, pol v1.PolicyInterface, sandbox, request string, statuses ...string) (*v1.PolicyChunk, []v1.PolicyChunk, error) {
	for _, status := range statuses {
		draft, err := pol.GetDraft(ctx, openshellWorkspace, sandbox, v1.WithStatusFilter(status))
		if err != nil {
			return nil, nil, err
		}
		chunks := slices.DeleteFunc(draft.Chunks, func(c v1.PolicyChunk) bool { return c.Status != status })
		for i := range chunks {
			if requestID(chunks[i].ID) == request {
				return &chunks[i], chunks, nil
			}
		}
	}
	return nil, nil, nil
}

// PendingProposals are the proposals waiting for the owner, as Proposals
// lists them, from the pending chunks alone (the notifier needs no more).
func (o *OpenShell) PendingProposals(ctx context.Context, w Sandbox) ([]PendingProposal, error) {
	pol, err := o.policy(ctx)
	if err != nil {
		return nil, err
	}
	draft, err := pol.GetDraft(ctx, openshellWorkspace, w.Name, v1.WithStatusFilter("pending"))
	if err != nil {
		return nil, err
	}
	out := []PendingProposal{}
	for _, c := range draft.Chunks {
		if c.Status == "pending" {
			out = append(out, pendingProposal(requestID(c.ID), c))
		}
	}
	return out, nil
}

// draftStatusFilter is the status a GetDraft call asks for ("" for all),
// for the fake's gateway and the tests'.
func draftStatusFilter(opts []v1.GetDraftOption) string {
	cfg := types.ApplyGetDraftOptions(opts)
	return cfg.StatusFilter()
}
