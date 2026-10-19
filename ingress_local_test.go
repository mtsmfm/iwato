//go:build uitest

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"iwato/internal/ingress"
	"iwato/internal/ingress/local"
)

// TestLocalIngressHostRouting runs Iwato's listener as main does: the
// local ingress's hosts go to the sandbox, everything else to the admin
// UI with its checks.
func TestLocalIngressHostRouting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				c.WriteMessage(mt, append([]byte(r.Header.Get("Origin")+" "), p...))
			}
		}
		fmt.Fprintf(w, "sandbox %s host=%s origin=%s referer=%s cookie=%s", r.URL.Path, r.Host, r.Header.Get("Origin"), r.Header.Get("Referer"), r.Header.Get("Cookie"))
	}))
	defer backend.Close()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig()
	c.Sandboxes = append(c.Sandboxes, newSandbox("ab-"))
	c.Sandboxes[0].Ingress = &SandboxIngress{Ports: map[string]PortIngress{"5432": {Mode: "tcp"}}}
	s := newServer(c, NewFake(), false, ln.Addr().String())
	_, port, _ := net.SplitHostPort(s.host)
	m := s.ingress
	m.backend.Dial = func(ctx context.Context, sandbox, host string, p uint16) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", strings.TrimPrefix(backend.URL, "http://"))
	}
	m.reconfigure(context.Background())
	m.mu.Lock()
	m.running["dev"] = true // the fake's sandboxes are stopped
	m.mu.Unlock()
	h := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go h.Serve(local.Split(ln, m.take, h.ReadHeaderTimeout, 32<<10))
	defer h.Close()

	pub := "3000.dev.localhost:" + port
	// send writes raw requests on one connection and reads the responses.
	send := func(reqs ...string) []string {
		t.Helper()
		conn, err := net.Dial("tcp", s.host)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		var out []string
		for _, r := range reqs {
			io.WriteString(conn, r)
			res, err := http.ReadResponse(br, nil)
			if err != nil {
				out = append(out, "error "+err.Error())
				return out
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			out = append(out, fmt.Sprintf("%d %s", res.StatusCode, b))
		}
		return out
	}
	get := func(host, path string, headers ...string) string {
		return "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\n" + strings.Join(append(headers, ""), "\r\n") + "\r\n"
	}

	// The sandbox's port, with the dev server's Host; the page's own
	// Origin and Referer rewritten, another site's passed on; the admin
	// cookie removed; the admin API's path is the sandbox's.
	got := send(get(pub, "/api/sandboxes", "Origin: http://"+pub, "Referer: http://"+pub+"/app", "Cookie: "+s.cookieName+"=secret; a=b"),
		get(strings.ToUpper(pub), "/x", "Origin: http://evil.example", "Referer: http://evil.example/"))
	if want := "200 sandbox /api/sandboxes host=localhost:3000 origin=http://localhost:3000 referer=http://localhost:3000/app cookie=a=b"; got[0] != want {
		t.Errorf("published host:\n got %s\nwant %s", got[0], want)
	}
	if want := "200 sandbox /x host=localhost:3000 origin=http://evil.example referer=http://evil.example/ cookie="; got[1] != want {
		t.Errorf("another origin:\n got %s\nwant %s", got[1], want)
	}
	// The admin UI stays on 127.0.0.1 and is not served on another host
	// of the same connection.
	if got := send(get(s.host, "/api/sandboxes")); !strings.HasPrefix(got[0], "401") {
		t.Errorf("admin: %s", got[0])
	}
	if got := send(get(pub, "/"), get(s.host, "/api/sandboxes")); !strings.HasPrefix(got[1], "421") {
		t.Errorf("admin host after a published one: %v", got)
	}
	if got := send(get(s.host, "/api/sandboxes"), get(pub, "/")); !strings.HasPrefix(got[1], "403") || strings.Contains(got[1], "sandbox") {
		t.Errorf("published host after the admin's: %v", got)
	}
	// Anything else is the admin UI's: its Host check refuses it.
	for _, host := range []string{
		"3000.nope.localhost:" + port, "3000.dev.localhost:1", "3000.dev.localhost:", "0.dev.localhost:" + port, "65536.dev.localhost:" + port,
		"03000.dev.localhost:" + port, "+3000.dev.localhost:" + port, "3000.dev.localhost.:" + port, "a.3000.dev.localhost:" + port,
		"dev.localhost:" + port, "3000.ab-.localhost:" + port, "localhost:" + port, "3000.dev.localhost.evil.example:" + port, "3000.dev.evil:" + port,
	} {
		if got := send(get(host, "/")); !strings.HasPrefix(got[0], "403") || strings.Contains(got[0], "sandbox") {
			t.Errorf("%s: %s", host, got[0])
		}
	}
	// A proxy in front (tailscale serve) is not this PC's browser.
	if got := send(get(pub, "/", "X-Forwarded-For: 100.64.0.9")); !strings.HasPrefix(got[0], "403") || strings.Contains(got[0], "sandbox") {
		t.Errorf("through a proxy: %s", got[0])
	}
	if got := send(get(pub, "/"), get(pub, "/", "Tailscale-User-Login: x@github")); !strings.HasPrefix(got[1], "403") {
		t.Errorf("a proxied request on the connection: %v", got)
	}
	// TCP mode cannot go through Host routing; a port nothing listens on.
	if got := send(get("5432.dev.localhost:"+port, "/")); !strings.HasPrefix(got[0], "501") || !strings.Contains(got[0], "TCP") {
		t.Errorf("tcp: %s", got[0])
	}
	if got := send(get("9999.dev.localhost:"+port, "/")); !strings.HasPrefix(got[0], "502") {
		t.Errorf("not listening: %s", got[0])
	}
	// WebSocket (Vite's HMR) with the page's Origin.
	d := websocket.Dialer{NetDial: func(string, string) (net.Conn, error) { return net.Dial("tcp", s.host) }}
	ws, _, err := d.Dial("ws://"+pub+"/ws", http.Header{"Origin": {"http://" + pub}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.WriteMessage(websocket.TextMessage, []byte("hmr"))
	if _, p, err := ws.ReadMessage(); err != nil || string(p) != "http://localhost:3000 hmr" {
		t.Errorf("websocket: %q %v", p, err)
	}
	// Turning publishing off ends the connections being served.
	keep, err := net.Dial("tcp", s.host)
	if err != nil {
		t.Fatal(err)
	}
	defer keep.Close()
	io.WriteString(keep, get(pub, "/"))
	br := bufio.NewReader(keep)
	if res, err := http.ReadResponse(br, nil); err != nil || res.StatusCode != 200 {
		t.Fatal(res, err)
	} else {
		io.Copy(io.Discard, res.Body)
	}
	// The ports tab's links (a name that is not a DNS label has none).
	u, _ := m.provider.URL(ingress.Target{Sandbox: "dev", Port: 3000})
	if _, ok := m.provider.URL(ingress.Target{Sandbox: "ab-", Port: 3000}); ok {
		t.Error("a name that is not a DNS label got a URL")
	}
	m.provider.Close()
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Errorf("the WebSocket stays open: %v", err)
	}
	keep.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil || strings.Contains(err.Error(), "timeout") {
		t.Errorf("the keep-alive connection stays open: %v", err)
	}
	if u != "http://3000.dev.localhost:"+port+"/" {
		t.Error(u)
	}
}
