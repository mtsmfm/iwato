// Package local is the ingress provider that needs no account (the
// default, ADR 0003): a sandbox's port is
// http://<port>.<sandbox>.localhost:<Iwato's port>/, served on Iwato's own
// loopback listener. Browsers resolve *.localhost to the loopback and treat
// it as a secure context, so it opens only on this PC.
//
// The listener is split by the Host of each connection's first request
// (Split) before the admin UI sees it: only <port>.<sandbox>.localhost
// with Iwato's port (or none), a known sandbox whose name is a DNS label
// and a port 1-65535 goes to the ingress core, from this PC directly (no
// proxy's headers). Everything else stays with the admin UI and its own
// Host check, so the admin UI and its API are never served on these hosts.
// There is no authentication (ADR 0003): anyone using a browser on this PC
// opens them, as a dev server run locally.
package local

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"iwato/internal/ingress"
)

// Config configures the provider.
type Config struct {
	// Port is Iwato's listen port (the URLs' port).
	Port int
	// AdminURL is the admin UI's address on this PC (the error pages).
	AdminURL string
	// Known tells whether a sandbox is configured (read per connection).
	Known func(sandbox string) bool
	// AdminCookie tells the admin UI's cookie, removed from requests
	// before they reach the sandbox (browsers do not send it here: it is
	// host-only on 127.0.0.1; this is a second guard).
	AdminCookie func(name string) bool
}

// Provider serves the *.localhost hosts.
type Provider struct {
	cfg   Config
	mu    sync.Mutex
	core  ingress.Core
	conns map[net.Conn]struct{} // being served: closed with the provider
}

// New makes the provider; connections come through Take.
func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string      { return "local" }
func (p *Provider) HasIdentity() bool { return false }

func (p *Provider) Start(_ context.Context, core ingress.Core) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.core = core
	return nil
}

// Close stops taking connections and ends those being served (keep-alive
// and WebSockets), as turning publishing off or to Tailscale means.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.core = nil
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
	return nil
}

// Sync and Forget have nothing to do: names are computed per connection.
func (p *Provider) Sync(context.Context, []ingress.SandboxState) error { return nil }
func (p *Provider) Forget(context.Context, string) error               { return nil }

// URL is the port's address (false when the sandbox's name is not a DNS
// label).
func (p *Provider) URL(t ingress.Target) (string, bool) {
	if !SandboxLabel(t.Sandbox) || t.Port == 0 {
		return "", false
	}
	return "http://" + strconv.Itoa(int(t.Port)) + "." + t.Sandbox + ".localhost:" + strconv.Itoa(p.cfg.Port) + "/", true
}

func (p *Provider) Status(sandbox string) ingress.Status {
	if !SandboxLabel(sandbox) {
		return ingress.Status{State: "error", Message: "名前が DNS のラベルに使えない (ハイフンで終わる、3・4 文字目が --) ため、*.localhost で開けません", Code: "local_name_invalid"}
	}
	return ingress.Status{State: "connected", DNSName: sandbox + ".localhost"}
}

var sandboxName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// SandboxLabel: Iwato's sandbox name rules (config.go) that are also a DNS
// label browsers keep as it is: not ending with a hyphen, and no "--" in
// the third and fourth places (xn-- and the other reserved prefixes).
func SandboxLabel(name string) bool {
	return sandboxName.MatchString(name) && !strings.HasSuffix(name, "-") && !(len(name) >= 4 && name[2:4] == "--")
}

// ParseHost reads <port>.<sandbox>.localhost[:listenPort] (case does not
// matter; no trailing dot, no leading zeros).
func ParseHost(hostport string, listenPort int) (ingress.Target, bool) {
	host := strings.ToLower(hostport)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if host[i+1:] != strconv.Itoa(listenPort) {
			return ingress.Target{}, false
		}
		host = host[:i]
	}
	labels := strings.Split(host, ".")
	if len(labels) != 3 || labels[2] != "localhost" || !SandboxLabel(labels[1]) {
		return ingress.Target{}, false
	}
	ps := labels[0]
	if ps == "" || len(ps) > 5 || ps[0] == '0' || strings.Trim(ps, "0123456789") != "" {
		return ingress.Target{}, false
	}
	n, err := strconv.Atoi(ps)
	if err != nil || n < 1 || n > 65535 {
		return ingress.Target{}, false
	}
	return ingress.Target{Sandbox: labels[1], Port: uint16(n)}, true
}

// Resolve is ParseHost for a known sandbox, while the provider runs.
func (p *Provider) Resolve(host string) (ingress.Target, bool) {
	p.mu.Lock()
	running := p.core != nil
	p.mu.Unlock()
	t, ok := ParseHost(host, p.cfg.Port)
	if !running || !ok || p.cfg.Known == nil || !p.cfg.Known(t.Sandbox) {
		return ingress.Target{}, false
	}
	return t, true
}

