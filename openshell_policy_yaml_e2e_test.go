package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"

	"iwato/internal/hostterm"
	"iwato/internal/openshell/client"
)

// TestOpenShellPolicyYAMLE2E checks the policy tab's YAML editor against
// the gateway that internal/openshell/wslc/e2e.sh starts (MODE=tls): the
// gateway takes a whole policy whose network_policies changed and whose
// other parts are as they were, keeps a rule that shares an endpoint with
// another under its own name (a merge would fold it), and Iwato refuses a
// stale version. The sandbox is created with the GitHub provider, whose
// initial rule (bound to it) is in its first policy and passes untouched.
func TestOpenShellPolicyYAMLE2E(t *testing.T) {
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
	creds, _ := profileCredentials(gh, map[string]string{"token": "ghp_e2eSECRETyaml"})
	if err := o.CreateProvider(ctx, "github-yaml", "iwato-github", creds); err != nil {
		t.Fatal(err)
	}
	defer o.DeleteProvider(context.Background(), "github-yaml")
	w := newSandbox("iwato-yaml-e2e")
	w.Presets = []PresetRef{{ID: "tools"}}
	w.Providers = []string{"github-yaml"}
	if err := o.Start(ctx, w); err != nil {
		t.Fatal(err)
	}
	defer o.Delete(context.Background(), w, true)
	before, err := o.LatestPolicy(ctx, w)
	if err != nil || before.Revision.Policy == nil {
		t.Fatal(err)
	}

	c.Sandboxes = []Sandbox{w}
	s := newServer(c, o, false, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	var got struct {
		Version uint32 `json:"version"`
		YAML    string `json:"yaml"`
	}
	r := reviewRequest(s, "GET", "/api/sandboxes/"+w.Name+"/policy/yaml", "", "", "", s.host, cookie, "")
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &got) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	post := func(version uint32, rules map[string]v1.NetworkPolicyRule, apply bool) (int, map[string]any) {
		doc, err := policyRulesYAML(rules)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"version": version, "yaml": doc, "apply": apply})
		r := reviewRequest(s, "POST", "/api/sandboxes/"+w.Name+"/policy/yaml", string(body), s.origin, "same-origin", s.host, cookie, csrf)
		var v map[string]any
		json.Unmarshal(r.Body.Bytes(), &v)
		return r.Code, v
	}
	rules, err := parsePolicyYAML([]byte(got.YAML))
	if err != nil {
		t.Fatal(err)
	}
	// A copy of the rule for mise-versions.jdx.dev under another name and
	// program, and the one for release assets taken out.
	edited := cloneRules(rules)
	copied := cloneRule(edited["preset_tools_mise_versions_jdx_dev"])
	copied.Name, copied.Binaries = "mise_versions_curl", []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}
	edited[copied.Name] = copied
	delete(edited, "preset_tools_release_assets_githubusercontent_com")
	if code, v := post(got.Version, edited, true); code != 200 {
		t.Fatal("apply:", code, v)
	}
	after, err := o.LatestPolicy(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	p := after.Revision.Policy
	if _, ok := p.NetworkPolicies["preset_tools_release_assets_githubusercontent_com"]; ok {
		t.Fatal("the removed rule stayed")
	}
	if r, ok := p.NetworkPolicies[copied.Name]; !ok || !samePresetRule(r, copied) || !samePresetRule(p.NetworkPolicies["preset_tools_mise_versions_jdx_dev"], rules["preset_tools_mise_versions_jdx_dev"]) {
		t.Fatalf("the copy was not kept under its own name: %+v", p.NetworkPolicies)
	}
	if !reflect.DeepEqual(p.Filesystem, before.Revision.Policy.Filesystem) || !reflect.DeepEqual(p.Landlock, before.Revision.Policy.Landlock) || !reflect.DeepEqual(p.Process, before.Revision.Policy.Process) {
		t.Fatalf("the fixed parts changed: %+v → %+v", before.Revision.Policy, p)
	}
	// The version it was loaded at is gone now.
	if code, v := post(got.Version, rules, false); code != 409 || v["code"] != "policy_yaml_stale" {
		t.Fatal("a stale save:", code, v)
	}
}
