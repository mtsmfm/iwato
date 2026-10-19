//go:build uitest

package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// An upload outlasts the server's ReadTimeout (15 seconds in serve).
func TestFilesSlowUpload(t *testing.T) {
	davDelay = 0
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:1")
	h := httptest.NewUnstartedServer(s.Handler())
	h.Config.ReadTimeout = 300 * time.Millisecond
	h.Start()
	defer h.Close()
	u, _ := url.Parse(h.URL)
	s.host, s.origin = u.Host, h.URL
	c, csrf := login(t, s)
	request(t, s, "/api/sandboxes/dev/start", "POST", "", c, csrf)
	k := makeKey(t, s, c, csrf, `{"name":"pc","kind":"password"}`)
	r, _ := http.NewRequest("PUT", h.URL+"/dav/dev/slow.txt", &slowBody{parts: 6})
	r.SetBasicAuth(k.Key.ID, k.Secret)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("slow PUT: %d", res.StatusCode)
	}
	davExpect(t, s, 200, davReq{method: "GET", path: "/dav/dev/slow.txt", user: k.Key.ID, pass: k.Secret})
}

func newFilesServer(t *testing.T) (*Server, *http.Cookie, string) {
	davDelay = 0
	s, _ := newTestServer(t)
	s.config.Sandboxes = append(s.config.Sandboxes, newSandbox("web"))
	s.publicHost, s.publicOrigin = "box.tail0000.ts.net", "https://box.tail0000.ts.net"
	c, csrf := login(t, s)
	for _, name := range []string{"dev", "web"} {
		if res, b := request(t, s, "/api/sandboxes/"+name+"/start", "POST", "", c, csrf); res.StatusCode != 200 {
			t.Fatal(res.StatusCode, string(b))
		}
	}
	return s, c, csrf
}

func TestFilesKeyAPI(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	if res, _ := request(t, s, "/api/files", "GET", "", nil, ""); res.StatusCode != 401 {
		t.Fatalf("API without a session: %d", res.StatusCode)
	}
	if res, _ := request(t, s, "/api/files/keys", "POST", `{"name":"pc","kind":"password"}`, c, ""); res.StatusCode != 403 {
		t.Fatalf("make without CSRF: %d", res.StatusCode)
	}
	for _, body := range []string{`{"name":"","kind":"password"}`, `{"name":"pc","kind":"other"}`, `{"name":"pc","kind":"url","sandbox":"nope"}`} {
		if res, _ := request(t, s, "/api/files/keys", "POST", body, c, csrf); res.StatusCode < 400 {
			t.Fatalf("accepted %s", body)
		}
	}
	k := makeKey(t, s, c, csrf, `{"name":"仕事の PC","kind":"password","sandbox":"dev","readOnly":true}`)
	if !strings.HasPrefix(k.Key.ID, "iwato-") || len(k.Secret) != 52 || k.Key.Hash != "" {
		t.Fatalf("made %+v", k)
	}
	res, b := request(t, s, "/api/files", "GET", "", c, "")
	if res.StatusCode != 200 || strings.Contains(string(b), k.Secret) || strings.Contains(string(b), davHash(k.Secret)) || !strings.Contains(string(b), "仕事の PC") {
		t.Fatalf("list: %d %s", res.StatusCode, b)
	}
	if !strings.Contains(string(b), `"publicOrigin":"https://box.tail0000.ts.net"`) {
		t.Fatalf("list lacks the origin: %s", b)
	}
	if res, _ := request(t, s, "/api/files/keys/"+k.Key.ID+"/revoke", "POST", "", c, csrf); res.StatusCode != 200 {
		t.Fatalf("revoke: %d", res.StatusCode)
	}
	if res, _ := request(t, s, "/api/files/keys/"+k.Key.ID+"/revoke", "POST", "", c, csrf); res.StatusCode != 404 {
		t.Fatalf("revoke again: %d", res.StatusCode)
	}
}

