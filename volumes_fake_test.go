//go:build uitest

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The page lists the volumes and removes a leftover one with CSRF; one a
// configured sandbox uses is refused with the reason.
func TestVolumesAPI(t *testing.T) {
	c := testConfig()
	c.Sandboxes[0].LegacyPersist = []string{"/sandbox/work"}
	d := NewFake()
	d.seedVolumes(c)
	s := newServer(c, d, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	get := func() []Volume {
		w := reviewRequest(s, "GET", "/api/volumes", "", s.origin, "same-origin", s.host, cookie, "")
		var body struct{ Volumes []Volume }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return body.Volumes
	}
	list := get()
	used, leftover := persistVolume(c, c.Sandboxes[0], "/sandbox/work"), persistVolume(c, Sandbox{Name: "old-app"}, "/sandbox/data")
	var unknown Volume
	for _, v := range list {
		if v.Name == used && v.UsedBy != "dev" {
			t.Fatalf("in use not shown: %+v", v)
		}
		if v.Sandbox == "scratch" {
			unknown = v
		}
	}
	if len(list) != 4 || unknown.Path != "" || unknown.Name == "" {
		t.Fatalf("%+v", list)
	}
	if code := reviewRequest(s, "GET", "/api/volumes", "", s.origin, "same-origin", s.host, nil, "").Code; code != 401 {
		t.Fatal("listed without a session", code)
	}
	if code := reviewRequest(s, "POST", "/api/volumes/"+leftover+"/delete", "", s.origin, "same-origin", s.host, cookie, "").Code; code != 403 {
		t.Fatal("removed without CSRF", code)
	}
	w := reviewRequest(s, "POST", "/api/volumes/"+used+"/delete", "", s.origin, "same-origin", s.host, cookie, csrf)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "サンドボックス dev が使っています") {
		t.Fatal("removed a volume in use", w.Code, w.Body.String())
	}
	if code := reviewRequest(s, "POST", "/api/volumes/..%2Fx/delete", "", s.origin, "same-origin", s.host, cookie, csrf).Code; code != 400 && code != 404 {
		t.Fatal("odd name accepted", code)
	}
	if w := reviewRequest(s, "POST", "/api/volumes/"+leftover+"/delete", "", s.origin, "same-origin", s.host, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(get()) != 3 {
		t.Fatal("not removed")
	}
	// Deleting the sandbox (keeping its data) leaves its volume for removal.
	if w := reviewRequest(s, "POST", "/api/sandboxes/dev/delete", `{"confirm":"dev"}`, s.origin, "same-origin", s.host, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := reviewRequest(s, "POST", "/api/volumes/"+used+"/delete", "", s.origin, "same-origin", s.host, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
