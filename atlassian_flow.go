package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
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

// The owner's OAuth flow for an Atlassian provider (atlassian.go):
//
//  1. GET  /api/oauth/atlassian: discovery; the scopes to choose from.
//  2. POST /api/oauth/atlassian/start {name, scopes, reconnect}: registers
//     a client for a fresh loopback listener and returns the authorization
//     URL. One flow at a time; a new one cancels the last.
//  3. The browser comes back to http://127.0.0.1:PORT/callback (Iwato's
//     own host), or the owner pastes that URL from a phone
//     (POST /api/oauth/atlassian/flows/ID/callback {url}). The listener
//     takes the first request with the flow's state and closes; it also
//     closes after oauthFlowTTL.
//  4. Iwato exchanges the code, creates the provider (named by the owner,
//     or atlassian, atlassian-2, …: providers_names.go) (or, to reconnect,
//     replaces its credential), hands the refresh token to the gateway
//     (ConfigureRefresh) and forgets it, then rotates once to see that
//     the gateway can refresh. The page polls
//     GET /api/oauth/atlassian/flows/ID.

// oauthFlowTTL bounds a flow's wait for the owner's authorization.
var oauthFlowTTL = 5 * time.Minute

type oauthFlow struct {
	id, name  string
	reconnect bool
	scopes    []string
	// state is the CSRF value; verifier the PKCE secret. Never logged,
	// never sent to the page (state is in the authorization URL only).
	state, verifier, clientID, redirectURI, authorizeURL string
	md                                                   oauthMetadata
	backend                                              oauthBackend
	port                                                 int
	srv                                                  *http.Server
	timer                                                *time.Timer
	expires                                              time.Time
	// status: waiting, exchanging, done, failed, expired, canceled.
	status           string
	message, warning flowText
	refresh          *RefreshView
	// ctx ends with a reset (oauthLife).
	ctx context.Context
}

type oauthFlows struct {
	mu      sync.Mutex
	current *oauthFlow
	// backend and profile override the real ones in tests.
	backend oauthBackend
	profile string
}

func (s *Server) oauthBackend(r *http.Request) oauthBackend {
	if s.oauth.backend != nil {
		return s.oauth.backend
	}
	if s.fake {
		origin := s.origin
		if o := r.Header.Get("Origin"); o != "" && s.allowedOrigin(o) {
			origin = o
		}
		return fakeHooks.oauth(s, origin)
	}
	return newAtlassianOAuth()
}

func (s *Server) oauthProfile() string {
	if s.oauth.profile != "" {
		return s.oauth.profile
	}
	return atlassianProfileID
}

// flowView is a flow for the page: never the state, verifier or code.
type flowView struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Reconnect    bool         `json:"reconnect"`
	Status       string       `json:"status"`
	Message      string       `json:"message,omitempty"`
	Warning      string       `json:"warning,omitempty"`
	AuthorizeURL string       `json:"authorizeURL,omitempty"`
	RedirectURI  string       `json:"redirectURI"`
	ExpiresAt    string       `json:"expiresAt"`
	Scopes       []string     `json:"scopes"`
	Refresh      *RefreshView `json:"refresh,omitempty"`
	Guide        string       `json:"guide"`
	flowCodes
}

func (f *oauthFlow) view() flowView {
	v := flowView{ID: f.id, Name: f.name, Reconnect: f.reconnect, Status: f.status, Message: f.message.Text, Warning: f.warning.Text, flowCodes: codesOf(f.message, f.warning), RedirectURI: f.redirectURI, ExpiresAt: f.expires.UTC().Format(time.RFC3339), Scopes: f.scopes, Refresh: f.refresh, Guide: atlassianMCPCommand}
	if f.status == "waiting" {
		v.AuthorizeURL = f.authorizeURL
	}
	return v
}

