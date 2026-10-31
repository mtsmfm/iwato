package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellGrokSubscriptionE2E runs against the gateway that
// internal/openshell/wslc/e2e.sh starts (MODE=tls): the
// iwato-grok-subscription profile imports with its refresh; Iwato's device
// code flow (with a fake auth.x.ai) creates a provider the real gateway
// refreshes, at a loopback token endpoint (a copy of the profile with that
// token_url), rotating the refresh token; the sandbox gets a placeholder in
// XAI_OAUTH_TOKEN, GROK_AUTH_PROVIDER_COMMAND in its environment, and the
// command prints the placeholder as grok's xAI session.
func TestOpenShellGrokSubscriptionE2E(t *testing.T) {
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
	o := &OpenShell{e: b, ready: make(chan struct{}), sdk: sdk, conn: conn, env: termEnv, terms: map[string]map[string]*hostterm.Session{}, prep: ImageState{Phase: "ready"}}
	close(o.ready)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	api := pb.NewOpenShellClient(conn)

	// The real profile imports, with its refresh and both endpoints.
	if err := ensureProfiles(ctx, api); err != nil {
		t.Fatal("import the profiles:", err)
	}
	o.profilesDone = true
	got, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: grokSubscriptionProfileID})
	if err != nil {
		t.Fatal(err)
	}
	gp := got.GetProfile()
	r := gp.GetCredentials()[0].GetRefresh()
	if r.GetTokenUrl() != xaiTokenURL || r.GetStrategy() != pb.ProviderCredentialRefreshStrategy_PROVIDER_CREDENTIAL_REFRESH_STRATEGY_OAUTH2_REFRESH_TOKEN || len(r.GetMaterial()) != 2 ||
		len(gp.GetEndpoints()) != 0 || gp.GetAnnotations()[credentialHostsKey] != "cli-chat-proxy.grok.com:443,api.x.ai:443" {
		t.Fatalf("the gateway's %s lost detail: %v", grokSubscriptionProfileID, gp)
	}

	// The test profile: the same, refreshing at the fake auth.x.ai.
	shortXAIPolls(t)
	fake := newFakeXAI(t)
	const testProfile = "iwato-grok-subscription-e2e"
	prof := profileWithTokenURL(grokSubscriptionProfileID, testProfile, fake.URL+"/oauth2/token")
	// Its providers get the initial rule that binds the credential (ADR
	// 0022): without a binding OpenShell withholds it.
	aliasProviderRules(t, testProfile, grokSubscriptionProfileID)
	if _, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: testProfile}); err == nil {
		_, _ = api.DeleteProviderProfile(ctx, &pb.DeleteProviderProfileRequest{Id: testProfile})
	}
	resp, err := api.ImportProviderProfiles(ctx, &pb.ImportProviderProfilesRequest{Profiles: []*pb.ProviderProfileImportItem{{Profile: prof, Source: "iwato-e2e"}}})
	if err != nil || !resp.GetImported() {
		t.Fatal("import the test profile:", err, diagnosticsError(resp.GetDiagnostics()))
	}
	defer api.DeleteProviderProfile(context.Background(), &pb.DeleteProviderProfileRequest{Id: testProfile})

	// Iwato's flow: the device code, the polls (slow_down, pending), the
	// tokens (fake xAI), then Create, Configure and Rotate on the gateway.
	w := newSandbox("iwato-grok-e2e")
	c.Sandboxes = append(c.Sandboxes, w)
	s := newServer(c, o, false, "127.0.0.1:29999")
	s.xai.backend = fake.oauth()
	s.xai.profile = testProfile
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		w := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return w.Code, w.Body.String()
	}
	x := &xaiTest{t: t, s: s, do: do}
	v := x.start(`{"name":"grok-e2e"}`)
	defer o.DeleteProvider(context.Background(), "grok-e2e")
	done := x.wait(v.ID, "done", "failed")
	if done.Status != "done" || done.Warning != "" || done.Refresh == nil || done.Refresh.Status != "refreshed" {
		t.Fatalf("the flow against the gateway: %+v %+v", done, done.Refresh)
	}
	// The gateway's refresh is Grok Build's: a public client, no secret,
	// no scope, no resource.
	fake.mu.Lock()
	var refreshes []string
	for _, f := range fake.forms {
		if f.Get("grant_type") == "refresh_token" {
			refreshes = append(refreshes, f.Get("refresh_token"))
			if f.Get("client_id") != xaiClientID || f.Has("client_secret") || f.Has("scope") || f.Has("resource") {
				t.Errorf("the gateway's refresh request has the fields %v", mapKeysOf(f))
			}
		}
	}
	fake.mu.Unlock()
	if len(refreshes) != 1 || refreshes[0] != "rt-SECRET-1" {
		t.Fatalf("%d refreshes", len(refreshes))
	}
	// A second refresh uses the rotated token the gateway stored.
	st, err := o.RotateRefresh(ctx, "grok-e2e", grokOAuthCredentialKey)
	if err != nil || st.Status != "refreshed" {
		t.Fatalf("second rotation: %+v %v", st, err)
	}
	fake.mu.Lock()
	last := fake.forms[len(fake.forms)-1]
	fake.mu.Unlock()
	if last.Get("refresh_token") != "rt-SECRET-rotated-1" {
		t.Fatal("the rotated refresh token was not used")
	}
	code, body := do("GET", "/api/providers", "")
	var l providerList
	if code != 200 || json.Unmarshal([]byte(body), &l) != nil || strings.Contains(body, "-SECRET") {
		t.Fatal(code, body)
	}
	if p, ok := provider(l, "grok-e2e"); !ok || p.Refresh == nil || p.Refresh.Status != "refreshed" || p.Profile != testProfile {
		t.Fatalf("listed: %+v", p)
	}
	// Reconnecting the same provider (a new grant, a new epoch).
	v = x.start(`{"name":"grok-e2e","reconnect":true}`)
	if done := x.wait(v.ID, "done", "failed"); done.Status != "done" || done.Warning != "" {
		t.Fatalf("reconnect against the gateway: %+v", done)
	}

	// The sandbox, through Iwato's path: Iwato writes ~/.config/iwato/env
	// at the start; a running sandbox gets the provider (the page's
	// attach) and restarts (stop and start); then a shell that reads the
	// image's env.sh has grok's command (the supervisor gives it
	// XAI_OAUTH_TOKEN), which prints the placeholder as an xAI session.
	// After the detach and a restart the command is gone, though the
	// file's line stays. (The e2e's sandboxes run on the host, not in the
	// image: the shell runs the image's env.sh itself.)
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	envSh, err := imageFiles.ReadFile("sandboximage/etc/env.sh")
	if err != nil {
		t.Fatal(err)
	}
	shell := func(cmd string) string {
		t.Helper()
		p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"/bin/sh", "-c", exportHome + string(envSh) + cmd}, client.Options{})
		if err != nil {
			t.Fatal("exec:", err)
		}
		out, _ := io.ReadAll(p)
		p.Wait()
		return string(out)
	}
	command := func() string { return shell(`printf %s "${GROK_AUTH_PROVIDER_COMMAND:-}"`) }
	// The list's state of the sandbox, as the page reads it.
	listed := func() Status {
		t.Helper()
		code, body := do("GET", "/api/sandboxes", "")
		var l struct{ Sandboxes []Status }
		if code != 200 || json.Unmarshal([]byte(body), &l) != nil {
			t.Fatal("list:", code, body)
		}
		for _, st := range l.Sandboxes {
			if st.Name == w.Name {
				return st
			}
		}
		t.Fatal("not listed:", body)
		return Status{}
	}
	// The page's 今すぐ再起動: stop, then start, through the API.
	restart := func() {
		t.Helper()
		if st := listed(); st.State != "running" || !st.RestartPending {
			t.Fatalf("before the restart: %+v", st)
		}
		if code, body := do("POST", "/api/sandboxes/"+w.Name+"/stop", "{}"); code != 200 {
			t.Fatal("stop:", code, body)
		}
		if st := listed(); st.State != "stopped" {
			t.Fatalf("after the stop: %+v", st)
		}
		if code, body := do("POST", "/api/sandboxes/"+w.Name+"/start", "{}"); code != 200 {
			t.Fatal("start:", code, body)
		}
		if st := listed(); st.State != "running" || st.RestartPending {
			t.Fatalf("after the restart: %+v", st)
		}
	}
	if got := command(); got != "" {
		t.Fatalf("a sandbox without the provider has the command: %q", got)
	}
	if out := shell(`cat "$HOME/` + termEnvPath + `"`); out != termEnvContent(false, sandboxTimezone("")) {
		t.Fatalf("the file after the start:\n%s", out)
	}
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"grok-e2e"}`); code != 200 || !strings.Contains(body, `"restart":true`) {
		t.Fatal("attach:", code, body)
	}
	restart()
	if got := command(); got != grokAuthCommand {
		t.Fatalf("after the attach and a restart, the terminals' command is %q", got)
	}
	out := shell(`sh -c "$GROK_AUTH_PROVIDER_COMMAND"`)
	// Never print a placeholder: the test's log may reach an agent's conversation.
	masked := strings.ReplaceAll(out, placeholderPrefix, "<placeholder>")
	var session struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Issuer      string `json:"issuer"`
	}
	if strings.Contains(out, "SECRET") || json.Unmarshal([]byte(out), &session) != nil || session.Issuer != xaiIssuer || session.ExpiresIn <= 0 ||
		!regexp.MustCompile(`^`+regexp.QuoteMeta(placeholderPrefix)+`:\S*`+grokOAuthCredentialKey+`$`).MatchString(session.AccessToken) {
		t.Fatalf("grok's auth command in the sandbox printed:\n%s", masked)
	}
	t.Logf("sandbox: grok's auth command prints an xAI session whose access_token is the %s placeholder", grokOAuthCredentialKey)
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/detach", `{"provider":"grok-e2e"}`); code != 200 {
		t.Fatal("detach:", code, body)
	}
	restart()
	if got := command(); got != "" {
		t.Fatalf("after the detach and a restart, the terminals' command is %q", got)
	}
	if out := shell(`cat "$HOME/` + termEnvPath + `"`); out != termEnvContent(false, sandboxTimezone("")) {
		t.Fatalf("the file after the detach:\n%s", out)
	}
}

func mapKeysOf[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
