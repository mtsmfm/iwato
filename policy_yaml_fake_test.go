//go:build uitest

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Through the server against the fake's gateway: read, check, apply,
// and the refusals.
func TestPolicyYAMLRoutes(t *testing.T) {
	d := NewFake()
	s := newServer(testConfig(), d, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, cookie, csrf)
	d.seedProviders()
	d.mu.Lock()
	d.prov.attached["dev"] = []string{"github-personal"}
	d.mu.Unlock()
	var got struct {
		Version uint32 `json:"version"`
		YAML    string `json:"yaml"`
		Fixed   string `json:"fixed"`
	}
	r := reviewRequest(s, "GET", "/api/sandboxes/dev/policy/yaml", "", "", "", s.host, cookie, "")
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &got) != nil || got.Version == 0 || !strings.Contains(got.YAML, "allow_pypi_org_443:") || strings.Contains(got.YAML, "_provider_") || !strings.Contains(got.Fixed, "landlock:") {
		t.Fatal(r.Code, r.Body.String())
	}
	post := func(version uint32, yaml string, apply bool) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"version": version, "yaml": yaml, "apply": apply})
		r := reviewRequest(s, "POST", "/api/sandboxes/dev/policy/yaml", string(b), s.origin, "same-origin", s.host, cookie, csrf)
		var v map[string]any
		json.Unmarshal(r.Body.Bytes(), &v)
		return r.Code, v
	}
	if code, v := post(got.Version, got.YAML, false); code != 409 || v["code"] != "policy_yaml_unchanged" {
		t.Fatal("unchanged:", code, v)
	}
	if code, v := post(got.Version, got.YAML+"extra: 1\n", false); code != 400 || v["code"] != "policy_yaml_invalid" {
		t.Fatal("an unknown field:", code, v)
	}
	add := func(host string) string {
		return strings.Replace(got.YAML, "network_policies:\n", "network_policies:\n  mine:\n    endpoints:\n      - { host: "+host+", port: 443, protocol: rest, enforcement: enforce, access: read-only }\n    binaries: [/usr/bin/curl]\n", 1)
	}
	// Endpoint ambiguity, as OpenShell would refuse it.
	audited := strings.Replace(add("api.example.com"), "enforcement: enforce, access: read-only", "enforcement: audit, access: read-only", 1)
	if code, v := post(got.Version, audited, false); code != 409 || v["ambiguities"] == nil {
		t.Fatal("an ambiguous endpoint:", code, v)
	}
	edited := add("tools.example.com")
	var code int
	var v map[string]any
	start := strings.Index(edited, "  allow_pypi_org_443:\n")
	end := start + 1 + strings.Index(edited[start+1:], "\n  allow")
	if start < 0 || end <= start {
		t.Fatal(edited)
	}
	edited = edited[:start] + edited[end+1:]
	code, v = post(got.Version, edited, false)
	var diff PolicyYAMLDiff
	b, _ := json.Marshal(v["diff"])
	json.Unmarshal(b, &diff)
	if code != 200 || len(diff.Added) != 1 || diff.Added[0].Name != "mine" || len(diff.Removed) != 1 || diff.Removed[0].Name != "allow_pypi_org_443" || len(diff.Changed) != 0 {
		t.Fatal("check:", code, v)
	}
	if code, v := post(got.Version, edited, true); code != 200 || uint32(v["version"].(float64)) != got.Version+1 {
		t.Fatal("apply:", code, v)
	}
	if code, v := post(got.Version, edited+"  x:\n    endpoints: [{ host: x.example.com, port: 443 }]\n    binaries: [/usr/bin/curl]\n", false); code != 409 || v["code"] != "policy_yaml_stale" {
		t.Fatal("a stale version:", code, v)
	}
	r = reviewRequest(s, "GET", "/api/sandboxes/dev/policy/yaml", "", "", "", s.host, cookie, "")
	json.Unmarshal(r.Body.Bytes(), &got)
	if !strings.Contains(got.YAML, "  mine:") || strings.Contains(got.YAML, "allow_pypi_org_443") || !strings.Contains(got.Fixed, "landlock:") {
		t.Fatal("after applying:", got.YAML)
	}
}

// A save during which a change landed elsewhere (between Iwato's version
// check and the gateway's update) says that change may be gone.
func TestPolicyYAMLWarnsOfAnOverwrite(t *testing.T) {
	d := NewFake()
	s := newServer(testConfig(), d, true, "127.0.0.1:29999")
	cookie, csrf := reviewLogin(t, s)
	reviewRequest(s, "POST", "/api/sandboxes/dev/start", "", s.origin, "same-origin", s.host, cookie, csrf)
	o := d.openShell()
	update := o.updateConfig
	o.updateConfig = func(ctx context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
		if u.Policy != nil {
			// An approval made with OpenShell's CLI, just before.
			rule := v1.NetworkPolicyRule{Name: "cli", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "cli.example.com", Port: 443}}}
			if _, err := update(ctx, &v1.ConfigUpdate{Name: u.Name, MergeOperations: []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: "cli", Rule: rule}}}}); err != nil {
				return nil, err
			}
		}
		return update(ctx, u)
	}
	var got struct {
		Version uint32 `json:"version"`
		YAML    string `json:"yaml"`
	}
	r := reviewRequest(s, "GET", "/api/sandboxes/dev/policy/yaml", "", "", "", s.host, cookie, "")
	json.Unmarshal(r.Body.Bytes(), &got)
	b, _ := json.Marshal(map[string]any{"version": got.Version, "apply": true, "yaml": strings.Replace(got.YAML, "network_policies:\n", "network_policies:\n  mine:\n    endpoints: [{ host: tools.example.com, port: 443 }]\n    binaries: [/usr/bin/curl]\n", 1)})
	r = reviewRequest(s, "POST", "/api/sandboxes/dev/policy/yaml", string(b), s.origin, "same-origin", s.host, cookie, csrf)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"warningCode":"policy_yaml_overwritten"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}
