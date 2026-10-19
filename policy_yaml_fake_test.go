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
	// A provider's host: only once confirmed, and the confirmation is for
	// that YAML alone.
	code, v := post(got.Version, add("api.github.com"), false)
	token, _ := v["confirm"].(string)
	if code != 409 || v["code"] != "policy_provider_host" || token == "" {
		t.Fatal("a provider's host:", code, v)
	}
	postConfirmed := func(yaml, confirm string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"version": got.Version, "yaml": yaml, "confirm": confirm})
		r := reviewRequest(s, "POST", "/api/sandboxes/dev/policy/yaml", string(b), s.origin, "same-origin", s.host, cookie, csrf)
		var v map[string]any
		json.Unmarshal(r.Body.Bytes(), &v)
		return r.Code, v
	}
	if code, v := postConfirmed(add("api.github.com"), token); code != 200 || v["diff"] == nil {
		t.Fatal("confirmed:", code, v)
	}
	if code, v := postConfirmed(strings.Replace(add("api.github.com"), "read-only", "full", 1), token); code != 409 || v["code"] != "policy_provider_host" {
		t.Fatal("another YAML with the same confirmation:", code, v)
	}
	// Endpoint ambiguity, as OpenShell would refuse it.
	audited := strings.Replace(add("api.example.com"), "enforcement: enforce, access: read-only", "enforcement: audit, access: read-only", 1)
	if code, v := post(got.Version, audited, false); code != 409 || v["ambiguities"] == nil {
		t.Fatal("an ambiguous endpoint:", code, v)
	}
	edited := add("tools.example.com")
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

// ADR 0006 across providers: a hand-written rule that reaches any known
// profile's credential (attached or not), and attaching a provider while a
// rule reaches its credential beyond its own rule, need the owner's
// confirmation.
func TestProviderHostsAcrossProfiles(t *testing.T) {
	_, d, do := providerServer(t)
	do("POST", "/api/sandboxes/dev/start", `{}`)
	// No GitHub provider attached: still GitHub's host, so it is asked
	// first; confirmed, it goes in.
	add := `{"binaries":["/usr/bin/curl"],"host":"api.github.com","ports":[443],"access":"full","rule":"gh_hand"}`
	code, body := do("POST", "/api/sandboxes/dev/policy/add", add)
	var ask struct{ Code, Confirm string }
	if code != 409 || json.Unmarshal([]byte(body), &ask) != nil || ask.Code != "policy_provider_host" || ask.Confirm == "" || !strings.Contains(body, `"scoped":true`) {
		t.Fatal(code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/policy/add", strings.Replace(add, `"rule"`, `"confirm":"`+ask.Confirm+`","rule"`, 1)); code != 200 {
		t.Fatal("confirmed:", code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/policy/remove", `{"rule":"gh_hand"}`); code != 200 {
		t.Fatal(code, body)
	}
	sb := newSandbox("dev")
	// A rule from before: full access to api.github.com.
	if _, err := d.EditPolicy(context.Background(), sb, PolicyEdit{Op: "add", Rule: "gh_full", Binaries: []string{"/usr/bin/curl"}, Host: "api.github.com", Ports: []uint32{443}, Access: "full"}); err != nil {
		t.Fatal(err)
	}
	code, body = do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`)
	if code != 409 || json.Unmarshal([]byte(body), &ask) != nil || ask.Code != "provider_attach_beyond_scope" || ask.Confirm == "" || !strings.Contains(body, "gh_full") {
		t.Fatal(code, body)
	}
	// Confirmed, it attaches; detached again for what follows.
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal","confirm":"`+ask.Confirm+`"}`); code != 200 {
		t.Fatal("confirmed attach:", code, body)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/detach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
	// Narrowed to reads (what the provider's own rule allows), it fits.
	if _, err := d.EditPolicy(context.Background(), sb, PolicyEdit{Op: "remove", Rule: "gh_full"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EditPolicy(context.Background(), sb, PolicyEdit{Op: "add", Rule: "gh_read", Binaries: []string{"/usr/bin/curl"}, Host: "api.github.com", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/repos/**"}}}); err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/sandboxes/dev/providers/attach", `{"provider":"github-personal"}`); code != 200 {
		t.Fatal(code, body)
	}
}
