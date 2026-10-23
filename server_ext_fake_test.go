//go:build uitest

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"

	"path/filepath"
	"strings"
	"testing"
)

func TestPairIssuesFreshSingleUseToken(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	if code := reviewRequest(s, "POST", "/api/pair", "", s.origin, "same-origin", s.host, cookie, "").Code; code != 403 {
		t.Fatal("pair without CSRF", code)
	}
	w := reviewRequest(s, "POST", "/api/pair", "", s.origin, "same-origin", s.host, cookie, csrf)
	var p struct{ URL, Token, QR string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || !strings.HasSuffix(p.URL, "#token="+p.Token) {
		t.Fatal(w.Code, w.Body.String())
	}
	// The QR code is an inline PNG (allowed by the CSP as img-src data:).
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(p.QR, "data:image/png;base64,"))
	img, perr := png.Decode(bytes.NewReader(raw))
	if !strings.HasPrefix(p.QR, "data:image/png;base64,") || err != nil || perr != nil || img.Bounds().Dx() < 100 {
		t.Fatal("pairing QR code", err, perr)
	}
	if code := reviewRequest(s, "POST", "/api/session", `{"token":"`+p.Token+`"}`, s.origin, "same-origin", s.host, nil, "").Code; code != 200 {
		t.Fatal("paired token rejected", code)
	}
	if code := reviewRequest(s, "POST", "/api/session", `{"token":"`+p.Token+`"}`, s.origin, "same-origin", s.host, nil, "").Code; code != 401 {
		t.Fatal("paired token reused", code)
	}
}

func TestProposalRoutesRequireSession(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	for _, path := range []string{"/api/sandboxes/dev/proposals", "/api/push"} {
		if code := reviewRequest(s, "GET", path, "", "", "", s.host, nil, "").Code; code != 401 {
			t.Errorf("%s without session: %d", path, code)
		}
	}
	cookie, csrf := reviewLogin(t, s)
	get := func(path string) (int, map[string]any) {
		w := reviewRequest(s, "GET", path, "", "", "", s.host, cookie, "")
		var v map[string]any
		json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v
	}
	reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, cookie, csrf)
	code, v := get("/api/sandboxes/dev/proposals")
	pending := v["pending"].([]any)
	if code != 200 || len(pending) != 6 {
		t.Fatal(code, v)
	}
	id := pending[0].(map[string]any)["id"].(string)
	if c := reviewRequest(s, "POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+id+`"`+digestField(s.rt, id)+`}`, s.origin, "same-origin", s.host, cookie, "").Code; c != 403 {
		t.Fatal("approve without CSRF", c)
	}
	if c := reviewRequest(s, "POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+id+`"`+digestField(s.rt, id)+`}`, s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 {
		t.Fatal("approve", c)
	}
	if c := reviewRequest(s, "POST", "/api/sandboxes/dev/proposals/approve", `{"requestId":"`+id+`"`+digestField(s.rt, id)+`}`, s.origin, "same-origin", s.host, cookie, csrf).Code; c != 409 {
		t.Fatal("replayed approval", c)
	}
}

