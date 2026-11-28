package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Claude Code with the owner's Claude account, signed in by Iwato
// (iwato-claude-subscription) with Claude Code's public client as `claude
// auth login` does (code_flow.go): both of Claude Code's redirects, the
// loopback one and Claude's page, which shows CODE#STATE to copy.

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

// pastedClaudeCode reads what Claude's page shows: CODE#STATE.
func pastedClaudeCode(raw string) (code, state string, err error) {
	raw = strings.TrimSpace(raw)
	code, state, ok := strings.Cut(raw, "#")
	if len(raw) > 8192 || !ok || code == "" || state == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", "", errCode("oauth_claude_paste_invalid", nil, "Claude のページに出たコード (… # … の形) をそのまま貼り付けてください")
	}
	return code, state, nil
}

// claudeService is the Claude sign-in for Claude Code (code_flow.go).
var claudeService = codeService{
	id: "claude", log: "claude oauth", profile: claudeSubscriptionProfileID, key: claudeOAuthCredentialKey,
	backend:  func() codeBackend { return newClaudeOAuth() },
	fake:     func(s *Server, origin string) codeBackend { return fakeHooks.claude(s, origin) },
	loopback: true,
	link:     claudeAuthorizeLink,
	paste:    pastedClaudeCode,
	notMine: func(name string) (string, string) {
		return "oauth_not_claude", name + " は Claude Code のプロバイダーではありません"
	},
	denied: func(code string) flowText {
		return coded("oauth_claude_denied", map[string]any{"code": code}, "Claude で許可されませんでした ("+code+")。")
	},
	expired:     "oauth_claude_expired",
	noRefresh:   coded("oauth_claude_no_refresh", nil, "refresh token がありません。ゲートウェイが更新できないので、プロバイダーは作りませんでした。"),
	refreshCode: "oauth_first_refresh_claude",
	refreshHint: "Anthropic がゲートウェイの更新の形 (フォーム形式) を受け付けていないかもしれません。",
}

func (s *Server) handleClaudeOAuth(w http.ResponseWriter, r *http.Request) {
	s.handleCodeOAuth(w, r, &claudeService)
}
