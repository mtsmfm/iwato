//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"iwato/internal/termsync"
	"iwato/internal/termsync/termsynctest"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	c := testConfig()
	s := newServer(c, NewFake(), true, "127.0.0.1:1")
	h := httptest.NewServer(s.Handler())
	u, _ := url.Parse(h.URL)
	s.host = u.Host
	s.origin = h.URL
	t.Cleanup(h.Close)
	return s, h
}

func TestFakeNeverExecutes(t *testing.T) {
	d := NewFake()
	w := Sandbox{Name: "fake"}
	if e := d.Start(context.Background(), w); e != nil {
		t.Fatal(e)
	}
	term, e := d.Attach(context.Background(), w, TermRef{Name: mainTerm}, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	term.Close()
	term.Close()
}

func TestFakeTerminals(t *testing.T) {
	d := NewFake()
	w := Sandbox{Name: "fake"}
	ctx := context.Background()
	_ = d.Start(ctx, w)
	if _, e := d.Attach(ctx, w, TermRef{Name: "2"}, 80, 24); e == nil {
		t.Fatal("a session that never ran was started without new")
	}
	main, _ := d.Attach(ctx, w, TermRef{Name: mainTerm}, 80, 24)
	two, e := d.Attach(ctx, w, TermRef{Name: "2", New: true}, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = d.Attach(ctx, w, TermRef{Name: "10", New: true}, 80, 24)
	if names, outdated, multi, _ := d.Terminals(ctx, w); !multi || strings.Join(names, ",") != "main,2,10" || strings.Join(outdated, ",") != "2" {
		t.Fatal(names)
	}
	if _, e := termsynctest.Start(main, 80, 24); e != nil {
		t.Fatal(e)
	}
	viewer, e := termsynctest.Start(two, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	_ = d.KillTerminal(ctx, w, "2")
	if viewer.Ended(2*time.Second) != nil {
		t.Fatal("a viewer of the killed session stayed attached")
	}
	if names, _, _, _ := d.Terminals(ctx, w); strings.Join(names, ",") != "main,10" {
		t.Fatal(names)
	}
	_ = d.Stop(ctx, w)
	_ = d.Start(ctx, w)
	if names, _, _, _ := d.Terminals(ctx, w); strings.Join(names, ",") != mainTerm {
		t.Fatal("sessions survived a stop", names)
	}
}

func TestFontsAreCachedOnlyWhenServed(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	w := reviewRequest(s, "GET", "/vendor/fonts/missing.ttf", "", "", "", s.host, nil, "")
	if w.Code != 404 || strings.Contains(w.Header().Get("Cache-Control"), "max-age") {
		t.Fatal("missing font cached", w.Code, w.Header().Get("Cache-Control"))
	}
	const font = "/vendor/fonts/moralerspace-neon-hw-2.0.0-regular.ttf"
	if _, err := fs.Stat(webFiles, "web"+font); err != nil {
		t.Skip("fonts not restored (scripts/bootstrap-build.ps1)")
	}
	w = reviewRequest(s, "GET", font, "", "", "", s.host, nil, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatal("font not cached", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestListShowsTheServerBuild(t *testing.T) {
	old := buildCommit
	buildCommit, buildTime, buildRelease = "98cb328", "2026-10-04T03:04:00Z", "1"
	defer func() { buildCommit, buildTime, buildRelease = old, "", "" }()
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	cookie, _ := reviewLogin(t, s)
	w := reviewRequest(s, "GET", "/api/sandboxes", "", s.origin, "same-origin", s.host, cookie, "")
	var v struct{ Server BuildInfo }
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Server != (BuildInfo{Version: version, Commit: "98cb328", Built: "2026-10-04T03:04:00Z"}) {
		t.Fatal(w.Body.String())
	}
}

func TestDebugOptionsOnlyInDebugBuilds(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	if w := reviewRequest(s, "GET", "/debug.js", "", "", "", s.host, nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "IwatoDebug = true") {
		t.Fatal("debug build", w.Code, w.Body.String())
	}
	buildRelease = "1"
	defer func() { buildRelease = "" }()
	if w := reviewRequest(s, "GET", "/debug.js", "", "", "", s.host, nil, ""); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("release build", w.Code, w.Body.String())
	}
}

func TestAuthCSRFOriginAndHost(t *testing.T) {
	s, _ := newTestServer(t)
	if r, _ := request(t, s, "/api/sandboxes", "GET", "", nil, ""); r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
	bootstrap := s.bootstrap
	c, csrf := login(t, s)
	if r, _ := request(t, s, "/api/session", "POST", `{"token":"`+bootstrap+`"}`, nil, ""); r.StatusCode != 401 {
		t.Fatal("reused bootstrap")
	}
	if r, _ := request(t, s, "/api/sandboxes/dev/start", "POST", "", c, ""); r.StatusCode != 403 {
		t.Fatal("csrf missing accepted")
	}
	if r, _ := request(t, s, "/api/sandboxes/dev/start", "POST", "", c, csrf); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.Host = "evil.example" }, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }} {
		r, _ := http.NewRequest("GET", s.origin+"/api/sandboxes", nil)
		r.AddCookie(c)
		change(r)
		res, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal("crosssite accepted", res.StatusCode)
		}
	}
	if r, _ := request(t, s, "/api/sandboxes/other/start", "POST", "", c, csrf); r.StatusCode != 404 {
		t.Fatal("unknown sandbox accepted")
	}
	if r, _ := request(t, s, "/api/sandboxes/dev/start", "GET", "", c, csrf); r.StatusCode != 405 {
		t.Fatal("mutating GET accepted")
	}
	if r, b := request(t, s, "/api/session", "GET", "", c, ""); r.StatusCode != 200 || !bytes.Contains(b, []byte(csrf)) {
		t.Fatal("reload failed")
	}
}

func TestBootstrapBadTokenAndExpiry(t *testing.T) {
	s, _ := newTestServer(t)
	if r, _ := request(t, s, "/api/session", "POST", `{"token":"bad"}`, nil, ""); r.StatusCode != 401 {
		t.Fatal("bad token")
	}
	s.bootstrapUntil = time.Now().Add(-time.Second)
	if r, _ := request(t, s, "/api/session", "POST", `{"token":"`+s.bootstrap+`"}`, nil, ""); r.StatusCode != 401 {
		t.Fatal("expired accepted")
	}
}

func TestWebSocketFakeReconnectRetainsHistory(t *testing.T) {
	s, _ := newTestServer(t)
	c, csrf := login(t, s)
	request(t, s, "/api/sandboxes/dev/start", "POST", "", c, csrf)
	ws := dial(t, s, c, csrf)
	v := viewWS(t, ws, 80, 24)
	waitText(t, v, "dev $")
	if e := v.Resize(120, 40); e != nil {
		t.Fatal(e)
	}
	if e := v.Input([]byte("echo reconnect-marker\r")); e != nil {
		t.Fatal(e)
	}
	waitText(t, v, "\nreconnect-marker\ndev $")
	ws.Close()
	again := dial(t, s, c, csrf)
	waitText(t, viewWS(t, again, 100, 30), "reconnect-marker")
}

// A ping is answered by iwato itself, also while frames wait for an ack.
func TestWebSocketPingIsAnswered(t *testing.T) {
	s, _ := newTestServer(t)
	c, csrf := login(t, s)
	request(t, s, "/api/sandboxes/dev/start", "POST", "", c, csrf)
	ws := dial(t, s, c, csrf)
	if e := ws.WriteMessage(websocket.BinaryMessage, append([]byte{termsync.KindHello}, termsync.Hello(80, 24)...)); e != nil {
		t.Fatal(e)
	}
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, p, e := ws.ReadMessage(); e != nil || p[0] != termsync.KindFrame {
		t.Fatal("no first frame", e)
	}
	for _, nonce := range []string{"\x07", "\x08"} {
		if e := ws.WriteMessage(websocket.BinaryMessage, []byte{termsync.KindPing, nonce[0]}); e != nil {
			t.Fatal(e)
		}
		for {
			_, p, e := ws.ReadMessage()
			if e != nil {
				t.Fatal("no pong", e)
			}
			if p[0] == termsync.KindPong {
				if string(p[1:]) != nonce {
					t.Fatalf("pong %q", p)
				}
				break
			}
		}
	}
}
