package tailscale

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"iwato/internal/ingress"
)

// fakeNode stands in for tsnet: it joins by writing its state file.
type fakeNode struct {
	cfg               NodeConfig
	mu                sync.Mutex
	handler           func(src, dst netip.AddrPort) (func(net.Conn), bool)
	upErr             error
	loggedOut, closed bool
	cert              *tls.Certificate
	logoutWait        chan struct{} // Logout waits for it when set
}

func (n *fakeNode) Up(ctx context.Context) (string, error) {
	if n.upErr != nil {
		return "", n.upErr
	}
	if err := os.WriteFile(filepath.Join(n.cfg.Dir, "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		return "", err
	}
	return n.cfg.Hostname + ".tail0000.ts.net", nil
}

func (n *fakeNode) Handle(fn func(src, dst netip.AddrPort) (func(net.Conn), bool)) func() {
	n.mu.Lock()
	n.handler = fn
	n.mu.Unlock()
	return func() { n.mu.Lock(); n.handler = nil; n.mu.Unlock() }
}

func (n *fakeNode) WhoIs(_ context.Context, src netip.AddrPort) (*ingress.Identity, error) {
	if src.Addr() == netip.MustParseAddr("100.64.0.9") {
		return &ingress.Identity{Provider: "tailscale", Node: "srv", Tags: []string{"tag:server"}}, nil
	}
	return &ingress.Identity{Provider: "tailscale", Login: "alice@github", Node: "pixel"}, nil
}

func (n *fakeNode) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) { return n.cert, nil }
func (n *fakeNode) Logout(context.Context) error {
	if n.logoutWait != nil {
		<-n.logoutWait
	}
	n.loggedOut = true
	return nil
}
func (n *fakeNode) Close() error { n.closed = true; return nil }

// connect makes a connection to the node's port as a tailnet peer.
func (n *fakeNode) connect(t *testing.T, src string, port uint16) net.Conn {
	t.Helper()
	n.mu.Lock()
	h := n.handler
	n.mu.Unlock()
	if h == nil {
		t.Fatal("the node handles no connections")
	}
	fn, ok := h(netip.MustParseAddrPort(src), netip.AddrPortFrom(netip.MustParseAddr("100.100.1.1"), port))
	if !ok || fn == nil {
		t.Fatal("the node refused the flow")
	}
	client, server := net.Pipe()
	go fn(server)
	return client
}

type fakes struct {
	mu         sync.Mutex
	nodes      []*fakeNode
	fail       error
	logoutWait chan struct{}
}

func (f *fakes) make(c NodeConfig) Node {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := &fakeNode{cfg: c, upErr: f.fail, logoutWait: f.logoutWait}
	f.nodes = append(f.nodes, n)
	return n
}

func (f *fakes) last() *fakeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[len(f.nodes)-1]
}

// coreFunc records the connections the provider hands over; it refuses
// port 9.
type coreFunc func(ingress.Conn)

func (f coreFunc) Admit(t ingress.Target) bool { return t.Port != 9 }
func (f coreFunc) Serve(c ingress.Conn)        { f(c) }

func waitStatus(t *testing.T, p *Provider, sb, state string) ingress.Status {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if st := p.Status(sb); st.State == state {
			return st
		}
	}
	t.Fatalf("%s: %+v, want %s", sb, p.Status(sb), state)
	return ingress.Status{}
}

func newProvider(t *testing.T, f *fakes, secret string) (*Provider, chan ingress.Conn) {
	dir := t.TempDir()
	p := New(Config{Dir: dir, Prefix: "iwato-", Tag: "tag:iwato", Secret: func() string { return secret }, NewNode: f.make, RetryAfter: time.Hour, SniffWait: 50 * time.Millisecond})
	got := make(chan ingress.Conn, 4)
	p.Start(context.Background(), coreFunc(func(c ingress.Conn) { got <- c }))
	t.Cleanup(func() { p.Close() })
	return p, got
}

