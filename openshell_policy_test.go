package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func (f *fakePolicy) List(_, _ string, _ ...v1.ListPolicyOption) (*v1.Pager[v1.SandboxPolicyRevision], error) {
	return v1.NewPager("", func(context.Context, string) (*v1.Page[v1.SandboxPolicyRevision], error) {
		return &v1.Page[v1.SandboxPolicyRevision]{Items: f.revisions}, nil
	}), nil
}

// testRules: git (two binaries, inspected), api (any binary, read-only on
// two ports) and raw (not inspected).
func testRules() map[string]v1.NetworkPolicyRule {
	allow := func(m, p string) v1.L7Rule { return v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: p}} }
	return map[string]v1.NetworkPolicyRule{
		"git": {Name: "git", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/git"}, {Path: "/usr/bin/ssh"}}, Endpoints: []v1.PolicyNetworkEndpoint{
			{Host: "github.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Rules: []v1.L7Rule{allow("GET", "/a/**"), allow("POST", "/a/x")}, DenyRules: []v1.L7DenyRule{{Method: "DELETE", Path: "/**"}}},
			{Host: "gitlab.com", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Rules: []v1.L7Rule{allow("GET", "/b/**")}},
		}},
		"api": {Name: "api", Endpoints: []v1.PolicyNetworkEndpoint{{Host: "api.example.com", Port: 443, Ports: []uint32{443, 8443}, Protocol: "rest", Access: v1.NetworkAccessPresetReadOnly}}},
		"raw": {Name: "raw", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/nc"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "10.0.0.1", Port: 22}}},
	}
}

func opsString(ops []v1.PolicyMergeOperation) string {
	var out []string
	for _, op := range ops {
		switch {
		case op.AddRule != nil:
			r := op.AddRule.Rule
			var bins []string
			for _, b := range r.Binaries {
				bins = append(bins, b.Path)
			}
			e := r.Endpoints[0]
			var rules []string
			for _, x := range e.Rules {
				rules = append(rules, x.Allow.Method+" "+x.Allow.Path)
			}
			out = append(out, fmt.Sprintf("add %s %v %s %v %s %d %d [%s] deny=%d", op.AddRule.RuleName, bins, e.Host, endpointPorts(e), e.Protocol, e.Enforcement, e.Access, strings.Join(rules, ","), len(e.DenyRules)))
		case op.RemoveRule != nil:
			out = append(out, "remove-rule "+op.RemoveRule.RuleName)
		case op.RemoveBinary != nil:
			out = append(out, "remove-binary "+op.RemoveBinary.RuleName+" "+op.RemoveBinary.BinaryPath)
		case op.RemoveEndpoint != nil:
			out = append(out, fmt.Sprintf("remove-endpoint %s %s:%d", op.RemoveEndpoint.RuleName, op.RemoveEndpoint.Host, op.RemoveEndpoint.Port))
		case op.AddAllowRules != nil:
			t := op.AddAllowRules.Target
			out = append(out, fmt.Sprintf("add-allow %s %s %v path=%q binaries=%d any=%v %s %s", t.RuleName, t.Host, t.Ports, *t.Path, len(t.Binaries), t.AnyBinary, op.AddAllowRules.Rules[0].Allow.Method, op.AddAllowRules.Rules[0].Allow.Path))
		}
	}
	return strings.Join(out, "; ")
}

func TestPolicyEditsBecomeMergeOperations(t *testing.T) {
	r := func(m, p string) []ProposalRule { return []ProposalRule{{Method: m, Path: p}} }
	for _, c := range []struct {
		edit PolicyEdit
		want string // or "error: ..."
	}{
		{PolicyEdit{Op: "remove", Rule: "git"}, "remove-rule git"},
		{PolicyEdit{Op: "remove", Rule: "nope"}, "error: この規則はポリシーにありません"},
		{PolicyEdit{Op: "remove-binary", Rule: "git", Binary: "/usr/bin/ssh"}, "remove-binary git /usr/bin/ssh"},
		{PolicyEdit{Op: "remove-binary", Rule: "git", Binary: "/usr/bin/curl"}, "error: このプログラムは規則にありません"},
		// Every port of the endpoint, in any order the UI sends them.
		{PolicyEdit{Op: "remove-endpoint", Rule: "api", Host: "API.example.com", Ports: []uint32{8443, 443}}, "remove-endpoint api api.example.com:443; remove-endpoint api api.example.com:8443"},
		{PolicyEdit{Op: "remove-endpoint", Rule: "api", Host: "api.example.com", Ports: []uint32{443}}, "error: この宛先は規則にありません"},
		// An L7 append declares the rule's whole scope, as the gateway requires.
		{PolicyEdit{Op: "add-allow", Rule: "git", Host: "github.com", Ports: []uint32{443}, Rules: r("PUT", "/a/y")}, `add-allow git github.com [443] path="" binaries=2 any=false PUT /a/y`},
		{PolicyEdit{Op: "add-allow", Rule: "api", Host: "api.example.com", Ports: []uint32{443, 8443}, Rules: r("POST", "/v1/x")}, `add-allow api api.example.com [443 8443] path="" binaries=0 any=true POST /v1/x`},
		{PolicyEdit{Op: "add-allow", Rule: "raw", Host: "10.0.0.1", Ports: []uint32{22}, Rules: r("GET", "/")}, "error: この宛先は要求を検査していません"},
		// One method and path: the endpoint goes out and comes back without it.
		{PolicyEdit{Op: "remove-allow", Rule: "git", Host: "github.com", Ports: []uint32{443}, Rules: r("POST", "/a/x")}, "remove-endpoint git github.com:443; add git [/usr/bin/git /usr/bin/ssh] github.com [443] rest 1 0 [GET /a/**] deny=1"},
		{PolicyEdit{Op: "remove-allow", Rule: "git", Host: "github.com", Ports: []uint32{443}, Rules: r("GET", "/zzz")}, "error: このメソッドとパスは宛先にありません"},
		{PolicyEdit{Op: "remove-allow", Rule: "git", Host: "gitlab.com", Ports: []uint32{443}, Rules: r("GET", "/b/**")}, "error: 宛先に残る最後の許可です。宛先ごと外してください"},
		// A new rule is named as the CLI names it; an existing one keeps its binaries.
		{PolicyEdit{Op: "add", Binaries: []string{"/usr/bin/curl"}, Host: "files.example-cdn.com", Ports: []uint32{443}, Access: "read-write", Audit: true}, "add allow_files_example_cdn_com_443 [/usr/bin/curl] files.example-cdn.com [443] rest 2 2 [] deny=0"},
		{PolicyEdit{Op: "add", Rule: "git", Host: "bitbucket.org", Ports: []uint32{443}, Rules: r("GET", "/**")}, "add git [/usr/bin/git /usr/bin/ssh] bitbucket.org [443] rest 1 0 [GET /**] deny=0"},
		{PolicyEdit{Op: "add", Rule: "new", AnyBinary: true, Host: "example.com", Ports: []uint32{80}}, "add new [] example.com [80]  0 0 [] deny=0"},
		{PolicyEdit{Op: "add", Rule: "new", Host: "example.com", Ports: []uint32{80}}, "error: プログラムを指定してください"},
	} {
		if err := c.edit.Validate(); err != nil {
			t.Errorf("%+v: %v", c.edit, err)
			continue
		}
		rules := testRules()
		ops, err := c.edit.operations(rules)
		got := opsString(ops)
		if err != nil {
			got = "error: " + err.Error()
		}
		if got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.edit, got, c.want)
		}
		if !reflect.DeepEqual(rules, testRules()) {
			t.Errorf("%+v changed the policy it read", c.edit)
		}
	}
}

func TestPolicyEditValidation(t *testing.T) {
	ep := func(e PolicyEdit) PolicyEdit {
		if e.Host == "" {
			e.Host = "example.com"
		}
		if e.Ports == nil {
			e.Ports = []uint32{443}
		}
		return e
	}
	for _, bad := range []PolicyEdit{
		{Op: "nope", Rule: "x"},
		{Op: "remove"},
		{Op: "remove", Rule: "a\nb"},
		{Op: "remove-binary", Rule: "x", Binary: "curl"},
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Host: "https://example.com"}),
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Host: "a b"}),
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Ports: []uint32{}}),
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Ports: []uint32{0}}),
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Ports: []uint32{70000}}),
		ep(PolicyEdit{Op: "remove-endpoint", Rule: "x", Ports: []uint32{443, 443}}),
		ep(PolicyEdit{Op: "add-allow", Rule: "x"}),
		ep(PolicyEdit{Op: "add-allow", Rule: "x", Rules: []ProposalRule{{Method: "TRACE", Path: "/"}}}),
		ep(PolicyEdit{Op: "add-allow", Rule: "x", Rules: []ProposalRule{{Method: "GET", Path: "a"}}}),
		ep(PolicyEdit{Op: "remove-allow", Rule: "x"}),
		ep(PolicyEdit{Op: "add", Rule: "a b", AnyBinary: true}),
		ep(PolicyEdit{Op: "add", Rule: "_provider_x", AnyBinary: true}),
		ep(PolicyEdit{Op: "add", AnyBinary: true, Binaries: []string{"/bin/sh"}}),
		ep(PolicyEdit{Op: "add", Binaries: []string{"sh"}}),
		ep(PolicyEdit{Op: "add", Binaries: []string{"/bin/sh", "/bin/sh"}}),
		ep(PolicyEdit{Op: "add", AnyBinary: true, Access: "write"}),
		ep(PolicyEdit{Op: "add", AnyBinary: true, Access: "read-only", Rules: []ProposalRule{{Method: "GET", Path: "/"}}}),
		ep(PolicyEdit{Op: "add", AnyBinary: true, Audit: true}),
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	for _, good := range []PolicyEdit{
		ep(PolicyEdit{Op: "add", Binaries: []string{"/usr/bin/curl"}, Host: "*.example.com", Ports: []uint32{443, 8443}}),
		ep(PolicyEdit{Op: "add", Rule: "my-rule.1", Binaries: []string{"/usr/bin/curl"}, Access: "full", Audit: true}),
		ep(PolicyEdit{Op: "remove-allow", Rule: "x", Rules: []ProposalRule{{Method: "*", Path: "**"}}}),
	} {
		if err := good.Validate(); err != nil {
			t.Errorf("rejected %+v: %v", good, err)
		}
	}
}

func TestEditPolicyUpdatesTheLatestRevision(t *testing.T) {
	var updates []*v1.ConfigUpdate
	f := &fakePolicy{status: &v1.PolicyStatusResult{ActiveVersion: 4, Revision: v1.SandboxPolicyRevision{Version: 4, Status: v1.PolicyLoadStatusLoaded, Policy: &v1.SandboxPolicy{NetworkPolicies: testRules()}}},
		revisions: []v1.SandboxPolicyRevision{{Version: 3, Status: v1.PolicyLoadStatusSuperseded, CreatedAt: time.Unix(100, 0), LoadedAt: time.Unix(110, 0)}, {Version: 4, Status: v1.PolicyLoadStatusLoaded, CreatedAt: time.Unix(200, 0)}, {Version: 5, Status: v1.PolicyLoadStatusFailed, LoadError: "bad", CreatedAt: time.Unix(300, 0)}}}
	o := &OpenShell{pol: f, sandboxConfig: f.sandboxConfig, logs: noLogs, updateConfig: func(_ context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
		updates = append(updates, u)
		return &v1.ConfigUpdateResult{Version: 5}, nil
	}}
	w, ctx := newSandbox("dev"), context.Background()
	if _, err := o.EditPolicy(ctx, w, PolicyEdit{Op: "remove", Rule: "nope"}); err == nil || len(updates) != 0 {
		t.Fatal("removed a rule that is not there", err)
	}
	change, err := o.EditPolicy(ctx, w, PolicyEdit{Op: "remove", Rule: "git"})
	if err != nil || len(updates) != 1 || updates[0].Name != "dev" || updates[0].Policy != nil || opsString(updates[0].MergeOperations) != "remove-rule git" {
		t.Fatal(err, updates)
	}
	// The revision the update made, to show until the sandbox loads it.
	if change.Version != 5 || change.Kind != "edit" || change.Rule != "git" {
		t.Fatalf("%+v", change)
	}
	list, err := o.PolicyHistory(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(list)
	if string(b) != `[{"version":5,"status":"failed","active":false,"createdAt":300,"loadError":"bad"},{"version":4,"status":"loaded","active":true,"createdAt":200},{"version":3,"status":"superseded","active":false,"createdAt":100,"loadedAt":110}]` {
		t.Fatal(string(b))
	}
	p, err := o.PolicyAt(ctx, w, 4)
	if err != nil || len(p.Rules) != 3 || p.Rules[1].Name != "git" || len(p.Rules[1].Endpoints[0].Deny) != 1 || p.Rules[0].Endpoints[0].Access != "read-only" {
		t.Fatalf("%v %+v", err, p)
	}
}

// Taking an endpoint out and putting it back (enforce, remove-allow) keeps
// its allowed_ips; the policy view shows them.
func TestPolicyEditsKeepAllowedIPs(t *testing.T) {
	allow := func(m, p string) v1.L7Rule { return v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: p}} }
	rules := func() map[string]v1.NetworkPolicyRule {
		return map[string]v1.NetworkPolicyRule{"ci": {Name: "ci", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{
			{Host: "ci.tailnet.example", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeAudit, Rules: []v1.L7Rule{allow("GET", "/"), allow("GET", "/a")}, AllowedIPs: []string{"100.64.0.0/10"}},
		}}}
	}
	for _, edit := range []PolicyEdit{
		{Op: "enforce", Rule: "ci", Host: "ci.tailnet.example", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/a"}}},
		{Op: "remove-allow", Rule: "ci", Host: "ci.tailnet.example", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/"}}},
	} {
		if err := edit.Validate(); err != nil {
			t.Fatal(err)
		}
		ops, err := edit.operations(rules())
		if err != nil {
			t.Fatal(edit.Op, err)
		}
		add := ops[len(ops)-1].AddRule
		if add == nil || strings.Join(add.Rule.Endpoints[0].AllowedIPs, ",") != "100.64.0.0/10" {
			t.Errorf("%s dropped allowed_ips: %+v", edit.Op, add)
		}
	}
	p := policyView(&v1.PolicyStatusResult{}, rules())
	if got := p.Rules[0].Endpoints[0].AllowedIPs; strings.Join(got, ",") != "100.64.0.0/10" {
		t.Fatal("the view lacks allowed_ips:", got)
	}
}
