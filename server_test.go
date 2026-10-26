package main

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func request(t *testing.T, s *Server, path, method, body string, cookie *http.Cookie, csrf string) (*http.Response, []byte) {
	t.Helper()
	r, _ := http.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	res, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, b
}
func login(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	res, b := request(t, s, "/api/session", "POST", `{"token":"`+s.bootstrap+`"}`, nil, "")
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	var a map[string]string
	json.Unmarshal(b, &a)
	if len(res.Cookies()) != 1 {
		t.Fatal("missing cookie")
	}
	c := res.Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	return c, a["csrf"]
}

func dial(t *testing.T, s *Server, c *http.Cookie, csrf string) *websocket.Conn {
	t.Helper()
	h := http.Header{"Origin": {s.origin}, "Cookie": {c.String()}}
	ws, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.origin, "http")+"/ws/dev?csrf="+csrf, h)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// Without -listen Iwato serves on 17600, or the next free port after it.
func TestDefaultPortShiftsPastPortsInUse(t *testing.T) {
	first, err := listenLocal("")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := listenLocal("")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, b := first.Addr().(*net.TCPAddr), second.Addr().(*net.TCPAddr)
	if a.Port < defaultPort || b.Port <= a.Port || !a.IP.IsLoopback() || !b.IP.IsLoopback() {
		t.Fatal(a, b)
	}
}
