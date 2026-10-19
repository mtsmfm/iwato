//go:build uitest

package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePreparer is a runtime whose preparation the test moves along.
type fakePreparer struct {
	*Fake
	mu sync.Mutex
	st ImageState
}

// The login block is printed again once the image and the gateway are
// ready, or with the reason once the preparation failed; an unused token is
// shown again, an expired one replaced, a used one not.
func TestLoginBlockAfterPreparation(t *testing.T) {
	rt := &fakePreparer{Fake: NewFake(), st: ImageState{Phase: "building", Message: "ゲートウェイを起動しています…"}}
	s := newServer(testConfig(), rt, false, "127.0.0.1:29999")
	s.publicOrigin = "https://pc.example.ts.net"
	s.images = &imageManager{}
	s.images.set(ImageState{Phase: "building", Message: "dev"})
	startup := s.loginBlock("Iwato started", "", true)
	if m := loginURL.FindStringSubmatch(startup); m == nil || m[2] != "#token="+s.bootstrap {
		t.Fatalf("startup block: %q", startup)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	go s.announcePreparation(ctx, &out, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	s.images.set(ImageState{Phase: "ready"})
	time.Sleep(30 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("announced before the gateway: %q", out.String())
	}
	token := s.bootstrap
	rt.set(ImageState{Phase: "ready"})
	got := waitFor(t, &out, "== Iwato is ready ==")
	if !strings.Contains(got, "http://127.0.0.1:29999/#token="+token+"\n") || !strings.Contains(got, "https://pc.example.ts.net/#token="+token) || !strings.Contains(got, "another device") || !strings.Contains(got, "10 minutes") {
		t.Fatalf("%q", got)
	}
	time.Sleep(30 * time.Millisecond)
	if strings.Count(out.String(), "==") != 2 {
		t.Fatalf("announced more than once: %q", out.String())
	}

	// A retry, then a failure with an expired token: a fresh one.
	s.mu.Lock()
	s.bootstrapUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	rt.set(ImageState{Phase: "building"})
	time.Sleep(30 * time.Millisecond)
	rt.set(ImageState{Phase: "failed", Message: "OpenShell を準備できませんでした:\nport 17670 in use"})
	got = waitFor(t, &out, "== Iwato could not be prepared ==")
	s.mu.Lock()
	fresh := s.bootstrap
	s.bootstrap = "" // used: below
	s.mu.Unlock()
	if !strings.Contains(got, "Reason: OpenShell を準備できませんでした: port 17670 in use\n") || strings.Count(got, token) != 2 || fresh == token || !strings.Contains(got, "#token="+fresh) {
		t.Fatalf("%q", got)
	}

	// Used: no token, how to get a new one.
	block := s.loginBlock("Iwato is ready", "", false)
	if strings.Contains(block, "#token=") || !strings.Contains(block, "has been used") || !strings.Contains(block, "another device") {
		t.Fatalf("%q", block)
	}
}

func (p *fakePreparer) set(st ImageState) { p.mu.Lock(); p.st = st; p.mu.Unlock() }

func (p *fakePreparer) Preparation() ImageState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *fakePreparer) RetryPreparation(context.Context) bool { return false }
