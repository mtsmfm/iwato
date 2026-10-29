// These tests exercise the HTTP boundary through real requests, rather
// than calling authentication/validation helpers directly.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"iwato/internal/termsync"
)

type reviewRuntime struct {
	mu       sync.Mutex
	attached int
	terms    []*reviewTerminal
}

func (*reviewRuntime) Status(_ context.Context, w Sandbox) Status {
	return Status{Name: w.Name, State: "running"}
}
func (*reviewRuntime) Start(context.Context, Sandbox) error { return nil }
func (r *reviewRuntime) Attach(context.Context, Sandbox, TermRef, int, int) (Terminal, error) {
	p, w := io.Pipe()
	t := &reviewTerminal{PipeReader: p, writer: w, closed: make(chan struct{}), exited: make(chan struct{})}
	r.mu.Lock()
	r.attached++
	r.terms = append(r.terms, t)
	r.mu.Unlock()
	return t, nil
}

type reviewTerminal struct {
	*io.PipeReader
	writer *io.PipeWriter
	closed chan struct{}
	exited chan struct{}
	once   sync.Once
}

func (*reviewTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (*reviewTerminal) Resize(int, int) error       { return nil }
func (t *reviewTerminal) Exited() <-chan struct{}   { return t.exited }
func (t *reviewTerminal) Close() error {
	t.once.Do(func() { close(t.closed); t.PipeReader.Close(); t.writer.Close() })
	return nil
}

type wsFrame struct {
	kind int
	data []byte
}

func reviewRequest(s *Server, method, path, body, origin, site, host string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if site != "" {
		r.Header.Set("Sec-Fetch-Site", site)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func reviewLogin(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	w := reviewRequest(s, "POST", "/api/session", `{"token":"`+s.bootstrap+`"}`, s.origin, "same-origin", s.host, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count: %d", len(cookies))
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response["csrf"]) != 64 {
		t.Fatalf("unexpected CSRF token size")
	}
	return cookies[0], response["csrf"]
}

func TestReviewHTTPAuthenticationBoundary(t *testing.T) {
	s := newServer(testConfig(), &reviewRuntime{}, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 28800 {
		t.Fatalf("weak session cookie: %+v", cookie)
	}
	for _, tc := range []struct {
		name, method, path, body, origin, site, host, csrf string
		cookie                                             *http.Cookie
		want                                               int
	}{
		{"read without cookie", "GET", "/api/sandboxes", "", "", "", s.host, "", nil, 401},
		{"read with cookie", "GET", "/api/sandboxes", "", "", "", s.host, "", cookie, 200},
		{"DNS rebinding host", "GET", "/api/sandboxes", "", "", "", "evil.example", "", cookie, 403},
		{"cross origin read", "GET", "/api/sandboxes", "", "https://evil.example", "", s.host, "", cookie, 403},
		{"same site other port", "GET", "/api/sandboxes", "", "http://127.0.0.1:30000", "", s.host, "", cookie, 403},
		{"fetch metadata cross site", "GET", "/api/sandboxes", "", "", "cross-site", s.host, "", cookie, 403},
		{"missing POST origin", "POST", "/api/sandboxes/dev/start", "", "", "", s.host, csrf, cookie, 403},
		{"missing POST csrf", "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, "", cookie, 403},
		{"wrong POST csrf", "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, "bad", cookie, 403},
		{"authorized start", "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, csrf, cookie, 200},
		{"unknown sandbox", "POST", "/api/sandboxes/other/start", "", s.origin, "same-origin", s.host, csrf, cookie, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := reviewRequest(s, tc.method, tc.path, tc.body, tc.origin, tc.site, tc.host, tc.cookie, tc.csrf)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("missing confidentiality headers")
			}
		})
	}
}

func TestReviewBootstrapSingleUseAndBounded(t *testing.T) {
	s := newServer(testConfig(), &reviewRuntime{}, false, "127.0.0.1:29999")
	token := s.bootstrap
	for _, body := range []string{`{"token":"` + token + `","unexpected":1}`, `{"token":"` + token + `"} {}`, `{"token":"` + strings.Repeat("a", 20<<10) + `"}`} {
		w := reviewRequest(s, "POST", "/api/session", body, s.origin, "same-origin", s.host, nil, "")
		if w.Code != 400 {
			t.Fatalf("invalid payload accepted: %d", w.Code)
		}
	}
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- reviewRequest(s, "POST", "/api/session", `{"token":"`+token+`"}`, s.origin, "same-origin", s.host, nil, "").Code
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 200 {
			success++
		} else if code != 401 {
			t.Fatalf("unexpected response: %d", code)
		}
	}
	if success != 1 {
		t.Fatalf("bootstrap succeeded %d times", success)
	}
	s2 := newServer(testConfig(), &reviewRuntime{}, false, s.host)
	s2.bootstrapUntil = time.Now().Add(-time.Second)
	if code := reviewRequest(s2, "POST", "/api/session", `{"token":"`+s2.bootstrap+`"}`, s2.origin, "same-origin", s2.host, nil, "").Code; code != 401 {
		t.Fatalf("expired bootstrap accepted: %d", code)
	}
}

func TestReviewWebsocketBoundariesAndClose(t *testing.T) {
	rt := &reviewRuntime{}
	ts := httptest.NewUnstartedServer(nil)
	s := newServer(testConfig(), rt, false, ts.Listener.Addr().String())
	ts.Config.Handler = s.Handler()
	ts.Start()
	defer ts.Close()
	cookie, csrf := reviewLogin(t, s)
	dial := func(origin, token string, withCookie bool) (*websocket.Conn, *http.Response, error) {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		if withCookie {
			h.Set("Cookie", cookie.String())
		}
		return websocket.DefaultDialer.Dial("ws://"+s.host+"/ws/dev?csrf="+token, h)
	}
	for _, tc := range []struct {
		origin, token string
		cookie        bool
		want          int
	}{{s.origin, csrf, false, 401}, {"", csrf, true, 403}, {"https://evil.example", csrf, true, 403}, {s.origin, "wrong", true, 403}} {
		conn, response, err := dial(tc.origin, tc.token, tc.cookie)
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || response.StatusCode != tc.want {
			t.Fatalf("unexpected handshake outcome: response=%v err=%v", response, err)
		}
	}
	rt.mu.Lock()
	n := rt.attached
	rt.mu.Unlock()
	if n != 0 {
		t.Fatal("unauthorized request reached backend")
	}
	hello := append([]byte{termsync.KindHello}, termsync.Hello(80, 24)...)
	text := func(s string) wsFrame { return wsFrame{websocket.TextMessage, []byte(s)} }
	bin := func(b ...byte) wsFrame { return wsFrame{websocket.BinaryMessage, b} }
	for _, messages := range [][]wsFrame{
		// The hello comes first, in binary, with a size that makes sense.
		{text(`{"type":"input","data":"x"}`)},
		{bin(append([]byte{termsync.KindHello}, termsync.Hello(0, 30)...)...)},
		{bin(termsync.KindInput, 'x')},
		// After it: only the termsync messages the browser sends.
		{bin(hello...), bin('x', 1)},
		{bin(hello...), bin()},
		{bin(hello...), bin(termsync.KindFrame, 0)},
		{bin(hello...), bin(termsync.KindPing, 1, 2, 3, 4, 5, 6, 7, 8, 9)},
		{bin(hello...), text(`{"type":"unrecognized"}`)},
		{bin(hello...), text(`not json`)},
	} {
		conn, _, err := dial(s.origin, csrf, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if err = conn.WriteMessage(m.kind, m.data); err != nil {
				t.Fatal(err)
			}
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for err == nil {
			_, _, err = conn.ReadMessage()
		}
		conn.Close()
		var timeout net.Error
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) || errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatalf("invalid messages %v did not close the connection: %v", messages, err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		n = s.connections["dev"]
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal slots leaked: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
	rt.mu.Lock()
	terms := append([]*reviewTerminal(nil), rt.terms...)
	rt.mu.Unlock()
	for _, term := range terms {
		select {
		case <-term.closed:
		default:
			t.Fatal("terminal not closed after websocket rejection")
		}
	}
	conn, _, err := dial(s.origin, csrf, true)
	if err != nil {
		t.Fatal(err)
	}
	// A single oversized frame must be rejected before it is relayed.
	if err = conn.WriteMessage(websocket.BinaryMessage, hello); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteMessage(websocket.BinaryMessage, append([]byte{termsync.KindInput}, bytes.Repeat([]byte(" "), termsync.MaxMessage+1)...)); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	conn.Close()
	if err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestReviewLoopbackBindingOnly(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0", "localhost:0", "[::1]:0", "192.0.2.1:0"} {
		if l, err := listenLocal(addr); err == nil {
			l.Close()
			t.Fatalf("unexpected listener for %q", addr)
		}
	}
	l, err := listenLocal("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestReviewWebsocketCapacityAndProcessExit(t *testing.T) {
	rt := &reviewRuntime{}
	ts := httptest.NewUnstartedServer(nil)
	s := newServer(testConfig(), rt, false, ts.Listener.Addr().String())
	ts.Config.Handler = s.Handler()
	ts.Start()
	defer ts.Close()
	cookie, csrf := reviewLogin(t, s)
	h := http.Header{"Origin": {s.origin}, "Cookie": {cookie.String()}}
	url := "ws://" + s.host + "/ws/dev?csrf=" + csrf
	var conns []*websocket.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(url, h)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		// The hello attaches the terminal.
		if err = conn.WriteMessage(websocket.BinaryMessage, append([]byte{termsync.KindHello}, termsync.Hello(80, 24)...)); err != nil {
			t.Fatal(err)
		}
	}
	conn, response, err := websocket.DefaultDialer.Dial(url, h)
	if conn != nil {
		conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != 429 {
		t.Fatalf("fifth connection was not limited: %v %v", response, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var terms []*reviewTerminal
	for terms == nil {
		rt.mu.Lock()
		if len(rt.terms) == 4 {
			terms = append([]*reviewTerminal(nil), rt.terms...)
		}
		rt.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("backend attaches did not complete")
		}
		if terms == nil {
			time.Sleep(time.Millisecond)
		}
	}
	// Model Windows process exit with its output pipe still open. Waiting only
	// on Read EOF would hang indefinitely in this situation.
	// Handshake completion does not order the server's Attach calls. End all
	// four processes instead of assuming a socket-to-attachment index mapping.
	for _, term := range terms {
		close(term.exited)
	}
	for _, conn := range conns {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err = conn.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("natural exit must stop frontend reconnect, got %v", err)
		}
	}
	for _, term := range terms {
		select {
		case <-term.closed:
		case <-time.After(2 * time.Second):
			t.Fatal("exited terminal handle not closed")
		}
	}
	for _, c := range conns {
		c.Close()
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		n := s.connections["dev"]
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection capacity leaked: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
}
