//go:build uitest

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// auth.openai.com as Codex's device login uses it: the user code (the
// interval as a string), the poll (403 while pending) answering a code and
// its verifier, and the code exchange as a form.
func newFakeOpenAI(t *testing.T, account string) (*httptest.Server, *int) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["client_id"] != codexClientID {
				jsonResponse(w, 400, map[string]string{"error": "invalid_client"})
				return
			}
			jsonResponse(w, 200, map[string]string{"device_auth_id": "dev-SECRET", "user_code": "ABCD-1234", "interval": "1"})
		case "/api/accounts/deviceauth/token":
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["device_auth_id"] != "dev-SECRET" || body["user_code"] != "ABCD-1234" {
				jsonResponse(w, 400, map[string]string{"error": "bad"})
				return
			}
			if polls++; polls < 2 {
				w.WriteHeader(403)
				return
			}
			jsonResponse(w, 200, map[string]string{"authorization_code": "code-SECRET", "code_verifier": "ver-SECRET", "code_challenge": "x"})
		case "/oauth/token":
			if r.ParseForm() != nil || r.PostForm.Get("code") != "code-SECRET" || r.PostForm.Get("code_verifier") != "ver-SECRET" ||
				r.PostForm.Get("redirect_uri") != "http://"+r.Host+"/deviceauth/callback" || r.PostForm.Get("client_id") != codexClientID {
				jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
			jsonResponse(w, 200, map[string]any{"access_token": "at-SECRET", "refresh_token": "rt-SECRET", "id_token": "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SECRET", "expires_in": 864000})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

// The ChatGPT sign-in creates the provider with the access token and the
// account the id token names; a sign-in without an account creates nothing.
func TestCodexFlowCreatesTheProvider(t *testing.T) {
	shortXAIPolls(t)
	o := newOAuthTest(t)
	srv, _ := newFakeOpenAI(t, "acct-1")
	o.s.codex.backend = &codexOAuth{Client: srv.Client(), Issuer: srv.URL, ID: codexClientID}
	x := &xaiTest{t: t, s: o.s, do: o.do, route: "codex"}
	v := x.start(`{"name":""}`)
	if v.Status != "waiting" || v.UserCode != "ABCD-1234" || v.VerificationURI != srv.URL+"/codex/device" {
		t.Fatalf("%+v", v)
	}
	if done := x.wait(v.ID, "done", "failed"); done.Status != "done" || done.Name != "codex-subscription" {
		t.Fatalf("%+v", done)
	}
	want := []string{"create codex-subscription iwato-codex-subscription " + codexOAuthCredentialKey + "," + codexAccountKey, "configure codex-subscription " + codexOAuthCredentialKey, "rotate codex-subscription"}
	if got := o.rt.log(); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if m := o.rt.material[0]; m["client_id"] != codexClientID || m["refresh_token"] != "rt-SECRET" {
		t.Fatal("material", m["client_id"])
	}
	if strings.Contains(o.logs.String(), "SECRET") {
		t.Fatal("a value reached the log")
	}

	srv2, _ := newFakeOpenAI(t, "")
	o.s.codex.backend = &codexOAuth{Client: srv2.Client(), Issuer: srv2.URL, ID: codexClientID}
	v = x.start(`{"name":"no-account"}`)
	if done := x.wait(v.ID, "done", "failed"); done.Status != "failed" || done.MessageCode != "oauth_codex_no_account" {
		t.Fatalf("%+v", done)
	}
	if len(o.rt.log()) != len(want) {
		t.Fatalf("%q", o.rt.log())
	}
}
