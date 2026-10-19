package ingress

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// routes is a fixed route table.
type routes map[uint16]PortOptions

func (r routes) Options(t Target) (PortOptions, bool) {
	if t.Sandbox != "dev" {
		return PortOptions{}, false
	}
	return r[t.Port], true
}

var owner = &Identity{Provider: "tailscale", Login: "alice@github", Node: "pixel"}

func ownerAuth() Authorizer {
	return ProviderAuth{Owners: func() []string { return []string{"alice@github"} }}
}

// fakeSandbox is a sandbox's loopback: each port maps to a local listener.
type fakeSandbox struct {
	mu      sync.Mutex
	ports   map[uint16]string // port → 127.0.0.1:N of the test's server
	running bool
}

func (f *fakeSandbox) dial(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error) {
	f.mu.Lock()
	addr, ok := f.ports[port]
	f.mu.Unlock()
	if !ok || host != "127.0.0.1" {
		return nil, fmt.Errorf("%w: %s:%d", ErrRefused, host, port)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// ingressFor runs the core behind a local listener, as a provider would:
// every connection is for dev:port with the owner's identity, TLS
// terminated when tlsConfig is set.
func ingressFor(t *testing.T, core *Server, port uint16, id *Identity, tlsConfig *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, secure := c, false
				if tlsConfig != nil {
					tc := tls.Server(c, tlsConfig)
					if tc.Handshake() != nil {
						c.Close()
						return
					}
					conn, secure = tc, true
				}
				core.Serve(Conn{Conn: conn, Target: &Target{Sandbox: "dev", Port: port}, Identity: id, Secure: secure, PublicHost: "iwato-dev.tail0000.ts.net"})
			}()
		}
	}()
	return ln.Addr().String()
}

func newCore(f *fakeSandbox, r routes) *Server {
	b := &Backend{Dial: f.dial, Running: func(string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.running }, Max: 3, Wait: 200 * time.Millisecond}
	s := &Server{Routes: r, Dialer: b, Auth: ownerAuth(), Logf: func(string, ...any) {}, DetectWait: 100 * time.Millisecond}
	b.OnBusy = s.CloseIdle
	return s
}

// get sends a request to the ingress as if it were the public host.
func get(t *testing.T, addr string, port uint16, path string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	req.Host = "iwato-dev.tail0000.ts.net:" + strconv.Itoa(int(port))
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func serveBackend(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestHTTPProxyRewritesHost(t *testing.T) {
	seen := make(chan *http.Request, 1)
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Domain: "localhost", Path: "/"})
		http.Redirect(w, r, "http://localhost:5173/next?x=1", http.StatusFound)
	}))
	f := &fakeSandbox{ports: map[uint16]string{5173: addr, 3000: addr, 3001: addr}, running: true}
	core := newCore(f, routes{3000: {Host: HostForwarded}, 3001: {Host: HostPreserve}})
	cases := []struct {
		port                       uint16
		host, origin, xfHost, back string
	}{
		{5173, "localhost:5173", "http://localhost:5173", "", "http://iwato-dev.tail0000.ts.net:5173/next?x=1"},
		{3000, "localhost:3000", "http://iwato-dev.tail0000.ts.net:3000", "iwato-dev.tail0000.ts.net:3000", ""},
		{3001, "iwato-dev.tail0000.ts.net:3001", "http://iwato-dev.tail0000.ts.net:3001", "", ""},
	}
	for _, c := range cases {
		in := ingressFor(t, core, c.port, owner, nil)
		origin := "http://iwato-dev.tail0000.ts.net:" + strconv.Itoa(int(c.port))
		res, _ := get(t, in, c.port, "/app", http.Header{"Origin": {origin}})
		r := <-seen
		if r.Host != c.host || r.Header.Get("Origin") != c.origin || r.Header.Get("X-Forwarded-Host") != c.xfHost || r.Header.Get("X-Forwarded-Proto") != "http" || r.Header.Get("X-Forwarded-For") != "127.0.0.1" {
			t.Errorf("port %d: upstream saw Host %q Origin %q XFH %q XFP %q XFF %q", c.port, r.Host, r.Header.Get("Origin"), r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-For"))
		}
		if c.back != "" && res.Header.Get("Location") != c.back {
			t.Errorf("Location %q, want %q", res.Header.Get("Location"), c.back)
		}
		if sc := res.Header.Get("Set-Cookie"); strings.Contains(strings.ToLower(sc), "domain") || !strings.HasPrefix(sc, "sid=1") {
			t.Errorf("Set-Cookie %q keeps Domain=localhost", sc)
		}
	}
}

