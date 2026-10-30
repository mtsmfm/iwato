package main

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellPresetsE2E checks, against the gateway that
// internal/openshell/wslc/e2e.sh starts (MODE=tls), that the gateway takes
// Iwato's presets as Iwato predicts: a sandbox created with all of them
// and the GitHub provider (its policy validated and composed with the
// provider's rule), and a preset put into its policy later and taken out
// again (merge operations, with the provider attached), an owner's preset
// twice with different parameters.
func TestOpenShellPresetsE2E(t *testing.T) {
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
	if err := ensureProfiles(ctx, pb.NewOpenShellClient(conn)); err != nil {
		t.Fatal(err)
	}
	o.profilesDone = true
	gh, _ := profileByID("iwato-github")
	creds, _ := profileCredentials(gh, map[string]string{"token": "ghp_e2eSECRETpresets"})
	if err := o.CreateProvider(ctx, "github-pre", "iwato-github", creds); err != nil {
		t.Fatal(err)
	}
	defer o.DeleteProvider(context.Background(), "github-pre")

	withUserPresets(t, map[string]string{"mirror.yaml": mirrorPreset, "shared.yaml": sharedPreset})
	w := newSandbox("iwato-presets-e2e")
	w.Presets = append(allBuiltinPresets(), PresetRef{ID: "user:shared"})
	w.Providers = []string{"github-pre"}
	if err := o.Start(ctx, w); err != nil {
		t.Fatal("start with every preset and the GitHub provider:", err)
	}
	defer o.Delete(context.Background(), w, true)
	base, effective, err := o.PolicyLayers(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	for name := range presetRules(w.Presets) {
		if _, ok := base[name]; !ok {
			t.Fatalf("the first policy lacks %s: %v", name, base)
		}
	}
	// The provider's initial rule, bound to it, in the first policy.
	if r, ok := base["provider_github_pre"]; !ok || len(r.Endpoints) == 0 || r.Endpoints[0].CredentialBinding == nil || r.Endpoints[0].CredentialBinding.Provider != "github-pre" {
		t.Fatalf("the provider's rule is not in the first policy: %+v (effective %v)", r, slices.Sorted(maps.Keys(effective)))
	}

	c.Sandboxes = []Sandbox{w}
	s := newServer(c, o, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	do := func(method, path, body string) (int, string) {
		r := reviewRequest(s, method, path, body, s.origin, "same-origin", s.host, cookie, csrf)
		return r.Code, r.Body.String()
	}
	// An owner's preset with a parameter, twice: the gateway keeps two
	// rules (their endpoints differ by path, so none is folded into the
	// other), and taking one out leaves the other.
	mirror := func(repo string) string { return `{"id":"user:mirror","params":{"repo":"` + repo + `"}}` }
	for _, repo := range []string{"o/a", "o/b"} {
		if code, body := do("POST", "/api/sandboxes/"+w.Name+"/presets/apply", mirror(repo)); code != 200 {
			t.Fatal("apply", repo, code, body)
		}
	}
	base, _, _ = o.PolicyLayers(ctx, w)
	if _, ok := base["mirror_o_a"]; !ok {
		t.Fatalf("o/a is not in the policy: %v", base)
	}
	if _, ok := base["mirror_o_b"]; !ok {
		t.Fatalf("o/b is not in the policy: %v", base)
	}
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/presets/remove", mirror("o/b")); code != 200 || !strings.Contains(body, `"kept":[]`) {
		t.Fatal("remove:", code, body)
	}
	// user:shared shares preset_packages_downloads_claude_ai with packages:
	// taken out, the rule stays as packages makes it.
	if code, body := do("POST", "/api/sandboxes/"+w.Name+"/presets/remove", `"user:shared"`); code != 200 || !strings.Contains(body, `"kept":[]`) {
		t.Fatal("remove a preset sharing a rule:", code, body)
	}
	base, _, _ = o.PolicyLayers(ctx, w)
	mise, _ := presetRefRules(PresetRef{ID: "packages"})
	oa, _ := presetRefRules(PresetRef{ID: "user:mirror", Params: map[string]string{"repo": "o/a"}})
	if _, ok := base["mirror_o_b"]; ok || !samePresetRule(base["mirror_o_a"], oa["mirror_o_a"]) || !samePresetRule(base["preset_packages_downloads_claude_ai"], mise["preset_packages_downloads_claude_ai"]) {
		t.Fatalf("after the removals: %+v", base)
	}
	if _, ok := base["allow_claude_ai_443"]; ok {
		t.Fatal("user:shared's own rule stayed")
	}
}
