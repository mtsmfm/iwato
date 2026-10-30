package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Iwato's OAuth client for Atlassian's remote MCP (atlassianResource).
// Iwato runs on the owner's PC, so the browser that authorizes it
// reaches its loopback redirect (http://127.0.0.1:PORT/callback); a
// phone pastes the failed redirect's URL instead (atlassian_flow.go).
//
//   - discovery: the resource's protected-resource metadata (RFC 9728),
//     then its authorization server's metadata (RFC 8414). The token
//     endpoint must be the profile's token_url: the gateway refreshes there
//     (OpenShell keeps token URLs in the profile), so a different one means
//     the profile is out of date, and Iwato stops.
//   - a public client by dynamic client registration (RFC 7591,
//     token_endpoint_auth_method none), one per flow: the redirect's port
//     changes. Client ID Metadata Documents need an https URL Iwato does
//     not have.
//   - PKCE S256, state, and the resource indicator (RFC 8707) on the
//     authorization and the code exchange. The gateway's refresh cannot
//     send resource (OpenShell v0.1.6); whether Atlassian needs it there is
//     checked by rotating once right after connecting.
//
// No token, code or verifier is logged or returned to the page.

// oauthMetadata is what discovery found.
type oauthMetadata struct {
	Resource              string
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	// Scopes are the resource's scopes_supported.
	Scopes []string
}

// oauthTokens is the token endpoint's answer. Never log it.
type oauthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Scope        string
	// Fields are the answer's field names (for the log: what Atlassian
	// sends, e.g. refresh_expires_in), Lifetimes its *expires_in values.
	Fields    []string
	Lifetimes map[string]int64
}

// oauthBackend talks to the authorization server: atlassianOAuth, or the
// fake's (fake_atlassian.go).
type oauthBackend interface {
	Discover(ctx context.Context) (oauthMetadata, error)
	Register(ctx context.Context, md oauthMetadata, redirectURI string) (string, error)
	Exchange(ctx context.Context, md oauthMetadata, clientID, code, verifier, redirectURI string) (oauthTokens, error)
}

type atlassianOAuth struct {
	Client *http.Client
	// Resource is the MCP server's URL, TokenURL the profile's token_url.
	Resource, TokenURL string
}

func newAtlassianOAuth() *atlassianOAuth {
	return &atlassianOAuth{Client: &http.Client{Timeout: 30 * time.Second}, Resource: atlassianResource, TokenURL: atlassianTokenURL}
}

// errTokenURL: the authorization server's token endpoint is not the
// profile's.
var errTokenURL = errors.New("token endpoint differs from the profile's token_url")

const maxOAuthBody = 1 << 20

func (a *atlassianOAuth) client() *http.Client {
	c := *a.Client
	// Metadata and tokens come from the URL asked, never a redirect.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (a *atlassianOAuth) getJSON(ctx context.Context, u string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(v); err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: invalid JSON", u)
	}
	return resp.StatusCode, nil
}

// getFirst reads the first of urls that exists (404 tries the next).
func (a *atlassianOAuth) getFirst(ctx context.Context, urls []string, v any) error {
	var err error
	for _, u := range urls {
		var code int
		if code, err = a.getJSON(ctx, u, v); err == nil || code != http.StatusNotFound {
			return err
		}
	}
	return err
}

func httpsURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("not an https URL: %q", s)
	}
	return u, nil
}

