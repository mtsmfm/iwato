package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Signing in with an agent's own public client by the authorization code
// with PKCE S256 and state, the code coming back as the agent's own login
// does: Claude Code (claude_oauth.go) and the Antigravity CLI
// (antigravity.go).
//
//  1. POST /api/oauth/ID/start {name, reconnect}: returns the
//     authorization URL that comes back to the service's own page, which
//     shows a code to copy (manualURL). With loopback, also one with the
//     same state and challenge that comes back to a loopback listener
//     (127.0.0.1, a fresh port: http://localhost:PORT/callback) for the
//     PC's browser (loopbackURL). One flow per service at a time; a new
//     one cancels the last.
//  2. The browser comes back to the listener, or the owner pastes the code
//     (POST /api/oauth/ID/flows/FLOW/code {code}). The page polls
//     GET /api/oauth/ID/flows/FLOW (and may cancel with .../cancel).
//  3. Iwato exchanges the code with the redirect it came back by, creates
//     the provider or, to reconnect, replaces its credential, hands the
//     refresh token to the gateway and forgets it (oauth_grant.go), then
//     rotates once.
//
// No token, code, state or verifier is logged or returned to the page
// (state is in the authorization URLs only).

// codeBackend builds the links and exchanges the code: the service's, or
// the fake's (fake_code.go).
type codeBackend interface {
	AuthorizeEndpoint() string
	ManualRedirect() string
	Exchange(ctx context.Context, code, verifier, state, redirectURI string) (oauthTokens, error)
	ClientID() string
}

// codeService is what differs between the services.
type codeService struct {
	// id is the route's (/api/oauth/ID); log prefixes the log.
	id, log      string
	profile, key string
	backend      func() codeBackend
	fake         func(s *Server, origin string) codeBackend
	loopback     bool
	// link is the authorization URL for one redirect.
	link func(endpoint, clientID, redirectURI, state, challenge string) string
	// paste reads what the service's page shows. state is empty when the
	// page shows the code alone: the owner pastes it into this flow, and
	// PKCE binds it to the verifier.
	paste func(raw string) (code, state string, err error)
	// notMine refuses a reconnect of another kind of provider; denied
	// (with the error's code) and expired are the flow's ends.
	notMine                  func(name string) (code, text string)
	denied                   func(code string) flowText
	expired                  string
	noRefresh                flowText
	refreshCode, refreshHint string
	// clientSecret is the client's secret for the gateway's refresh (an
	// installed app's, public), or empty.
	clientSecret string
}

