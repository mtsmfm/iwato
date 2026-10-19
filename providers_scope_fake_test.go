//go:build uitest

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// A scope change the policy of a sandbox the provider is attached to
// would refuse: a code, the sandbox and the conflicts, for the page.
func TestProviderScopeRefusal(t *testing.T) {
	s, d, do, _ := providerServerCookie(t)
	s.configPath = filepath.Join(t.TempDir(), "iwato.json")
	t.Cleanup(func() { unregisterProfile("iwato-github-for-gh") })
	if code, body := do("POST", "/api/providers", `{"name":"gh","profile":"iwato-github","values":{"token":"x"}}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"gh"}`); code != 200 {
		t.Fatal(code, body)
	}
	d.mu.Lock()
	revs := d.fakePolicy("dev")
	rules := cloneRules(revs[len(revs)-1].rules)
	rules["allow_gh"] = v1.NetworkPolicyRule{Name: "allow_gh", Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint("api.github.com", 443)}}
	d.policies["dev"] = append(revs, fakeRevision{rules: rules})
	d.started["dev"] = true
	d.mu.Unlock()
	code, body := do("POST", "/api/providers/gh/scope", `{"mode":"full"}`)
	var got struct {
		Code      string
		Params    map[string]string
		Conflicts []CredentialConflict
	}
	json.Unmarshal([]byte(body), &got)
	if code != 409 || got.Code != "provider_scope_conflict" || got.Params["sandbox"] != "dev" || len(got.Conflicts) == 0 || got.Conflicts[0].Rule != "allow_gh" {
		t.Fatal(code, body)
	}
}