func TestNodesFollowTheSandboxes(t *testing.T) {
	f := &fakes{}
	p, _ := newProvider(t, f, "tskey"+"-client-abc-def?ephemeral=true")
	ctx := context.Background()
	// Running: a node joins with a non-ephemeral, pre-approved key.
	p.Sync(ctx, []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}, {Name: "web", Exposed: true}})
	st := waitStatus(t, p, "dev", "connected")
	if st.DNSName != "iwato-dev.tail0000.ts.net" {
		t.Fatal(st)
	}
	n := f.last()
	if n.cfg.Hostname != "iwato-dev" || n.cfg.AuthKey != "tskey"+"-client-abc-def?ephemeral=false&preauthorized=true" || n.cfg.Tags[0] != "tag:iwato" {
		t.Fatalf("%+v", n.cfg)
	}
	if fi, err := os.Stat(n.cfg.Dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("state folder %v %v", fi, err)
	}
	// Stopped and never joined: no node.
	if p.Status("web").State != "off" || len(f.nodes) != 1 {
		t.Fatal(p.Status("web"))
	}
	if u, ok := p.URL(ingress.Target{Sandbox: "dev", Port: 5173}); !ok || u != "https://iwato-dev.tail0000.ts.net:5173/" {
		t.Fatal(u)
	}
	if u, _ := p.URL(ingress.Target{Sandbox: "dev", Port: 443}); u != "https://iwato-dev.tail0000.ts.net/" {
		t.Fatal(u)
	}
	// Stopped after it joined: the node stays (it serves the 停止中 page).
	p.Sync(ctx, []ingress.SandboxState{{Name: "dev", Exposed: true}, {Name: "web", Exposed: true}})
	if p.Status("dev").State != "connected" || n.closed {
		t.Fatal("the node of a stopped sandbox went away")
	}
	// Iwato again (a new provider): a joined node comes back without a key.
	p.Close()
	if !n.closed || n.loggedOut {
		t.Fatal("closing must not log out")
	}
	p2 := New(Config{Dir: p.cfg.Dir, Prefix: "iwato-", Tag: "tag:iwato", Secret: func() string { return "tskey" + "-client-abc-def" }, NewNode: f.make})
	defer p2.Close()
	p2.Sync(ctx, []ingress.SandboxState{{Name: "dev", Exposed: true}})
	waitStatus(t, p2, "dev", "connected")
	if k := f.last().cfg.AuthKey; k != "" {
		t.Fatalf("a joined node got the key %q", k)
	}
	// Not exposed any more: closed, state kept.
	p2.Sync(ctx, []ingress.SandboxState{{Name: "dev", Exposed: false}})
	if !f.last().closed || p2.Status("dev").State != "off" || !p2.joined("dev") {
		t.Fatal("disabling must close the node and keep its state")
	}
}

func TestForgetLogsOutAndRemovesState(t *testing.T) {
	f := &fakes{}
	p, _ := newProvider(t, f, "tskey"+"-auth-xyz")
	ctx := context.Background()
	p.Sync(ctx, []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}, {Name: "old", Running: true, Exposed: true}})
	waitStatus(t, p, "dev", "connected")
	waitStatus(t, p, "old", "connected")
	if k := f.nodes[0].cfg.AuthKey; k != "tskey"+"-auth-xyz" {
		t.Fatalf("an auth key is used as it is: %q", k)
	}
	var dev, old *fakeNode
	for _, n := range f.nodes {
		if n.cfg.Hostname == "iwato-dev" {
			dev = n
		} else {
			old = n
		}
	}
	if err := p.Forget(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if !dev.loggedOut || !dev.closed || p.joined("dev") {
		t.Fatal("forget must log out, close and remove the state")
	}
	if _, err := os.Stat(filepath.Join(p.cfg.Dir, "dev")); !os.IsNotExist(err) {
		t.Fatal("state folder left")
	}
	// A sandbox deleted while Iwato did not run: its node is closed, then a
	// Sync without it brings the state back up to log it out.
	p.Close()
	if !old.closed {
		t.Fatal()
	}
	p2 := New(Config{Dir: p.cfg.Dir, Prefix: "iwato-", Tag: "tag:iwato", NewNode: f.make})
	defer p2.Close()
	if err := p2.Sync(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := f.last(); n.cfg.Hostname != "iwato-old" || !n.loggedOut || p2.joined("old") {
		t.Fatalf("the leftover node: %+v", n.cfg)
	}
}

// A Sync that finds a deleted sandbox's state while the delete's Forget
// still runs waits for it: one node logs out, none starts on the folder.
func TestForgetOnce(t *testing.T) {
	f := &fakes{}
	p, _ := newProvider(t, f, "tskey"+"-auth-xyz")
	ctx := context.Background()
	p.Sync(ctx, []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}})
	waitStatus(t, p, "dev", "connected")
	f.nodes[0].logoutWait = make(chan struct{})
	first := make(chan error)
	go func() { first <- p.Forget(ctx, "dev") }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		p.mu.Lock()
		busy := p.forgetting["dev"] != nil
		p.mu.Unlock()
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forget did not start")
		}
	}
	second := make(chan error)
	go func() { second <- p.Sync(ctx, nil) }()
	time.Sleep(50 * time.Millisecond)
	close(f.nodes[0].logoutWait)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if len(f.nodes) != 1 || !f.nodes[0].loggedOut || p.joined("dev") {
		t.Fatalf("%d nodes", len(f.nodes))
	}
}

