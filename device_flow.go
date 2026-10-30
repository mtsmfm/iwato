package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Signing in with a device code, for xAI (Grok Build: xai.go) and OpenAI
// (Codex: codex.go), so the same steps work from the PC and from a phone:
// the page shows the code and the service's page, and Iwato polls.
//
//  1. POST /api/oauth/ID/start {name, reconnect}: asks the service for a
//     device code and starts polling. One flow per service at a time; a
//     new one cancels the last.
//  2. The owner opens the verification URL and approves the code. The
//     page polls GET /api/oauth/ID/flows/FLOW (and may cancel with
//     POST /api/oauth/ID/flows/FLOW/cancel).
//  3. Iwato receives the tokens, creates the provider (named by the owner,
//     or after the profile, -2, …) or, to reconnect, replaces its
//     credential, hands the refresh token to the gateway and forgets it
//     (oauth_grant.go), then rotates once.
//
// No token or device code is logged or returned to the page (the user
// code is shown: the owner types it at the service).

// deviceCode is the device authorization response.
type deviceCode struct {
	// DeviceCode is the secret the token requests carry.
	DeviceCode                             string
	UserCode, VerificationURI, CompleteURI string
	ExpiresIn, Interval                    int64
}

// errAuthorizationPending and errSlowDown: the owner has not approved yet
// (slow_down also lengthens the interval).
var (
	errAuthorizationPending = errors.New("authorization_pending")
	errSlowDown             = errors.New("slow_down")
)

// deviceBackend talks to the authorization server: xaiOAuth, codexOAuth,
// or the fake's (fake_xai.go).
type deviceBackend interface {
	DeviceCode(ctx context.Context) (deviceCode, error)
	// Poll asks for the tokens once: errAuthorizationPending or
	// errSlowDown while the owner has not approved.
	Poll(ctx context.Context, deviceCode string) (oauthTokens, error)
	ClientID() string
}

// deviceService is what differs between the services: the route, the
// profile and its refreshed credential, the backend and the words.
type deviceService struct {
	// id is the route's (/api/oauth/ID) and the fake's; log prefixes the log.
	id, log      string
	profile, key string
	backend      func() deviceBackend
	fake         func(s *Server, origin string) deviceBackend
	// notMine refuses a reconnect of another kind of provider; the codes
	// below are the service's own words for the flow's ends (error.CODE).
	notMine                      func(name string) (code, text string)
	startFailed                  string
	startText                    string
	expired, denied, codeExpired string
	noRefresh                    flowText
	refreshCode, refreshHint     string
	failed                       func(err error) (flowText, bool)
	// accept checks the tokens and gives the provider's credentials other
	// than key (nil: none), or refuses them.
	accept func(oauthTokens) (map[string]string, *flowText)
}

// deviceFlowMax bounds a flow's wait for the owner's approval (the
// services' expires_in is usually shorter).
var deviceFlowMax = 15 * time.Minute

type deviceFlow struct {
	id, name  string
	reconnect bool
	// device is the device code (secret); never sent to the page.
	device                        string
	userCode, verifyURI, complete string
	expires                       time.Time
	// status: waiting, exchanging, done, failed, expired, canceled.
	status           string
	message, warning flowText
	refresh          *RefreshView
	cancel           context.CancelFunc
	// life ends with a reset (oauthLife).
	life context.Context
}

type deviceFlows struct {
	mu      sync.Mutex
	current *deviceFlow
	// backend and profile override the real ones in tests; fake is the
	// fake backend's device codes (fake_xai.go).
	backend deviceBackend
	profile string
	fake    any
}

// deviceFlowsOf is the service's flows on the server.
func (s *Server) deviceFlowsOf(svc *deviceService) *deviceFlows {
	if svc == &codexService {
		return &s.codex
	}
	return &s.xai
}

