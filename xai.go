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
	"strings"
	"time"
)

// Grok Build with the owner's xAI account (iwato-grok-subscription):
// Iwato signs in at auth.x.ai by the device authorization grant
// (RFC 8628) with Grok Build's own public client and scopes, as
// `grok login --device-auth` does (device_flow.go).

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

// xaiService is the xAI sign-in (device_flow.go).
var xaiService = deviceService{
	id: "xai", log: "xai oauth", profile: grokSubscriptionProfileID, key: grokOAuthCredentialKey,
	backend: func() deviceBackend { return newXAIOAuth() },
	fake:    func(s *Server, origin string) deviceBackend { return fakeHooks.xai(s, origin) },
	notMine: func(name string) (string, string) {
		return "oauth_not_xai", name + " は Grok Build のプロバイダーではありません"
	},
	startFailed: "oauth_xai_start_failed", startText: "xAI にログインを始められません: ",
	expired:     "oauth_xai_expired",
	noRefresh:   coded("oauth_no_refresh", nil, "refresh token がありません (offline_access が許可されませんでした)。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
	refreshCode: "oauth_first_refresh_xai",
	refreshHint: "xAI が Grok Build の更新の形 (client_id と refresh_token だけ) を受け付けていないかもしれません。",
	failed: func(err error) (flowText, bool) {
		if strings.Contains(err.Error(), "access_denied") {
			return coded("oauth_xai_denied", nil, "xAI で許可されませんでした。"), true
		} else if strings.Contains(err.Error(), "expired_token") {
			return coded("oauth_xai_code_expired", nil, "コードの期限が切れました。もう一度始めてください。"), true
		}
		return flowText{}, false
	},
	// Grok Build's refresh sends the token's principal for team and
	// organization sign-ins; the gateway's does not, so only personal
	// accounts are taken.
	accept: func(t oauthTokens) (map[string]string, *flowText) {
		if p := tokenPrincipal(t.AccessToken); p == "Team" || p == "Organization" {
			log.Printf("xai oauth: refused a %s sign-in", p)
			m := coded("oauth_xai_team", map[string]any{"kind": p}, "チームや組織のアカウント ("+p+") には対応していません。xAI の許可の画面で個人のアカウントを選んでください。")
			return nil, &m
		}
		return nil, nil
	},
}
