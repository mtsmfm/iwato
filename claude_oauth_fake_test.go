//go:build uitest

package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func newClaudeTest(t *testing.T) *claudeTest {
	o := newOAuthTest(t)
	f := &fakeClaude{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := newClaudeOAuth()
	c.TokenURL = srv.URL
	o.s.claude.backend = c
	return &claudeTest{oauthTest: o, fake: f}
}

// The fake: Iwato's own pages stand in for Claude's, through the manual
// redirect.
func TestFakeClaudeFlow(t *testing.T) {
	s, _, do := providerServer(t)
	code, resp := do("POST", "/api/oauth/claude/start", `{"name":"claude-fake"}`)
	var v claudeFlowView
	if code != 200 || json.Unmarshal([]byte(resp), &v) != nil || !strings.HasPrefix(v.ManualURL, s.origin+"/fake/claude/authorize?") {
		t.Fatal(code, resp)
	}
	u, _ := url.Parse(v.ManualURL)
	code, page := do("GET", u.RequestURI(), "")
	if code != 200 || !strings.Contains(page, `id="approve"`) {
		t.Fatal(code, page)
	}
	q := u.Query()
	code, page = do("GET", "/fake/claude/code?"+url.Values{"code": {"fake-code"}, "state": {q.Get("state")}}.Encode(), "")
	if code != 200 || !strings.Contains(page, "fake-code#"+q.Get("state")) {
		t.Fatal(code, page)
	}
	b, _ := json.Marshal(map[string]string{"code": "fake-code#" + q.Get("state")})
	if code, resp := do("POST", "/api/oauth/claude/flows/"+v.ID+"/code", string(b)); code != 200 {
		t.Fatal(code, resp)
	}
	for i := 0; ; i++ {
		_, resp := do("GET", "/api/oauth/claude/flows/"+v.ID, "")
		if strings.Contains(resp, `"done"`) {
			break
		}
		if i > 1000 {
			t.Fatal(resp)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p, ok := provider(listProviders(t, do), "claude-fake"); !ok || p.Refresh == nil || p.Refresh.Status != "refreshed" {
		t.Fatalf("%+v", p)
	}
}

type claudeTest struct {
	*oauthTest
	fake *fakeClaude
}

func (c *claudeTest) start(body string) claudeFlowView {
	c.t.Helper()
	code, resp := c.do("POST", "/api/oauth/claude/start", body)
	var v claudeFlowView
	if code != 200 || json.Unmarshal([]byte(resp), &v) != nil {
		c.t.Fatal(code, resp)
	}
	return v
}

func (c *claudeTest) wait(id string) claudeFlowView {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, resp := c.do("GET", "/api/oauth/claude/flows/"+id, "")
		var v claudeFlowView
		if code != 200 || json.Unmarshal([]byte(resp), &v) != nil || strings.Contains(resp, "SECRET") {
			c.t.Fatal(code, resp)
		}
		if v.Status != "waiting" && v.Status != "exchanging" {
			return v
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("%+v", v)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Both of Claude Code's redirects: the browser coming back to the loopback
// listener, and the CODE#STATE a phone pastes, each exchanged with the
// redirect it came by; a wrong state is refused; the gateway gets Claude
// Code's client and the refresh token.
func TestClaudeFlow(t *testing.T) {
	c := newClaudeTest(t)
	v := c.start(`{}`)
	lb, err := url.Parse(v.LoopbackURL)
	if err != nil {
		t.Fatal(err)
	}
	q := lb.Query()
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(v.LoopbackURL, claudeAuthorizeURL+"?code=true&") || q.Get("client_id") != claudeClientID || q.Get("scope") != claudeScopes ||
		q.Get("code_challenge_method") != "S256" || !strings.HasPrefix(redirect, "http://localhost:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatal(v.LoopbackURL)
	}
	mq := mustQuery(t, v.ManualURL)
	if mq.Get("redirect_uri") != claudeManualRedirect || mq.Get("state") != q.Get("state") || mq.Get("code_challenge") != q.Get("code_challenge") {
		t.Fatal(v.ManualURL)
	}
	if code, _ := callbackGet(t, redirect, "code=good-code&state="+strings.Repeat("0", 64)); code != 400 {
		t.Fatal("a wrong state was taken")
	}
	if code, page := callbackGet(t, redirect, "code=good-code&state="+q.Get("state")); code != 200 || !strings.Contains(page, "許可を受け取りました") {
		t.Fatal(code, page)
	}
	done := c.wait(v.ID)
	if done.Status != "done" || done.Name != "claude-subscription" || done.LoopbackURL != "" || done.Refresh == nil {
		t.Fatalf("%+v", done)
	}
	if got := c.rt.log(); !slices.Equal(got, []string{"create claude-subscription iwato-claude-subscription-for-claude-subscription CLAUDE_CODE_OAUTH_TOKEN", "configure claude-subscription CLAUDE_CODE_OAUTH_TOKEN", "rotate claude-subscription"}) {
		t.Fatalf("%q", got)
	}
	if m := c.rt.material[0]; m["client_id"] != claudeClientID || m["refresh_token"] != "rt-SECRET" {
		t.Fatal("material", m["client_id"])
	}

	// Reconnecting from a phone: the pasted code goes with the manual
	// redirect.
	v = c.start(`{"name":"claude-subscription","reconnect":true}`)
	paste := func(code string) (int, string) {
		b, _ := json.Marshal(map[string]string{"code": code})
		return c.do("POST", "/api/oauth/claude/flows/"+v.ID+"/code", string(b))
	}
	state := mustQuery(t, v.ManualURL).Get("state")
	for _, bad := range []string{"good-code", "good-code#" + strings.Repeat("0", 64), "good code#" + state} {
		if code, _ := paste(bad); code != 400 {
			t.Fatal("taken:", bad)
		}
	}
	if code, resp := paste(" good-code#" + state + "\n"); code != 200 {
		t.Fatal(code, resp)
	}
	if done := c.wait(v.ID); done.Status != "done" || done.Message != "再接続しました。" {
		t.Fatalf("%+v", done)
	}
	c.fake.mu.Lock()
	last := c.fake.bodies[len(c.fake.bodies)-1]
	c.fake.mu.Unlock()
	if last["redirect_uri"] != claudeManualRedirect || last["state"] != state {
		t.Fatal(last["redirect_uri"])
	}
	if code, _ := c.do("POST", "/api/oauth/claude/start", `{"name":"grok-subscription","reconnect":true}`); code != 409 {
		t.Fatal("reconnected another profile's provider")
	}
	if strings.Contains(c.logs.String(), "SECRET") {
		t.Fatal("a value reached the log")
	}
	p, _ := profileByID(claudeSubscriptionProfileID)
	if r := p.proto().Credentials[0].Refresh; r.TokenUrl != claudeTokenURL || strings.Join(r.Scopes, " ") != claudeScopes || p.Credentials[0].Env[0] != claudeOAuthCredentialKey {
		t.Fatal("the constants and the YAML differ")
	}
}
