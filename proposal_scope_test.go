package main

import (
	"slices"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Proposals into the scope fields of a GitHub provider with its own
// profile, and the shapes it refuses (approving them adds a policy rule).
func TestWidenScopeTranslatesProposals(t *testing.T) {
	m, err := renderScope("gh", ProviderScope{Base: githubProfileID, Mode: scopeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	scoped := scopeTarget{name: "gh", label: "GitHub", base: githubProfileID, scope: ProviderScope{Base: githubProfileID, Mode: scopeRepos, Repos: []string{"a/b"}},
		creds: profileScopes("gh", "GitHub", m), binaries: []string{"/usr/bin/gh", "/usr/bin/git", "/usr/lib/git-core/git-remote-http", "/usr/lib/git-core/git-remote-https"}}
	legacy := scoped
	legacy.name, legacy.base, legacy.legacy, legacy.scope = "old", "", true, ProviderScope{}
	rest := func(host string, rules ...string) v1.PolicyNetworkEndpoint {
		e := restEndpoint(host, 443)
		for _, r := range rules {
			method, path, _ := strings.Cut(r, " ")
			e.Rules = append(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: method, Path: path}})
		}
		return e
	}
	rule := func(binary string, e ...v1.PolicyNetworkEndpoint) v1.NetworkPolicyRule {
		return v1.NetworkPolicyRule{Binaries: policyBinaries(binary), Endpoints: e}
	}
	gql := restEndpoint("api.github.com", 443)
	gql.Protocol, gql.Path = "graphql", "/graphql"
	gql.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{OperationType: "query"}}, {Allow: &v1.L7Allow{OperationType: "mutation", Fields: []string{"createPullRequest"}}}}
	audit := rest("api.github.com", "POST /repos/o/r/pulls")
	audit.Enforcement = v1.NetworkEnforcementModeAudit
	for _, c := range []struct {
		name             string
		rule             v1.NetworkPolicyRule
		targets          []scopeTarget
		repos, mutations []string
		reason           string
	}{
		{"push", rule("/usr/lib/git-core/git-remote-https", rest("github.com", "GET /o/r.git/info/refs", "POST /o/r.git/git-receive-pack")), nil, []string{"o/r"}, nil, ""},
		{"push without .git, fetch", rule("/usr/lib/git-core/git-remote-https", rest("github.com", "POST /o/r/git-receive-pack", "POST /x/y.git/git-upload-pack")), nil, []string{"o/r"}, nil, ""},
		{"REST writes", rule("/usr/bin/gh", rest("api.github.com", "POST /repos/o/r/pulls", "* /repos/p/q/**"), rest("uploads.github.com", "POST /repos/o/r/releases/1/assets")), nil, []string{"o/r", "p/q"}, nil, ""},
		{"already in scope", rule("/usr/bin/gh", rest("api.github.com", "GET /user", "PATCH /repos/a/b/issues/1")), nil, nil, nil, ""},
		{"named mutation", rule("/usr/bin/gh", gql), nil, nil, []string{"createPullRequest"}, ""},
		{"not the provider's", rule("/usr/bin/curl", rest("example.com", "POST /x")), nil, nil, nil, "-"},
		{"another program", rule("/usr/bin/curl", rest("api.github.com", "POST /repos/o/r/pulls")), nil, nil, nil, "プログラム /usr/bin/curl"},
		{"outside a repository", rule("/usr/bin/gh", rest("api.github.com", "POST /user/repos")), nil, nil, nil, "POST /user/repos は"},
		{"a glob for the repository", rule("/usr/bin/gh", rest("api.github.com", "POST /repos/*/r/pulls")), nil, nil, nil, "1 つに決まりません"},
		{"GraphQL as REST", rule("/usr/bin/gh", rest("api.github.com", "POST /graphql")), nil, nil, nil, "すべてのミューテーション"},
		{"uninspected", rule("/usr/bin/gh", l4Endpoint("api.github.com", 443)), nil, nil, nil, "検査せずに"},
		{"audited", rule("/usr/bin/gh", audit), nil, nil, nil, "監査"},
		{"mixed hosts", rule("/usr/bin/gh", rest("api.github.com", "POST /repos/o/r/pulls"), rest("example.com", "GET /x")), nil, nil, nil, "宛先でないもの (example.com)"},
		{"legacy provider", rule("/usr/bin/gh", rest("api.github.com", "POST /repos/o/r/pulls")), []scopeTarget{legacy}, nil, nil, "範囲を設定できるようになる前"},
		{"two providers", rule("/usr/bin/gh", rest("api.github.com", "POST /repos/o/r/pulls")), []scopeTarget{scoped, legacy}, nil, nil, "複数のプロバイダー"},
	} {
		targets := c.targets
		if targets == nil {
			targets = []scopeTarget{scoped}
		}
		w := widenScope(c.rule, targets)
		if c.reason == "-" {
			if w != nil {
				t.Errorf("%s: %+v", c.name, w)
			}
			continue
		}
		if w == nil {
			t.Errorf("%s: no widening", c.name)
			continue
		}
		if c.reason != "" {
			if !strings.Contains(w.Reason, c.reason) {
				t.Errorf("%s: reason %q", c.name, w.Reason)
			}
			continue
		}
		if w.Reason != "" || !slices.Equal(w.Repos, c.repos) || !slices.Equal(w.Mutations, c.mutations) {
			t.Errorf("%s: %+v", c.name, w)
		}
	}
	// Read-only becomes writing to the repositories; full stays full.
	if sc, err := addToScope(ProviderScope{Base: githubProfileID, Mode: scopeReadOnly, Mutations: []string{"x"}}, scopeAdd{repos: []string{"o/r"}, mutations: []string{"y"}}); err != nil || sc.Mode != scopeRepos || !slices.Equal(sc.Repos, []string{"o/r"}) || !slices.Equal(sc.Mutations, []string{"x", "y"}) {
		t.Fatal(sc, err)
	}
}

