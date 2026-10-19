//go:build uitest

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"iwato/internal/termsync/termsynctest"
)

func TestSessionsSurviveRestartAndStoreOnlyHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	first := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	if err := first.loadSessions(path); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := reviewLogin(t, first)
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), cookie.Value) || !strings.Contains(string(raw), sessionHash(cookie.Value)) {
		t.Fatal("the store must hold the hash, never the cookie value")
	}
	// A new process (other port, new bootstrap) accepts the same cookie.
	second := newServer(testConfig(), NewFake(), true, "127.0.0.1:30000")
	if err := second.loadSessions(path); err != nil {
		t.Fatal(err)
	}
	if second.cookieName != first.cookieName {
		t.Fatal("cookie name must be stable across restarts")
	}
	if c := reviewRequest(second, "GET", "/api/sandboxes", "", "", "", second.host, cookie, "").Code; c != 200 {
		t.Fatal("restored session rejected", c)
	}
	if c := reviewRequest(second, "POST", "/api/sandboxes/dev/start", "", second.origin, "same-origin", second.host, cookie, csrf).Code; c != 200 {
		t.Fatal("restored CSRF token rejected", c)
	}
	// Knowing the stored hash does not help.
	forged := *cookie
	forged.Value = sessionHash(cookie.Value)
	if c := reviewRequest(second, "GET", "/api/sandboxes", "", "", "", second.host, &forged, "").Code; c != 401 {
		t.Fatal("hash accepted as a cookie", c)
	}
}

func TestExpiredSessionsAreDroppedAndBadStoresRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	s.sessionTTL = time.Millisecond
	if err := s.loadSessions(path); err != nil {
		t.Fatal(err)
	}
	cookie, _ := reviewLogin(t, s)
	time.Sleep(5 * time.Millisecond)
	again := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	if err := again.loadSessions(path); err != nil {
		t.Fatal(err)
	}
	if c := reviewRequest(again, "GET", "/api/sandboxes", "", "", "", again.host, cookie, "").Code; c != 401 || len(again.sessions) != 0 {
		t.Fatal("expired session restored", c)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"cookieName":"x"}`), 0o600)
	if err := newServer(testConfig(), NewFake(), true, "127.0.0.1:1").loadSessions(bad); err == nil {
		t.Fatal("malformed store accepted")
	}
	if b, _ := os.ReadFile(bad); string(b) != `{"cookieName":"x"}` {
		t.Fatal("malformed store must not be overwritten")
	}
}

// The settings' logins: the list, revoking another (its terminal closes),
// every other one, and signing out; each POST needs the CSRF token.
func TestSessionsRevokeAndLogout(t *testing.T) {
	s, _ := newTestServer(t)
	signIn := func() (*http.Cookie, string) {
		s.mu.Lock()
		s.bootstrap, s.bootstrapUntil = randomHex(32), time.Now().Add(time.Minute)
		s.mu.Unlock()
		return login(t, s)
	}
	pc, pcCSRF := signIn()
	phone, phoneCSRF := signIn()
	tablet, _ := signIn()
	list := func() []sessionView {
		t.Helper()
		r, b := request(t, s, "/api/sessions", "GET", "", pc, "")
		var v struct{ Sessions []sessionView }
		if r.StatusCode != 200 || json.Unmarshal(b, &v) != nil {
			t.Fatal(r.StatusCode, string(b))
		}
		return v.Sessions
	}
	got := list()
	if len(got) != 3 || !got[0].Current || got[1].Current || got[0].Agent == "" || got[0].Created.IsZero() || got[0].Used.IsZero() {
		t.Fatalf("%+v", got)
	}
	phoneID := sessionID(sessionHash(phone.Value))
	if !slices.ContainsFunc(got, func(v sessionView) bool { return v.ID == phoneID && !v.Current }) {
		t.Fatal("the phone's session is not listed")
	}
	if r, _ := request(t, s, "/api/sessions/"+phoneID+"/revoke", "POST", "{}", pc, ""); r.StatusCode != 403 {
		t.Fatal("revoked without the CSRF token", r.StatusCode)
	}
	if r, _ := request(t, s, "/api/sessions/"+phoneID+"/revoke", "POST", "{}", pc, phoneCSRF); r.StatusCode != 403 {
		t.Fatal("another session's CSRF token accepted", r.StatusCode)
	}

	// The phone's terminal is open; revoking closes it.
	request(t, s, "/api/sandboxes/dev/start", "POST", "", pc, pcCSRF)
	spy := &endSpy{wsTerm: &wsTerm{ws: dial(t, s, phone, phoneCSRF)}}
	v, err := termsynctest.Start(spy, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitText(t, v, "dev $")
	if r, b := request(t, s, "/api/sessions/"+phoneID+"/revoke", "POST", "{}", pc, pcCSRF); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if err := v.Ended(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	spy.mu.Lock()
	end := spy.err
	spy.mu.Unlock()
	if !websocket.IsCloseError(end, 4401) {
		t.Fatal("the terminal ended with", end)
	}
	if r, _ := request(t, s, "/api/sandboxes", "GET", "", phone, ""); r.StatusCode != 401 {
		t.Fatal("the revoked session still works", r.StatusCode)
	}
	h := http.Header{"Origin": {s.origin}, "Cookie": {phone.String()}}
	if _, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.origin, "http")+"/ws/dev?csrf="+phoneCSRF, h); err == nil || res == nil || res.StatusCode != 401 {
		t.Fatal("a revoked session opened a terminal", err)
	}
	if r, _ := request(t, s, "/api/sessions/"+phoneID+"/revoke", "POST", "{}", pc, pcCSRF); r.StatusCode != 404 {
		t.Fatal("revoked twice", r.StatusCode)
	}

	// Every other one: the tablet goes, this one stays.
	r, b := request(t, s, "/api/sessions/revoke-others", "POST", "{}", pc, pcCSRF)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"revoked":1`) {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := request(t, s, "/api/sandboxes", "GET", "", tablet, ""); r.StatusCode != 401 {
		t.Fatal("the other session still works", r.StatusCode)
	}
	if got := list(); len(got) != 1 || !got[0].Current {
		t.Fatalf("%+v", got)
	}

	// Signing out ends this one and clears the cookie.
	r, b = request(t, s, "/api/sessions/logout", "POST", "{}", pc, pcCSRF)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 || r.Cookies()[0].MaxAge >= 0 {
		t.Fatal(r.StatusCode, string(b), r.Cookies())
	}
	if r, _ := request(t, s, "/api/sandboxes", "GET", "", pc, ""); r.StatusCode != 401 || len(s.sessions) != 0 {
		t.Fatal("signed out but the session works", r.StatusCode)
	}
}
