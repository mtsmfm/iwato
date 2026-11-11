//go:build uitest

package main

import (
	"strings"
	"testing"
	"time"
)

func TestSessionLimitsAPI(t *testing.T) {
	s, _ := newTestServer(t)
	cookie, csrf := login(t, s)
	if _, b := request(t, s, "/api/sessions", "GET", "", cookie, ""); !strings.Contains(string(b), `"limits":{"idleDays":7,"max":365,"maxDays":30}`) {
		t.Fatal(string(b))
	}
	// The page's GET /api/session renews the cookie up to the longest limit.
	if r, _ := request(t, s, "/api/session", "GET", "", cookie, ""); len(r.Cookies()) != 1 || r.Cookies()[0].MaxAge < 29*24*3600 || r.Cookies()[0].Value != cookie.Value {
		t.Fatalf("cookie not renewed: %+v", r.Cookies())
	}
	if r, _ := request(t, s, "/api/sessions/limits", "POST", `{"idleDays":10,"maxDays":20}`, cookie, ""); r.StatusCode != 403 {
		t.Fatal("changed without CSRF:", r.StatusCode)
	}
	if r, b := request(t, s, "/api/sessions/limits", "POST", `{"idleDays":10,"maxDays":20}`, cookie, csrf); r.StatusCode != 200 || s.config.Sessions == nil || s.config.Sessions.IdleDays != 10 {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, b := request(t, s, "/api/sessions/limits", "POST", `{"idleDays":21,"maxDays":20}`, cookie, csrf); r.StatusCode != 400 || !strings.Contains(string(b), "session_limits_order") {
		t.Fatal(r.StatusCode, string(b))
	}
	if r, _ := request(t, s, "/api/sessions/limits", "POST", `{"idleDays":7,"maxDays":30}`, cookie, csrf); r.StatusCode != 200 || s.config.Sessions != nil {
		t.Fatal("the defaults are not written:", r.StatusCode)
	}
	s.sessionTTL = 8 * time.Hour
	if r, b := request(t, s, "/api/sessions/limits", "POST", `{"idleDays":10,"maxDays":20}`, cookie, csrf); r.StatusCode != 409 || !strings.Contains(string(b), "session_limits_fixed") {
		t.Fatal(r.StatusCode, string(b))
	}
}