func TestHTTPKeepsConnectionsToTheSandbox(t *testing.T) {
	var mu sync.Mutex
	conns := map[string]bool{}
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns[r.RemoteAddr] = true
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	f := &fakeSandbox{ports: map[uint16]string{8000: addr}, running: true}
	core := newCore(f, routes{})
	in := ingressFor(t, core, 8000, owner, nil)
	for range 5 {
		// A new client connection each time: the sandbox side is reused.
		tr := &http.Transport{DisableKeepAlives: true}
		req, _ := http.NewRequest("GET", "http://"+in+"/", nil)
		res, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(res.Body)
		res.Body.Close()
	}
	if len(conns) != 1 {
		t.Fatalf("the sandbox saw %d connections for 5 requests", len(conns))
	}
}

func TestHTTP2OverTLS(t *testing.T) {
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proto=%s xfp=%s", r.Proto, r.Header.Get("X-Forwarded-Proto"))
	}))
	f := &fakeSandbox{ports: map[uint16]string{5173: addr}, running: true}
	core := newCore(f, routes{})
	cert := httptest.NewTLSServer(http.NotFoundHandler())
	defer cert.Close()
	in := ingressFor(t, core, 5173, owner, &tls.Config{Certificates: cert.TLS.Certificates, NextProtos: []string{"h2", "http/1.1"}})
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}
	res, err := (&http.Client{Transport: tr}).Get("https://" + in + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Proto != "HTTP/2.0" || string(b) != "proto=HTTP/1.1 xfp=https" {
		t.Fatalf("client %s, upstream said %q", res.Proto, b)
	}
}

