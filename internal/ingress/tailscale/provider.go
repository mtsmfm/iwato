// Package tailscale exposes each sandbox as a node of the owner's tailnet
// (tsnet in Iwato's process): https://<prefix><sandbox>.<tailnet>.ts.net:PORT/
// reaches the sandbox's 127.0.0.1:PORT, for the ports the core admits (a
// live process listens there), with the node's certificate; other ports
// are reset. Callers are identified with WhoIs.
package tailscale

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"iwato/internal/ingress"
)

// Node is one tailnet node (tsnet.Server in Iwato; a fake in tests).
type Node interface {
	// Up starts the node and waits until it runs; it returns its DNS
	// name (without the final dot).
	Up(ctx context.Context) (dnsName string, err error)
	// Handle receives every TCP connection to the node.
	Handle(fn func(src, dst netip.AddrPort) (func(net.Conn), bool)) (unregister func())
	WhoIs(ctx context.Context, src netip.AddrPort) (*ingress.Identity, error)
	GetCertificate(hi *tls.ClientHelloInfo) (*tls.Certificate, error)
	// Logout removes the node from the tailnet (a tagged node is deleted).
	Logout(ctx context.Context) error
	Close() error
}

// NodeConfig is what a node is made with.
type NodeConfig struct {
	Hostname string
	Dir      string
	// AuthKey is empty when the node joined already (its state is in Dir).
	AuthKey string
	Tags    []string
	Logf    func(format string, args ...any)
}

// Config configures the provider.
type Config struct {
	// Dir holds one state folder per sandbox (the node's private key:
	// only the owner may read it).
	Dir string
	// Prefix and Tag name and tag the nodes; Secret returns the OAuth
	// client secret (tskey-client-…) or auth key (tskey-auth-…), "" when
	// none is set.
	Prefix string
	Tag    string
	Secret func() string
	Logf   func(format string, args ...any)
	// NewNode makes a node (tsnet when nil).
	NewNode func(NodeConfig) Node
	// RetryAfter is how long a failed node waits before a Sync tries it
	// again (a minute when zero).
	RetryAfter time.Duration
	// SniffWait is how long the first byte is awaited to tell TLS (300 ms
	// when zero).
	SniffWait time.Duration
}

// Provider is the tailscale exposure provider.
type Provider struct {
	cfg  Config
	core ingress.Core

	mu     sync.Mutex
	nodes  map[string]*nodeState
	failed map[string]failure // the last failure of a node, by sandbox
	// forgetting: a Forget in progress, by sandbox (a Sync may ask for one
	// while the delete's runs; two nodes must not share a state folder).
	forgetting map[string]chan struct{}
	closed     bool
	wg         sync.WaitGroup
}

type failure struct {
	at     time.Time
	status ingress.Status
}

type nodeState struct {
	node       Node
	status     ingress.Status
	cancel     context.CancelFunc
	unregister func()
	dnsName    string
	done       chan struct{} // closed when Up ended
}

var _ ingress.Provider = (*Provider)(nil)

// New makes the provider; Start begins serving.
func New(cfg Config) *Provider {
	if cfg.NewNode == nil {
		cfg.NewNode = newTSNode
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Provider{cfg: cfg, nodes: map[string]*nodeState{}, failed: map[string]failure{}, forgetting: map[string]chan struct{}{}}
}

func (p *Provider) Name() string      { return "tailscale" }
func (p *Provider) HasIdentity() bool { return true }

func (p *Provider) Start(_ context.Context, core ingress.Core) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.core = core
	return nil
}

// Hostname is the node name of a sandbox.
func (p *Provider) Hostname(sandbox string) string { return p.cfg.Prefix + sandbox }

func (p *Provider) stateDir(sandbox string) string { return filepath.Join(p.cfg.Dir, sandbox) }

// joined reports whether the sandbox's node has its state (it joined).
func (p *Provider) joined(sandbox string) bool {
	_, err := os.Stat(filepath.Join(p.stateDir(sandbox), "tailscaled.state"))
	return err == nil
}

