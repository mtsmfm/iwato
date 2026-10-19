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

// allBuiltinPresets are Iwato's presets.
func allBuiltinPresets() []PresetRef {
	var refs []PresetRef
	for _, p := range builtinPresets() {
		refs = append(refs, PresetRef{ID: p.ID})
	}
	return refs
}

// mirrorPreset is an owner's preset with a parameter: one repository of a
// git server that takes no credential, each scoped by its path.
const mirrorPreset = `id: mirror
label: ミラーに書き込む
params:
  - { name: repo, label: リポジトリ, placeholder: OWNER/REPO, pattern: '[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}' }
network_policies:
  mirror_${repo}:
    endpoints:
      - host: git.example.com
        port: 443
        path: "/${repo}/**"
        protocol: rest
        enforcement: enforce
        rules:
          - allow: { method: GET, path: "/${repo}/**" }
          - allow: { method: POST, path: "/${repo}/git-receive-pack" }
        deny_rules:
          - { method: "*", path: "/${repo}/admin" }
    binaries: [/usr/bin/git]
`

// sharedPreset is an owner's preset with a rule of the same name as
// claude-mise's (downloads.claude.ai) and one of its own.
const sharedPreset = `id: shared
label: 共有する規則
network_policies:
  allow_claude_ai_443:
    endpoints:
      - host: claude.ai
        port: 443
        protocol: rest
        enforcement: enforce
        rules:
          - allow: { method: GET, path: /install.sh }
    binaries: [/usr/bin/curl]
  allow_downloads_claude_ai_443:
    endpoints:
      - host: downloads.claude.ai
        port: 443
        protocol: rest
        enforcement: enforce
        rules:
          - allow: { method: GET, path: "/claude-code-releases/**" }
    binaries:
      - /usr/bin/curl
      - /home/dev/.local/share/claude/versions/*
`

// withUserPresets puts files into a folder of the owner's presets for the
// test.
func withUserPresets(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	setUserPresetDir(dir)
	t.Cleanup(func() { setUserPresetDir("") })
	return dir
}

// Iwato's presets, all of them together, and every Iwato provider: no
// endpoint OpenShell would refuse as ambiguous, none that carries a
// credential uninspected, none (but claude-mise's old rules) on a provider's
// host. Every endpoint is inspected and enforced, names
// programs, and none is an agent's API (that is a provider's).
func TestBuiltinPresetsFitTheProviders(t *testing.T) {
	refs := allBuiltinPresets()
	if err := validPresetList(refs); err != nil {
		t.Fatal(err)
	}
	// The hosts a provider's credential reaches are the provider's (ADR
	// 0006): only claude-mise keeps mise's GitHub rules from before.
	for _, p := range builtinPresets() {
		if want := p.ID == "claude-mise"; (len(p.ProviderHosts) > 0) != want {
			t.Errorf("%s reaches a provider's hosts: %v", p.ID, p.ProviderHosts)
		}
	}
	rules := presetRules(refs)
	for name, rule := range rules {
		for _, e := range rule.Endpoints {
			if e.Protocol != "rest" || e.Enforcement != v1.NetworkEnforcementModeEnforce || e.TLS != v1.NetworkTLSModeUnspecified || len(e.AllowedIPs) > 0 ||
				e.Host == "api.anthropic.com" || e.Host == "api.x.ai" {
				t.Fatalf("%s: %+v", name, e)
			}
		}
		if len(rule.Binaries) == 0 {
			t.Fatalf("%s has no program", name)
		}
	}
	if a := findEndpointAmbiguities(rules); len(a) > 0 {
		t.Fatalf("the presets disagree: %v", a)
	}
	providersOnly := map[string]v1.NetworkPolicyRule{}
	var scopes []credScope
	for _, p := range providerProfiles {
		composeProviderRule(providersOnly, p.ID, p.proto())
		scopes = append(scopes, providerScopes(p.ID, p.ID)...)
	}
	with := withProviderRules(rules, providersOnly)
	if a := newAmbiguities(providersOnly, with); len(a) > 0 {
		t.Fatalf("a provider would be refused: %v", a)
	}
	if cs := credentialConflicts(rules, scopes); len(cs) > 0 {
		t.Fatalf("uninspected credentials: %+v", cs)
	}
	if got := sandboxPolicy(newSandbox("dev")).NetworkPolicies; len(got) != 0 {
		t.Fatalf("a sandbox without presets allows network: %v", got)
	}
}

// Presets with a rule of one name: the rule joins their programs.
func TestPresetsOfOneRuleNameJoin(t *testing.T) {
	withUserPresets(t, map[string]string{"shared.yaml": sharedPreset})
	rule := presetRules([]PresetRef{{ID: "claude-mise"}, {ID: "user:shared"}})["allow_downloads_claude_ai_443"]
	var paths []string
	for _, b := range rule.Binaries {
		paths = append(paths, b.Path)
	}
	for _, want := range []string{"/usr/bin/curl", "/home/dev/.local/share/claude/versions/*", "/home/dev/.local/share/mise/installs/claude/*/claude"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("%s missing: %v", want, paths)
		}
	}
	if len(rule.Endpoints) != 1 || len(rule.Endpoints[0].Rules) != 1 {
		t.Fatalf("%+v", rule.Endpoints)
	}
}

