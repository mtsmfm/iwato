package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A gateway that stops answering is prepared again once the engine is back,
// retried while that fails, and the terminals are resumed once; a reset (or
// another preparation) taking over ends the recovery.
func TestGatewayWatch(t *testing.T) {
	var mu sync.Mutex
	up, engineCalls, reconnects, resumes := false, 0, 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := gatewayWatch{
		every:   time.Millisecond,
		ready:   func() bool { return true },
		answers: func(context.Context) bool { mu.Lock(); defer mu.Unlock(); return up },
		engineUp: func(context.Context) bool {
			mu.Lock()
			defer mu.Unlock()
			engineCalls++
			return engineCalls > 2
		},
		reconnect: func(context.Context) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			if reconnects++; reconnects == 1 {
				return true, errors.New("gateway did not start")
			}
			up = true
			return true, nil
		},
		resume: func() { mu.Lock(); resumes++; mu.Unlock() },
	}
	go w.run(ctx)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		mu.Lock()
		done := resumes == 1
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not recovered")
		}
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if engineCalls != 4 || reconnects != 2 || resumes != 1 {
		t.Fatalf("engine checks %d, reconnects %d, resumes %d", engineCalls, reconnects, resumes)
	}
	mu.Unlock()

	// Not prepared (preparing, a reset, a failure): nothing is tried.
	tried := false
	w = gatewayWatch{every: time.Millisecond, ready: func() bool { return false }, answers: func(context.Context) bool { return false },
		engineUp: func(context.Context) bool { tried = true; return true }}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	w.run(ctx2)
	if tried {
		t.Fatal("recovered while not prepared")
	}

	// A reset took over: no resume, the recovery ends.
	resumed := false
	w = gatewayWatch{every: time.Millisecond, engineUp: func(context.Context) bool { return true },
		reconnect: func(context.Context) (bool, error) { return false, nil }, resume: func() { resumed = true }}
	w.recover(context.Background())
	if resumed {
		t.Fatal("resumed after a reset took over")
	}
}