func TestMissingSecretAndFailures(t *testing.T) {
	f := &fakes{}
	p, _ := newProvider(t, f, "")
	p.Sync(context.Background(), []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}})
	if st := p.Status("dev"); st.State != "error" || len(f.nodes) != 0 {
		t.Fatal(st)
	}
	f.fail = errors.New("invalid key")
	p2, _ := newProvider(t, f, "tskey"+"-auth-bad")
	p2.Sync(context.Background(), []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}})
	st := waitStatus(t, p2, "dev", "error")
	if st.Message == "" {
		t.Fatal(st)
	}
	// Retried only after RetryAfter, not on every Sync.
	p2.Sync(context.Background(), []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}})
	if len(f.nodes) != 1 {
		t.Fatalf("%d nodes", len(f.nodes))
	}
}

func TestConnectionsReachTheCore(t *testing.T) {
	f := &fakes{}
	p, got := newProvider(t, f, "tskey"+"-auth-xyz")
	p.Sync(context.Background(), []ingress.SandboxState{{Name: "dev", Running: true, Exposed: true}})
	waitStatus(t, p, "dev", "connected")
	n := f.last()
	// A port the core refuses is reset (no handler, handled).
	n.mu.Lock()
	h := n.handler
	n.mu.Unlock()
	if fn, ok := h(netip.MustParseAddrPort("100.64.0.2:51000"), netip.MustParseAddrPort("100.100.1.1:9")); fn != nil || !ok {
		t.Fatal("a refused port must be reset")
	}
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	n.cert = &srv.TLS.Certificates[0]

	// Plain: handed over as it is, with the caller's identity.
	c := n.connect(t, "100.64.0.2:51000", 5173)
	go c.Write([]byte("GET / HTTP/1.1\r\n"))
	conn := <-got
	if conn.Target.Sandbox != "dev" || conn.Target.Port != 5173 || conn.Secure || conn.Identity.Login != "alice@github" || conn.PublicHost != "iwato-dev.tail0000.ts.net" {
		t.Fatalf("%+v %+v", conn, conn.Identity)
	}
	buf := make([]byte, 4)
	io.ReadFull(conn, buf)
	if string(buf) != "GET " {
		t.Fatalf("%q", buf)
	}
	conn.Close()
	c.Close()

	// TLS: terminated with the node's certificate, HTTP/2 offered.
	c = n.connect(t, "100.64.0.9:51001", 443)
	tc := tls.Client(c, &tls.Config{InsecureSkipVerify: true, ServerName: "iwato-dev.tail0000.ts.net", NextProtos: []string{"h2"}})
	errc := make(chan error, 1)
	go func() { errc <- tc.Handshake() }()
	conn = <-got
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !conn.Secure || tc.ConnectionState().NegotiatedProtocol != "h2" || len(conn.Identity.Tags) != 1 {
		t.Fatalf("%+v", conn)
	}
	c.Close()
	conn.Close()
}
