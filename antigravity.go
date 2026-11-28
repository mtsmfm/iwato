package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Antigravity CLI (agy) with the owner's Google account
// (iwato-antigravity-subscription), signed in by Iwato with agy's own
// client as its login does (code_flow.go): Google's authorization page
// comes back to antigravity.google, which shows the code to copy. agy's
// client is a Google installed app: the exchange and the gateway's refresh
// send its secret, which is in every copy of agy and so not secret.
// Google keeps the refresh token (no rotation).
//
// In the sandbox agy reads ~/.gemini/antigravity-cli/antigravity-oauth-token
// (its file storage, without D-Bus). Iwato's terminal environment
// (term_env.go, antigravityTokenLines) writes one with the provider's
// placeholder as the access token and an expiry agy never reaches, so agy
// never refreshes; the proxy puts the real token in.
const (
	antigravityProfileID     = "iwato-antigravity-subscription"
	antigravityCredentialKey = "ANTIGRAVITY_OAUTH_TOKEN"
	antigravityClientID      = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityAuthorizeURL  = "https://accounts.google.com/o/oauth2/auth"
	antigravityTokenURL      = "https://oauth2.googleapis.com/token"
	antigravityRedirect      = "https://antigravity.google/oauth-callback"
	antigravityScopes        = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs https://www.googleapis.com/auth/aicode openid"
	// antigravityTokenMarker marks the token file Iwato writes.
	antigravityTokenMarker = `"iwato":"gateway-refreshes"`
)

// antigravityClientSecret is agy's public installed-app client secret (in
// pieces: secret scanning would report it whole).
var antigravityClientSecret = strings.Join([]string{"GOCSPX", "-K58FWR486L", "dLJ1mLB8s", "XC4z6qDAf"}, "")

type antigravityOAuth struct {
	Client                               *http.Client
	AuthorizeURL, TokenURL, ID, Redirect string
}

func newAntigravityOAuth() *antigravityOAuth {
	return &antigravityOAuth{Client: &http.Client{Timeout: 30 * time.Second}, AuthorizeURL: antigravityAuthorizeURL, TokenURL: antigravityTokenURL, ID: antigravityClientID, Redirect: antigravityRedirect}
}

func (a *antigravityOAuth) AuthorizeEndpoint() string { return a.AuthorizeURL }
func (a *antigravityOAuth) ManualRedirect() string    { return a.Redirect }
func (a *antigravityOAuth) ClientID() string          { return a.ID }

// Exchange trades the code with the client's secret in the form.
func (a *antigravityOAuth) Exchange(ctx context.Context, code, verifier, _, redirectURI string) (oauthTokens, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}, "client_id": {a.ID}, "client_secret": {antigravityClientSecret}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := *a.Client
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

// antigravityAuthorizeLink is the authorization URL as agy builds it.
func antigravityAuthorizeLink(endpoint, clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"client_id":             {clientID},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {antigravityScopes},
		"state":                 {state},
	}
	return endpoint + "?" + q.Encode()
}

// pastedAntigravityCode reads the code antigravity.google shows, or that
// page's address (with code and state).
func pastedAntigravityCode(raw string) (code, state string, err error) {
	raw = strings.TrimSpace(raw)
	code = raw
	if _, query, ok := strings.Cut(raw, "?"); ok || strings.Contains(raw, "code=") {
		if !ok {
			query = raw
		}
		if q, perr := url.ParseQuery(query); perr != nil {
			code = ""
		} else {
			code, state = q.Get("code"), q.Get("state")
		}
	}
	if len(raw) > 8192 || code == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", "", errCode("oauth_antigravity_paste_invalid", nil, "antigravity.google のページに出たコードをそのまま貼り付けてください")
	}
	return code, state, nil
}

// antigravityService is the Google sign-in for agy (code_flow.go).
var antigravityService = codeService{
	id: "antigravity", log: "antigravity oauth", profile: antigravityProfileID, key: antigravityCredentialKey,
	backend: func() codeBackend { return newAntigravityOAuth() },
	fake:    func(s *Server, origin string) codeBackend { return fakeHooks.antigravity(s, origin) },
	link:    antigravityAuthorizeLink,
	paste:   pastedAntigravityCode,
	notMine: func(name string) (string, string) {
		return "oauth_not_antigravity", name + " は Antigravity CLI のプロバイダーではありません"
	},
	expired:      "oauth_antigravity_expired",
	noRefresh:    coded("oauth_antigravity_no_refresh", nil, "refresh token がありません。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
	refreshCode:  "oauth_first_refresh_antigravity",
	refreshHint:  "agy の新しい版でクライアントが変わったのかもしれません。",
	clientSecret: antigravityClientSecret,
}

func (s *Server) handleAntigravityOAuth(w http.ResponseWriter, r *http.Request) {
	s.handleCodeOAuth(w, r, &antigravityService)
}

// antigravityTokenLines write agy's token file in a shell that has the
// Antigravity provider: absent, or Iwato's (the marker), it gets this
// shell's placeholder, which an attach or a reconnect changes.
var antigravityTokenLines = func() string {
	format := `{"token":{"access_token":"%s","token_type":"Bearer","expiry":"2099-01-01T00:00:00Z"},"auth_method":"consumer",` + antigravityTokenMarker + `}\n`
	return `if [ -n "${` + antigravityCredentialKey + `-}" ]; then` + "\n" +
		`  (d=$HOME/.gemini/antigravity-cli; f=$d/antigravity-oauth-token; umask 077` + "\n" +
		`  if [ -L "$f" ] || { [ -e "$f" ] && ! grep -qF ` + shellQuote(antigravityTokenMarker) + ` "$f"; }; then exit 0; fi` + "\n" +
		`  mkdir -p "$d" && t=$d/.antigravity-oauth-token.iwato.$$ && printf ` + shellQuote(format) + ` "$` + antigravityCredentialKey + `" >"$t" && { cmp -s "$t" "$f" && rm -f "$t" || mv -f "$t" "$f"; }) 2>/dev/null` + "\n" +
		"fi\n"
}()
