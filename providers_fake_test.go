//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func providerServer(t *testing.T) (*Server, *Fake, func(method, path, body string) (int, string)) {
	s, d, do, _ := providerServerCookie(t)
	return s, d, do
}

func providerServerCookie(t *testing.T) (*Server, *Fake, func(method, path, body string) (int, string), *http.Cookie) {
	c := testConfig()
	c.Sandboxes = append(c.Sandboxes, newSandbox("web"))
	d := NewFake()
	d.seedProviders()
	s := newServer(c, d, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return w.Code, w.Body.String()
	}
	return s, d, do, cookie
}

// readinessRuntime answers ProviderReadiness as the test says.
type readinessRuntime struct {
	*Fake
	mu    sync.Mutex
	state map[string][2]string // by receipt; missing: the Fake's answer
	asked int
}

// Attach and detach answer at once; the list follows the receipt (反映待ち)
// until the running sandbox applied the change, and keeps a failure with
// its reason until the sandbox restarts.
func TestProviderChangeIsAppliedInTheBackground(t *testing.T) {
	old := fakeApplyDelay
	fakeApplyDelay = time.Hour
	defer func() { fakeApplyDelay = old }()
	d := NewFake()
	d.seedProviders()
	rt := &readinessRuntime{Fake: d, state: map[string][2]string{}}
	s := newServer(testConfig(), rt, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return w.Code, w.Body.String()
	}
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	applying := func(name string) []ApplyView {
		t.Helper()
		p, ok := provider(listProviders(t, do), name)
		if !ok {
			t.Fatal("no provider", name)
		}
		return p.Applying
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	started := time.Now()
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"claude"}`); code != 200 || !strings.Contains(body, `"restart":true`) {
		t.Fatal(code, body)
	}
	if time.Since(started) > time.Second {
		t.Fatal("attach waited for the sandbox")
	}
	if got := applying("claude"); !slices.Equal(got, []ApplyView{{Sandbox: "dev", Attach: true, State: "pending"}}) {
		t.Fatalf("%+v", got)
	}
	// Kept in the configuration (a restarted Iwato follows it on).
	sb, _ := s.lookupSandbox("dev")
	if len(sb.ProviderApplying) != 1 || sb.ProviderApplying[0].Receipt == "" || sb.ProviderApplying[0].Provider != "claude" {
		t.Fatalf("%+v", sb.ProviderApplying)
	}
	if err := s.config.Validate(); err != nil {
		t.Fatal(err)
	}
	// Applied: the mark goes; 再起動待ち stays (processes from before).
	fakeApplyDelay = 0
	if got := applying("claude"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if sb, _ := s.lookupSandbox("dev"); sb.ProviderApplying != nil || sb.ProvidersChangedAt.IsZero() {
		t.Fatalf("%+v", sb)
	}
	// A detach the sandbox could not apply: the reason stays, unasked.
	fakeApplyDelay = time.Hour
	if code, body := do("POST", "/api/sandboxes/dev/providers/detach", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	sb, _ = s.lookupSandbox("dev")
	rt.state[sb.ProviderApplying[0].Receipt] = [2]string{"failed", "supervisor_rejected"}
	want := []ApplyView{{Sandbox: "dev", Attach: false, State: "failed", Reason: "supervisor_rejected"}}
	if got := applying("claude"); !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
	asked := rt.asked
	if got := applying("claude"); !slices.Equal(got, want) || rt.asked != asked {
		t.Fatalf("a failure is asked again: %+v", got)
	}
	// A new change of the same provider replaces the old one.
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	if got := applying("claude"); len(got) != 1 || got[0].State != "pending" || !got[0].Attach {
		t.Fatalf("%+v", got)
	}
	// Restarting gives the sandbox everything: nothing is followed.
	for _, op := range []string{"stop", "start"} {
		if code, body := do("POST", "/api/sandboxes/dev/"+op, `{}`); code != 200 {
			t.Fatal(code, body)
		}
	}
	if sb, _ := s.lookupSandbox("dev"); sb.ProviderApplying != nil {
		t.Fatalf("%+v", sb.ProviderApplying)
	}
	// A stopped sandbox gets no receipt to follow.
	if code, body := do("POST", "/api/sandboxes/dev/stop", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/detach", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	if got := applying("claude"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Creating checks the name, the profile and the values; the list never
// carries values; changing needs CSRF.
func TestProviderAPIValidation(t *testing.T) {
	s, _, do, cookie := providerServerCookie(t)
	l := listProviders(t, do)
	if len(l.Profiles) != len(providerProfiles) || l.Profiles[0].ID != "iwato-claude" || !l.Profiles[1].OAuth || !l.Profiles[2].OAuth || !l.Profiles[4].OAuth || l.Profiles[5].ID != "iwato-github" || l.Profiles[6].Group != groupModels || len(l.Providers) != 10 {
		t.Fatalf("%+v", l)
	}
	for _, c := range []struct{ body, want string }{
		{`{"name":"Bad","profile":"iwato-github","values":{"token":"x"}}`, "名前は"},
		{`{"name":"gh","profile":"github","values":{"token":"x"}}`, "unknown profile"},
		{`{"name":"gh","profile":"iwato-github","values":{}}`, "値を入力"},
		{`{"name":"gh","profile":"iwato-github","values":{"token":"a b"}}`, "空白"},
		{`{"name":"gh","profile":"iwato-github","values":{"token":"x","other":"y"}}`, "にない値"},
		{`{"name":"gh","profile":"iwato-github","values":{"token":"x"},"extra":1}`, "invalid request"},
	} {
		if code, body := do("POST", "/api/providers", c.body); code != 400 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.body, code, body)
		}
	}
	if w := reviewRequest(s, "POST", "/api/providers", `{"name":"gh","profile":"iwato-github","values":{"token":"x"}}`, s.origin, "same-origin", s.host, nil, ""); w.Code != 401 {
		t.Fatal("created without a session", w.Code)
	}
	if w := reviewRequest(s, "POST", "/api/providers", `{"name":"gh","profile":"iwato-github","values":{"token":"x"}}`, s.origin, "same-origin", s.host, cookie, ""); w.Code != 403 {
		t.Fatal("created without CSRF", w.Code)
	}
	if code, body := do("POST", "/api/providers", `{"name":"github-new","profile":"iwato-github","values":{"token":" ghp_SECRET \n"}}`); code != 200 {
		t.Fatal(code, body)
	}
	code, body := do("GET", "/api/providers", "")
	if code != 200 || strings.Contains(body, "SECRET") {
		t.Fatal("the list carries a value:", body)
	}
	p, ok := provider(listProviders(t, do), "github-new")
	if !ok || p.Profile != "iwato-github-for-github-new" || !slices.Equal(p.Keys, []string{"GH_TOKEN", "GITHUB_TOKEN"}) || len(p.Attached) != 0 {
		t.Fatalf("%+v", p)
	}
	if code, body := do("POST", "/api/providers", `{"name":"github-new","profile":"iwato-github","values":{"token":"x"}}`); code != 409 {
		t.Fatal("created twice:", code, body)
	}
	// Updating takes only values.
	if code, body := do("POST", "/api/providers/github-new/update", `{"values":{"token":"ghp_other"}}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, _ := do("POST", "/api/providers/github-new/update", `{"profile":"iwato-claude","values":{"token":"x"}}`); code != 400 {
		t.Fatal("changed the profile", code)
	}
	if code, _ := do("POST", "/api/providers/missing/update", `{"values":{"token":"x"}}`); code != 404 {
		t.Fatal("updated a missing provider", code)
	}
	if code, _ := do("POST", "/api/providers/github-new/rename", `{}`); code != 404 {
		t.Fatal("unknown operation", code)
	}
	if code, body := do("POST", "/api/providers/github-new/delete", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if _, ok := provider(listProviders(t, do), "github-new"); ok {
		t.Fatal("not deleted")
	}
}

// A sandbox gets at most one GitHub provider, at creation and later;
// attaching to one OpenShell does not have yet keeps the list for its
// creation; a provider in use is not deleted.
func TestProviderAttachRules(t *testing.T) {
	s, d, do := providerServer(t)
	// At creation.
	if code, body := do("POST", "/api/sandboxes", `{"name":"two","providers":["github-personal","github-work"]}`); code != 409 || !strings.Contains(body, "GitHub のプロバイダーは 1 つのサンドボックスに 1 つまで") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes", `{"name":"two","providers":["missing"]}`); code != 409 || !strings.Contains(body, "missing がありません") {
		t.Fatal(code, body)
	}
	if code, _ := do("POST", "/api/sandboxes", `{"name":"two","providers":["claude","claude"]}`); code != 400 {
		t.Fatal("a provider twice", code)
	}
	if code, body := do("POST", "/api/sandboxes", `{"name":"two","providers":["github-work","claude"]}`); code != 200 {
		t.Fatal(code, body)
	}
	if sb, _ := s.lookupSandbox("two"); !slices.Equal(sb.Providers, []string{"github-work", "claude"}) {
		t.Fatal(sb)
	}
	// Later, before the sandbox is created: the configuration changes.
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 || strings.Contains(body, `"restart":true`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-work"}`); code != 409 || !strings.Contains(body, "先に github-personal を外してください") {
		t.Fatal(code, body)
	}
	if code, _ := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"Bad"}`); code != 400 {
		t.Fatal(code)
	}
	if code, _ := do("POST", "/api/sandboxes/nope/providers/attach", `{"provider":"claude"}`); code != 404 {
		t.Fatal(code)
	}
	if p, _ := provider(listProviders(t, do), "github-personal"); !slices.Equal(p.Attached, []string{"dev"}) {
		t.Fatalf("%+v", p)
	}
	code, body := do("POST", "/api/providers/github-personal/delete", `{}`)
	if code != 409 || !strings.Contains(body, "サンドボックス dev に付いています") {
		t.Fatal("deleted a provider in use:", code, body)
	}
	// OpenShell creates the sandbox with the list and keeps it.
	if code, body := do("POST", "/api/sandboxes/two/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if _, attached, _ := d.ListProviders(context.Background()); !slices.Equal(attached["two"], []string{"github-work", "claude"}) {
		t.Fatal(attached)
	}
	if code, body := do("POST", "/api/sandboxes/two/providers/detach", `{"provider":"github-work"}`); code != 200 || !strings.Contains(body, `"restart":true`) {
		t.Fatal(code, body)
	}
	if _, attached, _ := d.ListProviders(context.Background()); !slices.Equal(attached["two"], []string{"claude"}) {
		t.Fatal(attached)
	}
	if sb, _ := s.lookupSandbox("two"); !slices.Equal(sb.Providers, []string{"claude"}) {
		t.Fatal("the configuration did not follow:", sb)
	}
	if code, body := do("POST", "/api/sandboxes/two/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	if p, _ := provider(listProviders(t, do), "github-personal"); !slices.Equal(p.Attached, []string{"dev", "two"}) {
		t.Fatalf("%+v", p)
	}
	if code, body := do("POST", "/api/providers/github-work/delete", `{}`); code != 200 {
		t.Fatal(code, body)
	}
}

// A change on a running sandbox marks it 再起動待ち (in the list, which
// a reload reads again) and its terminals from before; restarting clears it.
func TestProviderChangeAsksForRestart(t *testing.T) {
	s, _, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	pending := func() bool {
		t.Helper()
		_, body := do("GET", "/api/sandboxes", "")
		var l struct{ Sandboxes []Status }
		if json.Unmarshal([]byte(body), &l) != nil {
			t.Fatal(body)
		}
		return l.Sandboxes[0].RestartPending
	}
	outdated := func() []string {
		t.Helper()
		_, body := do("GET", "/api/sandboxes/dev/terminals", "")
		var l struct{ ProviderOutdated []string }
		if json.Unmarshal([]byte(body), &l) != nil {
			t.Fatal(body)
		}
		return l.ProviderOutdated
	}
	sb, _ := s.lookupSandbox("dev")
	for _, n := range []string{mainTerm, "2"} {
		if _, err := s.rt.Attach(context.Background(), sb, TermRef{Name: n, New: true}, 80, 24); err != nil {
			t.Fatal(err)
		}
	}
	if pending() || len(outdated()) != 0 {
		t.Fatal("pending before any change")
	}
	// The fake policy's uninspected api.anthropic.com goes first.
	if code, body := do("POST", "/api/sandboxes/dev/providers/fix", `{"provider":"claude"}`); code != 200 {
		t.Fatal(code, body)
	}
	time.Sleep(2 * time.Millisecond)
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"claude"}`); code != 200 || !strings.Contains(body, `"restart":true`) {
		t.Fatal(code, body)
	}
	if !pending() || !slices.Equal(outdated(), []string{"main", "2"}) {
		t.Fatal("not marked:", pending(), outdated())
	}
	// A terminal opened afterwards has the change.
	if _, err := s.rt.Attach(context.Background(), sb, TermRef{Name: "3", New: true}, 80, 24); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(outdated(), []string{"main", "2"}) {
		t.Fatal(outdated())
	}
	if code, body := do("POST", "/api/sandboxes/dev/stop", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if pending() || len(outdated()) != 0 {
		t.Fatal("still marked after the restart")
	}
	// A change while stopped needs no restart.
	if code, body := do("POST", "/api/sandboxes/dev/stop", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/detach", `{"provider":"claude"}`); code != 200 || strings.Contains(body, `"restart":true`) {
		t.Fatal(code, body)
	}
	if sb, _ := s.lookupSandbox("dev"); !sb.ProvidersChangedAt.IsZero() {
		t.Fatal("marked while stopped")
	}
}

func (r *readinessRuntime) ProviderReadiness(ctx context.Context, sandbox, provider, receipt string) (string, string, error) {
	r.mu.Lock()
	r.asked++
	st, ok := r.state[receipt]
	r.mu.Unlock()
	if ok {
		return st[0], st[1], nil
	}
	return r.Fake.ProviderReadiness(ctx, sandbox, provider, receipt)
}
