package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowRuntime starts a sandbox only when release is closed, as OpenShell
// while it provisions; stop answers as OpenShell for a sandbox still being
// provisioned.
type slowRuntime struct {
	mu       sync.Mutex
	starts   int
	started  chan struct{}
	release  chan struct{}
	canceled bool
	deleted  bool
}

func (r *slowRuntime) Status(context.Context, Sandbox) Status {
	return Status{Name: "dev", State: "stopped", Detail: "Provisioning…"}
}
func (r *slowRuntime) Start(ctx context.Context, w Sandbox) error {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	r.started <- struct{}{}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		r.canceled = true
		r.mu.Unlock()
		return startCanceled(ctx)
	}
}
func (r *slowRuntime) Attach(context.Context, Sandbox, TermRef, int, int) (Terminal, error) {
	return nil, nil
}
func (r *slowRuntime) Stop(context.Context, Sandbox) error { return errNotStoppable }
func (r *slowRuntime) Delete(context.Context, Sandbox, bool) error {
	r.deleted = true
	return nil
}

func newSlowServer(t *testing.T) (*Server, *slowRuntime, func(method, path, body string) *httptest.ResponseRecorder) {
	rt := &slowRuntime{started: make(chan struct{}, 4), release: make(chan struct{})}
	s := newServer(testConfig(), rt, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	return s, rt, func(method, path, body string) *httptest.ResponseRecorder {
		return reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
	}
}

// A second start while one is in progress waits for that one's result
// instead of failing; every page sees the sandbox starting meanwhile.
func TestSecondStartJoinsTheOneInProgress(t *testing.T) {
	_, rt, do := newSlowServer(t)
	first, second := make(chan int), make(chan int)
	go func() { first <- do("POST", "/api/sandboxes/dev/start", "").Code }()
	<-rt.started
	go func() { second <- do("POST", "/api/sandboxes/dev/start", "").Code }()
	var list struct{ Sandboxes []Status }
	json.Unmarshal(do("GET", "/api/sandboxes", "").Body.Bytes(), &list)
	if len(list.Sandboxes) != 1 || list.Sandboxes[0].State != "starting" || list.Sandboxes[0].Detail != "Provisioning…" {
		t.Fatalf("%+v", list.Sandboxes)
	}
	select {
	case c := <-second:
		t.Fatal("the second start did not wait", c)
	case <-time.After(50 * time.Millisecond):
	}
	close(rt.release)
	if a, b := <-first, <-second; a != 200 || b != 200 {
		t.Fatal(a, b)
	}
	if rt.starts != 1 {
		t.Fatal("started", rt.starts)
	}
	json.Unmarshal(do("GET", "/api/sandboxes", "").Body.Bytes(), &list)
	if list.Sandboxes[0].State != "stopped" {
		t.Fatalf("%+v", list.Sandboxes)
	}
}

// Stop gives up a start in progress: the start ends at once, and a stop
// that OpenShell cannot do yet answers that the wait was canceled.
func TestStopCancelsTheStartInProgress(t *testing.T) {
	_, rt, do := newSlowServer(t)
	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- do("POST", "/api/sandboxes/dev/start", "") }()
	<-rt.started
	w := do("POST", "/api/sandboxes/dev/stop", "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"canceled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if r := <-first; r.Code != 500 || !strings.Contains(r.Body.String(), "やめました") || !rt.canceled {
		t.Fatal(r.Code, r.Body.String())
	}
	// Nothing holds the sandbox afterwards.
	go func() { first <- do("POST", "/api/sandboxes/dev/start", "") }()
	<-rt.started
	close(rt.release)
	if r := <-first; r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestDeleteCancelsTheStartInProgress(t *testing.T) {
	_, rt, do := newSlowServer(t)
	first := make(chan int)
	go func() { first <- do("POST", "/api/sandboxes/dev/start", "").Code }()
	<-rt.started
	if c := do("POST", "/api/sandboxes/dev/delete", `{"confirm":"dev"}`).Code; c != 200 || !rt.deleted {
		t.Fatal(c)
	}
	if c := <-first; c != 500 {
		t.Fatal(c)
	}
}

// Stop and delete still refuse a start while they run (409).
func TestStartWhileStoppingIsRefused(t *testing.T) {
	s, _, do := newSlowServer(t)
	op, _, _, busy := s.beginOp("dev", "stop")
	if busy != "" {
		t.Fatal(busy)
	}
	w := do("POST", "/api/sandboxes/dev/start", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "停止処理が進行中です") {
		t.Fatal(w.Code, w.Body.String())
	}
	s.endOp("dev", op, nil)
}

// policyRuntime's revisions: each change makes the next one; active is
// what the sandbox has loaded.
type revisionRuntime struct {
	reviewRuntime
	mu      sync.Mutex
	version uint32
	active  uint32
}

func (r *revisionRuntime) Proposals(context.Context, Sandbox) (ProposalView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ProposalView{Available: true, Pending: []PendingProposal{}, Rejected: []RejectedProposal{}, Unlinked: []UnlinkedProposal{}, Policy: &CurrentPolicy{Version: r.version, Active: r.active, Rules: []PolicyRuleView{}}}, nil
}
func (r *revisionRuntime) DecideProposal(context.Context, Sandbox, string, string, string, *ProposalScope) (PolicyChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version++
	return PolicyChange{Version: r.version, Kind: "approve", Summary: "s", At: 1}, nil
}
func (r *revisionRuntime) EditPolicy(context.Context, Sandbox, PolicyEdit) (PolicyChange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version++
	return PolicyChange{Version: r.version, Kind: "edit", Rule: "git", At: 1}, nil
}
func (r *revisionRuntime) PolicyHistory(context.Context, Sandbox) ([]PolicyRevision, error) {
	return nil, nil
}
func (r *revisionRuntime) PolicyAt(context.Context, Sandbox, uint32) (*CurrentPolicy, error) {
	return nil, nil
}

// An approval or edit is listed with its revision until a while after the
// sandbox loaded it; every page (a reload too) gets it from the server.
func TestPolicyChangesAreListedUntilLoaded(t *testing.T) {
	rt := &revisionRuntime{version: 4, active: 4}
	s := newServer(testConfig(), rt, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	view := func() ProposalView {
		var v ProposalView
		json.Unmarshal(reviewRequest(s, "GET", "/api/sandboxes/dev/proposals", "", "", "", s.host, cookie, "").Body.Bytes(), &v)
		return v
	}
	if v := view(); v.Changes == nil || len(v.Changes) != 0 {
		t.Fatalf("%+v", v.Changes)
	}
	w := reviewRequest(s, "POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+strings.Repeat("a", 32)+`"`+digestField(s.rt, strings.Repeat("a", 32))+`}`, s.origin, "same-origin", s.host, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":5`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = reviewRequest(s, "POST", "/api/sandboxes/dev/policy/remove", `{"rule":"git"}`, s.origin, "same-origin", s.host, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":6`) {
		t.Fatal(w.Code, w.Body.String())
	}
	v := view()
	if len(v.Changes) != 2 || v.Changes[0].Version != 5 || v.Changes[0].Kind != "approve" || v.Changes[1].Rule != "git" || v.Changes[0].AppliedAt != 0 {
		t.Fatalf("%+v", v.Changes)
	}
	rt.mu.Lock()
	rt.active = 5
	rt.mu.Unlock()
	v = view()
	if v.Changes[0].AppliedAt == 0 || v.Changes[1].AppliedAt != 0 {
		t.Fatalf("%+v", v.Changes)
	}
	// A while after it was seen loaded, it goes.
	s.mu.Lock()
	s.policyChanges["dev"][0].AppliedAt -= int64(appliedShown/time.Second) + 1
	s.mu.Unlock()
	if v = view(); len(v.Changes) != 1 || v.Changes[0].Version != 6 {
		t.Fatalf("%+v", v.Changes)
	}
}