// Take serves a connection whose first request is r (read ahead: c still
// returns it) when r is for one of its hosts, from this PC directly.
// False: the caller keeps the connection (the admin UI).
func (p *Provider) Take(c net.Conn, r *http.Request) bool {
	t, ok := p.Resolve(r.Host)
	if !ok || !Direct(r) {
		return false
	}
	// Tracked until closed: a WebSocket outlives core.Serve (hijacked).
	c = &trackedConn{Conn: c, p: p}
	p.mu.Lock()
	core := p.core
	if core != nil {
		if p.conns == nil {
			p.conns = map[net.Conn]struct{}{}
		}
		p.conns[c] = struct{}{}
	}
	p.mu.Unlock()
	if core == nil {
		return false
	}
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	go core.Serve(ingress.Conn{Conn: c, Target: &t, Resolve: p.Resolve, Auth: auth{p.cfg.AdminCookie}, HTTPOnly: true, PublicHost: host, AdminURL: p.cfg.AdminURL})
	return true
}

// trackedConn leaves the provider's set when closed.
type trackedConn struct {
	net.Conn
	p *Provider
}

func (c *trackedConn) Close() error {
	c.p.mu.Lock()
	delete(c.p.conns, c)
	c.p.mu.Unlock()
	return c.Conn.Close()
}

func (c *trackedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// proxyHeaders are what a proxy in front (tailscale serve) adds: such
// requests come from elsewhere, though from 127.0.0.1.
var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "Tailscale-User-Login", "Tailscale-Headers-Info"}

// Direct: from the loopback, without a proxy's headers.
func Direct(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range proxyHeaders {
		if _, ok := r.Header[h]; ok {
			return false
		}
	}
	return true
}

// auth lets in direct requests from this PC; there is no identity.
type auth struct{ adminCookie func(string) bool }

func (auth) AllowConn(context.Context, *ingress.Identity, ingress.Target) error { return nil }

func (auth) AllowHTTP(w http.ResponseWriter, r *http.Request, _ *ingress.Identity, _ ingress.Target) bool {
	if Direct(r) {
		return true
	}
	w.Header().Set("Connection", "close")
	http.Error(w, "Iwato: *.localhost opens only in this PC's browser, not through a proxy", http.StatusForbidden)
	return false
}

func (a auth) Strip(r *http.Request) {
	if a.adminCookie == nil {
		return
	}
	var keep []string
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name != "" && !a.adminCookie(name) {
				keep = append(keep, strings.TrimSpace(part))
			}
		}
	}
	r.Header.Del("Cookie")
	if len(keep) > 0 {
		r.Header.Set("Cookie", strings.Join(keep, "; "))
	}
}

// Split returns the listener the admin UI serves: each connection's first
// request is read ahead (its headers, up to maxHeader bytes, within wait)
// and offered to take; the connections take refuses, and those whose
// first request cannot be read, are returned by Accept with the bytes
// read still to come.
func Split(ln net.Listener, take func(net.Conn, *http.Request) bool, wait time.Duration, maxHeader int) net.Listener {
	l := &splitListener{Listener: ln, take: take, wait: wait, max: maxHeader, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.acceptLoop()
	return l
}

type splitListener struct {
	net.Listener
	take  func(net.Conn, *http.Request) bool
	wait  time.Duration
	max   int
	conns chan net.Conn
	done  chan struct{}
	err   error // set before done is closed
}

func (l *splitListener) acceptLoop() {
	var delay time.Duration
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() || isTemporary(err) {
				// As http.Server does (too many open files…).
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			l.err = err
			close(l.done)
			return
		}
		delay = 0
		go l.classify(c)
	}
}

func isTemporary(err error) bool {
	t, ok := err.(interface{ Temporary() bool })
	return ok && t.Temporary()
}

func (l *splitListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, l.err
	}
}

var headerEnd = []byte("\r\n\r\n")

// idleWait is how long a connection may stay silent before its first
// request (the admin UI's IdleTimeout).
const idleWait = 60 * time.Second

// classify reads the first request's headers and hands the connection to
// take or to Accept.
func (l *splitListener) classify(c net.Conn) {
	// A browser opens connections ahead (preconnect) and sends the request
	// later: the wait starts with the first bytes. Nothing in idleWait:
	// nothing to serve (the admin UI would close it too).
	_ = c.SetReadDeadline(time.Now().Add(idleWait))
	chunk := make([]byte, 4096)
	n, _ := c.Read(chunk[:min(len(chunk), l.max)])
	if n == 0 {
		_ = c.Close()
		return
	}
	buf := append(make([]byte, 0, 2048), chunk[:n]...)
	_ = c.SetReadDeadline(time.Now().Add(l.wait))
	end := bytes.Index(buf, headerEnd)
	for end < 0 && len(buf) < l.max {
		n, err := c.Read(chunk[:min(len(chunk), l.max-len(buf))])
		buf = append(buf, chunk[:n]...)
		if end = bytes.Index(buf, headerEnd); end >= 0 || err != nil {
			break
		}
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &prefixConn{Conn: c, prefix: buf}
	if end >= 0 {
		if r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf[:end+len(headerEnd)]))); err == nil {
			r.RemoteAddr = c.RemoteAddr().String()
			if l.take(pc, r) {
				return
			}
		}
	}
	select {
	case l.conns <- pc:
	case <-l.done:
		_ = c.Close()
	}
}

// prefixConn returns the bytes read ahead before the connection's.
type prefixConn struct {
	net.Conn
	mu     sync.Mutex
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(b)
}

// CloseWrite half-closes when the connection can.
func (c *prefixConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
