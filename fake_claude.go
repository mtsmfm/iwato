//go:build uitest

package main

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
)

// The fake's Claude sign-in: the code exchange without a network, and an
// authorization page Iwato serves itself (/fake/claude/authorize). It
// redirects to the flow's loopback listener, or, for the manual redirect
// (/fake/claude/code), shows CODE#STATE to paste, as Claude's pages do.

type fakeClaudeOAuth struct{ origin string }

func (d fakeClaudeOAuth) AuthorizeEndpoint() string { return d.origin + "/fake/claude/authorize" }
func (d fakeClaudeOAuth) ManualRedirect() string    { return d.origin + "/fake/claude/code" }
func (fakeClaudeOAuth) ClientID() string            { return claudeClientID }

func (fakeClaudeOAuth) Exchange(_ context.Context, code, verifier, state, _ string) (oauthTokens, error) {
	if code != "fake-code" || verifier == "" || state == "" {
		return oauthTokens{}, errors.New("token request: HTTP 400: invalid_grant")
	}
	return oauthTokens{AccessToken: "fake-access-" + randomHex(8), RefreshToken: "fake-refresh-" + randomHex(8), ExpiresIn: 28800, Scope: claudeScopes,
		Fields: []string{"access_token", "expires_in", "refresh_token", "scope", "token_type"}, Lifetimes: map[string]int64{"expires_in": 28800}}, nil
}

var (
	fakeClaudeLoopback = regexp.MustCompile(`^http://localhost:[0-9]{1,5}/callback$`)
	fakeClaudeState    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var fakeClaudePage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Claude (テスト用の偽ページ)</title>
<body style="font:16px system-ui,sans-serif;margin:2em;max-width:36em">
<h1 style="font-size:1.3em">Claude (テスト用の偽ページ)</h1>
{{if .Denied}}<p id="result">拒否しました。</p>
{{else if .Code}}<p>このコードを Iwato に貼り付けてください:</p>
<p><code id="code" style="font-size:1.1em;word-break:break-all">{{.Code}}</code></p>
{{else}}<p>Claude Code がアカウントへのアクセスを求めています。</p>
<p><a id="approve" href="{{.Approve}}" style="display:inline-block;padding:.5em 1em;background:#c96442;color:#fff;border-radius:4px;text-decoration:none">許可する</a>
<a id="deny" href="{{.Deny}}" style="margin-left:1em">拒否する</a></p>{{end}}
</body>`))

// handleFakeClaude serves the fake's authorization page and its manual
// redirect page (serve -fake only).
func (s *Server) handleFakeClaude(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if r.Method != http.MethodGet || !fakeClaudeState.MatchString(state) {
		http.Error(w, "invalid request", 400)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Path == "/fake/claude/code" {
		if q.Get("code") == "" {
			_ = fakeClaudePage.Execute(w, map[string]any{"Denied": true})
			return
		}
		_ = fakeClaudePage.Execute(w, map[string]any{"Code": q.Get("code") + "#" + state})
		return
	}
	redirect := q.Get("redirect_uri")
	ok := fakeClaudeLoopback.MatchString(redirect)
	if u, err := url.Parse(redirect); err == nil && !ok {
		ok = u.Path == "/fake/claude/code" && u.RawQuery == "" && s.allowedOrigin(u.Scheme+"://"+u.Host)
	}
	if !ok || q.Get("code_challenge_method") != "S256" || q.Get("code") != "true" {
		http.Error(w, "invalid authorization request", 400)
		return
	}
	back := func(v url.Values) template.URL {
		v.Set("state", state)
		return template.URL(redirect + "?" + v.Encode())
	}
	_ = fakeClaudePage.Execute(w, map[string]any{"Approve": back(url.Values{"code": {"fake-code"}}), "Deny": back(url.Values{"error": {"access_denied"}})})
}