func (s *Server) deviceBackendOf(r *http.Request, svc *deviceService) deviceBackend {
	if fl := s.deviceFlowsOf(svc); fl.backend != nil {
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

// deviceProfile is the service's profile (tests use copies).
func (s *Server) deviceProfile(svc *deviceService) string {
	if p := s.deviceFlowsOf(svc).profile; p != "" {
		return p
	}
	return svc.profile
}

func (s *Server) xaiProfile() string { return s.deviceProfile(&xaiService) }

// deviceFlowView is a flow for the page: never the device code.
type deviceFlowView struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Reconnect       bool         `json:"reconnect"`
	Status          string       `json:"status"`
	Message         string       `json:"message,omitempty"`
	Warning         string       `json:"warning,omitempty"`
	UserCode        string       `json:"userCode,omitempty"`
	VerificationURI string       `json:"verificationURI,omitempty"`
	CompleteURI     string       `json:"verificationURIComplete,omitempty"`
	ExpiresAt       string       `json:"expiresAt"`
	Refresh         *RefreshView `json:"refresh,omitempty"`
	flowCodes
}

func (f *deviceFlow) view() deviceFlowView {
	v := deviceFlowView{ID: f.id, Name: f.name, Reconnect: f.reconnect, Status: f.status, Message: f.message.Text, Warning: f.warning.Text, flowCodes: codesOf(f.message, f.warning), ExpiresAt: f.expires.UTC().Format(time.RFC3339), Refresh: f.refresh}
	if f.status == "waiting" {
		v.UserCode, v.VerificationURI, v.CompleteURI = f.userCode, f.verifyURI, f.complete
	}
	return v
}

func (s *Server) handleXAIOAuth(w http.ResponseWriter, r *http.Request) {
	s.handleDeviceOAuth(w, r, &xaiService)
}

func (s *Server) handleCodexOAuth(w http.ResponseWriter, r *http.Request) {
	s.handleDeviceOAuth(w, r, &codexService)
}

func (s *Server) handleDeviceOAuth(w http.ResponseWriter, r *http.Request, svc *deviceService) {
	fl := s.deviceFlowsOf(svc)
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
		s.deviceStart(w, r, svc)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] != "flows" || parts[2] != "cancel" {
		fail(w, 404, "unknown route")
		return
	}
	fl.mu.Lock()
	f := fl.current
	if f == nil || !equal(f.id, parts[1]) {
		fl.mu.Unlock()
		failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
		return
	}
	if f.status == "waiting" {
		f.status, f.message = "canceled", coded("oauth_canceled", nil, "やめました。")
		f.cancel()
	}
	v := f.view()
	fl.mu.Unlock()
	jsonResponse(w, 200, v)
}

func (s *Server) deviceStart(w http.ResponseWriter, r *http.Request, svc *deviceService) {
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
	case body.Reconnect && providerBase(list[i]) != s.deviceProfile(svc):
		code, text := svc.notMine(body.Name)
		failCode(w, 409, code, map[string]any{"name": body.Name}, text)
		return
	case !body.Reconnect && body.Name != "" && i >= 0:
		failCode(w, 409, "oauth_provider_exists", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" はもうあります。再接続はその行の「再接続」から")
		return
	}
	backend := s.deviceBackendOf(r, svc)
	d, err := backend.DeviceCode(ctx)
	if err != nil {
		log.Printf("%s: %v", svc.log, english(err))
		var c *codedError
		if errors.As(err, &c) {
			failErr(w, 502, err)
			return
		}
		failCode(w, 502, svc.startFailed, map[string]any{"detail": err.Error()}, svc.startText+err.Error())
		return
	}
	wait := time.Duration(d.ExpiresIn) * time.Second
	if wait <= 0 || wait > deviceFlowMax {
		wait = deviceFlowMax
	}
	life := s.oauthLife()
	pctx, pcancel := context.WithTimeout(life, wait)
	f := &deviceFlow{id: randomHex(16), name: body.Name, reconnect: body.Reconnect, device: d.DeviceCode, userCode: d.UserCode, verifyURI: d.VerificationURI, complete: d.CompleteURI,
		expires: time.Now().Add(wait), status: "waiting", cancel: pcancel, life: life}
	fl := s.deviceFlowsOf(svc)
	fl.mu.Lock()
	if old := fl.current; old != nil && old.status == "waiting" {
		old.status, old.message = "canceled", coded("oauth_superseded", nil, "新しい接続を始めたのでやめました。")
		old.cancel()
	}
	fl.current = f
	v := f.view()
	fl.mu.Unlock()
	interval := time.Duration(max(d.Interval, 1)) * devicePollUnit
	if d.Interval <= 0 {
		interval = 5 * devicePollUnit
	}
	go s.devicePoll(pctx, f, svc, backend, interval)
	log.Printf("%s: %s: waiting for the owner's approval for %s", svc.log, nameLabel(body.Name), wait.Round(time.Second))
	jsonResponse(w, 200, v)
}

