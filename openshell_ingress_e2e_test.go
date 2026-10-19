package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/gorilla/websocket"
	"google.golang.org/grpc"

	"iwato/internal/hostterm"
	"iwato/internal/ingress"
	"iwato/internal/openshell/client"
)

// TestOpenShellIngressE2E reaches servers inside a sandbox through Iwato's
// ingress core and the gateway's ForwardTcp (no Tailscale: a local
// listener stands in for the provider), against the gateway that
// internal/openshell/wslc/e2e.sh runs (MODE=tls). It also tells whether the
// gateway relays to ::1.
func TestOpenShellIngressE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := enablePolicyAdvisor(ctx, sdk); err != nil {
		t.Fatal("policy advisor:", err)
	}
	w := newSandbox("iwato-ingress")
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	// These servers run in this test process, on the sandbox's loopback:
	// the fake wslc runs sandboxes as processes of this host, so the
	// sandbox's 127.0.0.1 is this host's, and the supervisor's relay
	// connects there as on WSLc. Servers the workload runs itself come
	// later (insideServer).
	serve := func(network, addr string, handle func(net.Conn)) bool {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return false
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go handle(c)
			}
		}()
		return true
	}
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			u := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "http://localhost:18080" }}
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
		}
		fmt.Fprintf(w, "INGRESS-OK host=%s\n", r.Host)
	})
	httpOn := func(network, addr string) bool {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return false
		}
		srv := &http.Server{Handler: files}
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
		return true
	}
	if !httpOn("tcp4", "127.0.0.1:18080") {
		t.Fatal("port 18080 is in use")
	}
	// Node's localhost may be ::1 only. Without IPv6 on this host (lo's
	// IPv6 off in a container) that part is reported as not verified.
	ipv6 := httpOn("tcp6", "[::1]:18081")
	if !ipv6 {
		t.Logf("::1: not verified here (no IPv6 loopback on this host)")
	}
	// A server that speaks first (as MySQL), then answers in capitals.
	serve("tcp4", "127.0.0.1:18082", func(c net.Conn) {
		defer c.Close()
		c.Write([]byte("HELLO\n"))
		b := make([]byte, 100)
		n, _ := c.Read(b)
		c.Write([]byte(strings.ToUpper(string(b[:n]))))
	})

	// The ports the ports tab lists, read with an exec. The sweep in
	// ingress.ListenScript leaves these alone: OpenShell's broker only
	// closes its own copies of sockets, and these servers run in this test
	// process, outside the sandbox.
	var ports []ingress.ListenPort
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if ports, err = o.ListeningPorts(ctx, w); err != nil {
			t.Fatal("ports:", err)
		}
		if has(ports, 18080, false) && (!ipv6 || has(ports, 18081, true)) && has(ports, 18082, false) {
			break
		}
	}
	if !has(ports, 18080, false) || (ipv6 && !has(ports, 18081, true)) || !has(ports, 18082, false) {
		t.Fatalf("listening ports: %+v", ports)
	}

	backend := &ingress.Backend{Dial: o.DialPort, Classify: classifyForward}
	core := &ingress.Server{
		Routes: allRoutes{}, Dialer: backend, Logf: t.Logf,
		Auth: ingress.ProviderAuth{Owners: func() []string { return []string{"owner@github"} }},
	}
	// As Iwato does: idle keep-alive connections give their places up to new ones.
	backend.OnBusy = core.CloseIdle
	listen := func(port uint16) string {
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
				go core.Serve(ingress.Conn{Conn: c, Target: &ingress.Target{Sandbox: w.Name, Port: port}, Identity: &ingress.Identity{Provider: "tailscale", Login: "owner@github", Node: "phone"}, PublicHost: "iwato-ingress.tail0000.ts.net"})
			}
		}()
		return ln.Addr().String()
	}
	get := func(t testing.TB, port uint16, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+listen(port)+path, nil)
		req.Host = fmt.Sprintf("iwato-ingress.tail0000.ts.net:%d", port)
		res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	if code, body := get(t, 18080, "/ingress-e2e.txt"); code != 200 || body != "INGRESS-OK host=localhost:18080\n" {
		t.Fatalf("127.0.0.1:18080: %d %q", code, body)
	}
	// Node's localhost may be ::1 only: the gateway relays there too.
	if ipv6 {
		code, body := get(t, 18081, "/ingress-e2e.txt")
		t.Logf("::1: the gateway relays to a server on ::1 only: %v (HTTP %d)", code == 200, code)
		if code != 200 || body != "INGRESS-OK host=localhost:18081\n" {
			t.Fatalf("[::1]:18081: %d %q", code, body)
		}
	}
	if code, body := get(t, 18089, "/"); code != 502 || !strings.Contains(body, "待ち受けているサーバーがありません") {
		t.Fatalf("nothing on 18089: %d %q", code, body)
	}
	// Many requests at once share the sandbox's places (never more than 14).
	in := listen(18080)
	var wg sync.WaitGroup
	errs := make(chan error, 30)
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Get("http://" + in + "/ingress-e2e.txt")
			if err != nil {
				errs <- err
				return
			}
			io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 {
				errs <- fmt.Errorf("HTTP %d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal("parallel requests:", err)
	}
	// A WebSocket (HMR) through the relay, its Origin rewritten.
	d := websocket.Dialer{NetDial: func(string, string) (net.Conn, error) { return net.Dial("tcp", listen(18080)) }}
	ws, _, err := d.Dial("ws://iwato-ingress.tail0000.ts.net:18080/ws", http.Header{"Origin": {"http://iwato-ingress.tail0000.ts.net:18080"}})
	if err != nil {
		t.Fatal("websocket:", err)
	}
	ws.WriteMessage(websocket.TextMessage, []byte("hmr"))
	if _, p, err := ws.ReadMessage(); err != nil || string(p) != "echo:hmr" {
		t.Fatalf("websocket: %q %v", p, err)
	}
	ws.Close()
	// Raw TCP, where the server speaks first.
	tc, err := net.Dial("tcp", listen(18082))
	if err != nil {
		t.Fatal(err)
	}
	tc.SetDeadline(time.Now().Add(20 * time.Second))
	greeting := make([]byte, 6)
	if _, err := io.ReadFull(tc, greeting); err != nil || string(greeting) != "HELLO\n" {
		t.Fatalf("TCP greeting %q %v", greeting, err)
	}
	tc.Write([]byte("psql"))
	reply, _ := io.ReadAll(tc)
	tc.Close()
	if string(reply) != "PSQL" {
		t.Fatalf("TCP reply %q", reply)
	}
	// Servers the workload itself runs, started by another exec than the
	// one that lists the ports (as a terminal's), in two shapes:
	//   - held: a child of a still-running exec. OpenShell's network broker
	//     (inside openshell-sandbox) is its ancestor, so it can read its
	//     memory to check the bind under Yama's ptrace_scope 1.
	//   - detached: in its own session, its exec gone (a server started from
	//     a terminal that outlives it). On WSLc it is reparented to the
	//     sandbox, PID 1 there. With the fake wslc the sandbox is not PID 1
	//     and it is reparented out of the sandbox's tree, so the broker can
	//     read it only under ptrace_scope 0 (e2e.yml sets it); otherwise its
	//     bind fails with EACCES and the case is skipped.
	// Each must be listed by ListeningPorts, answer through the core, and,
	// once it exits, be dropped from the list (v0.1.4 drops it by itself,
	// NVIDIA/OpenShell#4444; for v0.1.2 the sweep in ListenScript closes the
	// broker's stale copy, NVIDIA/OpenShell#4331) so that the
	// port reads as nothing listening (502).
	for _, mode := range []string{"held", "detached"} {
		t.Run(mode, func(t *testing.T) { insideServer(ctx, t, o, conn, w, mode, get) })
	}
	// Stopped: the gateway's refusal reads as stopped (503 停止中).
	if err := o.Stop(ctx, w); err != nil {
		t.Fatal("stop:", err)
	}
	if code, body := get(t, 18080, "/"); code != 503 || !strings.Contains(body, "停止中") {
		t.Fatalf("stopped: %d %q", code, body)
	}
	// The refusal may come with the dial or on the first read.
	if fc, derr := o.DialPort(ctx, w.Name, "127.0.0.1", 18080); derr != nil {
		err = derr
	} else {
		fc.SetReadDeadline(time.Now().Add(20 * time.Second))
		_, err = fc.Read(make([]byte, 1))
		fc.Close()
	}
	if err == nil || !errors.Is(classifyForward(err), ingress.ErrStopped) {
		t.Fatalf("dial a stopped sandbox: %v", err)
	}
	o.ForgetPorts(w.Name)
}