type codeFlow struct {
	id, name  string
	reconnect bool
	// state, verifier: secrets; never logged or sent to the page.
	state, verifier         string
	loopbackURL, manualURL  string
	redirectURI, manualBack string
	port                    int
	backend                 codeBackend
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

type codeFlows struct {
	mu      sync.Mutex
	current *codeFlow
	// backend overrides the real one in tests.
	backend codeBackend
}

func (s *Server) codeFlowsOf(svc *codeService) *codeFlows {
	if svc == &antigravityService {
		return &s.antigravity
	}
	return &s.claude
}

func (s *Server) codeBackendOf(r *http.Request, svc *codeService) codeBackend {
	if fl := s.codeFlowsOf(svc); fl.backend != nil {
		return fl.backend
	}
	if s.fake {
		origin := s.origin
		if o := r.Header.Get("Origin"); o != "" && s.allowedOrigin(o) {
			origin = o
		}
		return svc.fake(s, origin)
	}
	return svc.backend()
}

// codeFlowView is a flow for the page: never the code or the verifier.
type codeFlowView struct {
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

func (f *codeFlow) view() codeFlowView {
	v := codeFlowView{ID: f.id, Name: f.name, Reconnect: f.reconnect, Status: f.status, Message: f.message.Text, Warning: f.warning.Text, flowCodes: codesOf(f.message, f.warning), ExpiresAt: f.expires.UTC().Format(time.RFC3339), Refresh: f.refresh}
	if f.status == "waiting" {
		v.LoopbackURL, v.ManualURL = f.loopbackURL, f.manualURL
	}
	return v
}

// stop closes the listener and the timer. Call with the flows' mu held.
func (f *codeFlow) stop() {
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

func (s *Server) handleCodeOAuth(w http.ResponseWriter, r *http.Request, svc *codeService) {
	fl := s.codeFlowsOf(svc)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/oauth/"+svc.id), "/")
	if r.Method == http.MethodGet {
		if !s.require(w, r, false) {
			return
		}
		id, ok := strings.CutPrefix(rest, "flows/")
		if !ok {
			fail(w, 404, "unknown route")
			return
		}
		fl.mu.Lock()
		f := fl.current
		if f == nil || !equal(f.id, id) {
			fl.mu.Unlock()
			failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
			return
		}
		v := f.view()
		fl.mu.Unlock()
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
		s.codeStart(w, r, svc)
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
	fl.mu.Lock()
	f := fl.current
	if f == nil || !equal(f.id, parts[1]) {
		fl.mu.Unlock()
		failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
		return
	}
	if parts[2] == "cancel" {
		if f.status == "waiting" {
			f.status, f.message = "canceled", coded("oauth_canceled", nil, "やめました。")
			f.stop()
		}
		v := f.view()
		fl.mu.Unlock()
		jsonResponse(w, 200, v)
		return
	}
	fl.mu.Unlock()
	code, state, err := svc.paste(body.Code)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	if state == "" {
		state = f.state
	}
	if status, msg := s.codeAccept(svc, f, code, state, "", f.manualBack); status != 200 {
		fail(w, status, msg)
		return
	}
	fl.mu.Lock()
	v := f.view()
	fl.mu.Unlock()
	jsonResponse(w, 200, v)
}

// codeAccept takes the answer once: the code (or the error) with the
// flow's state, and the redirect it came back by. It returns the HTTP
// status and a message.
func (s *Server) codeAccept(svc *codeService, f *codeFlow, code, state, oauthErr, redirectURI string) (int, string) {
	fl := s.codeFlowsOf(svc)
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if f.status != "waiting" {
		return 409, "この接続の手続きは終わっています。Iwato の画面を見てください。"
	}
	if state == "" || !equal(state, f.state) {
		return 400, "state が一致しません。この接続の手続きのコードではありません。"
	}
	if oauthErr != "" {
		f.status, f.message = "failed", svc.denied(safeCode(oauthErr))
		f.stop()
		return 200, f.message.Text
	}
	if len(code) > 4096 || credentialValue(code) != nil {
		return 400, "code がありません。"
	}
	f.status, f.message = "exchanging", coded("oauth_receiving", nil, "トークンを受け取っています…")
	f.stop()
	go s.finishCode(svc, f, code, redirectURI)
	return 200, "許可を受け取りました。Iwato の画面に戻ってください。このタブは閉じてかまいません。"
}

// codeCallback serves the loopback redirect of one flow.
func (s *Server) codeCallback(svc *codeService, f *codeFlow) http.Handler {
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
		code, msg := s.codeAccept(svc, f, q.Get("code"), q.Get("state"), q.Get("error"), f.redirectURI)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = callbackPage.Execute(w, msg)
	})
}

func (s *Server) codeStart(w http.ResponseWriter, r *http.Request, svc *codeService) {
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
	case body.Reconnect && providerBase(list[i]) != svc.profile:
		code, text := svc.notMine(body.Name)
		failCode(w, 409, code, map[string]any{"name": body.Name}, text)
		return
	case !body.Reconnect && body.Name != "" && i >= 0:
		failCode(w, 409, "oauth_provider_exists", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" はもうあります。再接続はその行の「再接続」から")
		return
	}
	backend := s.codeBackendOf(r, svc)
	f := &codeFlow{id: randomHex(16), name: body.Name, reconnect: body.Reconnect, state: randomHex(32), backend: backend, status: "waiting",
		manualBack: backend.ManualRedirect(), expires: time.Now().Add(oauthFlowTTL), ctx: s.oauthLife()}
	var challenge string
	f.verifier, challenge = pkce()
	f.manualURL = svc.link(backend.AuthorizeEndpoint(), backend.ClientID(), f.manualBack, f.state, challenge)
	var ln net.Listener
	if svc.loopback {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			failCode(w, 500, "oauth_listen_failed", map[string]any{"detail": err.Error()}, "戻り先の待ち受けを開けません: "+err.Error())
			return
		}
		f.port = ln.Addr().(*net.TCPAddr).Port
		f.redirectURI = fmt.Sprintf("http://localhost:%d/callback", f.port)
		f.loopbackURL = svc.link(backend.AuthorizeEndpoint(), backend.ClientID(), f.redirectURI, f.state, challenge)
		f.srv = &http.Server{Handler: s.codeCallback(svc, f), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	}
	fl := s.codeFlowsOf(svc)
	fl.mu.Lock()
	if old := fl.current; old != nil && old.status == "waiting" {
		old.status, old.message = "canceled", coded("oauth_superseded", nil, "新しい接続を始めたのでやめました。")
		old.stop()
	}
	fl.current = f
	srv := f.srv
	minutes := int(oauthFlowTTL.Minutes())
	f.timer = time.AfterFunc(oauthFlowTTL, func() {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		if f.status == "waiting" {
			f.status, f.message = "expired", coded(svc.expired, map[string]any{"minutes": minutes}, fmt.Sprintf("%d 分以内に許可されなかったので、やめました。もう一度始めてください。", minutes))
			f.stop()
		}
	})
	v := f.view()
	fl.mu.Unlock()
	if srv != nil {
		go func() { _ = srv.Serve(ln) }()
		log.Printf("%s: %s: waiting on %s for %s", svc.log, nameLabel(body.Name), f.redirectURI, oauthFlowTTL)
	} else {
		log.Printf("%s: %s: waiting for the pasted code for %s", svc.log, nameLabel(body.Name), oauthFlowTTL)
	}
	jsonResponse(w, 200, v)
}

// finishCode exchanges the code and gives the grant to the gateway.
func (s *Server) finishCode(svc *codeService, f *codeFlow, code, redirectURI string) {
	fl := s.codeFlowsOf(svc)
	result := func(status string, message, warning flowText, refresh *RefreshView) {
		fl.mu.Lock()
		f.status, f.message, f.warning, f.refresh = status, message, warning, refresh
		fl.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()
	tokens, err := f.backend.Exchange(ctx, code, f.verifier, f.state, redirectURI)
	if err != nil {
		log.Printf("%s: %s: %v", svc.log, nameLabel(f.name), english(err))
		result("failed", coded("oauth_token_failed", map[string]any{"detail": err.Error()}, "トークンを受け取れませんでした: "+err.Error()), flowText{}, nil)
		return
	}
	g := oauthGrant{log: svc.log, name: f.name, reconnect: f.reconnect, profile: svc.profile, key: svc.key, clientID: f.backend.ClientID(), clientSecret: svc.clientSecret,
		noRefresh: svc.noRefresh, refreshCode: svc.refreshCode, refreshHint: svc.refreshHint,
		named: func(name string) {
			fl.mu.Lock()
			f.name = name
			fl.mu.Unlock()
		}}
	result(s.handOverGrant(ctx, g, tokens))
}

// cancelCodeFlows ends the flows of every code service (a reset).
func (s *Server) cancelCodeFlows(canceled flowText) {
	for _, fl := range []*codeFlows{&s.claude, &s.antigravity} {
		fl.mu.Lock()
		if f := fl.current; f != nil {
			if f.status == "waiting" || f.status == "exchanging" {
				f.status, f.message = "canceled", canceled
			}
			f.stop()
			fl.current = nil
		}
		fl.mu.Unlock()
	}
}
