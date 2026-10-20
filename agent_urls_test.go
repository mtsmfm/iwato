package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"iwato/internal/agentsmd"
	"iwato/internal/ingress"
	"iwato/internal/ingress/local"
)

func TestAgentsURLSection(t *testing.T) {
	writes := make(chan string, 10)
	m := &ingressManager{urls: map[string]string{}, urlsBusy: map[string]bool{}}
	m.writeURLs = func(_ context.Context, sb, section string) error {
		writes <- sb + "|" + section
		return nil
	}
	dev := ingress.SandboxState{Name: "dev", Running: true, Exposed: true}
	stopped := ingress.SandboxState{Name: "dev", Exposed: true}
	step := func(p ingress.Provider, st ingress.SandboxState, want string) {
		t.Helper()
		m.mu.Lock()
		m.provider = p
		m.mu.Unlock()
		m.refreshURLs([]ingress.SandboxState{st})
		wait := 200 * time.Millisecond
		if want != "" {
			wait = 5 * time.Second
		}
		select {
		case got := <-writes:
			if want == "" || !strings.Contains(got, want) || !strings.HasPrefix(got, "dev|"+agentsmd.Marker+"\n") {
				t.Fatalf("wrote %q, want %q", got, want)
			}
		case <-time.After(wait):
			if want != "" {
				t.Fatalf("no write, want %q", want)
			}
		}
		for {
			m.mu.Lock()
			busy := len(m.urlsBusy)
			m.mu.Unlock()
			if busy == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	lp := local.New(local.Config{Port: 17600})
	step(lp, dev, "`http://PORT.dev.localhost:17600/`")
	step(lp, dev, "")
	step(&recordingProvider{}, dev, "`https://iwato-dev.tail0000.ts.net:PORT/` (Tailscale")
	step(&recordingProvider{}, dev, "")
	step(&recordingProvider{}, stopped, "")
	step(&recordingProvider{}, dev, "https://iwato-dev.tail0000.ts.net:8000/")
	step(&recordingProvider{}, ingress.SandboxState{Name: "dev", Running: true}, "publishes no port")
	step(nil, dev, "")
	step(lp, ingress.SandboxState{Name: "dev", Running: true, Exposed: true}, "localhost")

	section := urlsSection(lp, dev)
	got := agentsmd.Compose([]byte("new\n"), []byte("old\n\n"+section))
	if string(got) != "new\n\n"+section {
		t.Fatalf("%q", got)
	}
}
