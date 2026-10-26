package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// fakeTokenEndpoint is Atlassian's token endpoint as the gateway's refresh
// sees it (loopback http: OpenShell allows it for tests). It rotates the
// refresh token on every use and accepts only the current one.
type fakeTokenEndpoint struct {
	mu      sync.Mutex
	current string
	n       int
	forms   []url.Values
}

func (f *fakeTokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/oauth/token" || r.Method != "POST" || r.ParseForm() != nil {
		http.Error(w, "bad", 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forms = append(f.forms, r.PostForm)
	if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != "client-123" || r.PostForm.Get("refresh_token") != f.current {
		jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	f.n++
	f.current = "rt-SECRET-rotated-" + string(rune('0'+f.n))
	// Longer than the profile's max_lifetime: the gateway caps it.
	jsonResponse(w, 200, map[string]any{"access_token": "at-SECRET-minted", "token_type": "Bearer", "expires_in": 7200, "refresh_token": f.current})
}

// TestOpenShellAtlassianE2E runs against the gateway that
// internal/openshell/wslc/e2e.sh starts (MODE=tls): the iwato-atlassian
// profile imports with its refresh; Iwato's whole OAuth flow (with a fake
// Atlassian) creates a provider the real gateway refreshes, at a loopback
// token endpoint (a copy of the profile with that token_url), rotating
// the refresh token; the sandbox gets a placeholder.
func TestOpenShellAtlassianE2E(t *testing.T) {
	addr := os.Getenv("OPENSHELL_E2E_ADDR")
	if addr == "" {
		t.Skip("run by internal/openshell/wslc/e2e.sh")
	}
	tlsDir := os.Getenv("OPENSHELL_E2E_TLS")
	ca, cert, key := filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "client", "tls.crt"), filepath.Join(tlsDir, "client", "tls.key")
	c := defaultConfig()
	c.WSLC = os.Getenv("OPENSHELL_E2E_WSLC")
	b, err := NewEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.Dial(addr, ca, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := v1.NewClient(v1.Config{Address: addr, TLS: &v1.TLSConfig{CAFile: ca, CertFile: cert, KeyFile: key}})
	if err != nil {
		t.Fatal(err)
	}
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api := pb.NewOpenShellClient(conn)

	// The real profile imports, with its refresh and the MCP path.
	if err := ensureProfiles(ctx, api); err != nil {
		t.Fatal("import the profiles:", err)
	}
	o.profilesDone = true
	got, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: atlassianProfileID})
	if err != nil {
		t.Fatal(err)
	}
	gp := got.GetProfile()
	r := gp.GetCredentials()[0].GetRefresh()
	if r.GetTokenUrl() != atlassianTokenURL || r.GetStrategy() != pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_REFRESH_TOKEN || len(r.GetMaterial()) != 2 || gp.GetEndpoints()[0].GetPath() != "/v2/mcp/**" {
		t.Fatalf("the gateway's iwato-atlassian lost detail: %v", gp)
	}
	// A provider of the real profile takes the refresh configuration (the
	// gateway does not call Atlassian before the expiry).
	if err := o.CreateProvider(ctx, "atlassian-real-e2e", atlassianProfileID, map[string]string{atlassianCredentialKey: "at-SECRET-real"}); err != nil {
		t.Fatal("create:", err)
	}
	defer o.DeleteProvider(context.Background(), "atlassian-real-e2e")
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := o.ConfigureRefresh(ctx, "atlassian-real-e2e", atlassianCredentialKey, map[string]string{"client_id": "client-real", "refresh_token": "rt-SECRET-real"}, []string{"refresh_token"}, exp); err != nil {
		t.Fatal("configure:", err)
	}
	st, err := o.RefreshStatus(ctx, "atlassian-real-e2e", atlassianCredentialKey)
	if err != nil || st.Status != "configured" || !st.ExpiresAt.Equal(exp) || st.NextRefreshAt.Sub(exp) != -300*time.Second {
		t.Fatalf("status of the real profile's provider: %+v %v", st, err)
	}
	// The gateway keeps token URLs in the profile.
	if err := o.ConfigureRefresh(ctx, "atlassian-real-e2e", atlassianCredentialKey, map[string]string{"client_id": "c", "refresh_token": "r", "token_url": "https://evil.example/token"}, []string{"refresh_token"}, exp); err == nil {
		t.Fatal("the gateway took a token_url from the material")
	}

	// The test profile: the same, refreshing at the loopback endpoint.
	tokens := &fakeTokenEndpoint{current: "rt-SECRET-1"}
	ts := httptest.NewServer(tokens)
	defer ts.Close()
	const testProfile = "iwato-atlassian-e2e"
	prof := atlassianProfile(testProfile, ts.URL+"/oauth/token")
	if _, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: testProfile}); err == nil {
		_, _ = api.DeleteProviderProfile(ctx, &pb.DeleteProviderProfileRequest{Id: testProfile})
	}
	resp, err := api.ImportProviderProfiles(ctx, &pb.ImportProviderProfilesRequest{Profiles: []*pb.ProviderProfileImportItem{{Profile: prof, Source: "iwato-e2e"}}})
	if err != nil || !resp.GetImported() {
		t.Fatal("import the test profile:", err, diagnosticsError(resp.GetDiagnostics()))
	}
	defer api.DeleteProviderProfile(context.Background(), &pb.DeleteProviderProfileRequest{Id: testProfile})

	// Iwato's flow: discovery, DCR, the redirect, the exchange (fake
	// Atlassian), then Create, Configure and Rotate on the real gateway.
	s := newServer(c, o, false, "127.0.0.1:29999")
	fake := newFakeAtlassian(t)
	s.oauth.backend = fake.oauth()
	s.oauth.profile = testProfile
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return w.Code, w.Body.String()
	}
	ot := &oauthFlowTest{t: t, s: s, do: do}
	v := ot.start(`{"name":"atlassian-e2e","scopes":["read:me"]}`)
	defer o.DeleteProvider(context.Background(), "atlassian-e2e")
	if code, page := callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL)); code != 200 {
		t.Fatal(code, page)
	}
	done := ot.wait(v.ID, "done", "failed")
	if done.Status != "done" || done.Warning != "" || done.Refresh == nil || done.Refresh.Status != "refreshed" {
		t.Fatalf("the flow against the gateway: %+v %+v", done, done.Refresh)
	}
	// The gateway's refresh: a public client, no secret, no resource, no
	// scope (the profile has none).
	tokens.mu.Lock()
	form := tokens.forms[0]
	tokens.mu.Unlock()
	if form.Get("refresh_token") != "rt-SECRET-1" || form.Has("client_secret") || form.Has("resource") || form.Has("scope") {
		t.Fatalf("the gateway's refresh request: %v", form.Encode())
	}
	// A second refresh uses the rotated token the gateway stored.
	st, err = o.RotateRefresh(ctx, "atlassian-e2e", atlassianCredentialKey)
	if err != nil || st.Status != "refreshed" {
		t.Fatalf("second rotation: %+v %v", st, err)
	}
	if lifetime := st.ExpiresAt.Sub(st.LastRefreshAt); lifetime < 3590*time.Second || lifetime > 3600*time.Second {
		t.Fatalf("lifetime %s, want max_lifetime (1h)", lifetime)
	}
	tokens.mu.Lock()
	if len(tokens.forms) != 2 || tokens.forms[1].Get("refresh_token") != "rt-SECRET-rotated-1" {
		t.Fatalf("the rotated refresh token was not used: %d requests", len(tokens.forms))
	}
	tokens.mu.Unlock()
	code, body := do("GET", "/api/providers", "")
	var l providerList
	if code != 200 || json.Unmarshal([]byte(body), &l) != nil || strings.Contains(body, "-SECRET") {
		t.Fatal(code, body)
	}
	if p, ok := provider(l, "atlassian-e2e"); !ok || p.Refresh == nil || p.Refresh.Status != "refreshed" || p.Profile != testProfile {
		t.Fatalf("listed: %+v", p)
	}
	// Reconnecting the same provider (a new grant, a new epoch).
	tokens.mu.Lock()
	tokens.current = "rt-SECRET-1"
	tokens.mu.Unlock()
	v = ot.start(`{"name":"atlassian-e2e","scopes":[],"reconnect":true}`)
	callbackGet(t, v.RedirectURI, "code=good-code&state="+stateOf(t, v.AuthorizeURL))
	if done := ot.wait(v.ID, "done", "failed"); done.Status != "done" || done.Warning != "" {
		t.Fatalf("reconnect against the gateway: %+v", done)
	}

	// The sandbox gets a placeholder, never the token.
	w := newSandbox("iwato-atl-e2e")
	w.Providers = []string{"atlassian-e2e"}
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"sh", "-c", "echo ATL=$ATLASSIAN_ACCESS_TOKEN"}, client.Options{})
	if err != nil {
		t.Fatal("exec:", err)
	}
	out, _ := io.ReadAll(p)
	p.Wait()
	if strings.Contains(string(out), "SECRET") || !regexp.MustCompile(`(?m)^ATL=` + placeholderPrefix + `:\S*ATLASSIAN_ACCESS_TOKEN$`).Match(out) {
		t.Fatalf("the sandbox's environment:\n%s", out)
	}
	t.Logf("sandbox: %s", strings.TrimSpace(string(out)))
}
