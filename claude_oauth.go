package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Claude Code with the owner's Claude account, signed in by Iwato
// (iwato-claude-subscription). Iwato signs in with Claude
// Code's public client as `claude auth login` does: the authorization code
// with PKCE S256 and state, and both of Claude Code's redirects.
//
//  1. POST /api/oauth/claude/start {name, reconnect}: opens a loopback
//     listener (127.0.0.1, a fresh port) and returns two authorization
//     URLs with the same state and challenge: one that comes back to
//     http://localhost:PORT/callback (the PC's browser), one that comes
//     back to Claude's own page, which shows CODE#STATE to copy (a phone).
//     One flow at a time; a new one cancels the last.
//  2. The browser comes back to the listener, or the owner pastes the code
//     (POST /api/oauth/claude/flows/ID/code {code}). The page polls
//     GET /api/oauth/claude/flows/ID (and may cancel with .../cancel).
//  3. Iwato exchanges the code with the redirect it came back by, creates
//     the provider (claude-subscription, -2, …) or, to reconnect, replaces
//     its credential, hands the refresh token to the gateway and forgets it
//     (oauth_grant.go), then rotates once.
//
// No token, code, state or verifier is logged or returned to the page
// (state is in the authorization URLs only).

// The constants are Claude Code's (its OAuth configuration: CLIENT_ID,
// CLAUDE_AI_AUTHORIZE_URL, TOKEN_URL, MANUAL_REDIRECT_URL); claudeTokenURL
// is the profile's token_url (TestClaudeFlow).
const (
	claudeSubscriptionProfileID = "iwato-claude-subscription"
	claudeOAuthCredentialKey    = "CLAUDE_CODE_OAUTH_TOKEN"
	claudeClientID              = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeAuthorizeURL          = "https://claude.com/cai/oauth/authorize"
	claudeTokenURL              = "https://platform.claude.com/v1/oauth/token"
	claudeManualRedirect        = "https://platform.claude.com/oauth/code/callback"
	// claudeScopes: what Claude Code asks for when it refreshes (its
	// login adds org:create_api_key, which Iwato leaves out).
	claudeScopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
)

// claudeBackend exchanges the code: claudeOAuth, or the fake's
// (fake_claude.go).
type claudeBackend interface {
	AuthorizeEndpoint() string
	ManualRedirect() string
	Exchange(ctx context.Context, code, verifier, state, redirectURI string) (oauthTokens, error)
	ClientID() string
}

type claudeOAuth struct {
	Client *http.Client
	// AuthorizeURL, TokenURL, ID and Redirect are Claude Code's.
	AuthorizeURL, TokenURL, ID, Redirect string
}

func newClaudeOAuth() *claudeOAuth {
	return &claudeOAuth{Client: &http.Client{Timeout: 30 * time.Second}, AuthorizeURL: claudeAuthorizeURL, TokenURL: claudeTokenURL, ID: claudeClientID, Redirect: claudeManualRedirect}
}

func (c *claudeOAuth) AuthorizeEndpoint() string { return c.AuthorizeURL }
func (c *claudeOAuth) ManualRedirect() string    { return c.Redirect }
func (c *claudeOAuth) ClientID() string          { return c.ID }

// Exchange trades the code as Claude Code does: a JSON body with state.
func (c *claudeOAuth) Exchange(ctx context.Context, code, verifier, state, redirectURI string) (oauthTokens, error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "code": code, "redirect_uri": redirectURI, "client_id": c.ID, "code_verifier": verifier, "state": state})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, bytes.NewReader(body))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	hc := *c.Client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("token request: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	return parseTokens(io.LimitReader(resp.Body, maxOAuthBody))
}

// claudeAuthorizeLink is the authorization URL as Claude Code builds it
// (code=true first: Claude's page then shows the code for the manual
// redirect).
func claudeAuthorizeLink(endpoint, clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {claudeScopes},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	return endpoint + "?code=true&" + q.Encode()
}

type claudeFlow struct {
	id, name  string
	reconnect bool
	// state, verifier: secrets; never logged or sent to the page.
	state, verifier         string
	loopbackURL, manualURL  string
	redirectURI, manualBack string
	port                    int
	backend                 claudeBackend
	srv                     *http.Server
	timer                   *time.Timer
	expires                 time.Time
	// status: waiting, exchanging, done, failed, expired, canceled.
	status           string
	message, warning flowText
	refresh          *RefreshView
	// ctx ends with a reset (oauthLife).
	ctx context.Context
}

type claudeFlows struct {
	mu      sync.Mutex
	current *claudeFlow
	// backend overrides the real one in tests.
	backend claudeBackend
}

func (s *Server) claudeBackend(r *http.Request) claudeBackend {
	if s.claude.backend != nil {
		return s.claude.backend
	}
	if s.fake {
		origin := s.origin
		if o := r.Header.Get("Origin"); o != "" && s.allowedOrigin(o) {
			origin = o
		}
		return fakeHooks.claude(s, origin)
	}
	return newClaudeOAuth()
}

