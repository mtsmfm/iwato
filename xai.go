package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Grok Build with the owner's xAI account (iwato-grok-subscription):
// Iwato signs in at auth.x.ai by the device authorization grant
// (RFC 8628) with Grok Build's own public client and scopes, as
// `grok login --device-auth` does, so the same steps work from the PC and
// from a phone: the page shows the code and xAI's page, and Iwato polls.
//
//  1. POST /api/oauth/xai/start {name, reconnect}: asks auth.x.ai for a
//     device code and starts polling. One flow at a time; a new one
//     cancels the last.
//  2. The owner opens the verification URL and approves the code. The
//     page polls GET /api/oauth/xai/flows/ID (and may cancel with
//     POST /api/oauth/xai/flows/ID/cancel).
//  3. Iwato receives the tokens, creates the provider (named by the owner,
//     or grok-subscription, grok-subscription-2, …) or, to reconnect,
//     replaces its credential, hands the refresh token to the gateway and
//     forgets it (oauth_grant.go), then rotates once.
//
// No token or device code is logged or returned to the page (the user
// code is shown: the owner types it at xAI).

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

// deviceBackend talks to the authorization server: xaiOAuth, or the
// fake's (fake_xai.go).
type deviceBackend interface {
	DeviceCode(ctx context.Context) (deviceCode, error)
	// Poll asks for the tokens once: errAuthorizationPending or
	// errSlowDown while the owner has not approved.
	Poll(ctx context.Context, deviceCode string) (oauthTokens, error)
	ClientID() string
}

type xaiOAuth struct {
	Client *http.Client
	// DeviceURL and TokenURL are auth.x.ai's (TokenURL is the profile's
	// token_url), ID and Scopes Grok Build's client and scopes.
	DeviceURL, TokenURL, ID, Scopes string
}

func newXAIOAuth() *xaiOAuth {
	return &xaiOAuth{Client: &http.Client{Timeout: 30 * time.Second}, DeviceURL: xaiDeviceCodeURL, TokenURL: xaiTokenURL, ID: xaiClientID, Scopes: xaiScopes}
}

func (x *xaiOAuth) ClientID() string { return x.ID }

func (x *xaiOAuth) post(ctx context.Context, u string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	c := *x.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c.Do(req)
}

var userCodePattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

func (x *xaiOAuth) DeviceCode(ctx context.Context) (deviceCode, error) {
	// referrer as Grok Build sends it (xai-grok-login/src/device_code.rs).
	resp, err := x.post(ctx, x.DeviceURL, url.Values{"client_id": {x.ID}, "scope": {x.Scopes}, "referrer": {"grok-build"}})
	if err != nil {
		return deviceCode{}, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return deviceCode{}, fmt.Errorf("device code request: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	var d struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		CompleteURI     string `json:"verification_uri_complete"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(&d); err != nil {
		return deviceCode{}, errors.New("device code request: invalid JSON")
	}
	if d.DeviceCode == "" || len(d.DeviceCode) > 4096 || credentialValue(d.DeviceCode) != nil {
		return deviceCode{}, errors.New("device code request: no usable device_code")
	}
	if !userCodePattern.MatchString(d.UserCode) {
		return deviceCode{}, errors.New("device code request: no usable user_code")
	}
	if _, err := httpsURL(d.VerificationURI); err != nil {
		return deviceCode{}, fmt.Errorf("device code request: verification_uri: %w", err)
	}
	if d.CompleteURI != "" {
		if _, err := httpsURL(d.CompleteURI); err != nil {
			d.CompleteURI = ""
		}
	}
	return deviceCode{DeviceCode: d.DeviceCode, UserCode: d.UserCode, VerificationURI: d.VerificationURI, CompleteURI: d.CompleteURI, ExpiresIn: d.ExpiresIn, Interval: d.Interval}, nil
}

func (x *xaiOAuth) Poll(ctx context.Context, code string) (oauthTokens, error) {
	resp, err := x.post(ctx, x.TokenURL, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code}, "client_id": {x.ID}})
	if err != nil {
		// The URL is the token endpoint; the form is not in the error.
		return oauthTokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return parseTokens(io.LimitReader(resp.Body, maxOAuthBody))
	}
	switch e := oauthErrorCode(resp.Body); e {
	case ": authorization_pending":
		return oauthTokens{}, errAuthorizationPending
	case ": slow_down":
		return oauthTokens{}, errSlowDown
	default:
		return oauthTokens{}, fmt.Errorf("token request: HTTP %d%s", resp.StatusCode, e)
	}
}