// A parameter goes into the rule's name (as _), hosts and paths, and only
// a value its pattern and Iwato's own set allow.
func TestPresetParams(t *testing.T) {
	withUserPresets(t, map[string]string{"mirror.yaml": mirrorPreset})
	repo := func(v string) []PresetRef {
		return []PresetRef{{ID: "user:mirror", Params: map[string]string{"repo": v}}}
	}
	rules := presetRules(repo("mtsmfm/my.repo"))
	rule, ok := rules["mirror_mtsmfm_my_repo"]
	if !ok || len(rules) != 1 {
		t.Fatalf("%v", rules)
	}
	var paths []string
	for _, e := range rule.Endpoints {
		for _, r := range e.Rules {
			paths = append(paths, e.Host+" "+r.Allow.Method+" "+r.Allow.Path)
		}
		for _, r := range e.DenyRules {
			paths = append(paths, e.Host+" deny "+r.Path)
		}
		paths = append(paths, e.Host+" endpoint "+e.Path)
	}
	for _, want := range []string{"git.example.com POST /mtsmfm/my.repo/git-receive-pack", "git.example.com endpoint /mtsmfm/my.repo/**", "git.example.com deny /mtsmfm/my.repo/admin"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("%s missing: %v", want, paths)
		}
	}
	if strings.Contains(strings.Join(paths, " "), "${") {
		t.Fatal(paths)
	}
	for _, bad := range []string{"", "*/*", "mtsmfm/**", "a/..", "mtsmfm/.", "mtsmfm", "a/b/c", "a/b\n", "a/[b]"} {
		if validPresetList(repo(bad)) == nil {
			t.Errorf("took %q", bad)
		}
	}
	for _, bad := range [][]PresetRef{{{ID: "nope"}}, {{ID: "user:mirror"}}, {{ID: "packages", Params: map[string]string{"repo": "a/b"}}}, {{ID: "packages"}, {ID: "packages"}}} {
		if validPresetList(bad) == nil {
			t.Errorf("took %+v", bad)
		}
	}
	// The configuration keeps a preset without parameters as its ID.
	b, _ := json.Marshal([]PresetRef{{ID: "packages"}, repo("a/b")[0]})
	var back []PresetRef
	if string(b) != `["packages",{"id":"user:mirror","params":{"repo":"a/b"}}]` || json.Unmarshal(b, &back) != nil || !back[1].same(repo("a/b")[0]) {
		t.Fatalf("%s %+v", b, back)
	}
}

// The owner's folder: good files become user:ID (never shadowing
// Iwato's), bad ones are listed with the reason, and a change shows on
// the next read.
func TestUserPresets(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { setUserPresetDir("") })
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", `id: claude-mise
label: 自分の
params:
  - { name: host, label: ホスト, pattern: '[a-z]+\.example\.com' }
network_policies:
  mine_${host}:
    endpoints:
      - { host: "${host}", port: 443, protocol: rest, enforcement: enforce, access: read-only }
    binaries: [/usr/bin/curl]