// claudeFlowView is a flow for the page: never the code or the verifier.
type claudeFlowView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Reconnect   bool         `json:"reconnect"`
	Status      string       `json:"status"`
	Message     string       `json:"message,omitempty"`
	Warning     string       `json:"warning,omitempty"`
	LoopbackURL string       `json:"loopbackURL,omitempty"`
	ManualURL   string       `json:"manualURL,omitempty"`
	ExpiresAt   string       `json:"expiresAt"`
	Refresh     *RefreshView `json:"refresh,omitempty"`
	flowCodes
}

func (f *claudeFlow) view() claudeFlowView {
	v := claudeFlowView{ID: f.id, Name: f.name, Reconnect: f.reconnect, Status: f.status, Message: f.message.Text, Warning: f.warning.Text, flowCodes: codesOf(f.message, f.warning), ExpiresAt: f.expires.UTC().Format(time.RFC3339), Refresh: f.refresh}
	if f.status == "waiting" {
		v.LoopbackURL, v.ManualURL = f.loopbackURL, f.manualURL
	}
	return v
}

// stop closes the listener and the timer. Call with claude.mu held.
func (f *claudeFlow) stop() {
	if f.timer != nil {
		f.timer.Stop()
	}
	if srv := f.srv; srv != nil {
		f.srv = nil
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if srv.Shutdown(ctx) != nil {
				srv.Close()
			}
		}()
	}
}