func TestPushRoutes(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	w := reviewRequest(s, "GET", "/api/push", "", "", "", s.host, cookie, "")
	if !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal(w.Body.String())
	}
	var err error
	if s.push, err = OpenPushStore(filepath.Join(t.TempDir(), "p.json"), "mailto:x@example.invalid"); err != nil {
		t.Fatal(err)
	}
	sub, _, _ := testSubscription(t, "https://fcm.googleapis.com/fcm/send/abc")
	body, _ := json.Marshal(map[string]any{"endpoint": sub.Endpoint, "keys": map[string]string{"p256dh": sub.P256dh, "auth": sub.Auth}})
	if c := reviewRequest(s, "POST", "/api/push/subscribe", string(body), s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 || s.push.Count() != 1 {
		t.Fatal("subscribe", c)
	}
	evil, _ := json.Marshal(map[string]any{"endpoint": "https://attacker.example/x", "keys": map[string]string{"p256dh": sub.P256dh, "auth": sub.Auth}})
	if c := reviewRequest(s, "POST", "/api/push/subscribe", string(evil), s.origin, "same-origin", s.host, cookie, csrf).Code; c != 400 {
		t.Fatal("arbitrary endpoint accepted", c)
	}
	if c := reviewRequest(s, "POST", "/api/push/unsubscribe", string(body), s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 || s.push.Count() != 0 {
		t.Fatal("unsubscribe", c)
	}
	if c := reviewRequest(s, "POST", "/api/push/test", "{}", s.origin, "same-origin", s.host, cookie, csrf).Code; c != 405 {
		t.Fatal("test notification", c)
	}
	page := `{"page":"` + strings.Repeat("a", 32) + `","active":true}`
	if c := reviewRequest(s, "POST", "/api/presence", page, s.origin, "same-origin", s.host, cookie, "").Code; c != 403 || s.presence.active() {
		t.Fatal("presence without CSRF", c)
	}
	if c := reviewRequest(s, "POST", "/api/presence", `{"page":"x","active":true}`, s.origin, "same-origin", s.host, cookie, csrf).Code; c != 400 {
		t.Fatal("bad page id", c)
	}
	if c := reviewRequest(s, "POST", "/api/presence", page, s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 || !s.presence.active() {
		t.Fatal("presence", c)
	}
}

func TestCreateAndDeleteSandboxPersistsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iwato.json")
	if _, err := ensureConfig(path); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(c, NewFake(), true, "127.0.0.1:29999")
	s.configPath = path
	cookie, csrf := reviewLogin(t, s)
	post := func(path, body string) int {
		return reviewRequest(s, "POST", path, body, s.origin, "same-origin", s.host, cookie, csrf).Code
	}
	if c := reviewRequest(s, "POST", "/api/sandboxes", `{"name":"web"}`, s.origin, "same-origin", s.host, cookie, "").Code; c != 403 {
		t.Fatal("create without CSRF", c)
	}
	for _, bad := range []string{`{"name":"Web"}`, `{"name":"-x"}`, `{"name":"a b"}`, `{"name":""}`} {
		if c := post("/api/sandboxes", bad); c != 400 {
			t.Errorf("%s: %d", bad, c)
		}
	}
	if c := post("/api/sandboxes", `{"name":"web"}`); c != 200 {
		t.Fatal("create", c)
	}
	if c := post("/api/sandboxes", `{"name":"web"}`); c != 409 {
		t.Fatal("duplicate", c)
	}
	saved, err := readConfig(path)
	if err != nil || len(saved.Sandboxes) != 1 || saved.Instance != c.Instance {
		t.Fatalf("not persisted: %+v %v", saved.Sandboxes, err)
	}
	w := reviewRequest(s, "GET", "/api/sandboxes", "", "", "", s.host, cookie, "")
	if !strings.Contains(w.Body.String(), `"name":"web"`) {
		t.Fatal(w.Body.String())
	}
	if c := post("/api/sandboxes/web/delete", `{"confirm":"web"}`); c != 200 {
		t.Fatal("delete", c)
	}
	if saved, _ = readConfig(path); len(saved.Sandboxes) != 0 {
		t.Fatal("deleted sandbox kept in config")
	}
	if c := post("/api/sandboxes/web/start", ""); c != 404 {
		t.Fatal("deleted sandbox still routable", c)
	}
}

func TestOperationsWaitForImages(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	s.images = &imageManager{}
	s.images.set(ImageState{Phase: "building", Current: "dev", Message: "building dev"})
	retried := 0
	s.retryImages = func() { retried++ }
	cookie, csrf := reviewLogin(t, s)
	post := func(path, body string) int {
		return reviewRequest(s, "POST", path, body, s.origin, "same-origin", s.host, cookie, csrf).Code
	}
	for path, body := range map[string]string{"/api/sandboxes/dev/start": ""} {
		if c := post(path, body); c != 409 {
			t.Errorf("%s while building: %d", path, c)
		}
	}
	w := reviewRequest(s, "GET", "/api/sandboxes", "", "", "", s.host, cookie, "")
	if !strings.Contains(w.Body.String(), `"phase":"building"`) {
		t.Fatal(w.Body.String())
	}
	if c := post("/api/images/retry", ""); c != 409 || retried != 0 {
		t.Fatal("retry allowed while building", c)
	}
	s.images.set(ImageState{Phase: "failed", Message: "x"})
	if c := post("/api/images/retry", ""); c != 200 || retried != 1 {
		t.Fatal("retry", c)
	}
	s.images.set(ImageState{Phase: "ready"})
	if c := post("/api/sandboxes/dev/start", ""); c != 200 {
		t.Fatal("start when ready", c)
	}
}

func TestCreateSandboxTakesTheSandboxImageButNoPersist(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	s.configPath = filepath.Join(t.TempDir(), "iwato.json")
	cookie, csrf := reviewLogin(t, s)
	post := func(body string) int {
		return reviewRequest(s, "POST", "/api/sandboxes", body, s.origin, "same-origin", s.host, cookie, csrf).Code
	}
	if c := post(`{"name":"box","image":" ubuntu:24.04 ","persist":[]}`); c != 200 {
		t.Fatal(c)
	}
	box, _ := s.lookupSandbox("box")
	if box.Image != "ubuntu:24.04" || len(box.LegacyPersist) != 0 {
		t.Fatalf("%+v", box)
	}
	if c := post(`{"name":"gpubox","gpu":true}`); c != 200 {
		t.Fatal(c)
	}
	if g, _ := s.lookupSandbox("gpubox"); !g.GPU {
		t.Fatalf("%+v", g)
	}
	if box.GPU {
		t.Fatalf("a sandbox created without gpu has it: %+v", box)
	}
	// Only the home persists: paths to persist are refused.
	for _, body := range []string{`{"name":"b1","persist":["/sandbox/work"]}`, `{"name":"b2","persist":["/work","/work/x"]}`, `{"name":"b3","image":"bad image"}`} {
		if c := post(body); c != 400 {
			t.Errorf("%s: %d", body, c)
		}
	}
}

// The policy of a stopped sandbox can be read and edited (OpenShell keeps it
// while the sandbox exists; the routes do not ask whether it runs).
func TestPolicyRoutesEditTheFakePolicy(t *testing.T) {
	s := newServer(testConfig(), NewFake(), true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	post := func(op, body, token string) int {
		return reviewRequest(s, "POST", "/api/sandboxes/dev/policy/"+op, body, s.origin, "same-origin", s.host, cookie, token).Code
	}
	policy := func() *CurrentPolicy {
		var v ProposalView
		json.Unmarshal(reviewRequest(s, "GET", "/api/sandboxes/dev/proposals", "", "", "", s.host, cookie, "").Body.Bytes(), &v)
		return v.Policy
	}
	// Not made yet: no policy. Made and stopped: its policy, editable.
	var none ProposalView
	json.Unmarshal(reviewRequest(s, "GET", "/api/sandboxes/dev/proposals", "", "", "", s.host, cookie, "").Body.Bytes(), &none)
	if none.Available || none.Policy != nil || none.Error == "" {
		t.Fatalf("%+v", none)
	}
	if c := reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 {
		t.Fatal("start", c)
	}
	if c := reviewRequest(s, "POST", "/api/sandboxes/dev/stop", "{}", s.origin, "same-origin", s.host, cookie, csrf).Code; c != 200 {
		t.Fatal("stop", c)
	}
	before := policy()
	add := `{"rule":"","binaries":["/usr/bin/curl"],"host":"example.com","ports":[443],"rules":[{"method":"GET","path":"/a/**"},{"method":"POST","path":"/a/b"}]}`
	if c := post("add", add, ""); c != 403 {
		t.Fatal("add without CSRF", c)
	}
	for _, bad := range []string{`{"host":"example.com","ports":[443],"binaries":["curl"]}`, `{"host":"example.com","ports":[443],"anyBinary":true,"extra":1}`, `{"host":"a b","ports":[443],"anyBinary":true}`} {
		if c := post("add", bad, csrf); c != 400 {
			t.Fatal(bad, c)
		}
	}
	if c := post("drop", `{"rule":"x"}`, csrf); c != 405 {
		t.Fatal("unknown operation", c)
	}
	if c := post("add", add, csrf); c != 200 {
		t.Fatal("add", c)
	}
	if c := post("remove-allow", `{"rule":"allow_example_com_443","host":"example.com","ports":[443],"rules":[{"method":"POST","path":"/a/b"}]}`, csrf); c != 200 {
		t.Fatal("remove-allow", c)
	}
	if c := post("remove-allow", `{"rule":"allow_example_com_443","host":"example.com","ports":[443],"rules":[{"method":"GET","path":"/a/**"}]}`, csrf); c != 409 {
		t.Fatal("removed the last method and path", c)
	}
	after := policy()
	var added *PolicyRuleView
	for i, r := range after.Rules {
		if r.Name == "allow_example_com_443" {
			added = &after.Rules[i]
		}
	}
	if after.Version != before.Version+2 || added == nil || len(added.Endpoints[0].Rules) != 1 || added.Endpoints[0].Rules[0].Path != "/a/**" {
		t.Fatalf("%+v", after)
	}
	var h struct{ Revisions []PolicyRevision }
	w := reviewRequest(s, "GET", "/api/sandboxes/dev/policy/history", "", "", "", s.host, cookie, "")
	if json.Unmarshal(w.Body.Bytes(), &h) != nil || len(h.Revisions) != int(after.Version) || h.Revisions[0].Version != after.Version || h.Revisions[0].Status != "pending" {
		t.Fatal(w.Body.String())
	}
	var first CurrentPolicy
	w = reviewRequest(s, "GET", "/api/sandboxes/dev/policy/history/1", "", "", "", s.host, cookie, "")
	if json.Unmarshal(w.Body.Bytes(), &first) != nil || first.Version != 1 || len(first.Rules) != 2 {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/sandboxes/dev/policy/history/0", "/api/sandboxes/dev/policy/history/x"} {
		if c := reviewRequest(s, "GET", path, "", "", "", s.host, cookie, "").Code; c != 400 {
			t.Fatal(path, c)
		}
	}
	if c := reviewRequest(s, "GET", "/api/sandboxes/dev/policy/history", "", "", "", s.host, nil, "").Code; c != 401 {
		t.Fatal("history without session", c)
	}
}

// The sandbox list carries a running sandbox's pending agent proposals (the
// badges while the policy tab is hidden); not a stopped one's, which the
// page keeps as it last read them.
func TestListCarriesPendingProposals(t *testing.T) {
	d := NewFake()
	c := testConfig()
	s := newServer(c, d, true, "127.0.0.1:29999")
	cookie, _ := reviewLogin(t, s)
	pending := func() *[]PendingProposal {
		t.Helper()
		w := reviewRequest(s, "GET", "/api/sandboxes", "", "", "", s.host, cookie, "")
		var v struct{ Sandboxes []Status }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Sandboxes) != 1 {
			t.Fatal(w.Code, w.Body.String())
		}
		return v.Sandboxes[0].Pending
	}
	if p := pending(); p != nil {
		t.Fatal("not created:", *p)
	}
	sb := c.Sandboxes[0]
	if err := d.Start(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	ids := func(p *[]PendingProposal) (out []string) {
		if p == nil {
			t.Fatal("no pending list")
		}
		for _, x := range *p {
			if x.Source != "agent" || x.State != "pending-approval" || x.Digest == "" {
				t.Fatalf("%+v", x)
			}
			out = append(out, x.Summary)
		}
		return out
	}
	if got := ids(pending()); len(got) != 3 {
		t.Fatal("the agent's proposals only:", got)
	}
	d.fakePropose(sb.Name, "new.example")
	if got := ids(pending()); len(got) != 4 {
		t.Fatal("a new proposal:", got)
	}
	if err := d.Stop(context.Background(), sb); err != nil {
		t.Fatal(err)
	}
	if p := pending(); p != nil {
		t.Fatal("stopped:", *p)
	}
}