// stop closes the listener (after the response in flight) and the timer.
// Call with oauth.mu held.
func (f *oauthFlow) stop() {
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

func (s *Server) handleOAuth(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/oauth/atlassian"), "/")
	if r.Method == http.MethodGet {
		if !s.require(w, r, false) {
			return
		}
		if rest == "" {
			s.oauthDiscover(w, r)
			return
		}
		id, ok := strings.CutPrefix(rest, "flows/")
		if !ok {
			fail(w, 404, "unknown route")
			return
		}
		s.oauth.mu.Lock()
		f := s.oauth.current
		if f == nil || !equal(f.id, id) {
			s.oauth.mu.Unlock()
			failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
			return
		}
		v := f.view()
		s.oauth.mu.Unlock()
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
		s.oauthStart(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] != "flows" || (parts[2] != "callback" && parts[2] != "cancel") {
		fail(w, 404, "unknown route")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	s.oauth.mu.Lock()
	f := s.oauth.current
	if f == nil || !equal(f.id, parts[1]) {
		s.oauth.mu.Unlock()
		failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
		return
	}
	if parts[2] == "cancel" {
		if f.status == "waiting" {
			f.status, f.message = "canceled", coded("oauth_canceled", nil, "やめました。")
			f.stop()
		}
		v := f.view()
		s.oauth.mu.Unlock()
		jsonResponse(w, 200, v)
		return
	}
	s.oauth.mu.Unlock()
	q, err := pastedCallback(body.URL, f.port)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	code, msg := s.oauthAccept(f, q)
	if code != 200 {
		fail(w, code, msg)
		return
	}
	s.oauth.mu.Lock()
	v := f.view()
	s.oauth.mu.Unlock()
	jsonResponse(w, 200, v)
}

// pastedCallback reads the redirect URL the owner pasted: the flow's own
// loopback redirect, nothing else.
func pastedCallback(raw string, port int) (url.Values, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 8192 {
		return nil, errCode("oauth_url_too_long", nil, "URL が長すぎます")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Path != "/callback" || u.User != nil {
		return nil, errCode("oauth_paste_invalid", nil, "ブラウザーのアドレス欄の URL (http://127.0.0.1:…/callback?…) をそのまま貼り付けてください")
	}
	host, p, err := net.SplitHostPort(u.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost") || p != strconv.Itoa(port) {
		return nil, errCode("oauth_paste_other", map[string]any{"port": port}, "この接続の戻り先 (http://127.0.0.1:"+strconv.Itoa(port)+"/callback) の URL ではありません")
	}
	return u.Query(), nil
}

// oauthAccept takes the authorization server's answer once: the code (or
// error) with the flow's state. It returns the HTTP status and a message.
func (s *Server) oauthAccept(f *oauthFlow, q url.Values) (int, string) {
	s.oauth.mu.Lock()
	defer s.oauth.mu.Unlock()
	if f.status != "waiting" {
		return 409, "この接続の手続きは終わっています。Iwato の画面を見てください。"
	}
	if st := q.Get("state"); st == "" || !equal(st, f.state) {
		return 400, "state が一致しません。この接続の手続きの URL ではありません。"
	}
	if e := q.Get("error"); e != "" {
		f.status, f.message = "failed", coded("oauth_atlassian_denied", map[string]any{"code": safeCode(e)}, "Atlassian で許可されませんでした ("+safeCode(e)+")。")
		f.stop()
		return 200, f.message.Text
	}
	code := q.Get("code")
	if len(code) > 4096 || credentialValue(code) != nil {
		return 400, "code がありません。"
	}
	f.status, f.message = "exchanging", coded("oauth_receiving", nil, "トークンを受け取っています…")
	f.stop()
	go s.finishOAuth(f, code)
	return 200, "許可を受け取りました。Iwato の画面に戻ってください。このタブは閉じてかまいません。"
}

var callbackPage = template.Must(template.New("").Parse(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Iwato</title><body style="font:16px system-ui,sans-serif;margin:2em"><p>{{.}}</p></body>`))

// callbackHandler serves the loopback redirect of one flow.
func (s *Server) callbackHandler(f *oauthFlow) http.Handler {
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
		code, msg := s.oauthAccept(f, r.URL.Query())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = callbackPage.Execute(w, msg)
	})
}

func (s *Server) oauthDiscover(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeout(r.Context(), 30*time.Second)
	defer cancel()
	md, err := s.oauthBackend(r).Discover(ctx)
	if err != nil {
		log.Printf("atlassian oauth: discovery: %v", english(err))
		failErr(w, 502, discoveryError(err))
		return
	}
	type scope struct {
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	defaults := defaultAtlassianScopes(md.Scopes)
	scopes := []scope{}
	for _, n := range defaults {
		scopes = append(scopes, scope{n, true})
	}
	for _, n := range md.Scopes {
		if !slices.Contains(defaults, n) {
			scopes = append(scopes, scope{n, false})
		}
	}
	jsonResponse(w, 200, map[string]any{"resource": md.Resource, "issuer": md.Issuer, "scopes": scopes, "guide": atlassianMCPCommand})
}

func discoveryMessage(err error) string { return discoveryError(err).Error() }

// discoveryError says why Atlassian's OAuth details are not usable.
func discoveryError(err error) error {
	if errors.Is(err, errTokenURL) {
		return errCode("oauth_token_url", map[string]any{"detail": err.Error()}, "Atlassian の token endpoint がプロファイル (iwato-atlassian) と違うので中止しました。Iwato を更新してください: "+err.Error())
	}
	return errCode("oauth_discovery_failed", map[string]any{"detail": err.Error()}, "Atlassian の OAuth の情報を取得できません: "+err.Error())
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		Reconnect bool     `json:"reconnect"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	// A new provider's name is optional: Iwato picks it when it creates
	// the provider (finishOAuth). Atlassian's token response names no
	// site, so it is atlassian(-N), not atlassian-SITE.
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
	case body.Reconnect && providerBase(list[i]) != s.oauthProfile():
		failCode(w, 409, "oauth_not_atlassian", map[string]any{"name": body.Name}, body.Name+" は Atlassian のプロバイダーではありません")
		return
	case !body.Reconnect && body.Name != "" && i >= 0:
		failCode(w, 409, "oauth_provider_exists", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" はもうあります。再接続はその行の「再接続」から")
		return
	}
	backend := s.oauthBackend(r)
	md, err := backend.Discover(ctx)
	if err != nil {
		log.Printf("atlassian oauth: discovery: %v", english(err))
		failErr(w, 502, discoveryError(err))
		return
	}
	scopes, err := checkScopes(body.Scopes, md.Scopes)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	// The redirect: Iwato's own loopback, a fresh port each time.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		failCode(w, 500, "oauth_listen_failed", map[string]any{"detail": err.Error()}, "戻り先の待ち受けを開けません: "+err.Error())
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f := &oauthFlow{id: randomHex(16), name: body.Name, reconnect: body.Reconnect, scopes: scopes, state: randomHex(32), md: md, port: port,
		redirectURI: fmt.Sprintf("http://127.0.0.1:%d/callback", port), status: "waiting", backend: backend, ctx: s.oauthLife()}
	clientID, err := backend.Register(ctx, md, f.redirectURI)
	if err != nil {
		ln.Close()
		log.Printf("atlassian oauth: client registration: %v", english(err))
		failCode(w, 502, "oauth_register_failed", map[string]any{"detail": err.Error()}, "Atlassian にクライアントを登録できません (dynamic client registration): "+err.Error())
		return
	}
	f.clientID = clientID
	var challenge string
	f.verifier, challenge = pkce()
	f.authorizeURL = authorizeURL(md, clientID, f.redirectURI, f.state, challenge, scopes)
	f.expires = time.Now().Add(oauthFlowTTL)
	f.srv = &http.Server{Handler: s.callbackHandler(f), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	s.oauth.mu.Lock()
	if old := s.oauth.current; old != nil && old.status == "waiting" {
		old.status, old.message = "canceled", coded("oauth_superseded", nil, "新しい接続を始めたのでやめました。")
		old.stop()
	}
	s.oauth.current = f
	srv := f.srv
	f.timer = time.AfterFunc(oauthFlowTTL, func() {
		s.oauth.mu.Lock()
		defer s.oauth.mu.Unlock()
		if f.status == "waiting" {
			f.status, f.message = "expired", coded("oauth_atlassian_expired", map[string]any{"minutes": int(oauthFlowTTL.Minutes())}, fmt.Sprintf("%d 分以内に許可されなかったので、待ち受けを閉じました。もう一度始めてください。", int(oauthFlowTTL.Minutes())))
			f.stop()
		}
	})
	v := f.view()
	s.oauth.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	log.Printf("atlassian oauth: %s: waiting on %s for %s (scopes %s)", f.label(), f.redirectURI, oauthFlowTTL, strings.Join(scopes, " "))
	jsonResponse(w, 200, v)
}

// finishOAuth exchanges the code and gives the grant to the gateway.
func (s *Server) finishOAuth(f *oauthFlow, code string) {
	result := func(status string, message, warning flowText, refresh *RefreshView) {
		s.oauth.mu.Lock()
		f.status, f.message, f.warning, f.refresh = status, message, warning, refresh
		s.oauth.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	label := f.label()
	tokens, err := f.backend.Exchange(ctx, f.md, f.clientID, code, f.verifier, f.redirectURI)
	if err != nil {
		log.Printf("atlassian oauth: %s: %v", label, english(err))
		result("failed", coded("oauth_token_failed", map[string]any{"detail": err.Error()}, "トークンを受け取れませんでした: "+err.Error()), flowText{}, nil)
		return
	}
	g := oauthGrant{log: "atlassian oauth", name: f.name, reconnect: f.reconnect, profile: s.oauthProfile(), key: atlassianCredentialKey, clientID: f.clientID,
		noRefresh:   coded("oauth_no_refresh", nil, "refresh token がありません (offline_access が許可されませんでした)。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
		refreshCode: "oauth_first_refresh_atlassian",
		refreshHint: "Atlassian が更新に resource を求めているかもしれません。",
		named: func(name string) {
			// The page shows the name created (f.name is written only
			// here, read by this goroutine and, under oauth.mu, by view).
			s.oauth.mu.Lock()
			f.name = name
			s.oauth.mu.Unlock()
		}}
	result(s.handOverGrant(ctx, g, tokens))
}

// label names the flow in the log: its provider, or "(new)" before a
// generated name is known.
func (f *oauthFlow) label() string {
	if f.name == "" {
		return "(new)"
	}
	return f.name
}

func safeScope(s string) string {
	var out []string
	for _, f := range strings.Fields(s) {
		if scopePattern.MatchString(f) {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
}

// markProviderChanged marks the running sandboxes that have the provider
// as 再起動待ち: a new authorization revokes the old placeholder.
func (s *Server) markProviderChanged(ctx context.Context, provider string) int {
	rt := s.rt.(providerRuntime)
	_, attached, err := s.providerState(ctx, rt)
	if err != nil {
		return 0
	}
	var names []string
	for _, sb := range s.sandboxList() {
		if slices.Contains(attached[sb.Name], provider) && s.rt.Status(ctx, sb).State == "running" {
			names = append(names, sb.Name)
		}
	}
	if len(names) == 0 {
		return 0
	}
	now := time.Now().UTC()
	if err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for i := range list {
			if slices.Contains(names, list[i].Name) {
				list[i].ProvidersChangedAt = now
			}
		}
		return list, nil
	}); err != nil {
		log.Printf("atlassian oauth: %s: %v", provider, english(err))
	}
	return len(names)
}
