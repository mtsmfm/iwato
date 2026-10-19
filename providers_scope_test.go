package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"google.golang.org/protobuf/proto"
)

// customProfileExample is a custom profile as the page's template has it.
const customProfileExample = `id: example-api
display_name: Example API
description: An API key for api.example.com
category: other
credentials:
  - name: api_key
    description: API キー
    env_vars: [EXAMPLE_API_KEY]
    required: true
    auth_style: bearer
    header_name: authorization
endpoints:
  - host: api.example.com
    port: 443
    protocol: rest
    access: read-only
    enforcement: enforce
binaries:
  - /usr/bin/curl
`

func mustRender(t *testing.T, name string, sc ProviderScope) *pb.ProviderProfile {
	t.Helper()
	sc, err := sc.normalize()
	if err != nil {
		t.Fatal(err)
	}
	m, err := renderScope(name, sc)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The rendered scopes: read-only is iwato-github under the provider's id;
// repositories add the pushes and the REST writes under each; the
// mutations are GraphQL rules; full write is access full. Each renders to
// YAML that Iwato's parser reads back to the same profile.
func TestScopeRendering(t *testing.T) {
	gh, _ := builtinProfile(githubProfileID)
	ro := mustRender(t, "work", ProviderScope{Base: githubProfileID})
	if ro.Id != "iwato-github-for-work" || !slices.EqualFunc(ro.Endpoints, gh.proto().Endpoints, func(a, b *sb.NetworkEndpoint) bool { return proto.Equal(a, b) }) {
		t.Fatalf("read-only differs from iwato-github: %v", ro)
	}
	repos := mustRender(t, "work", ProviderScope{Base: githubProfileID, Mode: scopeRepos, Repos: []string{"o/r", " o/r.git ", "x/y"}, Mutations: []string{"createPullRequest"}})
	var rules []string
	for _, e := range repos.Endpoints {
		for _, r := range e.Rules {
			a := r.Allow
			rules = append(rules, e.Host+e.Path+" "+a.Method+" "+a.Path+a.OperationType+strings.Join(a.Fields, ","))
		}
		if len(e.Rules) > 0 && e.Access != sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_UNSPECIFIED {
			t.Error("rules and access on", e.Host)
		}
	}
	for _, want := range []string{
		"api.github.com GET **", "api.github.com * /repos/o/r/**", "api.github.com * /repos/x/y/**",
		"api.github.com/graphql  query", "api.github.com/graphql  mutationcreatePullRequest",
		"github.com POST /**/git-upload-pack", "github.com POST /o/r.git/git-receive-pack", "github.com POST /o/r/git-receive-pack", "github.com POST /x/y/git-receive-pack",
		"uploads.github.com * /repos/o/r/**",
	} {
		if !slices.Contains(rules, want) {
			t.Errorf("missing %q in %q", want, rules)
		}
	}
	if slices.ContainsFunc(rules, func(r string) bool {
		return strings.Count(r, "/repos/o/r/**") > 0 && strings.HasPrefix(r, "github.com")
	}) {
		t.Error("REST rule on github.com")
	}
	full := mustRender(t, "work", ProviderScope{Base: githubProfileID, Mode: scopeFull, Mutations: []string{"x"}})
	for _, e := range full.Endpoints {
		if e.Access != sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_FULL || len(e.Rules) > 0 {
			t.Error("full:", e.Host, e.Access, e.Rules)
		}
	}
	for _, m := range []*pb.ProviderProfile{ro, repos, full} {
		y, err := profileYAML(m)
		if err != nil {
			t.Fatal(err)
		}
		back, err := parseProfileYAML([]byte(y))
		if err != nil || profileHash(back) != profileHash(m) {
			t.Fatalf("%s does not read back: %v\n%s", m.Id, err, y)
		}
	}
	// Provider names OpenShell's ids cannot hold as they are get a hash.
	for _, name := range []string{"a--b", "a-", "a-b"} {
		if id := scopedProfileID(githubProfileID, name); !validProfileID(id) {
			t.Errorf("%s: %s", name, id)
		}
	}
	if scopedProfileID(githubProfileID, "a--b") == scopedProfileID(githubProfileID, "a-b") {
		t.Error("a--b and a-b share a profile")
	}
}

// The other built-in profiles: read-only is the profile as it is, full
// opens every request on the same endpoints (no new host, port or path);
// both pass Iwato's checks and fit the built-in presets.
func TestScopeRenderingOtherProfiles(t *testing.T) {
	presets := presetRules(allBuiltinPresets())
	for _, p := range providerProfiles {
		if p.ID == githubProfileID {
			continue
		}
		base := p.proto()
		ro := mustRender(t, "x", ProviderScope{Base: p.ID, Mode: scopeReadOnly})
		if ro.Id != p.ID+"-for-x" || !slices.EqualFunc(ro.Endpoints, base.Endpoints, func(a, b *sb.NetworkEndpoint) bool { return proto.Equal(a, b) }) {
			t.Errorf("%s: read-only differs from the profile", p.ID)
		}
		if !proto.Equal(&pb.ProviderProfile{Credentials: ro.Credentials}, &pb.ProviderProfile{Credentials: base.Credentials}) {
			t.Errorf("%s: the credentials changed", p.ID)
		}
		full := mustRender(t, "x", ProviderScope{Base: p.ID, Mode: scopeFull, Repos: []string{"o/r"}, Mutations: []string{"m"}})
		if len(full.Endpoints) != len(base.Endpoints) {
			t.Fatalf("%s: %d endpoints", p.ID, len(full.Endpoints))
		}
		for i, e := range full.Endpoints {
			b := base.Endpoints[i]
			if e.Access != sb.NetworkAccessPreset_NETWORK_ACCESS_PRESET_FULL || len(e.Rules) > 0 || e.Host != b.Host || e.Path != b.Path || !slices.Equal(profileEndpointPorts(e), profileEndpointPorts(b)) || e.Protocol != b.Protocol {
				t.Errorf("%s: full: %v", p.ID, e)
			}
		}
		if err := withinBase(full, base); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		y, err := profileYAML(full)
		if err != nil {
			t.Fatal(err)
		}
		if back, err := parseProfileYAML([]byte(y)); err != nil || profileHash(back) != profileHash(full) {
			t.Fatalf("%s does not read back: %v\n%s", full.Id, err, y)
		}
		own := map[string]v1.NetworkPolicyRule{}
		composeProviderRule(own, "x", full)
		if a := newAmbiguities(own, withProviderRules(presets, own)); len(a) > 0 {
			t.Errorf("%s: full does not fit the presets: %v", p.ID, a)
		}
	}
	// Only GitHub has repositories and mutations.
	if sc, err := (ProviderScope{Base: "iwato-claude", Mode: scopeFull, Repos: []string{"o/r"}}).normalize(); err != nil || len(sc.Repos) > 0 {
		t.Fatal(sc, err)
	}
}

// Repositories go into rule paths verbatim: only OWNER/REPO, no globs.
func TestScopeRepoValidation(t *testing.T) {
	for in, want := range map[string]string{"mtsmfm/iwato": "mtsmfm/iwato", " o/r.git": "o/r", "octo-org/.github": "octo-org/.github", "a/b_c.d-e": "a/b_c.d-e"} {
		if got, err := normalizeRepo(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "o", "o/", "/r", "o/r/x", "o/*", "o/**", "*/r", "o/..", "o/.", "o/a..b", "-o/r", "o-/r", "o/r?", "o/[r]", "o/r{a,b}", "o /r", "o/r x", "https://github.com/o/r"} {
		if got, err := normalizeRepo(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	for _, sc := range []ProviderScope{
		{Base: githubProfileID, Mode: scopeRepos},
		{Base: githubProfileID, Mode: "write"},
		{Base: githubProfileID, Mutations: []string{"create-pr"}},
		{Base: "iwato-claude", Mode: scopeRepos},
		{Base: "openai"},
	} {
		if _, err := sc.normalize(); err == nil {
			t.Errorf("%+v accepted", sc)
		}
	}
}

// Custom profiles: not iwato-, the credentials only on inspected traffic,
// no ambiguous endpoints, values the owner enters.
func TestCustomProfileRules(t *testing.T) {
	if _, p, err := parseCustomProfile([]byte(customProfileExample)); err != nil || p.Label != "Example API" || !p.Custom || p.Credentials[0].Label != "API キー" || p.Credentials[0].Env[0] != "EXAMPLE_API_KEY" || p.DefaultName != "example-api" {
		t.Fatal(p, err)
	}
	for name, edit := range map[string][2]string{
		"reserved id":   {"id: example-api", "id: iwato-mine"},
		"bad id":        {"id: example-api", "id: Example_API"},
		"uninspected":   {"    protocol: rest\n    access: read-only\n", ""},
		"tls skip":      {"    enforcement: enforce\n", "    enforcement: enforce\n    tls: skip\n"},
		"no env vars":   {"    env_vars: [EXAMPLE_API_KEY]\n", ""},
		"unknown field": {"category: other", "category: other\nfoo: bar"},
		"ambiguous":     {"binaries:", "  - host: api.example.com\n    port: 443\n    protocol: rest\n    access: full\n    enforcement: audit\nbinaries:"},
	} {
		y := strings.Replace(customProfileExample, edit[0], edit[1], 1)
		if y == customProfileExample {
			t.Fatal(name, "did not change the example")
		}
		if _, _, err := parseCustomProfile([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Presets may not touch the hosts of a scoped provider's or a custom
// profile's credential either (credentialHits).
func TestScopedAndCustomProfilesOwnTheirHosts(t *testing.T) {
	_, p, err := parseCustomProfile([]byte(customProfileExample))
	if err != nil {
		t.Fatal(err)
	}
	registerProfile(p)
	t.Cleanup(func() { unregisterProfile(p.ID) })
	rule := func(host string) map[string]v1.NetworkPolicyRule {
		return map[string]v1.NetworkPolicyRule{"r": {Name: "r", Endpoints: []v1.PolicyNetworkEndpoint{{Host: host, Port: 443, Protocol: "rest", Access: v1.NetworkAccessPresetFull}}}}
	}
	if hits := credentialHits(rule("api.example.com")); len(hits) == 0 || !strings.Contains(hits[0], "Example API") {
		t.Fatal(hits)
	}
	registerScopes(map[string]ProviderScope{"w": {Base: githubProfileID, Mode: scopeFull}})
	t.Cleanup(func() { unregisterProfile(scopedProfileID(githubProfileID, "w")) })
	if hits := credentialHits(rule("github.com")); len(hits) == 0 {
		t.Fatal("github.com is not a provider's")
	}
	if hits := credentialHits(rule("example.org")); len(hits) != 0 {
		t.Fatal(hits)
	}
}