func (a *atlassianOAuth) Discover(ctx context.Context) (oauthMetadata, error) {
	res, err := httpsURL(a.Resource)
	if err != nil {
		return oauthMetadata{}, err
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
		BearerMethods        []string `json:"bearer_methods_supported"`
	}
	base := res.Scheme + "://" + res.Host
	if err := a.getFirst(ctx, []string{base + "/.well-known/oauth-protected-resource" + res.Path, base + "/.well-known/oauth-protected-resource"}, &prm); err != nil {
		return oauthMetadata{}, fmt.Errorf("protected resource metadata: %w", err)
	}
	if prm.Resource != a.Resource {
		return oauthMetadata{}, fmt.Errorf("protected resource metadata names %q, not %q", prm.Resource, a.Resource)
	}
	if len(prm.BearerMethods) > 0 && !slices.Contains(prm.BearerMethods, "header") {
		return oauthMetadata{}, errors.New("the resource does not take a bearer token in the Authorization header")
	}
	if len(prm.AuthorizationServers) == 0 {
		return oauthMetadata{}, errors.New("protected resource metadata names no authorization server")
	}
	issuer := prm.AuthorizationServers[0]
	iu, err := httpsURL(issuer)
	if err != nil || iu.RawQuery != "" {
		return oauthMetadata{}, fmt.Errorf("authorization server: not an https URL: %q", issuer)
	}
	var as struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		ChallengeMethods      []string `json:"code_challenge_methods_supported"`
		AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
		GrantTypes            []string `json:"grant_types_supported"`
		ResponseTypes         []string `json:"response_types_supported"`
	}
	path := strings.TrimSuffix(iu.Path, "/")
	if err := a.getFirst(ctx, []string{iu.Scheme + "://" + iu.Host + "/.well-known/oauth-authorization-server" + path, strings.TrimSuffix(issuer, "/") + "/.well-known/oauth-authorization-server"}, &as); err != nil {
		return oauthMetadata{}, fmt.Errorf("authorization server metadata: %w", err)
	}
	if as.Issuer != issuer {
		return oauthMetadata{}, fmt.Errorf("authorization server metadata names the issuer %q, not %q", as.Issuer, issuer)
	}
	if as.TokenEndpoint != a.TokenURL {
		return oauthMetadata{}, fmt.Errorf("%w: %q, the profile has %q", errTokenURL, as.TokenEndpoint, a.TokenURL)
	}
	// The browser goes to the authorization endpoint and Iwato registers
	// at the token endpoint's host only.
	tu, err := httpsURL(a.TokenURL)
	if err != nil {
		return oauthMetadata{}, err
	}
	for what, e := range map[string]string{"authorization_endpoint": as.AuthorizationEndpoint, "registration_endpoint": as.RegistrationEndpoint} {
		u, err := httpsURL(e)
		if err != nil {
			return oauthMetadata{}, fmt.Errorf("%s: %w", what, err)
		}
		if u.Host != tu.Host {
			return oauthMetadata{}, fmt.Errorf("%s is on %s, not on the token endpoint's host %s", what, u.Host, tu.Host)
		}
	}
	if !slices.Contains(as.ChallengeMethods, "S256") {
		return oauthMetadata{}, errors.New("the authorization server does not support PKCE S256")
	}
	if !slices.Contains(as.AuthMethods, "none") {
		return oauthMetadata{}, errors.New("the authorization server does not take public clients (token_endpoint_auth_method none)")
	}
	for _, g := range []string{"authorization_code", "refresh_token"} {
		if len(as.GrantTypes) > 0 && !slices.Contains(as.GrantTypes, g) {
			return oauthMetadata{}, fmt.Errorf("the authorization server does not support the %s grant", g)
		}
	}
	if len(as.ResponseTypes) > 0 && !slices.Contains(as.ResponseTypes, "code") {
		return oauthMetadata{}, errors.New("the authorization server does not support the code response type")
	}
	var scopes []string
	for _, s := range prm.ScopesSupported {
		if scopePattern.MatchString(s) && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	return oauthMetadata{Resource: prm.Resource, Issuer: issuer, AuthorizationEndpoint: as.AuthorizationEndpoint, TokenEndpoint: as.TokenEndpoint, RegistrationEndpoint: as.RegistrationEndpoint, Scopes: scopes}, nil
}

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~:/+=-]{1,256}$`)

// Register registers a public client for redirectURI (RFC 7591).
func (a *atlassianOAuth) Register(ctx context.Context, md oauthMetadata, redirectURI string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Iwato",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("client registration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("client registration: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	var reg struct {
		ClientID   string `json:"client_id"`
		AuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(&reg); err != nil {
		return "", errors.New("client registration: invalid JSON")
	}
	if !clientIDPattern.MatchString(reg.ClientID) {
		return "", errors.New("client registration: no usable client_id")
	}
	if reg.AuthMethod != "" && reg.AuthMethod != "none" {
		return "", fmt.Errorf("client registration: the server made a confidential client (%s)", safeCode(reg.AuthMethod))
	}
	return reg.ClientID, nil
}

// Exchange trades the authorization code (RFC 6749 4.1.3 with PKCE and the
// resource indicator).
func (a *atlassianOAuth) Exchange(ctx context.Context, md oauthMetadata, clientID, code, verifier, redirectURI string) (oauthTokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {md.Resource},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		// The URL is the token endpoint; the form is not in the error.
		return oauthTokens{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oauthTokens{}, fmt.Errorf("token request: HTTP %d%s", resp.StatusCode, oauthErrorCode(resp.Body))
	}
	return parseTokens(io.LimitReader(resp.Body, maxOAuthBody))
}

func parseTokens(r io.Reader) (oauthTokens, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return oauthTokens{}, errors.New("token request: invalid JSON")
	}
	t := oauthTokens{Lifetimes: map[string]int64{}}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(raw[k], &s)
		return s
	}
	for k, v := range raw {
		t.Fields = append(t.Fields, safeCode(k))
		if strings.HasSuffix(k, "expires_in") {
			var n json.Number
			if json.Unmarshal(v, &n) == nil {
				if i, err := n.Int64(); err == nil {
					t.Lifetimes[safeCode(k)] = i
				}
			}
		}
	}
	slices.Sort(t.Fields)
	t.AccessToken, t.RefreshToken, t.Scope = str("access_token"), str("refresh_token"), str("scope")
	t.ExpiresIn = t.Lifetimes["expires_in"]
	if tt := str("token_type"); tt != "" && !strings.EqualFold(tt, "bearer") {
		return oauthTokens{}, fmt.Errorf("token request: token_type %s, not Bearer", safeCode(tt))
	}
	if credentialValue(t.AccessToken) != nil {
		return oauthTokens{}, errors.New("token request: no usable access_token")
	}
	if t.RefreshToken != "" && credentialValue(t.RefreshToken) != nil {
		return oauthTokens{}, errors.New("token request: unusable refresh_token")
	}
	if t.ExpiresIn < 0 {
		t.ExpiresIn = 0
	}
	return t, nil
}

// oauthErrorCode is ": <error>" from an OAuth error body: the code only,
// never the description (it may echo what was sent).
func oauthErrorCode(r io.Reader) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&e) != nil || e.Error == "" {
		return ""
	}
	return ": " + safeCode(e.Error)
}

var safeCodePattern = regexp.MustCompile(`[^A-Za-z0-9_.:-]`)

// safeCode is a short identifier from a server, safe to show and log.
func safeCode(s string) string {
	s = safeCodePattern.ReplaceAllString(s, "")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

var scopePattern = regexp.MustCompile(`^[A-Za-z0-9:._/-]{1,128}$`)

// atlassianScopes are the scopes Atlassian's metadata lists: the fake
// backend's discovery (fake_atlassian.go) and the tests' Atlassian use them.
var atlassianScopes = []string{
	"offline_access", "read:me", "read:account", "email",
	"read:jira:agent-interface", "write:jira:agent-interface", "search:jira:agent-interface",
	"read:confluence:agent-interface", "write:confluence:agent-interface", "search:confluence:agent-interface",
	"read:rovo:agent-interface", "read:bitbucket:agent-interface", "write:bitbucket:agent-interface",
}

// defaultAtlassianScopes are the ones the dialog checks first: refresh,
// the user's identity and reading, writing and searching Jira and
// Confluence through the MCP (*:agent-interface).
func defaultAtlassianScopes(supported []string) []string {
	out := []string{"offline_access"}
	for _, s := range supported {
		switch {
		case s == "read:me":
		case strings.HasSuffix(s, ":agent-interface") && (strings.HasPrefix(s, "read:") || strings.HasPrefix(s, "write:") || strings.HasPrefix(s, "search:")) &&
			(strings.Contains(s, "jira") || strings.Contains(s, "confluence")):
		default:
			continue
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// checkScopes accepts the scopes the owner chose: known ones (when the
// resource lists them) and always offline_access, which the refresh needs.
func checkScopes(chosen, supported []string) ([]string, error) {
	out := []string{"offline_access"}
	if len(chosen) > 100 {
		return nil, errCode("oauth_scopes_too_many", nil, "スコープが多すぎます")
	}
	for _, s := range chosen {
		if !scopePattern.MatchString(s) {
			return nil, errCode("oauth_scope_invalid", map[string]any{"scope": fmt.Sprintf("%q", s)}, fmt.Sprintf("スコープの形が正しくありません: %q", s))
		}
		if len(supported) > 0 && s != "offline_access" && !slices.Contains(supported, s) {
			return nil, errCode("oauth_scope_unknown", map[string]any{"scope": s}, fmt.Sprintf("Atlassian が提供していないスコープです: %s", s))
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// pkce returns a verifier and its S256 challenge (RFC 7636).
func pkce() (verifier, challenge string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeURL(md oauthMetadata, clientID, redirectURI, state, challenge string, scopes []string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {md.Resource},
	}
	sep := "?"
	if strings.Contains(md.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return md.AuthorizationEndpoint + sep + q.Encode()
}