func TestWebSocketUpgrade(t *testing.T) {
	origins := make(chan string, 1)
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins <- r.Header.Get("Origin") + " " + r.Host
		// As Vite: only its own origin.
		u := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "http://localhost:5173" }}
		c, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, p, err := c.ReadMessage()
			if err != nil {
				return
			}
			c.WriteMessage(mt, append([]byte("echo:"), p...))
		}
	}))
	f := &fakeSandbox{ports: map[uint16]string{5173: addr}, running: true}
	core := newCore(f, routes{})
	in := ingressFor(t, core, 5173, owner, nil)
	d := websocket.Dialer{NetDial: func(string, string) (net.Conn, error) { return net.Dial("tcp", in) }}
	c, _, err := d.Dial("ws://iwato-dev.tail0000.ts.net:5173/hmr", http.Header{"Origin": {"http://iwato-dev.tail0000.ts.net:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if o := <-origins; o != "http://localhost:5173 localhost:5173" {
		t.Fatalf("upstream saw %q", o)
	}
	c.WriteMessage(websocket.TextMessage, []byte("hi"))
	_, p, err := c.ReadMessage()
	if err != nil || string(p) != "echo:hi" {
		t.Fatalf("%q %v", p, err)
	}
	if n := core.Dialer.(*Backend).InUse("dev"); n != 1 {
		t.Fatalf("a WebSocket holds %d places", n)
	}
}

// tcpServer answers with greeting first (if any), then echoes.
func tcpServer(t *testing.T, greeting string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.WriteString(c, greeting)
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestTCPPassthrough(t *testing.T) {
	f := &fakeSandbox{ports: map[uint16]string{5432: tcpServer(t, ""), 3306: tcpServer(t, "HELLO\n")}, running: true}
	core := newCore(f, routes{})
	// A client that speaks first with something that is not HTTP (psql).
	c, err := net.Dial("tcp", ingressFor(t, core, 5432, owner, nil))
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("\x00\x00\x00\x08\x04\xd2\x16\x2f"))
	buf := make([]byte, 8)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "\x00\x00\x00\x08\x04\xd2\x16\x2f" {
		t.Fatalf("%q %v", buf, err)
	}
	// Half-close goes through: the echo ends.
	c.(*net.TCPConn).CloseWrite()
	if rest, _ := io.ReadAll(c); len(rest) != 0 {
		t.Fatalf("after half-close: %q", rest)
	}
	c.Close()
	// A server that speaks first (MySQL): the client waits.
	c, err = net.Dial("tcp", ingressFor(t, core, 3306, owner, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "HELLO\n" {
		t.Fatalf("%q %v", line, err)
	}
}

func TestErrorPages(t *testing.T) {
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	f := &fakeSandbox{ports: map[uint16]string{8000: addr}, running: false}
	core := newCore(f, routes{})
	core.AdminURL = func() string { return "https://pc.tail0000.ts.net/" }
	in := ingressFor(t, core, 8000, owner, nil)
	res, body := get(t, in, 8000, "/", nil)
	if res.StatusCode != 503 || !strings.Contains(body, "サンドボックス dev は停止中です") || !strings.Contains(body, "https://pc.tail0000.ts.net/") {
		t.Fatalf("stopped: %d %s", res.StatusCode, body)
	}
	f.mu.Lock()
	f.running = true
	f.mu.Unlock()
	res, body = get(t, in, 8000, "/", nil)
	if res.StatusCode != 200 || body != "ok" {
		t.Fatalf("started: %d %s", res.StatusCode, body)
	}
	in2 := ingressFor(t, core, 9999, owner, nil)
	res, body = get(t, in2, 9999, "/", nil)
	if res.StatusCode != 502 || !strings.Contains(body, ":9999 で待ち受けているサーバーがありません") {
		t.Fatalf("nothing listening: %d %s", res.StatusCode, body)
	}
}

func TestRefusedConnections(t *testing.T) {
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	f := &fakeSandbox{ports: map[uint16]string{8000: addr}, running: true}
	core := newCore(f, routes{8001: {Disabled: true}})
	closed := func(addr string) bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return true
		}
		defer c.Close()
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := c.Read(make([]byte, 10))
		return n == 0
	}
	if !closed(ingressFor(t, core, 8000, &Identity{Login: "mallory@github", Node: "x"}, nil)) {
		t.Error("another user got through")
	}
	if !closed(ingressFor(t, core, 8000, &Identity{Node: "other-sandbox", Tags: []string{"tag:iwato"}}, nil)) {
		t.Error("a tagged node got through")
	}
	if !closed(ingressFor(t, core, 8001, owner, nil)) {
		t.Error("a disabled port got through")
	}
	if closed(ingressFor(t, core, 8000, owner, nil)) {
		t.Error("the owner was refused")
	}
}

func TestAuthPolicy(t *testing.T) {
	a := ownerAuth()
	ctx := context.Background()
	for _, c := range []struct {
		id *Identity
		ok bool
	}{
		{owner, true},
		{&Identity{Login: "ALICE@github", Node: "pc"}, true},
		{nil, false},
		{&Identity{Login: "bob@github", Node: "pc"}, false},
		{&Identity{Login: "alice@github", Node: "srv", Tags: []string{"tag:server"}}, false},
		{&Identity{Login: "alice@github", Node: "shared", Shared: true}, false},
	} {
		err := a.AllowConn(ctx, c.id, Target{"dev", 1})
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrNotOwner)) {
			t.Errorf("%+v: %v", c.id, err)
		}
	}
}

// refusedConn refuses on the first read, as the gateway's relay does.
type refusedConn struct{ net.Conn }

func (refusedConn) Read([]byte) (int, error)    { return 0, fmt.Errorf("%w: relay", ErrRefused) }
func (refusedConn) Write(p []byte) (int, error) { return len(p), nil }
func (refusedConn) Close() error                { return nil }

// failConn fails reads with err.
type failConn struct{ err error }

func (f failConn) Read([]byte) (int, error)       { return 0, f.err }
func (failConn) Write(p []byte) (int, error)      { return len(p), nil }
func (failConn) Close() error                     { return nil }
func (failConn) LocalAddr() net.Addr              { return nil }
func (failConn) RemoteAddr() net.Addr             { return nil }
func (failConn) SetDeadline(time.Time) error      { return nil }
func (failConn) SetReadDeadline(time.Time) error  { return nil }
func (failConn) SetWriteDeadline(time.Time) error { return nil }