// nameLabel is a provider's name for the log ("(new)" before it has one).
func nameLabel(name string) string {
	if name == "" {
		return "(new)"
	}
	return name
}

// devicePollUnit is a second of the server's polling interval (tests
// shorten it).
var devicePollUnit = time.Second

// devicePoll waits for the owner's approval, then hands the grant over.
func (s *Server) devicePoll(ctx context.Context, f *deviceFlow, svc *deviceService, backend deviceBackend, interval time.Duration) {
	defer f.cancel()
	fl := s.deviceFlowsOf(svc)
	result := func(status string, message, warning flowText, refresh *RefreshView) {
		fl.mu.Lock()
		f.status, f.message, f.warning, f.refresh = status, message, warning, refresh
		fl.mu.Unlock()
	}
	var tokens oauthTokens
	for {
		select {
		case <-ctx.Done():
			fl.mu.Lock()
			if f.status == "waiting" {
				f.status, f.message = "expired", coded(svc.expired, nil, "時間内に許可されませんでした。もう一度始めてください。")
			}
			fl.mu.Unlock()
			return
		case <-time.After(interval):
		}
		t, err := backend.Poll(ctx, f.device)
		switch {
		case errors.Is(err, errAuthorizationPending):
			continue
		case errors.Is(err, errSlowDown):
			interval += 5 * devicePollUnit
			continue
		case err != nil && ctx.Err() != nil:
			continue
		case err != nil:
			log.Printf("%s: %s: %v", svc.log, nameLabel(f.name), english(err))
			msg := coded("oauth_token_failed", map[string]any{"detail": err.Error()}, "トークンを受け取れませんでした: "+err.Error())
			if m, ok := svc.failed(err); ok {
				msg = m
			}
			fl.mu.Lock()
			if f.status == "waiting" {
				f.status, f.message = "failed", msg
			}
			fl.mu.Unlock()
			return
		}
		tokens = t
		break
	}
	fl.mu.Lock()
	if f.status != "waiting" {
		fl.mu.Unlock()
		return
	}
	f.status, f.message = "exchanging", coded("oauth_received", nil, "トークンを受け取りました…")
	f.device = ""
	fl.mu.Unlock()
	extra, refused := svc.accept(tokens)
	if refused != nil {
		result("failed", *refused, flowText{}, nil)
		return
	}
	hctx, cancel := context.WithTimeout(f.life, 2*time.Minute)
	defer cancel()
	g := oauthGrant{log: svc.log, name: f.name, reconnect: f.reconnect, profile: s.deviceProfile(svc), key: svc.key, clientID: backend.ClientID(), extra: extra,
		noRefresh: svc.noRefresh, refreshCode: svc.refreshCode, refreshHint: svc.refreshHint,
		named: func(name string) {
			fl.mu.Lock()
			f.name = name
			fl.mu.Unlock()
		}}
	result(s.handOverGrant(hctx, g, tokens))
}

// cancelDeviceFlows ends the services' flows (a reset).
func (s *Server) cancelDeviceFlows(canceled flowText) {
	for _, fl := range []*deviceFlows{&s.xai, &s.codex} {
		fl.mu.Lock()
		if f := fl.current; f != nil {
			if f.status == "waiting" || f.status == "exchanging" {
				f.status, f.message = "canceled", canceled
			}
			f.cancel()
			fl.current = nil
		}
		fl.mu.Unlock()
	}
}
