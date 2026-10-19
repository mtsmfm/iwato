package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// idleRuntime runs the sandbox until stopped; idle is what TerminalsIdle says.
type idleRuntime struct {
	mu      sync.Mutex
	idle    bool
	stopped chan struct{}
	ended   func(string)
	// during runs while the terminals are listed.
	during func()
}

func (r *idleRuntime) Status(context.Context, Sandbox) Status {
	return Status{Name: "dev", State: "running"}
}
func (r *idleRuntime) Start(context.Context, Sandbox) error { return nil }
func (r *idleRuntime) Attach(context.Context, Sandbox, TermRef, int, int) (Terminal, error) {
	return nil, nil
}
func (r *idleRuntime) Stop(context.Context, Sandbox) error { r.stopped <- struct{}{}; return nil }
func (r *idleRuntime) OnTerminalEnd(f func(string))        { r.ended = f }
func (r *idleRuntime) TerminalsIdle(context.Context, Sandbox) (bool, error) {
	r.mu.Lock()
	idle, during := r.idle, r.during
	r.mu.Unlock()
	if during != nil {
		during()
	}
	return idle, nil
}

func stoppedWithin(r *idleRuntime, d time.Duration) bool {
	select {
	case <-r.stopped:
		return true
	case <-time.After(d):
		return false
	}
}

// The sandbox stops a grace after its last terminal ended, only when no
// terminal runs by then, and not when an operation (a stop and a start) came
// in between.
func TestAutoStopAfterTheLastTerminal(t *testing.T) {
	rt := &idleRuntime{idle: true, stopped: make(chan struct{}, 4)}
	c := testConfig()
	s := newServer(c, rt, false, "127.0.0.1:29999")
	s.autoStop.grace = 20 * time.Millisecond

	rt.mu.Lock()
	rt.idle = false
	rt.mu.Unlock()
	rt.ended("dev")
	if stoppedWithin(rt, 100*time.Millisecond) {
		t.Fatal("stopped while a terminal runs")
	}

	rt.mu.Lock()
	rt.idle = true
	rt.mu.Unlock()
	s.autoStop.grace = 50 * time.Millisecond
	rt.ended("dev")
	op, _, _, _ := s.beginOp("dev", "start")
	s.endOp("dev", op, nil)
	if stoppedWithin(rt, 150*time.Millisecond) {
		t.Fatal("stopped after a start that came in between")
	}

	// An attach while the terminals are listed: the stop is not decided.
	rt.mu.Lock()
	rt.during = func() { s.autoStop.cancel("dev") }
	rt.mu.Unlock()
	rt.ended("dev")
	if stoppedWithin(rt, 150*time.Millisecond) {
		t.Fatal("stopped after an attach during the check")
	}
	rt.mu.Lock()
	rt.during = nil
	rt.mu.Unlock()

	rt.ended("dev")
	if !stoppedWithin(rt, time.Second) {
		t.Fatal("not stopped after its terminals ended")
	}
}