var sandboxDir = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Sync brings up the nodes of exposed sandboxes that run or joined before
// (a stopped sandbox keeps its node: its pages say it is stopped), closes
// those no longer exposed and forgets those of sandboxes that are gone.
func (p *Provider) Sync(ctx context.Context, sbs []ingress.SandboxState) error {
	known := map[string]bool{}
	want := map[string]bool{}
	for _, sb := range sbs {
		known[sb.Name] = true
		if sb.Exposed && (sb.Running || p.joined(sb.Name)) {
			want[sb.Name] = true
		}
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("provider closed")
	}
	var closeList []*nodeState
	for name, ns := range p.nodes {
		if !want[name] && known[name] {
			closeList = append(closeList, ns)
			delete(p.nodes, name)
		}
	}
	for name := range want {
		if p.nodes[name] != nil {
			continue
		}
		if f, ok := p.failed[name]; ok && time.Since(f.at) < p.retryAfter() {
			continue
		}
		p.startLocked(name)
	}
	p.mu.Unlock()
	for _, ns := range closeList {
		p.closeNode(ns)
	}
	// Nodes of sandboxes deleted while Iwato did not run.
	var errs []error
	entries, _ := os.ReadDir(p.cfg.Dir)
	for _, e := range entries {
		if e.IsDir() && sandboxDir.MatchString(e.Name()) && !known[e.Name()] {
			if err := p.Forget(ctx, e.Name()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (p *Provider) retryAfter() time.Duration {
	if p.cfg.RetryAfter > 0 {
		return p.cfg.RetryAfter
	}
	return time.Minute
}

// authKey is the secret as tsnet takes it: an OAuth client secret makes a
// non-ephemeral, pre-approved key for each node (tsnet's default would be
// ephemeral, and an ephemeral node offline for a while is deleted).
func authKey(secret string) string {
	if !strings.HasPrefix(secret, "tskey-client-") {
		return secret
	}
	base, _, _ := strings.Cut(secret, "?")
	return base + "?ephemeral=false&preauthorized=true"
}

// startLocked makes the sandbox's node and brings it up in the background.
func (p *Provider) startLocked(sandbox string) {
	fail := func(code string, params map[string]any, msg string) {
		p.failed[sandbox] = failure{at: time.Now(), status: ingress.Status{State: "error", Message: msg, Code: code, Params: params}}
	}
	key := ""
	if !p.joined(sandbox) {
		if p.cfg.Secret != nil {
			key = authKey(p.cfg.Secret())
		}
		if key == "" {
			fail("node_no_secret", nil, "Tailscale の OAuth クライアントのシークレット (または auth key) がありません。設定で貼り付けてください")
			return
		}
	}
	dir := p.stateDir(sandbox)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("node_state_dir", map[string]any{"detail": err.Error()}, "状態フォルダーを作れません: "+err.Error())
		return
	}
	ns := &nodeState{status: ingress.Status{State: "starting", Message: "tailnet に参加しています…", Code: "node_joining"}, done: make(chan struct{})}
	p.nodes[sandbox] = ns
	logf := func(format string, args ...any) {
		p.cfg.Logf("tailscale %s: "+format, append([]any{sandbox}, args...)...)
	}
	ns.node = p.cfg.NewNode(NodeConfig{Hostname: p.Hostname(sandbox), Dir: dir, AuthKey: key, Tags: []string{p.cfg.Tag}, Logf: logf})
	ctx, cancel := context.WithCancel(context.Background())
	ns.cancel = cancel
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(ns.done)
		up, cancelUp := context.WithTimeout(ctx, 3*time.Minute)
		dns, err := ns.node.Up(up)
		cancelUp()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.nodes[sandbox] != ns {
			return // closed meanwhile
		}
		if err != nil {
			logf("up: %v", err)
			fail("node_join_failed", map[string]any{"detail": err.Error()}, "tailnet に参加できません: "+err.Error())
			delete(p.nodes, sandbox)
			go p.closeNode(ns)
			return
		}
		delete(p.failed, sandbox)
		ns.dnsName = dns
		ns.status = ingress.Status{State: "connected", DNSName: dns}
		ns.unregister = ns.node.Handle(func(src, dst netip.AddrPort) (func(net.Conn), bool) {
			p.mu.Lock()
			core := p.core
			p.mu.Unlock()
			if core == nil || !core.Admit(ingress.Target{Sandbox: sandbox, Port: dst.Port()}) {
				return nil, true // reset: the port looks closed
			}
			return func(c net.Conn) { p.accept(sandbox, ns, src, dst, c) }, true
		})
		logf("up as %s", dns)
	}()
}

// closeNode stops a node (its state stays: it comes back as the same node).
func (p *Provider) closeNode(ns *nodeState) {
	if ns.cancel != nil {
		ns.cancel()
	}
	<-ns.done
	if ns.unregister != nil {
		ns.unregister()
	}
	if ns.node != nil {
		_ = ns.node.Close()
	}
}

// accept identifies the caller, terminates TLS when the client starts it
// and hands the connection to the core. Plain HTTP stays plain: the
// tailnet already encrypts it, and the same URL then serves ws:// and
// clients that do not follow redirects.
func (p *Provider) accept(sandbox string, ns *nodeState, src, dst netip.AddrPort, c net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	id, err := ns.node.WhoIs(ctx, src)
	cancel()
	if err != nil {
		p.cfg.Logf("tailscale %s: whois %s: %v", sandbox, src, err)
		_ = c.Close()
		return
	}
	wait := p.cfg.SniffWait
	if wait <= 0 {
		wait = 300 * time.Millisecond
	}
	pc, first := ingress.Peek(c, 1, wait)
	conn := net.Conn(pc)
	secure := false
	if len(first) == 1 && first[0] == 0x16 {
		tc := tls.Server(pc, &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
			GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
				// A client that connects by address sends no name.
				if hi.ServerName == "" {
					h := *hi
					h.ServerName = ns.dnsName
					hi = &h
				}
				return ns.node.GetCertificate(hi)
			},
		})
		_ = tc.SetDeadline(time.Now().Add(30 * time.Second))
		if err := tc.Handshake(); err != nil {
			p.cfg.Logf("tailscale %s: TLS from %s: %v", sandbox, src, err)
			_ = c.Close()
			return
		}
		_ = tc.SetDeadline(time.Time{})
		conn, secure = tc, true
	}
	p.mu.Lock()
	core := p.core
	p.mu.Unlock()
	if core == nil {
		_ = c.Close()
		return
	}
	core.Serve(ingress.Conn{Conn: conn, Target: &ingress.Target{Sandbox: sandbox, Port: dst.Port()}, Identity: id, Secure: secure, PublicHost: ns.dnsName})
}

