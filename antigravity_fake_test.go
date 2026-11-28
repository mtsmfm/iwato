//go:build uitest

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Google's token endpoint for agy's code exchange: a form with the
// client's secret.
type fakeGoogle struct {
	mu    sync.Mutex
	forms []url.Values
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
		http.Error(w, "bad", 400)
		return
	}
	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	f.mu.Unlock()
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("client_id") != antigravityClientID || r.PostForm.Get("client_secret") != antigravityClientSecret ||
		r.PostForm.Get("code") != "4/good-code" || r.PostForm.Get("code_verifier") == "" || r.PostForm.Get("redirect_uri") != antigravityRedirect {
		jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	jsonResponse(w, 200, map[string]any{"access_token": "ya29.at-SECRET", "refresh_token": "1//rt-SECRET", "expires_in": 3599, "token_type": "Bearer", "scope": antigravityScopes, "id_token": "x.y.z"})
}

// The pasted code (alone, or the page's address with its state) is
// exchanged with agy's client and secret; the gateway gets the secret as
// secret material; a wrong state or another profile's provider is refused.
func TestAntigravityFlow(t *testing.T) {
	o := newOAuthTest(t)
	g := &fakeGoogle{}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	a := newAntigravityOAuth()
	a.TokenURL = srv.URL
	o.s.antigravity.backend = a
	c := &claudeTest{oauthTest: o, route: "antigravity"}
	v := c.start(`{}`)
	q := mustQuery(t, v.ManualURL)
	if v.LoopbackURL != "" || !strings.HasPrefix(v.ManualURL, antigravityAuthorizeURL+"?") || q.Get("client_id") != antigravityClientID || q.Get("redirect_uri") != antigravityRedirect ||
		q.Get("scope") != antigravityScopes || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) != 64 {
		t.Fatal(v.ManualURL)
	}
	paste := func(id, code string) (int, string) {
		b, _ := json.Marshal(map[string]string{"code": code})
		return c.do("POST", "/api/oauth/antigravity/flows/"+id+"/code", string(b))
	}
	for _, bad := range []string{"", "4/good code", antigravityRedirect + "?code=4/good-code&state=" + strings.Repeat("0", 64), antigravityRedirect + "?state=" + q.Get("state")} {
		if code, _ := paste(v.ID, bad); code != 400 {
			t.Fatal("taken:", bad)
		}
	}
	if code, resp := paste(v.ID, " 4/good-code\n"); code != 200 {
		t.Fatal(code, resp)
	}
	done := c.wait(v.ID)
	if done.Status != "done" || done.Name != "antigravity-subscription" || done.ManualURL != "" {
		t.Fatalf("%+v", done)
	}
	if got := o.rt.log(); !slices.Equal(got, []string{"create antigravity-subscription iwato-antigravity-subscription " + antigravityCredentialKey, "configure antigravity-subscription " + antigravityCredentialKey, "rotate antigravity-subscription"}) {
		t.Fatalf("%q", got)
	}
	if m := o.rt.material[0]; m["client_id"] != antigravityClientID || m["client_secret"] != antigravityClientSecret || m["refresh_token"] != "1//rt-SECRET" {
		t.Fatal("material", m["client_id"])
	}
	if k := o.rt.secretKeys[0]; !slices.Equal(k, []string{"refresh_token", "client_secret"}) {
		t.Fatal(k)
	}

	// Reconnecting with the page's address.
	v = c.start(`{"name":"antigravity-subscription","reconnect":true}`)
	state := mustQuery(t, v.ManualURL).Get("state")
	if code, resp := paste(v.ID, antigravityRedirect+"?state="+state+"&code="+url.QueryEscape("4/good-code")); code != 200 {
		t.Fatal(code, resp)
	}
	if done := c.wait(v.ID); done.Status != "done" || done.Message != "再接続しました。" {
		t.Fatalf("%+v", done)
	}
	if code, _ := c.do("POST", "/api/oauth/antigravity/start", `{"name":"claude","reconnect":true}`); code != 409 {
		t.Fatal("reconnected another profile's provider")
	}
	if strings.Contains(o.logs.String(), "SECRET") || strings.Contains(o.logs.String(), antigravityClientSecret) {
		t.Fatal("a value reached the log")
	}
	p, _ := profileByID(antigravityProfileID)
	if r := p.proto().Credentials[0].Refresh; r.TokenUrl != antigravityTokenURL || p.Credentials[0].Env[0] != antigravityCredentialKey {
		t.Fatal("the constants and the YAML differ")
	}
}
