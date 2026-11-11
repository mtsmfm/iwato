//go:build uitest

package main

import (
	"strings"
	"testing"
)

// Where Iwato cannot start at sign-in the page hides the setting; where it
// can, POST (with CSRF) turns it on and off.
func TestAutostartAPI(t *testing.T) {
	s, _ := newTestServer(t)
	cookie, csrf := login(t, s)
	if _, b := request(t, s, "/api/autostart", "GET", "", cookie, ""); !strings.Contains(string(b), `"supported":false`) {
		t.Fatal(string(b))
	}
	s.autostart = &memoryAutostart{}
	if r, _ := request(t, s, "/api/autostart", "POST", `{"enabled":true}`, cookie, ""); r.StatusCode != 403 {
		t.Fatal("changed without CSRF:", r.StatusCode)
	}
	if r, b := request(t, s, "/api/autostart", "POST", `{"enabled":true}`, cookie, csrf); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(b))
	}
	if _, b := request(t, s, "/api/autostart", "GET", "", cookie, ""); string(b) != `{"enabled":true,"supported":true}`+"\n" {
		t.Fatal(string(b))
	}
}