// Forget logs the sandbox's node out (a tagged node leaves the tailnet)
// and removes its state, so that a sandbox made again under the name joins
// as a new node with the same name.
func (p *Provider) Forget(ctx context.Context, sandbox string) error {
	if !sandboxDir.MatchString(sandbox) {
		return fmt.Errorf("invalid sandbox name %q", sandbox)
	}
	p.mu.Lock()
	if ch := p.forgetting[sandbox]; ch != nil {
		p.mu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	p.forgetting[sandbox] = ch
	ns := p.nodes[sandbox]
	delete(p.nodes, sandbox)
	delete(p.failed, sandbox)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.forgetting, sandbox)
		p.mu.Unlock()
		close(ch)
	}()
	var node Node
	if ns != nil && ns.node != nil {
		<-ns.done
		if ns.unregister != nil {
			ns.unregister()
		}
		node = ns.node
		if ns.status.State != "connected" {
			_ = node.Close()
			node = nil
		}
	}
	var logoutErr error
	if node == nil && p.joined(sandbox) {
		// Bring the node back without a key to log it out.
		node = p.cfg.NewNode(NodeConfig{Hostname: p.Hostname(sandbox), Dir: p.stateDir(sandbox), Tags: []string{p.cfg.Tag}, Logf: p.cfg.Logf})
		up, cancel := context.WithTimeout(ctx, time.Minute)
		_, logoutErr = node.Up(up)
		cancel()
	}
	if node != nil {
		if logoutErr == nil {
			lo, cancel := context.WithTimeout(ctx, 30*time.Second)
			logoutErr = node.Logout(lo)
			cancel()
		}
		_ = node.Close()
	}
	if err := os.RemoveAll(p.stateDir(sandbox)); err != nil {
		return err
	}
	if logoutErr != nil {
		p.cfg.Logf("tailscale %s: logout: %v (remove the node %s in the Tailscale admin console)", sandbox, logoutErr, p.Hostname(sandbox))
		return &ingress.LogoutError{Node: p.Hostname(sandbox), Err: logoutErr}
	}
	p.cfg.Logf("tailscale %s: logged out and state removed", sandbox)
	return nil
}

func (p *Provider) URL(t ingress.Target) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ns := p.nodes[t.Sandbox]
	if ns == nil || ns.dnsName == "" || ns.status.State != "connected" {
		return "", false
	}
	host := ns.dnsName
	if t.Port != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(int(t.Port)))
	}
	return (&url.URL{Scheme: "https", Host: host, Path: "/"}).String(), true
}

func (p *Provider) Status(sandbox string) ingress.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ns := p.nodes[sandbox]; ns != nil {
		return ns.status
	}
	if f, ok := p.failed[sandbox]; ok {
		return f.status
	}
	return ingress.Status{State: "off", Message: "サンドボックスを起動すると tailnet に参加します", Code: "node_off"}
}

// Close stops every node (their states stay).
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	nodes := p.nodes
	p.nodes = map[string]*nodeState{}
	p.mu.Unlock()
	for _, ns := range nodes {
		p.closeNode(ns)
	}
	p.wg.Wait()
	return nil
}
