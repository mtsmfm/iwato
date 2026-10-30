package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAtlassian is Atlassian's protected resource and authorization server
// (one TLS test server). No test reaches the network.
type fakeAtlassian struct {
	srv *httptest.Server
	mu  sync.Mutex
	// tokenEndpoint overrides the advertised token endpoint.
	tokenEndpoint string
	registered    []map[string]any
	exchanged     []url.Values
	// noRefresh leaves the refresh token out of the answer.
	noRefresh bool
}

func (f *fakeAtlassian) oauth() *atlassianOAuth {
	return &atlassianOAuth{Client: f.srv.Client(), Resource: f.srv.URL + "/v2/mcp", TokenURL: f.srv.URL + "/oauth/token"}
}

func mapKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func callbackGet(t *testing.T, redirect, query string) (int, string) {
	t.Helper()
	resp, err := http.Get(redirect + "?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func stateOf(t *testing.T, authorize string) string {
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// waitClosed waits until nothing serves the redirect's address. It asks for a
// path the handler answers with a 404 (no state change) instead of only
// connecting: under OpenShell's sandbox broker a closed listener can stay in
// LISTEN, accepting connections that nobody answers, until the broker
// reclaims it.
func waitClosed(t *testing.T, redirect string) {
	t.Helper()
	u, _ := url.Parse(redirect)
	client := http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + u.Host + "/")
		if err != nil {
			return
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("the callback listener stays open")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The profile: refresh by the gateway at Atlassian's token URL, the MCP
// path only, mise's claude.
func TestAtlassianProfile(t *testing.T) {
	prof, ok := profileByID(atlassianProfileID)
	if !ok || !prof.OAuth || prof.Guide != `claude mcp add --transport http atlassian 'https://mcp.atlassian.com/v2/mcp?tools=all' --header 'Authorization: Bearer ${ATLASSIAN_ACCESS_TOKEN}'` {
		t.Fatalf("%+v", prof)
	}
	p := prof.proto()
	c := p.Credentials[0]
	r := c.Refresh
	if c.EnvVars[0] != "ATLASSIAN_ACCESS_TOKEN" || c.AuthStyle != "bearer" || r.TokenUrl != "https://auth.atlassian.com/oauth/token" || r.Strategy.String() != "PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_REFRESH_TOKEN" || len(r.Scopes) != 0 {
		t.Fatal(c)
	}
	if len(r.Material) != 2 || r.Material[0].Name != "client_id" || r.Material[0].Secret || r.Material[1].Name != "refresh_token" || !r.Material[1].Secret {
		t.Fatal(r.Material)
	}
	e := initialEndpoints(t, atlassianProfileID)
	if len(e) != 1 || e[0].Host != "mcp.atlassian.com" || e[0].Port != 443 || e[0].Path != "/v2/mcp/**" || e[0].Protocol != "mcp" || e[0].Mcp == nil || e[0].Mcp.AllowAllKnownMcpMethods == nil || !*e[0].Mcp.AllowAllKnownMcpMethods ||
		len(e[0].Rules) != 1 || e[0].Rules[0].Allow.Method != "tools/call" || !slices.Contains(e[0].Rules[0].Allow.Params["name"].Any, "searchJiraIssuesUsingJql") || slices.Contains(e[0].Rules[0].Allow.Params["name"].Any, "execute") {
		t.Fatal(e)
	}
	if len(p.Binaries) != 2 || p.Binaries[0].Path != "/home/dev/.local/share/claude/versions/*" || p.Binaries[1].Path != "/home/dev/.local/share/mise/installs/claude/*/claude" {
		t.Fatal(p.Binaries)
	}
}

// oauthFlowTest drives a sign-in flow through the server's API.
type oauthFlowTest struct {
	t  *testing.T
	s  *Server
	do func(method, path, body string) (int, string)
}

func newFakeAtlassian(t *testing.T) *fakeAtlassian {
	f := &fakeAtlassian{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/v2/mcp", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, map[string]any{
			"resource":                 f.srv.URL + "/v2/mcp",
			"authorization_servers":    []string{f.srv.URL + "/tenant1"},
			"bearer_methods_supported": []string{"header"},
			"scopes_supported":         atlassianScopes,
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/tenant1", func(w http.ResponseWriter, r *http.Request) {
		te := f.srv.URL + "/oauth/token"
		f.mu.Lock()
		if f.tokenEndpoint != "" {
			te = f.tokenEndpoint
		}
		f.mu.Unlock()
		jsonResponse(w, 200, map[string]any{
			"issuer":                                f.srv.URL + "/tenant1",
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        te,
			"registration_endpoint":                 f.srv.URL + "/tenant1/dcr/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"response_types_supported":              []string{"code", "token"},
		})
	})
	mux.HandleFunc("/tenant1/dcr/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad", 400)
			return
		}
		f.mu.Lock()
		f.registered = append(f.registered, body)
		f.mu.Unlock()
		jsonResponse(w, 201, map[string]any{"client_id": "client-123", "token_endpoint_auth_method": "none", "redirect_uris": body["redirect_uris"]})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil || r.Method != "POST" {
			http.Error(w, "bad", 400)
			return
		}
		f.mu.Lock()
		f.exchanged = append(f.exchanged, r.PostForm)
		noRefresh := f.noRefresh
		f.mu.Unlock()
		if r.PostForm.Get("code") != "good-code" {
			jsonResponse(w, 400, map[string]string{"error": "invalid_grant", "error_description": "code good-code-ECHO was not issued"})
			return
		}
		resp := map[string]any{"access_token": "at-SECRET-1", "token_type": "Bearer", "expires_in": 3600, "scope": "offline_access read:me"}
		if !noRefresh {
			resp["refresh_token"] = "rt-SECRET-1"
			resp["refresh_token_expires_in"] = 7776000
		}
		jsonResponse(w, 200, resp)
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (o *oauthFlowTest) start(body string) flowView {
	o.t.Helper()
	code, resp := o.do("POST", "/api/oauth/atlassian/start", body)
	var v flowView
	if code != 200 || json.Unmarshal([]byte(resp), &v) != nil {
		o.t.Fatal(code, resp)
	}
	return v
}

func (o *oauthFlowTest) flow(id string) flowView {
	o.t.Helper()
	code, resp := o.do("GET", "/api/oauth/atlassian/flows/"+id, "")
	var v flowView
	if code != 200 || json.Unmarshal([]byte(resp), &v) != nil {
		o.t.Fatal(code, resp)
	}
	if strings.Contains(resp, "SECRET") || strings.Contains(resp, "good-code") {
		o.t.Fatal("a value reached the page:", resp)
	}
	return v
}

func (o *oauthFlowTest) wait(id string, statuses ...string) flowView {
	o.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := o.flow(id)
		if slices.Contains(statuses, v.Status) {
			return v
		}
		if time.Now().After(deadline) {
			o.t.Fatalf("flow stays %s: %+v", v.Status, v)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