// A profile other than GitHub's: a write on its own endpoints becomes
// mode full; reads are within it already; paths beyond its endpoints, or
// a provider made before the scopes, stay policy rules.
func TestWidenScopeToWrite(t *testing.T) {
	m, err := renderScope("hf", ProviderScope{Base: "iwato-huggingface", Mode: scopeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	hf := scopeTarget{name: "hf", label: "Hugging Face", base: "iwato-huggingface", scope: ProviderScope{Base: "iwato-huggingface", Mode: scopeReadOnly},
		creds: profileScopes("hf", "Hugging Face", m), binaries: []string{"/usr/bin/curl"}}
	m, err = renderScope("cv", ProviderScope{Base: "iwato-civitai", Mode: scopeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	cv := scopeTarget{name: "cv", label: "Civitai", base: "iwato-civitai", scope: ProviderScope{Base: "iwato-civitai", Mode: scopeReadOnly},
		creds: profileScopes("cv", "Civitai", m), binaries: []string{"/usr/bin/curl"}}
	legacy := hf
	legacy.name, legacy.base, legacy.legacy, legacy.scope = "huggingface", "", true, ProviderScope{}
	rest := func(host string, rules ...string) v1.PolicyNetworkEndpoint {
		e := restEndpoint(host, 443)
		for _, r := range rules {
			method, path, _ := strings.Cut(r, " ")
			e.Rules = append(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: method, Path: path}})
		}
		return e
	}
	rule := func(e ...v1.PolicyNetworkEndpoint) v1.NetworkPolicyRule {
		return v1.NetworkPolicyRule{Binaries: policyBinaries("/usr/bin/curl"), Endpoints: e}
	}
	for _, c := range []struct {
		name   string
		rule   v1.NetworkPolicyRule
		target scopeTarget
		write  bool
		reason string
	}{
		{"upload", rule(rest("huggingface.co", "POST /api/repos/create", "PUT /o/r/**")), hf, true, ""},
		{"a read", rule(rest("huggingface.co", "GET /o/r/**")), hf, false, ""},
		{"within the path", rule(rest("civitai.com", "POST /api/v1/x")), cv, true, ""},
		{"beyond the path", rule(rest("civitai.com", "POST /api/v1/x", "POST /other")), cv, false, "宛先 (パス) の外"},
		{"uninspected", rule(l4Endpoint("huggingface.co", 443)), hf, false, "検査せずに"},
		{"legacy provider", rule(rest("huggingface.co", "POST /api/repos/create")), legacy, false, "範囲を設定できるようになる前"},
	} {
		w := widenScope(c.rule, []scopeTarget{c.target})
		switch {
		case w == nil:
			t.Errorf("%s: no widening", c.name)
		case c.reason != "" && !strings.Contains(w.Reason, c.reason):
			t.Errorf("%s: reason %q", c.name, w.Reason)
		case c.reason == "" && (w.Reason != "" || w.Write != c.write || len(w.Repos) > 0 || len(w.Mutations) > 0):
			t.Errorf("%s: %+v", c.name, w)
		}
	}
	// Already full: nothing to add.
	full := hf
	full.scope.Mode = scopeFull
	if w := widenScope(rule(rest("huggingface.co", "POST /api/repos/create")), []scopeTarget{full}); w == nil || w.Reason != "" || w.Write {
		t.Fatalf("%+v", w)
	}
	if sc, err := addToScope(hf.scope, scopeAdd{write: true}); err != nil || sc.Mode != scopeFull {
		t.Fatal(sc, err)
	}
}