func TestDialerFallsBackToIPv6Loopback(t *testing.T) {
	echo := tcpServer(t, "")
	var hosts []string
	var mu sync.Mutex
	b := &Backend{Dial: func(ctx context.Context, sb, host string, port uint16) (net.Conn, error) {
		mu.Lock()
		hosts = append(hosts, host)
		mu.Unlock()
		if host == "127.0.0.1" {
			return refusedConn{}, nil
		}
		return net.Dial("tcp", echo)
	}}
	c, err := b.DialContext(context.Background(), Target{"dev", 4000})
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("abc"))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "abc" {
		t.Fatalf("%q %v", buf, err)
	}
	c.Close()
	// The next dial goes to ::1 first.
	c, _ = b.DialContext(context.Background(), Target{"dev", 4000})
	c.Close()
	if strings.Join(hosts, ",") != "127.0.0.1,::1,::1" {
		t.Fatalf("dialed %v", hosts)
	}
	// ::1 unusable (no IPv6) after a refusal: still nothing listening.
	b.Dial = func(ctx context.Context, sb, host string, port uint16) (net.Conn, error) {
		if host == "::1" {
			return failConn{errors.New("port-forward connect to [::1]:4002: Address not available")}, nil
		}
		return refusedConn{}, nil
	}
	c, _ = b.DialContext(context.Background(), Target{"dev", 4002})
	if _, err := c.Read(buf); !errors.Is(err, ErrRefused) {
		t.Fatalf("::1 unusable: %v", err)
	}
	c.Close()
	// Neither: refused.
	b.Dial = func(context.Context, string, string, uint16) (net.Conn, error) { return refusedConn{}, nil }
	c, _ = b.DialContext(context.Background(), Target{"dev", 4001})
	if _, err := c.Read(buf); !errors.Is(err, ErrRefused) {
		t.Fatalf("%v", err)
	}
	c.Close()
	if b.InUse("dev") != 0 {
		t.Fatal("places leaked")
	}
}

func TestDialerCap(t *testing.T) {
	echo := tcpServer(t, "")
	running := true
	b := &Backend{Dial: func(ctx context.Context, sb, host string, port uint16) (net.Conn, error) {
		return net.Dial("tcp", echo)
	},
		Running: func(string) bool { return running }, Max: 2, Wait: 50 * time.Millisecond}
	ctx := context.Background()
	c1, _ := b.DialContext(ctx, Target{"dev", 1})
	c2, _ := b.DialContext(ctx, Target{"dev", 2})
	if _, err := b.DialContext(ctx, Target{"dev", 3}); !errors.Is(err, ErrBusy) {
		t.Fatalf("third: %v", err)
	}
	// Another sandbox has its own places.
	other, err := b.DialContext(ctx, Target{"web", 1})
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	c1.Close()
	c1.Close() // twice releases once
	c3, err := b.DialContext(ctx, Target{"dev", 3})
	if err != nil {
		t.Fatal(err)
	}
	if b.InUse("dev") != 2 {
		t.Fatal(b.InUse("dev"))
	}
	c2.Close()
	c3.Close()
	running = false
	if _, err := b.DialContext(ctx, Target{"dev", 1}); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if DefaultMaxConns != 14 {
		t.Fatal("the cap leaves 6 of OpenShell's 20 to the terminal and files")
	}
}

func TestBusyAnswers503(t *testing.T) {
	hold := make(chan struct{})
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-hold
		}
		io.WriteString(w, "ok")
	}))
	f := &fakeSandbox{ports: map[uint16]string{8000: addr}, running: true}
	core := newCore(f, routes{})
	in := ingressFor(t, core, 8000, owner, nil)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); get(t, in, 8000, "/slow", nil) }()
	}
	for core.Dialer.(*Backend).InUse("dev") < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	res, _ := get(t, in, 8000, "/", nil)
	close(hold)
	wg.Wait()
	if res.StatusCode != 503 || res.Header.Get("Retry-After") != "5" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
}