`)
	write("typo.yaml", "id: typo\nlabel: x\nnetwork_policies:\n  r:\n    endpoints: [{host: a.example, port: 443, protocoll: rest}]\n    binaries: [/usr/bin/curl]\n")
	write("unknown.yaml", "id: unknown\nlabel: x\nnetwork_policies:\n  r_${nope}:\n    endpoints: [{host: a.example, port: 443}]\n    binaries: [/usr/bin/curl]\n")
	write("provider.yaml", "id: provider\nlabel: x\nnetwork_policies:\n  _provider_x:\n    endpoints: [{host: a.example, port: 443}]\n    binaries: [/usr/bin/curl]\n")
	write("github.yaml", "id: github\nlabel: x\nnetwork_policies:\n  r:\n    endpoints: [{host: github.com, port: 443, protocol: rest, enforcement: enforce, rules: [{allow: {method: GET, path: /o/r/releases/**}}]}]\n    binaries: [/usr/bin/curl]\n")
	write("audit.yaml", "id: audit\nlabel: x\nnetwork_policies:\n  r:\n    endpoints: [{host: console.vast.ai, port: 443, path: /api/v0/**, protocol: rest, enforcement: audit, rules: [{allow: {method: GET, path: /other}}]}]\n    binaries: [/usr/bin/curl]\n")
	write("enforced.yaml", "id: enforced\nlabel: x\nnetwork_policies:\n  r:\n    endpoints: [{host: console.vast.ai, port: 443, protocol: rest, enforcement: enforce, rules: [{allow: {method: GET, path: /other}}]}]\n    binaries: [/usr/bin/curl]\n")
	write("nobin.yaml", "id: nobin\nlabel: x\nnetwork_policies:\n  r:\n    endpoints: [{host: a.example, port: 443}]\n    binaries: []\n")
	setUserPresetDir(dir)
	list, errs := presetList()
	mine := findPreset("user:claude-mise")
	if mine == nil || findPreset("claude-mise").Source != "builtin" || len(list) != len(builtinPresets())+2 {
		t.Fatalf("%+v", list)
	}
	var bad []string
	for _, e := range errs {
		bad = append(bad, e.File)
	}
	// An audited endpoint lets every request through: its rules do not
	// keep it off the provider's paths, an enforced one's do.
	if !strings.Contains(errs[0].Error, "console.vast.ai:443") || !strings.Contains(errs[1].Error, "github.com:443 (GitHub)") || findPreset("user:enforced") == nil {
		t.Fatalf("%+v", errs)
	}
	if !slices.Equal(bad, []string{"audit.yaml", "github.yaml", "nobin.yaml", "provider.yaml", "typo.yaml", "unknown.yaml"}) {
		t.Fatalf("%+v", errs)
	}
	// A coded error goes to the page with its code and params, to the log in English.
	if e := errs[5]; e.Code != "preset_param_unknown" || e.Params["name"] != "nope" || e.en != "rule r_${nope}: no params named ${nope}" {
		t.Fatalf("%+v", e)
	}
	rules := presetRules([]PresetRef{{ID: "user:claude-mise", Params: map[string]string{"host": "api.example.com"}}})
	if r, ok := rules["mine_api_example_com"]; !ok || r.Endpoints[0].Host != "api.example.com" {
		t.Fatalf("%v", rules)
	}
	if validPresetList([]PresetRef{{ID: "user:claude-mise", Params: map[string]string{"host": "evil.com"}}}) == nil {
		t.Fatal("took a value its pattern refuses")
	}
	os.Remove(filepath.Join(dir, "a.yaml"))
	reloadUserPresets()
	if findPreset("user:claude-mise") != nil {
		t.Fatal("a removed file stayed")
	}
}

// presetRemoval takes out what the presets made, puts back a shared rule
// as the others make it, and leaves a rule changed since.
func TestPresetRemoval(t *testing.T) {
	withUserPresets(t, map[string]string{"shared.yaml": sharedPreset})
	mise, shared := PresetRef{ID: "claude-mise"}, PresetRef{ID: "user:shared"}
	all := presetRules([]PresetRef{mise, shared})
	base := map[string]v1.NetworkPolicyRule{}
	for name, r := range all {
		r = cloneRule(r)
		// As the gateway stores them: the order and the port fields differ.
		slices.Reverse(r.Binaries)
		r.Endpoints[0].Port, r.Endpoints[0].Ports = 0, []uint32{443}
		base[name] = r
	}
	changed := cloneRule(base["allow_claude_ai_443"])
	changed.Binaries = append(changed.Binaries, v1.PolicyNetworkBinary{Path: "/usr/bin/wget"})
	base["allow_claude_ai_443"] = changed
	mine, _ := presetRefRules(shared)
	ops, kept := presetRemoval(base, mine, all, presetRules([]PresetRef{mise}))
	var got []string
	for _, op := range ops {
		switch {
		case op.RemoveRule != nil:
			got = append(got, "remove "+op.RemoveRule.RuleName)
		case op.AddRule != nil:
			got = append(got, "add "+op.AddRule.RuleName)
		}
	}
	if !slices.Equal(got, []string{"remove allow_downloads_claude_ai_443", "add allow_downloads_claude_ai_443"}) || !slices.Equal(kept, []string{"allow_claude_ai_443"}) {
		t.Fatalf("%v %v", got, kept)
	}
	// A rule of the preset that OpenShell folded into another: named, with
	// where its endpoint is, never a silent success.
	folded := cloneRule(base["allow_claude_ai_443"])
	delete(base, "allow_claude_ai_443")
	base["hand"] = folded
	if _, kept = presetRemoval(base, mine, all, presetRules([]PresetRef{mise})); len(kept) != 1 || !strings.Contains(kept[0], "allow_claude_ai_443") || !strings.Contains(kept[0], "hand") {
		t.Fatalf("%v", kept)
	}
	// Putting a fold back: the changed rule as it was, the new one out.
	before := map[string]v1.NetworkPolicyRule{"hand": base["allow_downloads_claude_ai_443"]}
	after := map[string]v1.NetworkPolicyRule{"hand": all["allow_downloads_claude_ai_443"], "new": folded, "_provider_x": folded}
	got = nil
	for _, op := range rollbackOps(before, after) {
		switch {
		case op.RemoveRule != nil:
			got = append(got, "remove "+op.RemoveRule.RuleName)
		case op.AddRule != nil:
			got = append(got, "add "+op.AddRule.RuleName)
		}
	}
	if !slices.Equal(got, []string{"remove hand", "add hand", "remove new"}) {
		t.Fatalf("%v", got)
	}
}
