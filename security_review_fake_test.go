//go:build uitest

package main

import (
	"net/http/httptest"
	"testing"
)

func TestReviewCrossSiteOnlyTopLevelPageNavigation(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	cookie, _ := reviewLogin(t, s)
	do := func(method, path, mode, dest string) int {
		r := httptest.NewRequest(method, s.origin+path, nil)
		r.Host = s.host
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", mode)
		r.Header.Set("Sec-Fetch-Dest", dest)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	// A phone camera / chat app opening the pairing link.
	if c := do("GET", "/", "navigate", "document"); c != 200 {
		t.Fatal("cross-site page navigation refused", c)
	}
	for _, tc := range []struct{ method, path, mode, dest string }{
		{"GET", "/api/sandboxes", "navigate", "document"},
		{"GET", "/api/session", "cors", "empty"},
		{"GET", "/", "navigate", "iframe"},
		{"GET", "/app.js", "no-cors", "script"},
		{"GET", "/ws/dev", "websocket", "empty"},
		{"POST", "/", "navigate", "document"},
	} {
		if c := do(tc.method, tc.path, tc.mode, tc.dest); c != 403 {
			t.Errorf("%+v accepted: %d", tc, c)
		}
	}
}