// insideServerScript (python3 -I -c SCRIPT PORT held|detached) serves HTTP
// on 127.0.0.1:PORT inside the sandbox: "/" answers INSIDE-OK, "/exit"
// answers BYE and exits. It prints one line, LISTENING or BIND-FAILED.
// detached forks a child in its own session that reports through a pipe
// and lets go of the exec's output, and the exec ends. Either way it exits
// after 5 minutes at the latest.
const insideServerScript = `import http.server, os, socketserver, sys, threading
port, mode = int(sys.argv[1]), sys.argv[2]
if mode == "detached":
    r, w = os.pipe()
    if os.fork():
        os.close(w)
        print(os.read(r, 200).decode().strip() or "NO-REPORT", flush=True)
        sys.exit(0)
    os.close(r)
    os.setsid()
    null = os.open(os.devnull, os.O_RDWR)
    for fd in (0, 1, 2):
        os.dup2(null, fd)
    def report(msg):
        os.write(w, msg.encode())
        os.close(w)
else:
    def report(msg):
        print(msg, flush=True)
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"BYE\n" if self.path == "/exit" else b"INSIDE-OK\n"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        if self.path == "/exit":
            threading.Thread(target=self.server.shutdown).start()
    def log_message(self, *args):
        pass
class Server(http.server.HTTPServer):
    def server_bind(self):
        # Without HTTPServer's getfqdn (a DNS lookup).
        socketserver.TCPServer.server_bind(self)
        self.server_name, self.server_port = "localhost", port
try:
    server = Server(("127.0.0.1", port), Handler)
except OSError as e:
    report("BIND-FAILED errno=%s %s" % (e.errno, e.strerror))
    sys.exit(0)
report("LISTENING pid=%d" % os.getpid())
timer = threading.Timer(300, os._exit, (0,))
timer.daemon = True
timer.start()
server.serve_forever()
server.server_close()
`

