package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// Stopping a sandbox once its terminals have ended (always on). The
// runtime tells when one of a sandbox's terminals ends (the end of the
// session Iwato holds for it: the process exited, it was killed, or the
// attachment broke); after autoStopGrace the server asks the runtime
// whether any terminal runs, and stops the sandbox when none does. A
// terminal opened during the grace keeps it running. Everything else in
// the sandbox (background servers, the desktop) stops with it.
//
// The runtime asks the sandbox's daemon (iwato-entrypoint's list, which
// every daemon version answers). Without a daemon the main terminal is the
// sandbox's main process, whose end already stops the sandbox; the runtime
// says it cannot tell and nothing happens.

// autoStopGrace is how long the sandbox waits after its last terminal ended.
const autoStopGrace = time.Minute

// autoStopRuntime is the runtime side: OnTerminalEnd registers the
// callback for a terminal's end; TerminalsIdle tells whether a running
// sandbox has no terminal running (false when it cannot tell).
type autoStopRuntime interface {
	OnTerminalEnd(func(sandbox string))
	TerminalsIdle(context.Context, Sandbox) (bool, error)
}

// autoStops holds each sandbox's pending check. gen grows with every
// operation on the sandbox (beginOp) and terminal attach, so a check armed
// before one does nothing. gates order the check's final decision and Stop
// against cancel: a cancel (an attach) waits for a stop already decided,
// and a stop is not decided after a cancel.
type autoStops struct {
	mu     sync.Mutex
	grace  time.Duration // autoStopGrace when zero (tests shorten it)
	timers map[string]*autoStopTimer
	gen    map[string]int
	gates  map[string]*sync.Mutex
}

// gate is the sandbox's lock between the final check with Stop and cancel.
func (a *autoStops) gate(name string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gates == nil {
		a.gates = map[string]*sync.Mutex{}
	}
	g := a.gates[name]
	if g == nil {
		g = &sync.Mutex{}
		a.gates[name] = g
	}
	return g
}

type autoStopTimer struct {
	t   *time.Timer
	gen int
	at  time.Time
}

func (a *autoStops) arm(name string, check func(gen int)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timers == nil {
		a.timers, a.gen = map[string]*autoStopTimer{}, map[string]int{}
	}
	if old := a.timers[name]; old != nil {
		old.t.Stop()
	}
	grace := a.grace
	if grace == 0 {
		grace = autoStopGrace
	}
	gen := a.gen[name]
	at := &autoStopTimer{gen: gen, at: time.Now().Add(grace)}
	at.t = time.AfterFunc(grace, func() {
		a.mu.Lock()
		if a.timers[name] == at {
			delete(a.timers, name)
		}
		a.mu.Unlock()
		check(gen)
	})
	a.timers[name] = at
}

// cancel drops the sandbox's pending check (an operation began, or a
// terminal is attached).
func (a *autoStops) cancel(name string) {
	g := a.gate(name)
	g.Lock()
	defer g.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gen == nil {
		a.timers, a.gen = map[string]*autoStopTimer{}, map[string]int{}
	}
	a.gen[name]++
	if t := a.timers[name]; t != nil {
		t.t.Stop()
		delete(a.timers, name)
	}
}

func (a *autoStops) current(name string, gen int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gen[name] == gen
}

// pending is when the sandbox's check runs (zero when none is armed).
func (a *autoStops) pending(name string) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.timers[name]; t != nil {
		return t.at
	}
	return time.Time{}
}

// watchTerminalEnds connects the runtime's terminal ends to the checks.
func (s *Server) watchTerminalEnds() {
	if rt, ok := s.rt.(autoStopRuntime); ok {
		rt.OnTerminalEnd(s.terminalEnded)
	}
}

// terminalEnded arms the sandbox's check. Ends during an operation (a stop
// or delete closes the sessions) are not the owner's terminals ending.
func (s *Server) terminalEnded(name string) {
	if _, ok := s.lookupSandbox(name); !ok || s.opKind(name) != "" {
		return
	}
	s.autoStop.arm(name, func(gen int) { s.autoStopCheck(name, gen) })
}

// autoStopCheck stops the sandbox when it still exists, nothing happened to
// it since the check was armed, it runs, and no terminal runs.
func (s *Server) autoStopCheck(name string, gen int) {
	sb, ok := s.lookupSandbox(name)
	rt, isRT := s.rt.(autoStopRuntime)
	stopper, canStop := s.rt.(stopRuntime)
	if !ok || !isRT || !canStop || !s.autoStop.current(name, gen) {
		return
	}
	// Claimed as a stop, but only when nothing else holds the sandbox (a
	// start is not canceled for this).
	s.mu.Lock()
	if s.ops[name] != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := timeout(s.ctx, opTimeout("stop"))
	op := &sandboxOp{kind: "stop", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.ops[name] = op
	s.mu.Unlock()
	var err error
	defer func() { s.endOp(name, op, err) }()
	if s.rt.Status(ctx, sb).State != "running" {
		return
	}
	idle, ierr := rt.TerminalsIdle(ctx, sb)
	if ierr != nil {
		log.Printf("sandbox %s: not stopped automatically, its terminals cannot be listed: %v", name, english(ierr))
		return
	}
	if !idle {
		return
	}
	// Decided only when nothing canceled it meanwhile (an attach); a cancel
	// from now on waits for the stop.
	g := s.autoStop.gate(name)
	g.Lock()
	defer g.Unlock()
	if _, ok := s.lookupSandbox(name); !ok || !s.autoStop.current(name, gen) {
		return
	}
	log.Printf("sandbox %s: stopping, its terminals have ended", name)
	if err = stopper.Stop(ctx, sb); err != nil {
		log.Printf("sandbox %s: automatic stop: %v", name, english(err))
		return
	}
	s.clearProvidersChanged(name)
}

// autoStopStatus fills the list's fields for the sandbox.
func (s *Server) autoStopStatus(st *Status, sb Sandbox) {
	if at := s.autoStop.pending(sb.Name); st.State == "running" && !at.IsZero() {
		st.AutoStopAt = at.Unix()
	}
}
