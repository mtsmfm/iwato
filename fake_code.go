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

// The fake's pasted-code sign-ins (Claude, Antigravity): the code exchange
// without a network, and an authorization page Iwato serves itself
// (/fake/ID/authorize). It redirects to the flow's loopback listener, or,
// for the manual redirect (/fake/ID/code), shows the code to paste as the
// service's page does: CODE#STATE for Claude, the code alone for
// Antigravity.

type fakeCodeOAuth struct{ origin, service string }

func (d fakeCodeOAuth) AuthorizeEndpoint() string {
	return d.origin + "/fake/" + d.service + "/authorize"
}
func (d fakeCodeOAuth) ManualRedirect() string { return d.origin + "/fake/" + d.service + "/code" }
func (d fakeCodeOAuth) ClientID() string {
	if d.service == "antigravity" {
		return antigravityClientID
	}
	return claudeClientID
}

func (fakeCodeOAuth) Exchange(_ context.Context, code, verifier, state, _ string) (oauthTokens, error) {
	if code != "fake-code" || verifier == "" || state == "" {
		return oauthTokens{}, errors.New("token request: HTTP 400: invalid_grant")
	}
	return oauthTokens{AccessToken: "fake-access-" + randomHex(8), RefreshToken: "fake-refresh-" + randomHex(8), ExpiresIn: 28800,
		Fields: []string{"access_token", "expires_in", "refresh_token", "scope", "token_type"}, Lifetimes: map[string]int64{"expires_in": 28800}}, nil
}

var (
	fakeCodeLoopback = regexp.MustCompile(`^http://localhost:[0-9]{1,5}/callback$`)
	fakeCodeState    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var fakeCodePage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Name}} (テスト用の偽ページ)</title>
<body style="font:16px system-ui,sans-serif;margin:2em;max-width:36em">
<h1 style="font-size:1.3em">{{.Name}} (テスト用の偽ページ)</h1>
{{if .Denied}}<p id="result">拒否しました。</p>
{{else if .Code}}<p>このコードを Iwato に貼り付けてください:</p>
<p><code id="code" style="font-size:1.1em;word-break:break-all">{{.Code}}</code></p>
{{else}}<p>{{.Name}} がアカウントへのアクセスを求めています。</p>
<p><a id="approve" href="{{.Approve}}" style="display:inline-block;padding:.5em 1em;background:#c96442;color:#fff;border-radius:4px;text-decoration:none">許可する</a>
<a id="deny" href="{{.Deny}}" style="margin-left:1em">拒否する</a></p>{{end}}
</body>`))

// handleFakeCode serves the fake's authorization page and its manual
// redirect page (serve -fake only).
func (s *Server) handleFakeCode(w http.ResponseWriter, r *http.Request) {
	service, page, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/fake/"), "/")
	name := map[string]string{"claude": "Claude", "antigravity": "Google"}[service]
	q := r.URL.Query()
	state := q.Get("state")
	if r.Method != http.MethodGet || name == "" || !fakeCodeState.MatchString(state) {
		http.Error(w, "invalid request", 400)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if page == "code" {
		code := q.Get("code")
		if code == "" {
			_ = fakeCodePage.Execute(w, map[string]any{"Name": name, "Denied": true})
			return
		}
		if service == "claude" {
			code += "#" + state
		}
		_ = fakeCodePage.Execute(w, map[string]any{"Name": name, "Code": code})
		return
	}
	redirect := q.Get("redirect_uri")
	ok := service == "claude" && fakeCodeLoopback.MatchString(redirect)
	if u, err := url.Parse(redirect); err == nil && !ok {
		ok = u.Path == "/fake/"+service+"/code" && u.RawQuery == "" && s.allowedOrigin(u.Scheme+"://"+u.Host)
	}
	asked := q.Get("code") == "true"
	if service == "antigravity" {
		asked = q.Get("access_type") == "offline" && q.Get("client_id") == antigravityClientID
	}
	if !ok || q.Get("code_challenge_method") != "S256" || !asked {
		http.Error(w, "invalid authorization request", 400)
		return
	}
	back := func(v url.Values) template.URL {
		v.Set("state", state)
		return template.URL(redirect + "?" + v.Encode())
	}
	_ = fakeCodePage.Execute(w, map[string]any{"Name": name, "Approve": back(url.Values{"code": {"fake-code"}}), "Deny": back(url.Values{"error": {"access_denied"}})})
}
