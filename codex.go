package main

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"
)

// Codex with the owner's ChatGPT account (iwato-codex-subscription).
// Iwato signs in at auth.openai.com with Codex's own public client and
// its device code login (`codex login --device-auth`: codex-rs/login/src/
// device_code_auth.rs), not RFC 8628: the owner enters the code at
// auth.openai.com/codex/device, the poll answers an authorization code and
// its PKCE verifier, and Iwato exchanges them at the token endpoint. The
// gateway refreshes the access token there (Codex's own refresh sends the
// same fields as JSON; the gateway sends a form, which the endpoint takes
// for the code exchange).
//
// In the sandbox Codex reads ~/.codex/auth.json. Iwato's terminal
// environment (term_env.go, codexAuthLines) writes one with the
// placeholders of the provider's two credentials: the access token (the
// bearer) and the ChatGPT account id (Codex's chatgpt-account-id header).
// The proxy puts the real values in. Its refresh token is a marker, and
// Codex's own refresh goes to a closed local port.
const (
	codexSubscriptionProfileID = "iwato-codex-subscription"
	codexOAuthCredentialKey    = "CODEX_CHATGPT_ACCESS_TOKEN"
	codexAccountKey            = "CODEX_CHATGPT_ACCOUNT_ID"
	codexIssuer                = "https://auth.openai.com"
	codexTokenURL              = codexIssuer + "/oauth/token"
	codexClientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	// codexAuthMarker is the refresh token of the auth.json Iwato writes.
	codexAuthMarker = "iwato-gateway-refreshes"
)

type codexOAuth struct {
	Client *http.Client
	// Issuer is auth.openai.com; ID Codex's client.
	Issuer, ID string
}

func newCodexOAuth() *codexOAuth {
	return &codexOAuth{Client: &http.Client{Timeout: 30 * time.Second}, Issuer: codexIssuer, ID: codexClientID}
}

func (c *codexOAuth) ClientID() string { return c.ID }

func (c *codexOAuth) post(ctx context.Context, u, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	hc := *c.Client
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return hc.Do(req)
}

var codexDeviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,512}$`)

func (c *codexOAuth) DeviceCode(ctx context.Context) (deviceCode, error) {
	body, _ := json.Marshal(map[string]string{"client_id": c.ID})
	resp, err := c.post(ctx, c.Issuer+"/api/accounts/deviceauth/usercode", "application/json", body)
	if err != nil {
		return deviceCode{}, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return deviceCode{}, errCodeEn("oauth_codex_device_disabled", nil,
			"OpenAI でデバイスコードのログインが使えません。ChatGPT の設定 (セキュリティ) で Codex のデバイスコードのログインを有効にしてください",
			"Device code sign-in is not available. Turn on device code authorization for Codex in ChatGPT's settings (Security)", nil)
	}
	if resp.StatusCode != http.StatusOK {
		return deviceCode{}, fmt.Errorf("device code request: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	var d struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		UserCode2    string          `json:"usercode"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(&d); err != nil {
		return deviceCode{}, errors.New("device code request: invalid JSON")
	}
	if d.UserCode == "" {
		d.UserCode = d.UserCode2
	}
	if !codexDeviceIDPattern.MatchString(d.DeviceAuthID) {
		return deviceCode{}, errors.New("device code request: no usable device_auth_id")
	}
	if !userCodePattern.MatchString(d.UserCode) {
		return deviceCode{}, errors.New("device code request: no usable user_code")
	}
	// Codex sends the interval as a string.
	interval, _ := strconv.ParseInt(strings.Trim(string(d.Interval), `" `), 10, 64)
	return deviceCode{DeviceCode: d.DeviceAuthID + " " + d.UserCode, UserCode: d.UserCode, VerificationURI: c.Issuer + "/codex/device", ExpiresIn: 15 * 60, Interval: interval}, nil
}

