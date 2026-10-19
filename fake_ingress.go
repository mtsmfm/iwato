//go:build uitest

package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"

	"iwato/internal/ingress"
)

// The fake's ingress: a provider that joins no tailnet and only reports
// as the tailscale one would (each sandbox a node named prefix+sandbox on
// tail0000.ts.net, kept while the sandbox is stopped), and fake ports.

type fakeIngressProvider struct {
	prefix string
	mu     sync.Mutex
	joined map[string]bool
}

func newFakeIngressProvider(cfg *IngressConfig, _ func() string) ingress.Provider {
	return &fakeIngressProvider{prefix: cfg.prefix(), joined: map[string]bool{}}
}

func (p *fakeIngressProvider) Name() string                              { return "tailscale" }
func (p *fakeIngressProvider) HasIdentity() bool                         { return true }
func (p *fakeIngressProvider) Start(context.Context, ingress.Core) error { return nil }
func (p *fakeIngressProvider) Close() error                              { return nil }

func (p *fakeIngressProvider) Sync(_ context.Context, sbs []ingress.SandboxState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	known := map[string]bool{}
	for _, sb := range sbs {
		known[sb.Name] = true
		if sb.Exposed && sb.Running {
			p.joined[sb.Name] = true
		}
		if !sb.Exposed {
			delete(p.joined, sb.Name)
		}
	}
	for name := range p.joined {
		if !known[name] {
			delete(p.joined, name)
		}
	}
	return nil
}

func (p *fakeIngressProvider) Forget(_ context.Context, sandbox string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.joined, sandbox)
	return nil
}

func (p *fakeIngressProvider) dns(sandbox string) string {
	return p.prefix + sandbox + ".tail0000.ts.net"
}

func (p *fakeIngressProvider) URL(t ingress.Target) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.joined[t.Sandbox] {
		return "", false
	}
	return "https://" + p.dns(t.Sandbox) + ":" + strconv.Itoa(int(t.Port)) + "/", true
}

func (p *fakeIngressProvider) Status(sandbox string) ingress.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.joined[sandbox] {
		return ingress.Status{State: "off", Message: "サンドボックスを起動すると tailnet に参加します"}
	}
	return ingress.Status{State: "connected", DNSName: p.dns(sandbox)}
}

// DialPort: the fake's sandboxes run nothing.
func (d *Fake) DialPort(context.Context, string, string, uint16) (net.Conn, error) {
	return nil, errors.New("fake: no sandbox to connect to")
}

func (d *Fake) ForgetPorts(string) {}

// ListeningPorts are a dev server on ::1 and 127.0.0.1 (as Vite), an app,
// a database and a server on ::1 only.
func (d *Fake) ListeningPorts(_ context.Context, w Sandbox) ([]ingress.ListenPort, error) {
	if w.Name != "dev" {
		return []ingress.ListenPort{{Port: 8000, Addresses: []string{"0.0.0.0"}}}, nil
	}
	return []ingress.ListenPort{
		{Port: 3000, Addresses: []string{"0.0.0.0"}},
		{Port: 4000, Addresses: []string{"::1"}, IPv6Only: true},
		{Port: 5173, Addresses: []string{"127.0.0.1", "::1"}},
		{Port: 5432, Addresses: []string{"127.0.0.1"}},
	}, nil
}