func TestFilesAppPassword(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	all := makeKey(t, s, c, csrf, `{"name":"Mac","kind":"password"}`)
	devRO := makeKey(t, s, c, csrf, `{"name":"iPhone","kind":"password","sandbox":"dev","readOnly":true}`)
	pub := s.publicHost

	// No credentials: a challenge (the tailnet); this PC's /dav/ shows empty.
	res, _ := davDo(t, s, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub})
	if res.StatusCode != 401 || !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("no credentials: %d %v", res.StatusCode, res.Header)
	}
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/", host: pub})
	if body := davExpect(t, s, 207, davReq{method: "PROPFIND", path: "/dav/"}); strings.Contains(body, "/dav/dev/") {
		t.Fatalf("anonymous root lists sandboxes: %s", body)
	}
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/dev/"})
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub, user: all.Key.ID, pass: all.Secret + "x"})
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub, user: devRO.Key.ID, pass: all.Secret})
	// A secret URL's token is not a password.
	url := makeKey(t, s, c, csrf, `{"name":"PC","kind":"url"}`)
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub, user: url.Key.ID, pass: url.Secret})

	body := davExpect(t, s, 207, davReq{method: "PROPFIND", path: "/dav/", host: pub, user: all.Key.ID, pass: all.Secret})
	if !strings.Contains(body, "/dav/dev/") || !strings.Contains(body, "/dav/web/") {
		t.Fatalf("root for all: %s", body)
	}
	body = davExpect(t, s, 207, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub, user: all.Key.ID, pass: all.Secret})
	if !strings.Contains(body, "/dav/dev/README.md") {
		t.Fatalf("dev: %s", body)
	}
	davExpect(t, s, 201, davReq{method: "PUT", path: "/dav/web/a.txt", body: "hello", host: pub, user: all.Key.ID, pass: all.Secret})
	if got := davExpect(t, s, 200, davReq{method: "GET", path: "/dav/web/a.txt", host: pub, user: all.Key.ID, pass: all.Secret}); got != "hello" {
		t.Fatalf("GET: %q", got)
	}
	// This PC (http://127.0.0.1) takes app passwords too.
	davExpect(t, s, 207, davReq{method: "PROPFIND", path: "/dav/dev/", user: all.Key.ID, pass: all.Secret})

	// Scope: one sandbox; others are not there.
	body = davExpect(t, s, 207, davReq{method: "PROPFIND", path: "/dav/", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	if !strings.Contains(body, "/dav/dev/") || strings.Contains(body, "/dav/web/") {
		t.Fatalf("root for dev: %s", body)
	}
	davExpect(t, s, 404, davReq{method: "GET", path: "/dav/web/a.txt", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	davExpect(t, s, 404, davReq{method: "PROPFIND", path: "/dav/nope/", host: pub, user: all.Key.ID, pass: all.Secret})
	// Read-only.
	davExpect(t, s, 200, davReq{method: "GET", path: "/dav/dev/README.md", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	davExpect(t, s, 403, davReq{method: "PUT", path: "/dav/dev/b.txt", body: "x", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	davExpect(t, s, 403, davReq{method: "LOCK", path: "/dav/dev/README.md", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	davExpect(t, s, 403, davReq{method: "DELETE", path: "/dav/dev/README.md", host: pub, user: devRO.Key.ID, pass: devRO.Secret})
	// The list is read-only for everyone.
	davExpect(t, s, 403, davReq{method: "MKCOL", path: "/dav/", host: pub, user: all.Key.ID, pass: all.Secret})

	// Last use is recorded.
	for _, k := range s.davKeys.list() {
		if k.ID == all.Key.ID && (k.LastUsed.IsZero() || k.LastClient == "") {
			t.Fatalf("last use not recorded: %+v", k)
		}
	}
	// Revoked: refused at once.
	request(t, s, "/api/files/keys/"+all.Key.ID+"/revoke", "POST", "", c, csrf)
	davExpect(t, s, 401, davReq{method: "PROPFIND", path: "/dav/dev/", host: pub, user: all.Key.ID, pass: all.Secret})
}

func TestFilesSecretURL(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	k := makeKey(t, s, c, csrf, `{"name":"この PC","kind":"url","sandbox":"dev"}`)
	base := "/dav/" + k.Secret
	body := davExpect(t, s, 207, davReq{method: "PROPFIND", path: base + "/"})
	if !strings.Contains(body, base+"/dev/") || strings.Contains(body, "/web/") {
		t.Fatalf("secret root: %s", body)
	}
	davExpect(t, s, 201, davReq{method: "PUT", path: base + "/dev/x.txt", body: "via url"})
	if got := davExpect(t, s, 200, davReq{method: "GET", path: base + "/dev/x.txt"}); got != "via url" {
		t.Fatalf("GET: %q", got)
	}
	// MOVE keeps the secret in the destination.
	davExpect(t, s, 201, davReq{method: "MOVE", path: base + "/dev/x.txt", header: map[string]string{"Destination": s.origin + base + "/dev/y.txt"}})
	// localhost is this PC too.
	_, port, _ := strings.Cut(s.host, ":")
	davExpect(t, s, 207, davReq{method: "PROPFIND", path: base + "/dev/", host: "localhost:" + port})
	// Scope.
	davExpect(t, s, 404, davReq{method: "PROPFIND", path: base + "/web/"})

	// Only straight from this PC: not through tailscale serve (the tailnet
	// host, or a relayed request that claims 127.0.0.1), not another Host.
	davExpect(t, s, 403, davReq{method: "PROPFIND", path: base + "/dev/", host: s.publicHost})
	for _, h := range []string{"X-Forwarded-For", "Tailscale-User-Login", "Forwarded"} {
		davExpect(t, s, 403, davReq{method: "PROPFIND", path: base + "/dev/", header: map[string]string{h: "100.64.0.2"}})
	}
	davExpect(t, s, 403, davReq{method: "PROPFIND", path: base + "/dev/", host: "evil.example:" + port})
	// A wrong or revoked token.
	davExpect(t, s, 404, davReq{method: "PROPFIND", path: "/dav/" + strings.Repeat("a", 52) + "/dev/"})
	request(t, s, "/api/files/keys/"+k.Key.ID+"/revoke", "POST", "", c, csrf)
	davExpect(t, s, 404, davReq{method: "PROPFIND", path: base + "/dev/"})
}

func TestFilesReadOnlySecretURL(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	k := makeKey(t, s, c, csrf, `{"name":"ro","kind":"url","readOnly":true}`)
	base := "/dav/" + k.Secret
	davExpect(t, s, 200, davReq{method: "GET", path: base + "/web/README.md"})
	davExpect(t, s, 403, davReq{method: "PUT", path: base + "/web/a.txt", body: "x"})
}

func TestFilesRequestChecks(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	k := makeKey(t, s, c, csrf, `{"name":"Mac","kind":"password"}`)
	auth := davReq{method: "GET", path: "/dav/dev/README.md", host: s.publicHost, user: k.Key.ID, pass: k.Secret}
	// DNS rebinding: an unknown Host.
	q := auth
	q.host = "evil.example"
	davExpect(t, s, 403, q)
	// Web pages elsewhere (a browser that has the password).
	q = auth
	q.header = map[string]string{"Sec-Fetch-Site": "cross-site"}
	davExpect(t, s, 403, q)
	q = auth
	q.header = map[string]string{"Origin": "https://evil.example"}
	davExpect(t, s, 403, q)
	// No Origin is fine (WebDAV clients), for writes too.
	q = auth
	q.method, q.path, q.body = "PUT", "/dav/dev/w.txt", "x"
	davExpect(t, s, 201, q)
	// A sandbox's files never run as Iwato's pages.
	res, _ := davDo(t, s, auth)
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP: %q", csp)
	}
	// OPTIONS without credentials, on /dav/ and the root (Windows).
	for _, p := range []string{"/dav/dev/", "/"} {
		res, _ := davDo(t, s, davReq{method: "OPTIONS", path: p, host: s.publicHost})
		if res.StatusCode != 200 || res.Header.Get("DAV") == "" {
			t.Fatalf("OPTIONS %s: %d %v", p, res.StatusCode, res.Header)
		}
	}
	// The page's API keeps its own checks.
	r, _ := http.NewRequest("POST", s.origin+"/api/sandboxes/dev/start", nil)
	r.AddCookie(c)
	r.Header.Set("X-CSRF-Token", csrf)
	if res, err := http.DefaultClient.Do(r); err != nil || res.StatusCode != 403 {
		t.Fatalf("API without Origin: %v %v", res, err)
	}
	// Depth: infinity.
	q = auth
	q.method, q.path, q.header = "PROPFIND", "/dav/dev/", map[string]string{"Depth": "infinity"}
	davExpect(t, s, 403, q)
}

func TestFilesStoppedSandbox(t *testing.T) {
	s, c, csrf := newFilesServer(t)
	k := makeKey(t, s, c, csrf, `{"name":"Mac","kind":"password"}`)
	q := func(method, path string) davReq {
		body := ""
		if method == "PUT" {
			body = "x"
		}
		return davReq{method: method, path: path, body: body, host: s.publicHost, user: k.Key.ID, pass: k.Secret}
	}
	davExpect(t, s, 201, q("PUT", "/dav/dev/kept.txt"))
	if res, b := request(t, s, "/api/sandboxes/dev/stop", "POST", "", c, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	body := davExpect(t, s, 207, q("PROPFIND", "/dav/dev/"))
	if !strings.Contains(body, ".txt") || strings.Contains(body, "kept.txt") {
		t.Fatalf("stopped: %s", body)
	}
	davExpect(t, s, 503, q("PUT", "/dav/dev/new.txt"))
	if res, b := request(t, s, "/api/sandboxes/dev/start", "POST", "", c, csrf); res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(b))
	}
	if got := davExpect(t, s, 200, q("GET", "/dav/dev/kept.txt")); got != "x" {
		t.Fatalf("after start: %q", got)
	}
}