func (c *codexOAuth) Poll(ctx context.Context, device string) (oauthTokens, error) {
	id, code, _ := strings.Cut(device, " ")
	body, _ := json.Marshal(map[string]string{"device_auth_id": id, "user_code": code})
	resp, err := c.post(ctx, c.Issuer+"/api/accounts/deviceauth/token", "application/json", body)
	if err != nil {
		return oauthTokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusNotFound:
		return oauthTokens{}, errAuthorizationPending
	default:
		return oauthTokens{}, fmt.Errorf("token request: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	var g struct {
		Code     string `json:"authorization_code"`
		Verifier string `json:"code_verifier"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(&g); err != nil || g.Code == "" || g.Verifier == "" {
		return oauthTokens{}, errors.New("token request: no authorization code")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ID}, "code": {g.Code},
		"redirect_uri": {c.Issuer + "/deviceauth/callback"}, "code_verifier": {g.Verifier}}
	resp2, err := c.post(ctx, c.Issuer+"/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return oauthTokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("token request: HTTP %d%s", resp2.StatusCode, oauthErrorCode(resp2.Body))
	}
	return parseTokens(io.LimitReader(resp2.Body, maxOAuthBody))
}

var codexAccountPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)

// codexAccount is the ChatGPT account (workspace) of the sign-in, as Codex
// takes it: the id token's chatgpt_account_id claim (unverified), else the
// access token's.
func codexAccount(t oauthTokens) string {
	for _, jwt := range []string{t.IDToken, t.AccessToken} {
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			continue
		}
		b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			continue
		}
		var c struct {
			Auth struct {
				Account string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(b, &c) == nil && codexAccountPattern.MatchString(c.Auth.Account) {
			return c.Auth.Account
		}
	}
	return ""
}

// codexService is the ChatGPT sign-in for Codex (device_flow.go).
var codexService = deviceService{
	id: "codex", log: "codex oauth", profile: codexSubscriptionProfileID, key: codexOAuthCredentialKey,
	backend: func() deviceBackend { return newCodexOAuth() },
	fake:    func(s *Server, origin string) deviceBackend { return fakeHooks.codex(s, origin) },
	notMine: func(name string) (string, string) {
		return "oauth_not_codex", name + " は Codex のプロバイダーではありません"
	},
	startFailed: "oauth_codex_start_failed", startText: "OpenAI にログインを始められません: ",
	expired:     "oauth_codex_expired",
	noRefresh:   coded("oauth_no_refresh", nil, "refresh token がありません (offline_access が許可されませんでした)。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
	refreshCode: "oauth_first_refresh_codex",
	refreshHint: "OpenAI が更新をフォームの形で受け付けていないかもしれません。",
	failed:      func(error) (flowText, bool) { return flowText{}, false },
	accept: func(t oauthTokens) (map[string]string, *flowText) {
		a := codexAccount(t)
		if a == "" {
			log.Printf("codex oauth: the sign-in names no ChatGPT account")
			m := coded("oauth_codex_no_account", nil, "ChatGPT のアカウント (chatgpt_account_id) がトークンにありません。")
			return nil, &m
		}
		return map[string]string{codexAccountKey: a}, nil
	},
}

// codexIDToken is the id token of the auth.json Iwato writes: Codex needs
// one in JWT form and reads only optional claims from it.
var codexIDToken = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
	base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".iwato"

// codexAuthLines write ~/.codex/auth.json (or $CODEX_HOME's) in a shell
// that has the Codex sign-in provider: absent, or Iwato's (the marker), it
// gets the placeholders of this shell. The placeholders carry a revision
// that OpenShell keeps resolving to the current credential; an attach or a
// reconnect gives new ones, so a new shell writes the file again. Codex's
// own refresh goes to a closed local port (CODEX_REFRESH_TOKEN_URL_OVERRIDE)
// instead of auth.openai.com: the gateway refreshes. A last_refresh in the
// future keeps Codex from refreshing on its 8-day schedule.
var codexAuthLines = func() string {
	format := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"` + codexIDToken +
		`","access_token":"%s","refresh_token":"` + codexAuthMarker + `","account_id":"%s"},"last_refresh":"2100-01-01T00:00:00Z"}\n`
	marker := shellQuote(`"refresh_token":"` + codexAuthMarker + `"`)
	return `if [ -n "${` + codexOAuthCredentialKey + `-}" ] && [ -n "${` + codexAccountKey + `-}" ]; then` + "\n" +
		`  (d=${CODEX_HOME:-$HOME/.codex}; f=$d/auth.json; umask 077` + "\n" +
		`  if [ -L "$f" ] || { [ -e "$f" ] && ! grep -qF ` + marker + ` "$f"; }; then exit 0; fi` + "\n" +
		`  mkdir -p "$d" && t=$d/.auth.json.iwato.$$ && printf ` + shellQuote(format) + ` "$` + codexOAuthCredentialKey + `" "$` + codexAccountKey + `" >"$t" && { cmp -s "$t" "$f" && rm -f "$t" || mv -f "$t" "$f"; }) 2>/dev/null` + "\n" +
		`  if [ -z "${CODEX_REFRESH_TOKEN_URL_OVERRIDE-}" ] && grep -qsF ` + marker + ` "${CODEX_HOME:-$HOME/.codex}/auth.json"; then export CODEX_REFRESH_TOKEN_URL_OVERRIDE=http://127.0.0.1:9/; fi` + "\n" +
		"fi\n"
}()
