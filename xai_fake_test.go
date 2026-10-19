//go:build uitest

package main

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// The device code flow: the page gets the code and xAI's page, Iwato polls
// through pending and slow_down, creates the provider and hands the grant
// to the gateway (Grok Build's client), and refuses team sign-ins and
// denials without creating anything.
func TestXAIFlowCreatesTheProvider(t *testing.T) {
	shortXAIPolls(t)
	o := newOAuthTest(t)
	fake := newFakeXAI(t)
	o.s.xai.backend = fake.oauth()
	x := &xaiTest{t: t, s: o.s, do: o.do}

	v := x.start(`{"name":""}`)
	if v.Status != "waiting" || v.UserCode != "ABCD-1234" || v.VerificationURI != "https://accounts.x.example/device" || !strings.Contains(v.CompleteURI, "user_code=ABCD-1234") {
		t.Fatalf("%+v", v)
	}
	done := x.wait(v.ID, "done", "failed")
	// The fake has grok-subscription already.
	if done.Status != "done" || done.Name != "grok-subscription-2" || done.Warning != "" || done.UserCode != "" || done.Refresh == nil || done.Refresh.Status != "refreshed" {
		t.Fatalf("%+v %+v", done, done.Refresh)
	}
	want := []string{"create grok-subscription-2 iwato-grok-subscription-for-grok-subscription-2 XAI_OAUTH_TOKEN", "configure grok-subscription-2 XAI_OAUTH_TOKEN", "rotate grok-subscription-2"}
	if got := o.rt.log(); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if m := o.rt.material[0]; m["client_id"] != xaiClientID || m["refresh_token"] != "rt-SECRET-1" {
		t.Fatal("material", m["client_id"])
	}
	// slow_down added 5 units to the 1 the server asked for.
	fake.mu.Lock()
	polls := fake.polls
	fake.mu.Unlock()
	if polls != 3 {
		t.Fatal("polls", polls)
	}
	if strings.Contains(o.logs.String(), "SECRET") {
		t.Fatal("a value reached the log")
	}

	// Reconnecting replaces the grant of the same provider.
	v = x.start(`{"name":"grok-subscription-2","reconnect":true}`)
	if done := x.wait(v.ID, "done", "failed"); done.Status != "done" || done.Message != "再接続しました。" {
		t.Fatalf("%+v", done)
	}
	if got := o.rt.log()[3:]; !slices.Equal(got, []string{"delete-refresh grok-subscription-2", "update grok-subscription-2 XAI_OAUTH_TOKEN", "configure grok-subscription-2 XAI_OAUTH_TOKEN", "rotate grok-subscription-2"}) {
		t.Fatalf("%q", got)
	}
	if code, resp := o.do("POST", "/api/oauth/xai/start", `{"name":"atlassian-work","reconnect":true}`); code != 409 {
		t.Fatal(code, resp)
	}

	// A team's sign-in and a denial create nothing.
	calls := len(o.rt.log())
	fake.mu.Lock()
	fake.principal = "Team"
	fake.mu.Unlock()
	v = x.start(`{"name":"grok-team"}`)
	if done := x.wait(v.ID, "done", "failed"); done.Status != "failed" || !strings.Contains(done.Message, "Team") {
		t.Fatalf("%+v", done)
	}
	fake.mu.Lock()
	fake.principal, fake.deny = "", true
	fake.mu.Unlock()
	v = x.start(`{"name":"grok-denied"}`)
	if done := x.wait(v.ID, "done", "failed"); done.Status != "failed" || done.Message != "xAI で許可されませんでした。" {
		t.Fatalf("%+v", done)
	}
	if len(o.rt.log()) != calls {
		t.Fatalf("%q", o.rt.log()[calls:])
	}

	// A new flow cancels the waiting one; cancel stops it.
	fake.mu.Lock()
	fake.deny, fake.pending = false, 1<<30
	fake.mu.Unlock()
	first := x.start(`{}`)
	second := x.start(`{}`)
	if code, _ := o.do("GET", "/api/oauth/xai/flows/"+first.ID, ""); code != 404 {
		t.Fatal("the old flow is still there")
	}
	if code, resp := o.do("POST", "/api/oauth/xai/flows/"+second.ID+"/cancel", `{}`); code != 200 || !strings.Contains(resp, `"canceled"`) {
		t.Fatal(code, resp)
	}
}

// The fake's xAI: the verification page approves the code and the flow
// creates the provider in the fake's gateway.
func TestFakeXAIFlow(t *testing.T) {
	shortXAIPolls(t)
	s, _, do := providerServer(t)
	x := &xaiTest{t: t, s: s, do: do}
	v := x.start(`{"name":"grok-fake"}`)
	if !strings.HasPrefix(v.CompleteURI, s.origin+"/fake/xai/device?user_code=") {
		t.Fatalf("%+v", v)
	}
	if code, body := do("GET", "/fake/xai/device?user_code="+url.QueryEscape(v.UserCode), ""); code != 200 || !strings.Contains(body, `id="approve"`) {
		t.Fatal(code, body)
	}
	if code, body := do("GET", "/fake/xai/device?user_code="+url.QueryEscape(v.UserCode)+"&decision=approve", ""); code != 200 || !strings.Contains(body, "許可しました") {
		t.Fatal(code, body)
	}
	if done := x.wait(v.ID, "done", "failed"); done.Status != "done" || done.Name != "grok-fake" || done.Refresh == nil || done.Refresh.Status != "refreshed" {
		t.Fatalf("%+v", done)
	}
	if p, ok := provider(listProviders(t, do), "grok-fake"); !ok || p.Refresh == nil || p.Refresh.Status != "refreshed" {
		t.Fatalf("%+v", p)
	}
}