// The scope API: a new GitHub provider gets its own profile, the simple
// scope and the owner's YAML change it (checked first), reset goes back,
// a shared one is refused; custom profiles are files in the config
// folder, loaded again when Iwato starts, not deleted while in use.
func TestProviderScopeAPI(t *testing.T) {
	s, _, do, _ := providerServerCookie(t)
	dir := t.TempDir()
	s.configPath = filepath.Join(dir, "iwato.json")
	t.Cleanup(func() {
		for _, id := range []string{"iwato-github-for-gh", "example-api"} {
			unregisterProfile(id)
		}
	})
	if code, body := do("POST", "/api/providers", `{"name":"gh","profile":"iwato-github","values":{"token":"x"}}`); code != 200 {
		t.Fatal(code, body)
	}
	p, _ := provider(listProviders(t, do), "gh")
	if p.Profile != "iwato-github-for-gh" || p.Base != githubProfileID || p.Scope == nil || p.Scope.Mode != scopeReadOnly || p.Scope.Missing {
		t.Fatalf("%+v %+v", p, p.Scope)
	}
	if code, body := do("POST", "/api/providers/gh/scope", `{"mode":"repos","repos":["o/r","o/*"]}`); code != 400 || !strings.Contains(body, "OWNER/REPO") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/providers/gh/scope", `{"mode":"repos","repos":["o/r"],"mutations":["createPullRequest"]}`); code != 200 {
		t.Fatal(code, body)
	}
	if sc, _ := s.providerScope("gh"); sc.Mode != scopeRepos || !slices.Equal(sc.Repos, []string{"o/r"}) {
		t.Fatal(sc)
	}
	code, body := do("GET", "/api/provider-profiles/iwato-github-for-gh", "")
	if code != 200 || !strings.Contains(body, `"source":"scope"`) || !strings.Contains(body, "/o/r.git/git-receive-pack") {
		t.Fatal(code, body)
	}
	var got struct{ YAML string }
	json.Unmarshal([]byte(body), &got)
	// The owner's YAML: the provider's id and credentials stay.
	// Nor may it send the token to another host, a glob or another port,
	// or let other programs use it.
	for _, bad := range []string{
		strings.Replace(got.YAML, "host: uploads.github.com", "host: evil.example", 1),
		strings.Replace(got.YAML, "host: uploads.github.com", "host: '*.github.com'", 1),
		strings.Replace(got.YAML, "port: 443\n    path: /graphql", "port: 8443\n    path: /graphql", 1),
		strings.Replace(got.YAML, "  - /usr/bin/gh\n", "  - /usr/bin/gh\n  - /usr/bin/curl\n", 1),
	} {
		if bad == got.YAML {
			t.Fatal("the edit did not apply")
		}
		if code, body := do("POST", "/api/providers/gh/scope", jsonString(t, map[string]string{"yaml": bad})); code != 400 {
			t.Fatal("widened YAML accepted:", code, body)
		}
	}
	for _, bad := range []string{strings.Replace(got.YAML, "id: iwato-github-for-gh", "id: other", 1), strings.Replace(got.YAML, "GH_TOKEN", "TOKEN", 1)} {
		if code, body := do("POST", "/api/providers/gh/scope", jsonString(t, map[string]string{"yaml": bad})); code != 400 {
			t.Fatal(code, body)
		}
	}
	custom := "# mine\n" + strings.Replace(got.YAML, "/o/r/git-receive-pack", "/o/r2/git-receive-pack", 1)
	if code, body := do("POST", "/api/providers/gh/scope", jsonString(t, map[string]string{"yaml": custom})); code != 200 {
		t.Fatal(code, body)
	}
	if p, _ := provider(listProviders(t, do), "gh"); !p.Scope.Custom {
		t.Fatalf("%+v", p.Scope)
	}
	if code, body := do("GET", "/api/provider-profiles/iwato-github-for-gh", ""); code != 200 || !strings.Contains(body, "# mine") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/providers/gh/scope", `{"reset":true}`); code != 200 {
		t.Fatal(code, body)
	}
	if p, _ := provider(listProviders(t, do), "gh"); p.Scope.Custom || p.Scope.Mode != scopeRepos {
		t.Fatalf("%+v", p.Scope)
	}
	// The provider of the shared iwato-github stays read-only.
	if code, body := do("POST", "/api/providers/github-personal/scope", `{"mode":"full"}`); code != 409 || !strings.Contains(body, "追加し直し") {
		t.Fatal(code, body)
	}
	// Every built-in type: a provider of its own profile, read-only or
	// full; one made before (the fake's huggingface) stays as it is.
	t.Cleanup(func() { unregisterProfile("iwato-huggingface-for-hf") })
	if code, body := do("POST", "/api/providers", `{"name":"hf","profile":"iwato-huggingface","values":{"token":"x"}}`); code != 200 {
		t.Fatal(code, body)
	}
	if p, _ := provider(listProviders(t, do), "hf"); p.Profile != "iwato-huggingface-for-hf" || p.Base != "iwato-huggingface" || p.Scope == nil || !slices.Equal(p.Scope.Modes, []string{scopeReadOnly, scopeFull}) {
		t.Fatalf("%+v %+v", p, p.Scope)
	}
	if code, body := do("POST", "/api/providers/hf/scope", `{"mode":"repos","repos":["o/r"]}`); code != 400 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/providers/hf/scope", `{"mode":"full"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("GET", "/api/provider-profiles/iwato-huggingface-for-hf", ""); code != 200 || !strings.Contains(body, "access: full") || strings.Contains(body, "git-upload-pack") {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/providers/huggingface/scope", `{"mode":"full"}`); code != 409 || !strings.Contains(body, "scope_legacy_provider") {
		t.Fatal(code, body)
	}
	do("POST", "/api/providers/hf/delete", `{}`)
	// Deleting the provider forgets its scope and profile.
	if code, body := do("POST", "/api/providers/gh/delete", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if _, ok := s.providerScope("gh"); ok {
		t.Fatal("the scope outlived the provider")
	}
	if _, ok := profileByID("iwato-github-for-gh"); ok {
		t.Fatal("the profile outlived the provider")
	}

	// A custom profile: checked, then a file of its own.
	if code, body := do("POST", "/api/provider-profiles", jsonString(t, map[string]string{"yaml": strings.Replace(customProfileExample, "id: example-api", "id: iwato-x", 1)})); code != 400 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/provider-profiles", jsonString(t, map[string]string{"yaml": customProfileExample})); code != 200 {
		t.Fatal(code, body)
	}
	// Saving it again replaces Iwato's own; a gateway profile Iwato did not
	// add (the fake's CLI-imported openai) is not overwritten.
	if code, body := do("POST", "/api/provider-profiles", jsonString(t, map[string]string{"yaml": customProfileExample})); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/provider-profiles", jsonString(t, map[string]string{"yaml": strings.Replace(customProfileExample, "id: example-api", "id: openai", 1)})); code != 409 || !strings.Contains(body, "上書きしない") {
		t.Fatal(code, body)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "profiles", "example-api.yaml")); err != nil || string(b) != customProfileExample {
		t.Fatal(string(b), err)
	}
	l := listProviders(t, do)
	if !slices.ContainsFunc(l.Profiles, func(p providerProfile) bool {
		return p.ID == "example-api" && p.Custom && p.Group == customProfileGroup
	}) {
		t.Fatalf("%+v", l.Profiles)
	}
	if code, body := do("POST", "/api/providers", `{"name":"ex","profile":"example-api","values":{"api_key":"k"}}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/provider-profiles/example-api/delete", `{}`); code != 409 || !strings.Contains(body, "ex") {
		t.Fatal(code, body)
	}
	do("POST", "/api/providers/ex/delete", `{}`)
	// Loaded again when Iwato starts.
	unregisterProfile("example-api")
	s.loadProfiles()
	if p, ok := profileByID("example-api"); !ok || !p.Custom {
		t.Fatal("not loaded from the file")
	}
	if code, body := do("POST", "/api/provider-profiles/example-api/delete", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "profiles", "example-api.yaml")); !os.IsNotExist(err) {
		t.Fatal("the file is left:", err)
	}
}