// Idle keep-alive connections to the sandbox hold places nobody uses: a new
// connection takes theirs instead of waiting for the limit.
func TestIdleConnectionsGivePlacesUp(t *testing.T) {
	hold := make(chan struct{})
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold
		io.WriteString(w, "ok")
	}))
	f := &fakeSandbox{ports: map[uint16]string{8000: addr, 5432: tcpServer(t, "HELLO\n")}, running: true}
	core := newCore(f, routes{})
	b := core.Dialer.(*Backend)
	b.Wait = 5 * time.Second
	in := ingressFor(t, core, 8000, owner, nil)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); get(t, in, 8000, "/", nil) }()
	}
	for b.InUse("dev") < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	close(hold)
	wg.Wait() // the 3 connections are kept, idle
	start := time.Now()
	c, err := b.DialContext(context.Background(), Target{"dev", 5432})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v", d)
	}
	if line, _ := bufio.NewReader(c).ReadString('\n'); line != "HELLO\n" {
		t.Fatalf("%q", line)
	}
}

func TestParseProcNetTCP(t *testing.T) {
	text := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1
   1: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1
   2: 3500007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1
   3: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 4 1
   4: 0A00020F:1F41 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 5 1
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:0FA0 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 6 1
   1: 00000000000000000000000000000000:1435 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 7 1
   2: 0000000000000000FFFF00000100007F:2328 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 8 1
`
	want := "3000[0.0.0.0] 4000[::1]v6 5173[::] 8080[127.0.0.1] 9000[127.0.0.1]"
	var s []string
	for _, p := range ParseProcNetTCP(text) {
		v := ""
		if p.IPv6Only {
			v = "v6"
		}
		s = append(s, fmt.Sprintf("%d%v%s", p.Port, p.Addresses, v))
	}
	if strings.Join(s, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(s, " "), want)
	}
}

// halfCloseLost is a relay to the sandbox that does not pass a half-close
// on: the sandbox's server sees the end only when the connection closes.
type halfCloseLost struct{ net.Conn }

func (halfCloseLost) CloseWrite() error { return nil }

// A WebSocket the client closed gives its place back at once, even when
// the relay loses the half-close the proxy sends the sandbox's server (as
// TestOpenShellIngressE2E: many requests, a WebSocket, then raw TCP).
func TestClosedWebSocketGivesItsPlaceBack(t *testing.T) {
	hold := make(chan struct{})
	addr := serveBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				mt, p, err := c.ReadMessage()
				if err != nil {
					return
				}
				c.WriteMessage(mt, p)
			}
		}
		if r.URL.Path == "/slow" {
			<-hold
		}
		io.WriteString(w, "ok")
	}))
	f := &fakeSandbox{ports: map[uint16]string{18080: addr, 18081: addr, 18082: tcpServer(t, "HELLO\n")}, running: true}
	// The e2e's Backend: 14 places, no OnBusy (idle connections keep theirs).
	b := &Backend{Max: 14, Wait: time.Second, Dial: func(ctx context.Context, sandbox, host string, port uint16) (net.Conn, error) {
		c, err := f.dial(ctx, sandbox, host, port)
		if err != nil {
			return nil, err
		}
		return halfCloseLost{c}, nil
	}}
	core := &Server{Routes: routes{}, Dialer: b, Auth: ownerAuth(), Logf: t.Logf, DetectWait: 100 * time.Millisecond}
	get(t, ingressFor(t, core, 18081, owner, nil), 18081, "/", nil)
	// The keep-alive pool fills up: 12 to 18080, 1 to 18081, all idle.
	in := ingressFor(t, core, 18080, owner, nil)
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() { defer wg.Done(); get(t, in, 18080, "/slow", nil) }()
	}
	for b.InUse("dev") < 13 {
		time.Sleep(5 * time.Millisecond)
	}
	close(hold)
	wg.Wait()
	d := websocket.Dialer{NetDial: func(string, string) (net.Conn, error) { return net.Dial("tcp", in) }}
	ws, _, err := d.Dial("ws://iwato-dev.tail0000.ts.net:18080/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte("hmr"))
	if _, p, err := ws.ReadMessage(); err != nil || string(p) != "hmr" {
		t.Fatalf("websocket: %q %v", p, err)
	}
	if n := b.InUse("dev"); n != 14 {
		t.Fatalf("%d places in use, want 14", n)
	}
	ws.Close()
	c, err := net.Dial("tcp", ingressFor(t, core, 18082, owner, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || line != "HELLO\n" {
		t.Fatalf("TCP greeting %q %v (%d places in use)", line, err, b.InUse("dev"))
	}
}