// tokenPrincipal is the principal_type claim of a JWT access token
// (unverified, as Grok Build reads it), or "".
func tokenPrincipal(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		PrincipalType  string `json:"principal_type"`
		PrincipalType2 string `json:"principalType"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	if c.PrincipalType != "" {
		return safeCode(c.PrincipalType)
	}
	return safeCode(c.PrincipalType2)
}

// xaiFlowMax bounds a flow's wait for the owner's approval (xAI's
// expires_in is usually shorter).
var xaiFlowMax = 15 * time.Minute

type xaiFlow struct {
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

type xaiFlows struct {
	mu      sync.Mutex
	current *xaiFlow
	// backend and profile override the real ones in tests; fake is the
	// fake backend's device codes (fake_xai.go).
	backend deviceBackend
	profile string
	fake    any
}

func (s *Server) xaiBackend(r *http.Request) deviceBackend {
	if s.xai.backend != nil {
		return s.xai.backend
	}
	if s.fake {
		origin := s.origin
		if o := r.Header.Get("Origin"); o != "" && s.allowedOrigin(o) {
			origin = o
		}
		return fakeHooks.xai(s, origin)
	}
	return newXAIOAuth()
}

func (s *Server) xaiProfile() string {
	if s.xai.profile != "" {
		return s.xai.profile
	}
	return grokSubscriptionProfileID
}

// xaiFlowView is a flow for the page: never the device code.
type xaiFlowView struct {
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

func (f *xaiFlow) view() xaiFlowView {
	v := xaiFlowView{ID: f.id, Name: f.name, Reconnect: f.reconnect, Status: f.status, Message: f.message.Text, Warning: f.warning.Text, flowCodes: codesOf(f.message, f.warning), ExpiresAt: f.expires.UTC().Format(time.RFC3339), Refresh: f.refresh}
	if f.status == "waiting" {
		v.UserCode, v.VerificationURI, v.CompleteURI = f.userCode, f.verifyURI, f.complete
	}
	return v
}

func (s *Server) handleXAIOAuth(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/oauth/xai"), "/")
	if r.Method == http.MethodGet {
		if !s.require(w, r, false) {
			return
		}
		id, ok := strings.CutPrefix(rest, "flows/")
		if !ok {
			fail(w, 404, "unknown route")
			return
		}
		s.xai.mu.Lock()
		f := s.xai.current
		if f == nil || !equal(f.id, id) {
			s.xai.mu.Unlock()
			failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
			return
		}
		v := f.view()
		s.xai.mu.Unlock()
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
		s.xaiStart(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] != "flows" || parts[2] != "cancel" {
		fail(w, 404, "unknown route")
		return
	}
	s.xai.mu.Lock()
	f := s.xai.current
	if f == nil || !equal(f.id, parts[1]) {
		s.xai.mu.Unlock()
		failCode(w, 404, "oauth_flow_missing", nil, "この接続の手続きはありません")
		return
	}
	if f.status == "waiting" {
		f.status, f.message = "canceled", coded("oauth_canceled", nil, "やめました。")
		f.cancel()
	}
	v := f.view()
	s.xai.mu.Unlock()
	jsonResponse(w, 200, v)
}

func (s *Server) xaiStart(w http.ResponseWriter, r *http.Request) {
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
	case body.Reconnect && providerBase(list[i]) != s.xaiProfile():
		failCode(w, 409, "oauth_not_xai", map[string]any{"name": body.Name}, body.Name+" は Grok Build (xAI のサブスクリプション) のプロバイダーではありません")
		return
	case !body.Reconnect && body.Name != "" && i >= 0:
		failCode(w, 409, "oauth_provider_exists", map[string]any{"name": body.Name}, "プロバイダー "+body.Name+" はもうあります。再接続はその行の「再接続」から")
		return
	}
	backend := s.xaiBackend(r)
	d, err := backend.DeviceCode(ctx)
	if err != nil {
		log.Printf("xai oauth: %v", english(err))
		failCode(w, 502, "oauth_xai_start_failed", map[string]any{"detail": err.Error()}, "xAI にログインを始められません: "+err.Error())
		return
	}
	wait := time.Duration(d.ExpiresIn) * time.Second
	if wait <= 0 || wait > xaiFlowMax {
		wait = xaiFlowMax
	}
	life := s.oauthLife()
	pctx, pcancel := context.WithTimeout(life, wait)
	f := &xaiFlow{id: randomHex(16), name: body.Name, reconnect: body.Reconnect, device: d.DeviceCode, userCode: d.UserCode, verifyURI: d.VerificationURI, complete: d.CompleteURI,
		expires: time.Now().Add(wait), status: "waiting", cancel: pcancel, life: life}
	s.xai.mu.Lock()
	if old := s.xai.current; old != nil && old.status == "waiting" {
		old.status, old.message = "canceled", coded("oauth_superseded", nil, "新しい接続を始めたのでやめました。")
		old.cancel()
	}
	s.xai.current = f
	v := f.view()
	s.xai.mu.Unlock()
	interval := time.Duration(max(d.Interval, 1)) * xaiPollUnit
	if d.Interval <= 0 {
		interval = 5 * xaiPollUnit
	}
	go s.xaiPoll(pctx, f, backend, interval)
	log.Printf("xai oauth: %s: waiting for the owner's approval for %s", xaiLabel(body.Name), wait.Round(time.Second))
	jsonResponse(w, 200, v)
}

func xaiLabel(name string) string {
	if name == "" {
		return "(new)"
	}
	return name
}

// xaiPollUnit is a second of the server's polling interval (tests
// shorten it).
var xaiPollUnit = time.Second

// xaiPoll waits for the owner's approval, then hands the grant over.
func (s *Server) xaiPoll(ctx context.Context, f *xaiFlow, backend deviceBackend, interval time.Duration) {
	defer f.cancel()
	result := func(status string, message, warning flowText, refresh *RefreshView) {
		s.xai.mu.Lock()
		f.status, f.message, f.warning, f.refresh = status, message, warning, refresh
		s.xai.mu.Unlock()
	}
	var tokens oauthTokens
	for {
		select {
		case <-ctx.Done():
			s.xai.mu.Lock()
			if f.status == "waiting" {
				f.status, f.message = "expired", coded("oauth_xai_expired", nil, "時間内に許可されませんでした。もう一度始めてください。")
			}
			s.xai.mu.Unlock()
			return
		case <-time.After(interval):
		}
		t, err := backend.Poll(ctx, f.device)
		switch {
		case errors.Is(err, errAuthorizationPending):
			continue
		case errors.Is(err, errSlowDown):
			interval += 5 * xaiPollUnit
			continue
		case err != nil && ctx.Err() != nil:
			continue
		case err != nil:
			log.Printf("xai oauth: %s: %v", xaiLabel(f.name), english(err))
			msg := coded("oauth_token_failed", map[string]any{"detail": err.Error()}, "トークンを受け取れませんでした: "+err.Error())
			if strings.Contains(err.Error(), "access_denied") {
				msg = coded("oauth_xai_denied", nil, "xAI で許可されませんでした。")
			} else if strings.Contains(err.Error(), "expired_token") {
				msg = coded("oauth_xai_code_expired", nil, "コードの期限が切れました。もう一度始めてください。")
			}
			s.xai.mu.Lock()
			if f.status == "waiting" {
				f.status, f.message = "failed", msg
			}
			s.xai.mu.Unlock()
			return
		}
		tokens = t
		break
	}
	s.xai.mu.Lock()
	if f.status != "waiting" {
		s.xai.mu.Unlock()
		return
	}
	f.status, f.message = "exchanging", coded("oauth_received", nil, "トークンを受け取りました…")
	f.device = ""
	s.xai.mu.Unlock()
	// Grok Build's refresh sends the token's principal for team and
	// organization sign-ins; the gateway's does not, so only personal
	// accounts are taken.
	if p := tokenPrincipal(tokens.AccessToken); p == "Team" || p == "Organization" {
		log.Printf("xai oauth: %s: refused a %s sign-in", xaiLabel(f.name), p)
		result("failed", coded("oauth_xai_team", map[string]any{"kind": p}, "チームや組織のアカウント ("+p+") には対応していません。xAI の許可の画面で個人のアカウントを選んでください。"), flowText{}, nil)
		return
	}
	hctx, cancel := context.WithTimeout(f.life, 2*time.Minute)
	defer cancel()
	g := oauthGrant{log: "xai oauth", name: f.name, reconnect: f.reconnect, profile: s.xaiProfile(), key: grokOAuthCredentialKey, clientID: backend.ClientID(),
		noRefresh:   coded("oauth_no_refresh", nil, "refresh token がありません (offline_access が許可されませんでした)。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
		refreshCode: "oauth_first_refresh_xai",
		refreshHint: "xAI が Grok Build の更新の形 (client_id と refresh_token だけ) を受け付けていないかもしれません。",
		named: func(name string) {
			s.xai.mu.Lock()
			f.name = name
			s.xai.mu.Unlock()
		}}
	result(s.handOverGrant(hctx, g, tokens))
}
