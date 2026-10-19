package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellScopeE2E checks, against the gateway
// internal/openshell/wslc/e2e.sh starts (MODE=tls), the provider scopes
// (providers_scope.go): OpenShell's lint takes every rendered GitHub
// scope, both scopes of the other built-in profiles and a custom profile;
// a new GitHub provider gets its own profile; a scope change updates it,
// the running sandbox it is attached to gets the new rules in its
// effective policy, and Iwato follows the change (反映待ち); deleting the
// provider deletes its profile.
func TestOpenShellScopeE2E(t *testing.T) {
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
	if err := ensureProfiles(ctx, api); err != nil {
		t.Fatal(err)
	}
	o.profilesDone = true

	// OpenShell's lint takes every kind of scope, and a custom profile.
	var items []*pb.ProviderProfileImportItem
	for name, sc := range map[string]ProviderScope{
		"lint-ro":    {Base: githubProfileID, Mode: scopeReadOnly},
		"lint-repos": {Base: githubProfileID, Mode: scopeRepos, Repos: []string{"octo-org/octo-repo", "octo-org/.github"}},
		"lint-mut":   {Base: githubProfileID, Mode: scopeRepos, Repos: []string{"a/b"}, Mutations: []string{"createPullRequest", "addComment"}},
		"lint-full":  {Base: githubProfileID, Mode: scopeFull},
	} {
		m, err := renderScope(name, sc)
		if err != nil {
			t.Fatal(name, err)
		}
		items = append(items, &pb.ProviderProfileImportItem{Profile: m, Source: name})
	}
	// Every other built-in profile, read-only and with writes allowed.
	for _, p := range providerProfiles {
		if p.ID == githubProfileID {
			continue
		}
		for _, mode := range []string{scopeReadOnly, scopeFull} {
			m, err := renderScope("lint-"+mode, ProviderScope{Base: p.ID, Mode: mode})
			if err != nil {
				t.Fatal(p.ID, err)
			}
			items = append(items, &pb.ProviderProfileImportItem{Profile: m, Source: m.Id})
		}
	}
	custom, _, err := parseCustomProfile([]byte(customProfileExample))
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, &pb.ProviderProfileImportItem{Profile: custom, Source: "custom"})
	lint, err := api.LintProviderProfiles(ctx, &pb.LintProviderProfilesRequest{Profiles: items})
	if err != nil || !lint.GetValid() {
		t.Fatalf("lint: %v %v", err, lint.GetDiagnostics())
	}
	for _, d := range lint.GetDiagnostics() {
		t.Logf("lint %s: %s %s: %s", d.GetSource(), d.GetSeverity(), d.GetField(), d.GetMessage())
	}

	w := newSandbox("iwato-scope-e2e")
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start:", err)
	}
	defer o.Delete(context.Background(), w, true)
	c.Sandboxes = []Sandbox{w}
	s := newServer(c, o, false, "127.0.0.1:29998")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		r := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return r.Code, r.Body.String()
	}
	const name = "github-scope"
	id := scopedProfileID(githubProfileID, name)
	if code, body := do("POST", "/api/providers", `{"name":"`+name+`","profile":"iwato-github","values":{"token":"ghp_e2eSECRETscope"}}`); code != 200 {
		t.Fatal("create:", code, body)
	}
	deleted := false
	defer func() {
		if !deleted {
			o.DetachProvider(context.Background(), w.Name, name)
			o.DeleteProvider(context.Background(), name)
			o.DeleteProviderProfile(context.Background(), id)
		}
	}()
	if p, err := o.ProviderProfile(ctx, id); err != nil || p == nil {
		t.Fatal("the provider's profile:", p, err)
	}
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/attach", `{"provider":"`+name+`"}`); code != 200 {
		t.Fatal("attach:", code, body)
	}

	// Write to one repository: the sandbox's effective policy gets the push.
	if code, body := do("POST", "/api/providers/"+name+"/scope", `{"mode":"repos","repos":["octo-org/octo-repo"],"mutations":["createPullRequest"]}`); code != 200 {
		t.Fatal("scope:", code, body)
	}
	push := func() bool {
		_, effective, err := o.PolicyLayers(ctx, w)
		if err != nil {
			t.Fatal(err)
		}
		for key, r := range effective {
			if !strings.HasPrefix(key, "_provider_github_scope") {
				continue
			}
			for _, e := range r.Endpoints {
				if e.Host == "github.com" && slices.ContainsFunc(e.Rules, func(l v1.L7Rule) bool {
					return l.Allow != nil && l.Allow.Path == "/octo-org/octo-repo.git/git-receive-pack"
				}) {
					return true
				}
			}
		}
		return false
	}
	deadline := time.Now().Add(time.Minute)
	for !push() {
		if time.Now().After(deadline) {
			t.Fatal("the effective policy lacks the push after the scope change")
		}
		time.Sleep(2 * time.Second)
	}
	// Iwato follows the change: the observation's receipt becomes ready.
	sb, _ := s.lookupSandbox(w.Name)
	i := slices.IndexFunc(sb.ProviderApplying, func(a ProviderApply) bool { return a.Provider == name && a.Scope })
	if i < 0 {
		t.Fatal("no pending scope change:", sb.ProviderApplying)
	}
	receipt := sb.ProviderApplying[i].Receipt
	var state, reason string
	for deadline = time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if state, reason, err = o.ProviderReadiness(ctx, w.Name, name, receipt); err != nil {
			t.Fatal(err)
		}
		if state != "pending" {
			break
		}
	}
	t.Logf("the scope change's readiness: %s %s", state, reason)
	if state != "ready" && state != "superseded" {
		t.Fatalf("readiness %s (%s)", state, reason)
	}

	// The owner's YAML, then back to the simple scope.
	y, err := profileYAML(mustRender(t, name, ProviderScope{Base: githubProfileID, Mode: scopeFull}))
	if err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/providers/"+name+"/scope", jsonString(t, map[string]string{"yaml": y})); code != 200 {
		t.Fatal("yaml:", code, body)
	}
	if code, body := do("POST", "/api/providers/"+name+"/scope", `{"reset":true}`); code != 200 {
		t.Fatal("reset:", code, body)
	}

	// Deleting the provider deletes its profile.
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/providers/detach", `{"provider":"`+name+`"}`); code != 200 {
		t.Fatal("detach:", code, body)
	}
	if code, body := do("POST", "/api/providers/"+name+"/delete", `{}`); code != 200 {
		t.Fatal("delete:", code, body)
	}
	deleted = true
	if _, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: id}); status.Code(err) != codes.NotFound {
		t.Fatal("the profile outlived its provider:", err)
	}
}
