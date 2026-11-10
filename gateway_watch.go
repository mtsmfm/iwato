package main

import (
	"context"
	"log"
	"net"
	"strconv"
	"time"
)

// gatewayWatch brings OpenShell back when its gateway stops answering after
// a preparation: the engine's VM restarted under Iwato (a WSL update,
// Docker Desktop restarting). Once the engine is usable again it prepares
// again, which starts the gateway's container (OpenShell then starts the
// sandboxes it had), and resumes the terminals.
type gatewayWatch struct {
	every time.Duration
	// ready: the last preparation succeeded (not one running, a reset or a
	// failure).
	ready    func() bool
	answers  func(context.Context) bool
	engineUp func(context.Context) bool
	// reconnect prepares again (or waits for a preparation already running)
	// and returns its error; false while a reset runs.
	reconnect func(context.Context) (bool, error)
	resume    func()
}

// gatewayMisses checks in a row without an answer mean the gateway is gone
// (not a busy moment).
const gatewayMisses = 3

func (w gatewayWatch) run(ctx context.Context) {
	misses := 0
	for sleepCtx(ctx, w.every) {
		if !w.ready() || w.answers(ctx) {
			misses = 0
			continue
		}
		if misses++; misses < gatewayMisses {
			continue
		}
		misses = 0
		noticef("the OpenShell gateway stopped answering: connecting again once the engine is usable")
		w.recover(ctx)
	}
}

// recover waits for the engine (checked every while) and prepares again,
// waiting longer after each failed preparation.
func (w gatewayWatch) recover(ctx context.Context) {
	wait := w.every
	for {
		if !w.engineUp(ctx) {
			if !sleepCtx(ctx, w.every) {
				return
			}
			continue
		}
		started, err := w.reconnect(ctx)
		if !started {
			return
		}
		if err == nil {
			log.Println("OpenShell is back")
			w.resume()
			return
		}
		log.Printf("connecting to OpenShell again failed (retrying in %s): %v", wait, english(err))
		if !sleepCtx(ctx, wait) {
			return
		}
		wait = min(wait*2, 2*time.Minute)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// watchGateway runs the watch for this server's OpenShell.
func (s *Server) watchGateway(ctx context.Context, o *OpenShell) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(gatewayPort))
	gatewayWatch{
		every: 10 * time.Second,
		ready: func() bool { return o.Preparation().Phase == "ready" },
		answers: func(ctx context.Context) bool {
			c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
			if err == nil {
				c.Close()
			}
			return err == nil
		},
		engineUp: func(ctx context.Context) bool {
			if s.checks == nil {
				return true
			}
			s.checks.refresh(ctx)
			return s.checks.engine() == nil
		},
		reconnect: func(ctx context.Context) (bool, error) {
			var ready chan struct{}
			if !s.resets.unlessRunning(func() { ready = o.Reconnect(ctx) }) {
				return false, nil
			}
			if ready == nil {
				// Another preparation (the page's Retry) runs: its result.
				ready = o.readyChan()
			}
			select {
			case <-ready:
			case <-ctx.Done():
				return true, ctx.Err()
			}
			return true, o.prepErr()
		},
		resume: func() { go o.ResumeTerminals(ctx, s.sandboxList) },
	}.run(ctx)
}
