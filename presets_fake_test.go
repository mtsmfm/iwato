//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Two repositories through the same preset (each its own endpoint path)
// stay two rules: taking one out leaves the other's access whole. A preset
// whose rule OpenShell would fold into an existing rule is refused, the
// policy left as it was.
func TestPresetInstancesAndFolds(t *testing.T) {
	s, d, do := providerServer(t)
	for _, step := range [][2]string{{"/api/sandboxes/dev/start", `{}`}, {"/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`}} {
		if code, body := do("POST", step[0], step[1]); code != 200 {
			t.Fatal(step[0], code, body)
		}
	}
	withUserPresets(t, map[string]string{"mirror.yaml": mirrorPreset})
	ref := func(repo string) string { return `{"id":"user:mirror","params":{"repo":"` + repo + `"}}` }
	for _, repo := range []string{"o/a", "o/b"} {
		if code, body := do("POST", "/api/sandboxes/dev/presets/apply", ref(repo)); code != 200 {
			t.Fatal(repo, code, body)
		}
	}
	ctx := context.Background()
	sb, _ := s.lookupSandbox("dev")
	rules, _, _ := d.PolicyLayers(ctx, sb)
	want, _ := presetRefRules(PresetRef{ID: "user:mirror", Params: map[string]string{"repo": "o/a"}})
	if !samePresetRule(rules["mirror_o_a"], want["mirror_o_a"]) || rules["mirror_o_b"].Name == "" {
		t.Fatalf("%+v", rules)
	}
	if code, body := do("POST", "/api/sandboxes/dev/presets/remove", ref("o/b")); code != 200 || !strings.Contains(body, `"kept":[]`) {
		t.Fatal(code, body)
	}
	rules, _, _ = d.PolicyLayers(ctx, sb)
	if _, ok := rules["mirror_o_b"]; ok || !samePresetRule(rules["mirror_o_a"], want["mirror_o_a"]) {
		t.Fatalf("%+v", rules)
	}
	for _, r := range rules {
		for _, e := range r.Endpoints {
			if strings.Contains(e.Path, "o/b") {
				t.Fatalf("o/b's access stayed: %+v", r)
			}
		}
	}

	// A rule of the same programs and endpoint: OpenShell would fold the
	// preset's into it.
	hand := v1.NetworkPolicyRule{Name: "hand_npm", Binaries: []v1.PolicyNetworkBinary{{Path: "/**"}},
		Endpoints: []v1.PolicyNetworkEndpoint{{Host: "registry.npmjs.org", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Access: v1.NetworkAccessPresetReadOnly}}}
	if _, err := d.ApplyPolicyChange(ctx, sb, []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: "hand_npm", Rule: hand}}}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := d.PolicyLayers(ctx, sb)
	if code, body := do("POST", "/api/sandboxes/dev/presets/apply", `"packages"`); code != 409 || !strings.Contains(body, "preset_packages_registry_npmjs_org → ") || !strings.Contains(body, "hand_npm") {
		t.Fatal(code, body)
	}
	after, _, _ := d.PolicyLayers(ctx, sb)
	if len(rollbackOps(before, after)) != 0 {
		t.Fatal("the refused preset changed the policy")
	}
	if sb, _ = s.lookupSandbox("dev"); len(sb.Presets) != 1 {
		t.Fatalf("%+v", sb.Presets)
	}
}

// A preset that is gone (Iwato's removed in an update, or the owner's
// file deleted) after it went into a policy: listed as missing, its rules
// stay as plain rules, and taking it out only drops it from the list.
func TestGonePreset(t *testing.T) {
	s, d, do := providerServer(t)
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	dir := withUserPresets(t, map[string]string{"shared.yaml": sharedPreset})
	if code, body := do("POST", "/api/sandboxes/dev/presets/apply", `"user:shared"`); code != 200 {
		t.Fatal(code, body)
	}
	os.Remove(filepath.Join(dir, "shared.yaml"))
	ctx := context.Background()
	sb, _ := s.lookupSandbox("dev")
	before, _, _ := d.PolicyLayers(ctx, sb)
	if code, body := do("GET", "/api/sandboxes/dev/presets", ""); code != 200 || !strings.Contains(body, `"id":"user:shared","label":"user:shared","missing":true`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/presets/remove", `"user:shared"`); code != 200 || !strings.Contains(body, `"kept":[]`) {
		t.Fatal(code, body)
	}
	after, _, _ := d.PolicyLayers(ctx, sb)
	if _, ok := after["allow_claude_ai_443"]; !ok || len(rollbackOps(before, after)) != 0 {
		t.Fatalf("the policy changed: %v", after)
	}
	if sb, _ = s.lookupSandbox("dev"); len(sb.Presets) != 0 || len(presetRules([]PresetRef{{ID: "user:shared"}})) != 0 {
		t.Fatalf("%+v", sb.Presets)
	}
}

