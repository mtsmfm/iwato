package main

import (
	"slices"
	"sync"
)

// policyLocks serialise Iwato's writes to a sandbox's policy: every writer
// (an edit, the YAML editor, approving, rejecting and undoing proposals,
// presets, a recreate's carried rules, attaching and detaching providers
// and changing a provider's scope) holds its sandbox's lock from reading
// the policy (and the providers it checks against) until the gateway has
// the change, so that no check runs on a policy another writer is about
// to change. Order: providerMu first, then the sandboxes' locks in name
// order (lockPolicy takes several in that order); never providerMu while
// holding a policy lock. Changes made directly in OpenShell (its CLI, the
// supervisor) are not covered.
type policyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lockPolicy locks the sandboxes' policies and returns the unlock.
func (s *Server) lockPolicy(names ...string) func() {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	p := &s.policyLocks
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	var held []*sync.Mutex
	for _, n := range names {
		m := p.locks[n]
		if m == nil {
			m = &sync.Mutex{}
			p.locks[n] = m
		}
		held = append(held, m)
	}
	p.mu.Unlock()
	for _, m := range held {
		m.Lock()
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].Unlock()
		}
	}
}
