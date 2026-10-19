//go:build uitest

package main

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The fake's Atlassian: discovery, registration and the code exchange
// without a network, and an authorization page Iwato serves itself
// (/fake/atlassian/authorize) that redirects to the flow's loopback
// listener as Atlassian would.

type fakeOAuth struct{ origin string }

func (d fakeOAuth) Discover(context.Context) (oauthMetadata, error) {
	return oauthMetadata{Resource: atlassianResource, Issuer: d.origin + "/fake/atlassian", AuthorizationEndpoint: d.origin + "/fake/atlassian/authorize",
		TokenEndpoint: atlassianTokenURL, RegistrationEndpoint: d.origin + "/fake/atlassian/register", Scopes: atlassianScopes}, nil
}

func (fakeOAuth) Register(context.Context, oauthMetadata, string) (string, error) {
	return "fake-client-" + randomHex(6), nil
}

func (fakeOAuth) Exchange(_ context.Context, _ oauthMetadata, _, code, verifier, _ string) (oauthTokens, error) {
	if code != "fake-code" || verifier == "" {
		return oauthTokens{}, errors.New("token request: HTTP 400: invalid_grant")
	}
	return oauthTokens{AccessToken: "fake-access-" + randomHex(8), RefreshToken: "fake-refresh-" + randomHex(8), ExpiresIn: 3600, Scope: "offline_access read:me",
		Fields: []string{"access_token", "expires_in", "refresh_token", "scope", "token_type"}, Lifetimes: map[string]int64{"expires_in": 3600}}, nil
}

var fakeRedirectPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]{1,5}/callback$`)

var fakeAuthorizePage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Atlassian (テスト用の偽ページ)</title>
<body style="font:16px system-ui,sans-serif;margin:2em;max-width:36em">
<h1 style="font-size:1.3em">Atlassian (テスト用の偽ページ)</h1>
<p>Iwato が Atlassian の MCP へのアクセスを求めています。スコープ:</p>
<p><code>{{.Scope}}</code></p>
<p><a id="approve" href="{{.Approve}}" style="display:inline-block;padding:.5em 1em;background:#0c66e4;color:#fff;border-radius:4px;text-decoration:none">許可する</a>
<a id="deny" href="{{.Deny}}" style="margin-left:1em">拒否する</a></p>
</body>`))

// handleFakeAuthorize is the fake's authorization page (serve -fake only).
func (s *Server) handleFakeAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if r.Method != http.MethodGet || !fakeRedirectPattern.MatchString(redirect) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(state) || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "invalid authorization request", 400)
		return
	}
	back := func(v url.Values) template.URL {
		v.Set("state", state)
		return template.URL(redirect + "?" + v.Encode())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = fakeAuthorizePage.Execute(w, map[string]any{
		"Scope":   strings.Join(strings.Fields(q.Get("scope")), " "),
		"Approve": back(url.Values{"code": {"fake-code"}}),
		"Deny":    back(url.Values{"error": {"access_denied"}}),
	})
}