// Through the endpoints on the fake runtime: a sandbox not created yet
// only records the preset; a created one gets its rules (beside the
// GitHub provider's) and loses them again; a create names the presets.
func TestApplyAndRemovePresets(t *testing.T) {
	s, d, do := providerServer(t)
	withUserPresets(t, map[string]string{"mirror.yaml": mirrorPreset})
	if code, body := do("GET", "/api/presets", ""); code != 200 || !strings.Contains(body, `"user:mirror"`) || strings.Contains(body, `"pattern"`) || strings.Contains(body, "Binaries") {
		t.Fatal(code, body)
	}
	repo := `{"id":"user:mirror","params":{"repo":"mtsmfm/iwato"}}`
	if code, body := do("POST", "/api/sandboxes/web/presets/apply", repo); code != 200 || !strings.Contains(body, `"onCreate":true`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/web/presets/apply", repo); code != 409 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/web/presets/remove", repo); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/start", `{}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/presets/apply", repo); code != 200 {
		t.Fatal(code, body)
	}
	ctx := context.Background()
	sb, _ := s.lookupSandbox("dev")
	base, _, _ := d.PolicyLayers(ctx, sb)
	if _, ok := base["mirror_mtsmfm_iwato"]; !ok || len(sb.Presets) != 1 {
		t.Fatalf("%v %+v", base, sb.Presets)
	}
	if code, body := do("GET", "/api/sandboxes/dev/presets", ""); code != 200 || !strings.Contains(body, `"label":"ミラーに書き込む (mtsmfm/iwato)"`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/presets/remove", repo); code != 200 || !strings.Contains(body, `"kept":[]`) {
		t.Fatal(code, body)
	}
	sb, _ = s.lookupSandbox("dev")
	base, _, _ = d.PolicyLayers(ctx, sb)
	if _, ok := base["mirror_mtsmfm_iwato"]; ok || len(sb.Presets) != 0 {
		t.Fatalf("%v %+v", base, sb.Presets)
	}

	if code, body := do("POST", "/api/sandboxes", `{"name":"bad","presets":["nope"]}`); code != 400 {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes", `{"name":"mise","presets":["packages",`+repo+`],"providers":["github-personal"]}`); code != 200 {
		t.Fatal(code, body)
	}
	sb, _ = s.lookupSandbox("mise")
	if len(sb.Presets) != 2 || sb.Presets[0].ID != "packages" || sb.Presets[1].Params["repo"] != "mtsmfm/iwato" {
		t.Fatalf("%+v", sb.Presets)
	}
	if _, ok := sandboxPolicy(sb).NetworkPolicies["mirror_mtsmfm_iwato"]; !ok {
		t.Fatal("the first policy lacks the preset")
	}
}

// The owner's presets edited in the UI: a new file named after the ID,
// checked as the folder's files are (a provider's host refused), read,
// replaced and removed by file name only.
func TestUserPresetEditor(t *testing.T) {
	_, _, do := providerServer(t)
	dir := withUserPresets(t, nil)
	post := func(method, path, yamlText string) (int, string) {
		b, _ := json.Marshal(map[string]string{"yaml": yamlText})
		return do(method, path, string(b))
	}
	if code, body := post("POST", "/api/presets/user", mirrorPreset); code != 200 || !strings.Contains(body, `"file":"mirror.yaml"`) {
		t.Fatal(code, body)
	}
	if findPreset("user:mirror") == nil {
		t.Fatal("not listed")
	}
	if code, body := post("POST", "/api/presets/user", mirrorPreset); code != 409 {
		t.Fatal("a second mirror:", code, body)
	}
	// As specific as the GitHub provider's initial rule, without its
	// credential: OpenShell would refuse the two together.
	github := strings.ReplaceAll(strings.ReplaceAll(mirrorPreset, "git.example.com", "github.com"), `"/${repo}/**"`, `"/**"`)
	if code, body := post("PUT", "/api/presets/user/mirror.yaml", github); code != 400 || !strings.Contains(body, "preset_provider_ambiguous") {
		t.Fatal("a provider's host:", code, body)
	}
	if code, body := do("GET", "/api/presets/user/mirror.yaml", ""); code != 200 || !strings.Contains(body, "git.example.com") {
		t.Fatal("the refused change was saved:", code, body)
	}
	if code, body := post("PUT", "/api/presets/user/mirror.yaml", strings.Replace(mirrorPreset, "ミラーに書き込む", "ミラー", 1)); code != 200 || findPreset("user:mirror").Label != "ミラー" {
		t.Fatal(code, body)
	}
	for _, bad := range []string{"../x.yaml", "..%2Fx.yaml", "a/b.yaml", ".hidden.yaml", "x.txt"} {
		if code, _ := do("GET", "/api/presets/user/"+bad, ""); code == 200 {
			t.Errorf("read %s", bad)
		}
	}
	if code, body := do("DELETE", "/api/presets/user/mirror.yaml", ""); code != 200 || findPreset("user:mirror") != nil {
		t.Fatal(code, body)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}