func (s *Server) handleClaudeOAuth(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/oauth/claude"), "/")
	if r.Method == http.MethodGet {
		if !s.require(w, r, false) {
			return
		}
		id, ok := strings.CutPrefix(rest, "flows/")
		if !ok {
			fail(w, 404, "unknown route")
			return
		}
		s.claude.mu.Lock()
		f := s.claude.current
		if f == nil || !equal(f.id, id) {
			s.claude.mu.Unlock()
			failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
			return
		}
		v := f.view()
		s.claude.mu.Unlock()
		jsonResponse(w, 200, v)
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	if rest == "start" {
		s.claudeStart(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] != "flows" || (parts[2] != "code" && parts[2] != "cancel") {
		fail(w, 404, "unknown route")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	s.claude.mu.Lock()
	f := s.claude.current
	if f == nil || !equal(f.id, parts[1]) {
		s.claude.mu.Unlock()
		failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
		return
	}
	if parts[2] == "cancel" {
		if f.status == "waiting" {
			f.status, f.message = "canceled", coded("oauth_canceled", nil, "やめました。")
			f.stop()
		}
		v := f.view()
		s.claude.mu.Unlock()
		jsonResponse(w, 200, v)
		return
	}
	s.claude.mu.Unlock()
	code, state, err := pastedClaudeCode(body.Code)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	if status, msg := s.claudeAccept(f, code, state, "", f.manualBack); status != 200 {
		fail(w, status, msg)
		return
	}
	s.claude.mu.Lock()
	v := f.view()
	s.claude.mu.Unlock()
	jsonResponse(w, 200, v)
}

// pastedClaudeCode reads what Claude's page shows: CODE#STATE.
func pastedClaudeCode(raw string) (code, state string, err error) {
	raw = strings.TrimSpace(raw)
	code, state, ok := strings.Cut(raw, "#")
	if len(raw) > 8192 || !ok || code == "" || state == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", "", errCode("oauth_claude_paste_invalid", nil, "Claude のページに出たコード (… # … の形) をそのまま貼り付けてください")
	}
	return code, state, nil
}

// claudeAccept takes the answer once: the code (or the error) with the
// flow's state, and the redirect it came back by. It returns the HTTP
// status and a message.
func (s *Server) claudeAccept(f *claudeFlow, code, state, oauthErr, redirectURI string) (int, string) {
	s.claude.mu.Lock()
	defer s.claude.mu.Unlock()
	if f.status != "waiting" {
		return 409, "この接続の手続きは終わっています。Iwato の画面を見てください。"
	}
	if state == "" || !equal(state, f.state) {
		return 400, "state が一致しません。この接続の手続きのコードではありません。"
	}
	if oauthErr != "" {
		f.status, f.message = "failed", coded("oauth_claude_denied", map[string]any{"code": safeCode(oauthErr)}, "Claude で許可されませんでした ("+safeCode(oauthErr)+")。")
		f.stop()
		return 200, f.message.Text
	}
	if len(code) > 4096 || credentialValue(code) != nil {
		return 400, "code がありません。"
	}
	f.status, f.message = "exchanging", coded("oauth_receiving", nil, "トークンを受け取っています…")
	f.stop()
	go s.finishClaude(f, code, redirectURI)
	return 200, "許可を受け取りました。Iwato の画面に戻ってください。このタブは閉じてかまいません。"
}

// claudeCallback serves the loopback redirect of one flow.
func (s *Server) claudeCallback(f *claudeFlow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		if h, p, err := net.SplitHostPort(r.Host); err != nil || (h != "127.0.0.1" && h != "localhost") || p != strconv.Itoa(f.port) {
			http.Error(w, "invalid host", 400)
			return
		}
		q := r.URL.Query()
		code, msg := s.claudeAccept(f, q.Get("code"), q.Get("state"), q.Get("error"), f.redirectURI)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = callbackPage.Execute(w, msg)
	})
}

func (s *Server) claudeStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Reconnect bool   `json:"reconnect"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if (body.Reconnect || body.Name != "") && !providerNamePattern.MatchString(body.Name) {
		failCode(w, 400, "name_invalid", map[string]any{"max": 63}, "名前は英小文字で始まり、英小文字・数字・ハイフンの 63 文字以内にしてください")
		return
	}
	rt, hasRT := s.rt.(providerRuntime)
	if _, ok := s.rt.(providerRefresher); !ok || !hasRT {
		fail(w, 501, "providers unavailable")
		return
	}
	ctx, cancel := timeout(r.Context(), 45*time.Second)
	defer cancel()
	list, _, err := rt.ListProviders(ctx)
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == body.Name })
	switch {
	case body.Reconnect && i < 0:
		failCode(w, 404, "provider_missing", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" がありません")
		return
	case body.Reconnect && providerBase(list[i]) != claudeSubscriptionProfileID:
		failCode(w, 409, "oauth_not_claude", map[string]any{"name": body.Name}, body.Name+" は Claude Code (Claude でログイン) のプロバイダーではありません")
		return
	case !body.Reconnect && body.Name != "" && i >= 0:
		failCode(w, 409, "oauth_provider_exists", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" はもうあります。再接続はその行の「再接続」から")
		return
	}
	backend := s.claudeBackend(r)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		failCode(w, 500, "oauth_listen_failed", map[string]any{"detail": err.Error()}, "戻り先の待ち受けを開けません: "+err.Error())
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f := &claudeFlow{id: randomHex(16), name: body.Name, reconnect: body.Reconnect, state: randomHex(32), port: port, backend: backend, status: "waiting",
		redirectURI: fmt.Sprintf("http://localhost:%d/callback", port), manualBack: backend.ManualRedirect(), expires: time.Now().Add(oauthFlowTTL), ctx: s.oauthLife()}
	var challenge string
	f.verifier, challenge = pkce()
	f.loopbackURL = claudeAuthorizeLink(backend.AuthorizeEndpoint(), backend.ClientID(), f.redirectURI, f.state, challenge)
	f.manualURL = claudeAuthorizeLink(backend.AuthorizeEndpoint(), backend.ClientID(), f.manualBack, f.state, challenge)
	f.srv = &http.Server{Handler: s.claudeCallback(f), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	s.claude.mu.Lock()
	if old := s.claude.current; old != nil && old.status == "waiting" {
		old.status, old.message = "canceled", coded("oauth_superseded", nil, "新しい接続を始めたのでやめました。")
		old.stop()
	}
	s.claude.current = f
	srv := f.srv
	f.timer = time.AfterFunc(oauthFlowTTL, func() {
		s.claude.mu.Lock()
		defer s.claude.mu.Unlock()
		if f.status == "waiting" {
			f.status, f.message = "expired", coded("oauth_claude_expired", map[string]any{"minutes": int(oauthFlowTTL.Minutes())}, fmt.Sprintf("%d 分以内に許可されなかったので、やめました。もう一度始めてください。", int(oauthFlowTTL.Minutes())))
			f.stop()
		}
	})
	v := f.view()
	s.claude.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	log.Printf("claude oauth: %s: waiting on %s for %s", nameLabel(body.Name), f.redirectURI, oauthFlowTTL)
	jsonResponse(w, 200, v)
}

// finishClaude exchanges the code and gives the grant to the gateway.
func (s *Server) finishClaude(f *claudeFlow, code, redirectURI string) {
	result := func(status string, message, warning flowText, refresh *RefreshView) {
		s.claude.mu.Lock()
		f.status, f.message, f.warning, f.refresh = status, message, warning, refresh
		s.claude.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	tokens, err := f.backend.Exchange(ctx, code, f.verifier, f.state, redirectURI)
	if err != nil {
		log.Printf("claude oauth: %s: %v", nameLabel(f.name), english(err))
		result("failed", coded("oauth_token_failed", map[string]any{"detail": err.Error()}, "トークンを受け取れませんでした: "+err.Error()), flowText{}, nil)
		return
	}
	g := oauthGrant{log: "claude oauth", name: f.name, reconnect: f.reconnect, profile: claudeSubscriptionProfileID, key: claudeOAuthCredentialKey, clientID: f.backend.ClientID(),
		noRefresh:   coded("oauth_claude_no_refresh", nil, "refresh token がありません。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
		refreshCode: "oauth_first_refresh_claude",
		refreshHint: "Anthropic がゲートウェイの更新の形 (フォーム形式) を受け付けていないかもしれません。",
		named: func(name string) {
			s.claude.mu.Lock()
			f.name = name
			s.claude.mu.Unlock()
		}}
	result(s.handOverGrant(ctx, g, tokens))
}