// insideServer starts insideServerScript in the sandbox through an exec of
// its own (mode held or detached) and checks that Iwato's port listing
// shows it while it runs and drops it after it exits.
func insideServer(ctx context.Context, t *testing.T, o *OpenShell, conn *grpc.ClientConn, w Sandbox, mode string, get func(testing.TB, uint16, string) (int, string)) {
	// A port free on this host (the fake's sandboxes share its network).
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"python3", "-I", "-c", insideServerScript, strconv.Itoa(int(port)), mode}, client.Options{})
	if err != nil {
		t.Fatal("exec:", err)
	}
	defer p.Close()
	_ = p.CloseWrite()
	exited := false
	// After a failure: end the server (the fake shares this host's
	// loopback), so that a detached one does not wait for its timer.
	defer func() {
		if !exited {
			if res, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/exit", port)); err == nil {
				res.Body.Close()
			}
		}
	}()
	lines := make(chan string, 1)
	out := bufio.NewReader(p)
	go func() {
		line, err := out.ReadString('\n')
		if err != nil && line == "" {
			line = "no output: " + err.Error()
		}
		lines <- strings.TrimSpace(line)
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(60 * time.Second):
		t.Fatal("the server in the sandbox did not report within 60 s")
	}
	t.Logf("inside the sandbox (%s), 127.0.0.1:%d: %s", mode, port, line)
	if strings.HasPrefix(line, "BIND-FAILED") {
		exited = true
		// Only the one known limit of the fake skips: a detached server
		// refused (EACCES) because Yama keeps the broker from reading a
		// process outside its tree (the fake's sandboxes share this
		// host's kernel). CI (IWATO_E2E_REQUIRE_INSIDE=1) sets
		// ptrace_scope to 0 and allows no skip at all.
		scope, _ := os.ReadFile("/proc/sys/kernel/yama/ptrace_scope")
		yama := strings.TrimSpace(string(scope))
		if mode != "detached" || !strings.HasPrefix(line, "BIND-FAILED errno=13 ") || yama == "0" || os.Getenv("IWATO_E2E_REQUIRE_INSIDE") != "" {
			t.Fatalf("a %s server in the sandbox cannot bind (%s; kernel.yama.ptrace_scope=%q). The sandbox's log has \"network notification denied\" "+
				"if OpenShell's network broker could not read the process's memory", mode, line, yama)
		}
		t.Skipf("NOT VERIFIED: a detached server in the sandbox cannot bind here (%s). OpenShell's network broker (openshell-sandbox) reads the process's memory "+
			"to check the bind (process_vm_readv, then /proc/PID/mem), and with kernel.yama.ptrace_scope=%s only its descendants: the fake wslc does not run the "+
			"sandbox as PID 1, so a detached process leaves its tree. Set ptrace_scope to 0 to run it (e2e.yml does)", line, yama)
	}
	if !strings.HasPrefix(line, "LISTENING") {
		t.Fatalf("the server in the sandbox: %q", line)
	}

	var ports []ingress.ListenPort
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if ports, err = o.ListeningPorts(ctx, w); err != nil {
			t.Fatal("ports:", err)
		}
		if has(ports, port, false) {
			break
		}
	}
	if !has(ports, port, false) {
		t.Fatalf("a server another exec started in the sandbox is not listed: port %d, listed %+v", port, ports)
	}
	if code, body := get(t, port, "/"); code != 200 || body != "INSIDE-OK\n" {
		t.Fatalf("through the core: %d %q", code, body)
	}
	if code, body := get(t, port, "/exit"); code != 200 || body != "BYE\n" {
		t.Fatalf("/exit: %d %q", code, body)
	}
	exited = true
	if mode == "held" {
		// Its exec ends with it.
		done := make(chan struct{})
		go func() { io.Copy(io.Discard, out); p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the server's exec did not end after /exit")
		}
		// Before any sweep: whether the broker's copy still listens.
		if raw, err := insideOutput(ctx, conn, w, "cat /proc/net/tcp /proc/net/tcp6"); err == nil {
			t.Logf("NVIDIA/OpenShell#4331: the exited server's port still in LISTEN before the sweep: %v", has(ingress.ParseProcNetTCP(raw), port, false))
		}
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if ports, err = o.ListeningPorts(ctx, w); err != nil {
			t.Fatal("ports:", err)
		}
		if !has(ports, port, false) {
			break
		}
	}
	if has(ports, port, false) {
		t.Fatalf("port %d is still listed 15 s after its server exited (the sweep did not close OpenShell's copy?): %+v", port, ports)
	}
	if code, body := get(t, port, "/"); code != 502 || !strings.Contains(body, "待ち受けているサーバーがありません") {
		t.Fatalf("after the server exited: %d %q", code, body)
	}
}

// insideOutput runs sh -c script in the sandbox and returns its output.
func insideOutput(ctx context.Context, conn *grpc.ClientConn, w Sandbox, script string) (string, error) {
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"sh", "-c", script}, client.Options{})
	if err != nil {
		return "", err
	}
	defer p.Close()
	_ = p.CloseWrite()
	b, err := io.ReadAll(io.LimitReader(p, 4<<20))
	_, _ = p.Wait()
	if len(b) == 0 && err != nil {
		return "", err
	}
	return string(b), nil
}

// allRoutes exposes every port.
type allRoutes struct{}

func (allRoutes) Options(ingress.Target) (ingress.PortOptions, bool) {
	return ingress.PortOptions{}, true
}

func has(ports []ingress.ListenPort, port uint16, v6only bool) bool {
	return slices.ContainsFunc(ports, func(p ingress.ListenPort) bool { return p.Port == port && p.IPv6Only == v6only })
}
