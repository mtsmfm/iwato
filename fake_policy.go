//go:build uitest

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// fakeRevision is one version of a fake sandbox's network policy.
type fakeRevision struct {
	at         time.Time
	rules      map[string]v1.NetworkPolicyRule
	provenance map[string]string
}

// fakeLoadDelay stands in for the sandbox's settings poll.
const fakeLoadDelay = 3 * time.Second

// Fake paths, as in the standard image.
const (
	fakeGitRemote = "/usr/lib/git-core/git-remote-http"
	fakeClaudeBin = "/sandbox/.local/share/claude/versions/2.1.293"
	fakeNode      = "/sandbox/.local/share/mise/installs/node/22.11.0/bin/node"
	fakeNodeGlob  = "/sandbox/.local/share/mise/installs/node/*/bin/node"
	fakeMise      = "/usr/local/lib/mise/mise"
	// fakeGHE is a GitHub Enterprise host (not the GitHub provider's).
	fakeGHE     = "github.example.com"
	fakeGHERule = "allow_github_example_com_443"
)

// fakePolicy is the sandbox's revisions, oldest first; it starts with two.
// The second is the current policy: rules from approved
// proposals (one widened to a glob, one merged from two proposals), rules
// by hand, and none for an approved proposal whose rule was taken out.
// The caller holds d.mu.
func (d *Fake) fakePolicy(name string) []fakeRevision {
	if d.policies == nil {
		d.policies = map[string][]fakeRevision{}
	}
	if d.policies[name] == nil {
		pypi := v1.NetworkPolicyRule{Name: "allow_pypi_org_443", Binaries: policyBinaries("/usr/bin/python3.*", "/usr/local/bin/pip"), Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint("pypi.org", 443), l4Endpoint("files.pythonhosted.org", 443)}}
		api := restEndpoint("api.example.com", 443, 8443)
		api.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/v1/**"}}, {Allow: &v1.L7Allow{Method: "POST", Path: "/v1/items"}}}
		api.DenyRules = []v1.L7DenyRule{{Method: "DELETE", Path: "/v1/**"}}
		mise := v1.NetworkPolicyRule{Name: "allow_mise_versions_jdx_dev_443", Binaries: policyBinaries("/usr/local/bin/mise"), Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint("mise-versions.jdx.dev", 443)}}
		first := map[string]v1.NetworkPolicyRule{pypi.Name: pypi, mise.Name: mise}
		second := cloneRules(first)
		// The mise proposal's rule was taken out by hand.
		delete(second, mise.Name)
		// From two approved proposals (gh, then git), in audit mode: the
		// requests it logged show on it. GitHub Enterprise: the GitHub
		// provider's rule enforces api.github.com, and OpenShell refuses
		// an audited endpoint there (endpoint_ambiguity.go).
		ghe := restEndpoint(fakeGHE, 443)
		ghe.Enforcement = v1.NetworkEnforcementModeAudit
		ghe.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}
		second[fakeGHERule] = v1.NetworkPolicyRule{Name: fakeGHERule, Binaries: policyBinaries("/usr/bin/gh", "/usr/bin/git"), Endpoints: []v1.PolicyNetworkEndpoint{ghe}}
		// Approved with node widened to a glob, reads only; an endpoint was
		// added by hand after.
		npm := restEndpoint("registry.npmjs.org", 443)
		npm.Access = v1.NetworkAccessPresetReadOnly
		pkg := restEndpoint("npm.pkg.github.com", 443)
		pkg.Access = v1.NetworkAccessPresetReadOnly
		second["allow_registry_npmjs_org_443"] = v1.NetworkPolicyRule{Name: "allow_registry_npmjs_org_443", Binaries: policyBinaries(fakeNodeGlob), Endpoints: []v1.PolicyNetworkEndpoint{npm, pkg}}
		second["allow_api_example_com_443"] = v1.NetworkPolicyRule{Name: "allow_api_example_com_443", Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{api}}
		// A host on the tailnet: its CGNAT address is internal, so the
		// endpoint lists it in allowed_ips.
		ci := restEndpoint("ci.tailnet.example", 443)
		ci.Access = v1.NetworkAccessPresetReadOnly
		ci.AllowedIPs = []string{"100.64.0.0/10"}
		second["allow_ci_tailnet_example_443"] = v1.NetworkPolicyRule{Name: "allow_ci_tailnet_example_443", Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{ci}}
		// Added before a provider: not inspected, so attaching the
		// Claude provider conflicts (credential_conflicts.go).
		second["allow_api_anthropic_com_443"] = v1.NetworkPolicyRule{Name: "allow_api_anthropic_com_443", Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint("api.anthropic.com", 443)}}
		if name == "dev" && d.seedPolicy != nil {
			second = cloneRules(d.seedPolicy)
		}
		// The attached providers' initial rules (provider_rules.go), as
		// the sandbox was created with them.
		var list []ProviderInfo
		for _, p := range d.prov.list {
			list = append(list, p)
		}
		if withInitial, err := withProviderInitialRules(second, list, d.prov.attached[name]); err == nil {
			second = withInitial
		}
		now := time.Now()
		d.policies[name] = []fakeRevision{{at: now.Add(-2 * time.Hour), rules: first}, {at: now.Add(-time.Hour), rules: second}}
	}
	return d.policies[name]
}

// fakeDraft is the sandbox's proposals as OpenShell keeps them: pending,
// approved (their rules above) and rejected. The caller holds d.mu.
func (d *Fake) fakeDraft(name string) []v1.PolicyChunk {
	if d.drafts == nil {
		d.drafts = map[string][]v1.PolicyChunk{}
	}
	if d.drafts[name] != nil {
		return d.drafts[name]
	}
	now := time.Now()
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	day := 24 * 60
	rule := func(name string, binaries []string, endpoints ...v1.PolicyNetworkEndpoint) *v1.NetworkPolicyRule {
		return &v1.NetworkPolicyRule{Name: name, Binaries: policyBinaries(binaries...), Endpoints: endpoints}
	}
	auto := func(binary, host string, port int) string {
		return fmt.Sprintf("Allow %s to connect to %s:%d (HTTPS).", binary[strings.LastIndex(binary, "/")+1:], host, port)
	}
	git := restEndpoint("github.com", 443)
	git.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/mtsmfm/iwato.git/info/refs"}}, {Allow: &v1.L7Allow{Method: "POST", Path: "/mtsmfm/iwato.git/git-receive-pack"}}}
	downloads := restEndpoint("downloads.claude.ai", 443)
	downloads.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/claude-code-releases/2.1.294/manifest.json"}}, {Allow: &v1.L7Allow{Method: "GET", Path: "/claude-code-releases/2.1.294/linux-x64/claude"}}}
	gh := restEndpoint(fakeGHE, 443)
	gh.Enforcement = v1.NetworkEnforcementModeAudit
	gh.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}
	npm := restEndpoint("registry.npmjs.org", 443)
	npm.Access = v1.NetworkAccessPresetReadOnly
	pr := restEndpoint("api.github.com", 443)
	pr.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "POST", Path: "/repos/octo-org/octo-repo/pulls"}}}
	misePlaceholder := restEndpoint("api.github.com", 443)
	misePlaceholder.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}
	c := func(id, status, ruleName, binary, rationale string, hits int32, first, last time.Time, r *v1.NetworkPolicyRule) v1.PolicyChunk {
		return v1.PolicyChunk{ID: id, Status: status, RuleName: ruleName, Binary: binary, Rationale: rationale, HitCount: hits, FirstSeen: first, LastSeen: last, CreatedAt: first, ProposedRule: r, ReviewToken: "t-" + id}
	}
	chunks := []v1.PolicyChunk{
		// Waiting.
		c("fake-git", "pending", "allow_github_com_443", fakeGitRemote, "iwato リポジトリにブランチを push する", 3, ago(16), ago(12), rule("allow_github_com_443", []string{fakeGitRemote}, git)),
		// A REST write under a repository, by the GitHub provider's gh.
		c("fake-pr", "pending", "allow_api_github_com_443", "/usr/bin/gh", "octo-org/octo-repo に PR を作る", 1, ago(14), ago(14), rule("allow_api_github_com_443", []string{"/usr/bin/gh"}, pr)),
		c("fake-downloads", "pending", "allow_downloads_claude_ai_443", fakeClaudeBin, auto(fakeClaudeBin, "downloads.claude.ai", 443), 4, ago(174), ago(23), rule("allow_downloads_claude_ai_443", []string{fakeClaudeBin}, downloads)),
		c("fake-datadog", "pending", "allow_http_intake_logs_us5_datadoghq_com_443", fakeClaudeBin, auto(fakeClaudeBin, "http-intake.logs.us5.datadoghq.com", 443), 37, ago(302), ago(10), rule("allow_http_intake_logs_us5_datadoghq_com_443", []string{fakeClaudeBin}, l4Endpoint("http-intake.logs.us5.datadoghq.com", 443))),
		// Not inspected, to where the attached GitHub provider's token
		// goes: approving it as proposed is refused (credential_conflicts.go).
		// OpenShell's own proposal for api.github.com, where the GitHub
		// provider's rule enforces: approving it audited is refused
		// (endpoint_ambiguity.go). Its only rule, GET /, was seen by no
		// one: the advisor copied it from an existing endpoint
		// (canonicalize_advisor_add_rule).
		c("fake-mise-gh", "pending", "allow_api_github_com_443", fakeMise, auto(fakeMise, "api.github.com", 443), 2, ago(8), ago(3), rule("allow_api_github_com_443", []string{fakeMise}, misePlaceholder)),
		c("fake-uploads", "pending", "allow_uploads_github_com_443", "/usr/bin/gh", "リリースにビルドしたファイルを上げる", 1, ago(5), ago(5), rule("allow_uploads_github_com_443", []string{"/usr/bin/gh"}, l4Endpoint("uploads.github.com", 443))),
		// Approved: two into one rule, one widened to a glob, and one
		// whose rule was taken out by hand.
		c("fake-gh", "approved", fakeGHERule, "/usr/bin/gh", "PR の一覧と CI の結果を見る", 1, ago(2*day+40), ago(2*day+40), rule(fakeGHERule, []string{"/usr/bin/gh"}, gh)),
		c("fake-git-api", "approved", fakeGHERule, "/usr/bin/git", auto("/usr/bin/git", fakeGHE, 443), 2, ago(day+90), ago(day+80), rule(fakeGHERule, []string{"/usr/bin/git"}, gh)),
		c("fake-npm", "approved", "allow_registry_npmjs_org_443", fakeNode, auto(fakeNode, "registry.npmjs.org", 443), 6, ago(150), ago(70), rule("allow_registry_npmjs_org_443", []string{fakeNodeGlob}, npm)),
		c("fake-mise", "approved", "allow_mise_versions_jdx_dev_443", "/usr/local/bin/mise", auto("/usr/local/bin/mise", "mise-versions.jdx.dev", 443), 1, ago(3*day), ago(3*day), rule("allow_mise_versions_jdx_dev_443", []string{"/usr/local/bin/mise"}, l4Endpoint("mise-versions.jdx.dev", 443))),
		// Rejected: by the owner (seen twice since), and by OpenShell (an
		// approved proposal covers it).
		c("fake-curl", "rejected", "allow_mise_versions_jdx_dev_443", "/usr/bin/curl", auto("/usr/bin/curl", "mise-versions.jdx.dev", 443), 3, ago(day), ago(25), rule("allow_mise_versions_jdx_dev_443", []string{"/usr/bin/curl"}, l4Endpoint("mise-versions.jdx.dev", 443))),
		c("fake-gh-auto", "rejected", fakeGHERule, "/usr/bin/gh", auto("/usr/bin/gh", fakeGHE, 443), 1, ago(2*day), ago(2*day), rule(fakeGHERule, []string{"/usr/bin/gh"}, l4Endpoint(fakeGHE, 443))),
	}
	if name == "experiment" {
		// 100 rules over four endpoints: the approve dialog scrolls.
		var eps []v1.PolicyNetworkEndpoint
		for e := range 4 {
			ep := restEndpoint(fmt.Sprintf("api%d.many.example", e+1), 443)
			for i := range 25 {
				method := "GET"
				if i%5 == 4 {
					method = "POST"
				}
				ep.Rules = append(ep.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: method, Path: fmt.Sprintf("/v1/items/%d", e*25+i)}})
			}
			eps = append(eps, ep)
		}
		chunks = append(chunks, c("fake-many", "pending", "allow_many_example", "/usr/bin/curl", "多数の API を呼ぶ", 100, ago(4), ago(2), rule("allow_many_example", []string{"/usr/bin/curl"}, eps...)))
		// Over the dialog's rule limit: not approvable as proposed.
		huge := restEndpoint("api.huge.example", 443)
		for i := range proposalLimits.Rules + 2 {
			huge.Rules = append(huge.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: "GET", Path: fmt.Sprintf("/v2/things/%d", i)}})
		}
		chunks = append(chunks, c("fake-huge", "pending", "allow_huge_example", "/usr/bin/curl", "さらに多数の API を呼ぶ", 130, ago(3), ago(1), rule("allow_huge_example", []string{"/usr/bin/curl"}, huge)))
	}
	decided := map[string]time.Time{"fake-gh": ago(2*day + 30), "fake-git-api": ago(day + 60), "fake-npm": ago(60), "fake-mise": ago(3*day - 10), "fake-curl": ago(day - 30), "fake-gh-auto": ago(2 * day)}
	for i := range chunks {
		if at, ok := decided[chunks[i].ID]; ok {
			chunks[i].DecidedAt = at
		}
	}
	for i := range chunks {
		switch chunks[i].ID {
		case "fake-curl":
			chunks[i].RejectionReason = rejectReason(1, "")
		case "fake-gh-auto":
			chunks[i].RejectionReason = "already covered by approved chunk fake-gh (agent_authored or prior auto-approval)"
		}
	}
	d.drafts[name] = chunks
	return chunks
}

// fakeMerge applies merge operations as the gateway does (simulateMerge)
// and refuses, in OpenShell's words, a result whose endpoints are
// ambiguous with the attached providers' rules. The caller holds d.mu.
func (d *Fake) fakeMerge(name string, ops []v1.PolicyMergeOperation) error {
	revs := d.fakePolicy(name)
	rules, err := simulateMerge(revs[len(revs)-1].rules, ops)
	if err != nil {
		return err
	}
	if err := fakeValidate(d.fakeCompose(name, rules)); err != nil {
		return err
	}
	if rulesKey(rules) == rulesKey(revs[len(revs)-1].rules) {
		// As the gateway: the same policy makes no revision.
		return nil
	}
	d.policies[name] = append(revs, fakeRevision{at: time.Now(), rules: rules})
	return nil
}

// fakeValidate is the gateway's validate_endpoint_ambiguities.
func fakeValidate(rules map[string]v1.NetworkPolicyRule) error {
	list := findEndpointAmbiguities(rules)
	if len(list) == 0 {
		return nil
	}
	var lines []string
	for _, a := range list {
		lines = append(lines, a.String())
	}
	return errors.New("network endpoint ambiguity validation failed:\n" + strings.Join(lines, "\n"))
}

// fakeBindings refuses bindings as the gateway does: to a provider that is
// not attached, or whose profile has endpoints. The caller holds d.mu.
func (d *Fake) fakeBindings(name string, rules map[string]v1.NetworkPolicyRule) error {
	for _, p := range boundProviders(rules) {
		if !slices.Contains(d.prov.attached[name], p) {
			return fmt.Errorf("credential_binding references provider '%s', but that provider is not attached to the sandbox", p)
		}
		if _, ok := builtinProfile(d.prov.list[p].Profile); !ok {
			return fmt.Errorf("credential_binding provider '%s' profile already defines endpoints; profile endpoints remain the credential boundary", p)
		}
	}
	return nil
}

// fakeCompose is rules with the attached providers' rules, as the gateway
// composes them. The caller holds d.mu.
func (d *Fake) fakeCompose(name string, rules map[string]v1.NetworkPolicyRule) map[string]v1.NetworkPolicyRule {
	rules = cloneRules(rules)
	for _, p := range d.prov.attached[name] {
		if prof, ok := profileByID(d.prov.list[p].Profile); ok {
			composeProviderRule(rules, p, prof.proto())
		} else if d.prov.list[p].Profile == "openai" {
			m, _ := parseProfileYAML([]byte(fakeGatewayProfile))
			composeProviderRule(rules, p, m)
		}
	}
	return rules
}

// fakeStatus is the revision (1-based; 0 for the latest) as GetStatus
// reports it.
func (d *Fake) fakeStatus(name string, version int) (*v1.PolicyStatusResult, error) {
	revs := d.fakePolicy(name)
	if version == 0 {
		version = len(revs)
	}
	if version > len(revs) {
		return nil, errors.New("policy revision not found")
	}
	active := len(revs)
	if time.Since(revs[active-1].at) < fakeLoadDelay {
		active--
	}
	rev := revs[version-1]
	status := v1.PolicyLoadStatusSuperseded
	switch {
	case version > active:
		status = v1.PolicyLoadStatusPending
	case version == active:
		status = v1.PolicyLoadStatusLoaded
	}
	// The parts fixed at creation, as Iwato creates them.
	policy := sandboxPolicy(Sandbox{Name: name})
	policy.NetworkPolicies = rev.rules
	return &v1.PolicyStatusResult{ActiveVersion: uint32(active), Revision: v1.SandboxPolicyRevision{Version: uint32(version), Status: status, CreatedAt: rev.at, Policy: policy, Provenance: maps.Clone(rev.provenance)}}, nil
}

func (d *Fake) EditPolicy(ctx context.Context, w Sandbox, edit PolicyEdit) (PolicyChange, error) {
	return d.openShell().EditPolicy(ctx, w, edit)
}

func (d *Fake) PolicyHistory(_ context.Context, w Sandbox) ([]PolicyRevision, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := []PolicyRevision{}
	for v := len(d.fakePolicy(w.Name)); v > 0 && len(list) < policyHistorySize; v-- {
		st, _ := d.fakeStatus(w.Name, v)
		list = append(list, PolicyRevision{Version: uint32(v), Status: strings.ToLower(st.Revision.Status.String()), Active: st.ActiveVersion == uint32(v), CreatedAt: st.Revision.CreatedAt.Unix()})
	}
	return list, nil
}

func (d *Fake) PolicyAt(_ context.Context, w Sandbox, version uint32) (*CurrentPolicy, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := d.fakeStatus(w.Name, int(version))
	if err != nil {
		return nil, err
	}
	return policyView(st, st.Revision.Policy.NetworkPolicies), nil
}

// gatewayCall stands for one call to the gateway: it waits
// gatewayLatency and counts the call. The caller does not hold d.mu.
func (d *Fake) gatewayCall(name string) {
	d.callsMu.Lock()
	if d.calls == nil {
		d.calls = map[string]int{}
	}
	d.calls[name]++
	d.callsMu.Unlock()
	time.Sleep(d.gatewayLatency)
}

// gatewayCalls are the calls counted since the last time it was read.
func (d *Fake) gatewayCalls() map[string]int {
	d.callsMu.Lock()
	defer d.callsMu.Unlock()
	c := d.calls
	d.calls = nil
	return c
}

// fakeLogsAt is when the fake's log ends: the same lines on every read.
var fakeLogsAt = time.Now()

// fakeLogs are the sandbox's L7 log lines: the audited fake endpoint's
// requests, and denials.
func fakeLogs() []v1.LogLine {
	now := fakeLogsAt
	var lines []v1.LogLine
	for i, r := range []string{"GET /api/v3/repos/mtsmfm/iwato", "GET /api/v3/repos/mtsmfm/iwato/pulls", "GET /api/v3/repos/mtsmfm/iwato/pulls", "POST /api/v3/repos/mtsmfm/iwato/pulls", "GET /api/v3/user"} {
		method, path, _ := strings.Cut(r, " ")
		lines = append(lines, v1.LogLine{Timestamp: now.Add(time.Duration(i-10) * time.Minute), Source: "sandbox", Level: "OCSF",
			Message: "HTTP:" + method + " [INFO] ALLOWED /usr/bin/gh(42) -> " + method + " https://" + fakeGHE + path + " [policy:" + fakeGHERule + " engine:l7]"})
	}
	// Denied, as the L7 relay writes it (no process: the CONNECT before
	// names it): on the enforced example API, on the read-only CI host, and
	// on the GitHub provider's read-only API. And a program the example
	// rule does not name.
	at := now.Add(-9 * time.Minute)
	add := func(msg string) {
		at = at.Add(20 * time.Second)
		lines = append(lines, v1.LogLine{Timestamp: at, Source: "sandbox", Level: "OCSF", Message: msg})
	}
	connect := func(binary string, pid int, host, rule string) {
		add(fmt.Sprintf("NET:OPEN [INFO] ALLOWED %s(%d) -> %s:443 [policy:%s engine:opa]", binary, pid, host, rule))
	}
	deny := func(method, host, path, rule, reason string) {
		add(fmt.Sprintf("HTTP:%s [MED] DENIED %s http://%s:443%s [policy:%s engine:l7] [reason:L7_REQUEST deny %s %s:443%s reason=%s]", method, method, host, path, rule, method, host, path, reason))
	}
	const api, ci, gh = "allow_api_example_com_443", "allow_ci_tailnet_example_443", "provider_github_personal"
	connect("/usr/bin/curl", 301, "api.example.com", api)
	deny("PUT", "api.example.com", "/v1/items/42", api, "PUT /v1/items/42 not permitted by policy")
	deny("GET", "api.example.com", "/v2/status", api, "GET /v2/status not permitted by policy")
	deny("PUT", "api.example.com", "/v1/items/42", api, "PUT /v1/items/42 not permitted by policy")
	deny("DELETE", "api.example.com", "/v1/items/7", api, "DELETE /v1/items/7 blocked by deny rule")
	add("NET:OPEN [MED] DENIED /usr/bin/wget(304) -> api.example.com:443 [policy:- engine:opa] [reason:binary '/usr/bin/wget' not allowed in policy '" + api + "' (ancestors: [/usr/bin/bash], cmdline: [])]")
	connect("/usr/bin/curl", 302, "ci.tailnet.example", ci)
	deny("POST", "ci.tailnet.example", "/api/builds", ci, "POST /api/builds not permitted by policy")
	connect("/usr/bin/gh", 303, "api.github.com", gh)
	deny("POST", "api.github.com", "/repos/mtsmfm/iwato/releases", gh, "POST /repos/mtsmfm/iwato/releases not permitted by policy")
	return lines
}

// fakeGateway answers OpenShell's policy API for the fake, so that the
// fakePropose adds an agent's pending proposal for HOST:443 (the fake
// terminal's propose HOST).
func (d *Fake) fakePropose(sandbox, host string) string {
	if !regexp.MustCompile(`^[a-z0-9.-]{1,100}$`).MatchString(host) {
		return "propose: bad host"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	chunks := d.fakeDraft(sandbox)
	name := "allow_" + strings.NewReplacer(".", "_", "-", "_").Replace(host) + "_443"
	id := fmt.Sprintf("fake-propose-%d", len(chunks))
	now := time.Now()
	d.drafts[sandbox] = append(chunks, v1.PolicyChunk{ID: id, Status: "pending", RuleName: name, Binary: "/usr/bin/curl", Rationale: "propose " + host,
		HitCount: 1, FirstSeen: now, LastSeen: now, CreatedAt: now, ReviewToken: "t-" + id,
		ProposedRule: &v1.NetworkPolicyRule{Name: name, Binaries: policyBinaries("/usr/bin/curl"), Endpoints: []v1.PolicyNetworkEndpoint{l4Endpoint(host, 443)}}})
	return "proposed " + name
}

// fake runs the same code as the gateway (openshell_proposals.go,
// openshell_policy_lists.go). Each call takes d.mu.
type fakeGateway struct {
	v1.PolicyInterface
	d *Fake
}

func (g fakeGateway) chunk(sandbox, id string) (*v1.PolicyChunk, error) {
	chunks := g.d.fakeDraft(sandbox)
	for i := range chunks {
		if chunks[i].ID == id {
			return &chunks[i], nil
		}
	}
	return nil, errors.New("chunk not found")
}

// GetDraft answers the chunks of the status asked for (all without one),
// and, as the gateway, the draft version over all of them: the fake drafts
// its proposals once, so it stays 1.
func (g fakeGateway) GetDraft(_ context.Context, _, sandbox string, opts ...v1.GetDraftOption) (*v1.DraftPolicy, error) {
	g.d.gatewayCall("GetDraft")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	status := draftStatusFilter(opts)
	all := g.d.fakeDraft(sandbox)
	var chunks []v1.PolicyChunk
	for _, c := range all {
		if status == "" || c.Status == status {
			chunks = append(chunks, c)
		}
	}
	version := uint64(0)
	if len(all) > 0 {
		version = 1
	}
	return &v1.DraftPolicy{Chunks: chunks, DraftVersion: version}, nil
}

func (g fakeGateway) GetStatus(_ context.Context, _, sandbox string, opts ...v1.GetStatusOption) (*v1.PolicyStatusResult, error) {
	g.d.gatewayCall("GetStatus")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	cfg := types.ApplyGetStatusOptions(opts)
	return g.d.fakeStatus(sandbox, int(cfg.Version()))
}

// ApproveDraftChunk merges the chunk's rule (AddRule under its rule name),
// pending or rejected, as the gateway does.
func (g fakeGateway) ApproveDraftChunk(_ context.Context, _, sandbox, id, _ string) (*v1.ApproveResult, error) {
	g.d.gatewayCall("ApproveDraftChunk")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	c, err := g.chunk(sandbox, id)
	if err != nil {
		return nil, err
	}
	if c.Status != "pending" && c.Status != "rejected" {
		return nil, fmt.Errorf("chunk status is '%s', expected 'pending' or 'rejected'", c.Status)
	}
	rule := *c.ProposedRule
	rule.Name = c.RuleName
	if err := g.d.fakeMerge(sandbox, approvalOps(c.RuleName, rule)); err != nil {
		// As the gateway answers (the chunk's evaluation failed).
		return nil, fmt.Errorf("proposal is not applicable: candidate invalid: %w", err)
	}
	c.Status, c.DecidedAt, c.LastSeen, c.RejectionReason = "approved", time.Now(), time.Now(), ""
	return &v1.ApproveResult{PolicyVersion: uint32(len(g.d.policies[sandbox]))}, nil
}

func (g fakeGateway) RejectDraftChunk(_ context.Context, _, sandbox, id, reason string) error {
	g.d.gatewayCall("RejectDraftChunk")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	c, err := g.chunk(sandbox, id)
	if err != nil {
		return err
	}
	if c.Status == "approved" {
		if err := g.d.fakeMerge(sandbox, []v1.PolicyMergeOperation{{RemoveBinary: &v1.RemoveNetworkBinary{RuleName: c.RuleName, BinaryPath: c.Binary}}}); err != nil {
			return err
		}
	}
	c.Status, c.DecidedAt, c.LastSeen, c.RejectionReason = "rejected", time.Now(), time.Now(), reason
	return nil
}

func (g fakeGateway) EditDraftChunk(_ context.Context, _, sandbox, id string, rule *v1.NetworkPolicyRule) error {
	g.d.gatewayCall("EditDraftChunk")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	c, err := g.chunk(sandbox, id)
	if err != nil {
		return err
	}
	if c.Status != "pending" {
		return fmt.Errorf("chunk status is '%s', expected 'pending'", c.Status)
	}
	// As the gateway's evaluation: a rule name the policy does not have.
	revs := g.d.fakePolicy(sandbox)
	composed := g.d.fakeCompose(sandbox, revs[len(revs)-1].rules)
	c.RuleName = freeRuleName(composed, c.RuleName)
	c.ProposedRule, c.ReviewToken = rule, c.ReviewToken+"-edited"
	return nil
}

// UndoDraftChunk takes the chunk's program out of its rule and puts it back
// to pending, as the gateway does.
func (g fakeGateway) UndoDraftChunk(_ context.Context, _, sandbox, id string) (*v1.UndoResult, error) {
	g.d.gatewayCall("UndoDraftChunk")
	g.d.mu.Lock()
	defer g.d.mu.Unlock()
	c, err := g.chunk(sandbox, id)
	if err != nil {
		return nil, err
	}
	if c.Status != "approved" {
		return nil, fmt.Errorf("chunk status is '%s', expected 'approved'", c.Status)
	}
	if err := g.d.fakeMerge(sandbox, []v1.PolicyMergeOperation{{RemoveBinary: &v1.RemoveNetworkBinary{RuleName: c.RuleName, BinaryPath: c.Binary}}}); err != nil {
		return nil, err
	}
	c.Status, c.DecidedAt, c.RejectionReason = "pending", time.Time{}, ""
	return &v1.UndoResult{PolicyVersion: uint32(len(g.d.policies[sandbox]))}, nil
}

// fakeConfig is the effective policy (GetSandboxConfig): the latest
// revision with the attached providers' rules, composed as OpenShell does.
func (d *Fake) fakeConfig(_ context.Context, w Sandbox) (*v1.SandboxConfig, error) {
	d.gatewayCall("GetSandboxConfig")
	d.mu.Lock()
	defer d.mu.Unlock()
	revs := d.fakePolicy(w.Name)
	rules := d.fakeCompose(w.Name, revs[len(revs)-1].rules)
	return &v1.SandboxConfig{PolicyVersion: uint32(len(revs)), Policy: &v1.SandboxPolicy{NetworkPolicies: rules}}, nil
}

// openShell runs OpenShell's policy code against the fake's gateway, one
// for the fake as for the gateway (it keeps the decided proposals).
func (d *Fake) openShell() *OpenShell {
	d.shellOnce.Do(func() { d.shell = d.newOpenShell() })
	return d.shell
}

func (d *Fake) newOpenShell() *OpenShell {
	return &OpenShell{pol: fakeGateway{d: d}, sandboxConfig: d.fakeConfig, providerList: d.ListProviders,
		logs: func(context.Context, Sandbox) ([]v1.LogLine, error) { d.gatewayCall("GetLogs"); return fakeLogs(), nil },
		updateConfig: func(_ context.Context, u *v1.ConfigUpdate) (*v1.ConfigUpdateResult, error) {
			d.gatewayCall("UpdateConfig")
			d.mu.Lock()
			defer d.mu.Unlock()
			if u.Policy != nil {
				// A whole policy: its rules replace the latest revision's.
				rules := cloneRules(u.Policy.NetworkPolicies)
				if err := fakeValidate(d.fakeCompose(u.Name, rules)); err != nil {
					return nil, err
				}
				if err := d.fakeBindings(u.Name, rules); err != nil {
					return nil, err
				}
				d.policies[u.Name] = append(d.fakePolicy(u.Name), fakeRevision{at: time.Now(), rules: rules, provenance: maps.Clone(u.Annotations)})
				return &v1.ConfigUpdateResult{Version: uint32(len(d.policies[u.Name]))}, nil
			}
			if err := d.fakeMerge(u.Name, u.MergeOperations); err != nil {
				return nil, err
			}
			return &v1.ConfigUpdateResult{Version: uint32(len(d.policies[u.Name]))}, nil
		}}
}

// created says whether the fake sandbox exists in "OpenShell".
func (d *Fake) created(w Sandbox) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.started[w.Name] || d.stopped[w.Name]
}

func (d *Fake) Proposals(ctx context.Context, w Sandbox) (ProposalView, error) {
	// As OpenShell: a sandbox has a policy once it is made, running or not.
	if !d.created(w) {
		return ProposalView{Pending: []PendingProposal{}, Rejected: []RejectedProposal{}, Unlinked: []UnlinkedProposal{}}, errors.New("サンドボックスがありません")
	}
	return d.openShell().Proposals(ctx, w)
}

func (d *Fake) PendingProposals(ctx context.Context, w Sandbox) ([]PendingProposal, error) {
	if !d.created(w) {
		return nil, errors.New("サンドボックスがありません")
	}
	return d.openShell().PendingProposals(ctx, w)
}

func (d *Fake) DecideProposal(ctx context.Context, w Sandbox, action, id, digest string, scope *ProposalScope) (PolicyChange, error) {
	return d.openShell().DecideProposal(ctx, w, action, id, digest, scope)
}

func (d *Fake) UndoProposal(ctx context.Context, w Sandbox, id string, restore bool) (PolicyChange, error) {
	return d.openShell().UndoProposal(ctx, w, id, restore)
}

// Exists says whether the fake sandbox was created.
func (d *Fake) Exists(_ context.Context, w Sandbox) (bool, error) { return d.created(w), nil }

// ApplyPolicyOps merges as the gateway does (a recreate's carried rules).
func (d *Fake) ApplyPolicyOps(ctx context.Context, w Sandbox, ops []v1.PolicyMergeOperation) error {
	_, err := d.openShell().applyOps(ctx, w, ops)
	return err
}

// PolicyLayers is the fake sandbox's latest revision and effective
// policy (none before it starts).
func (d *Fake) PolicyLayers(ctx context.Context, w Sandbox) (map[string]v1.NetworkPolicyRule, map[string]v1.NetworkPolicyRule, error) {
	if !d.created(w) {
		return nil, nil, nil
	}
	return d.openShell().PolicyLayers(ctx, w)
}

func (d *Fake) ProposedRule(ctx context.Context, w Sandbox, id string) (*v1.NetworkPolicyRule, string, error) {
	return d.openShell().ProposedRule(ctx, w, id)
}

func (d *Fake) LatestPolicy(ctx context.Context, w Sandbox) (*v1.PolicyStatusResult, error) {
	if !d.created(w) {
		return nil, errors.New("サンドボックスがありません")
	}
	return d.openShell().LatestPolicy(ctx, w)
}

func (d *Fake) PolicyStore(ctx context.Context, w Sandbox) (PolicyStoreState, error) {
	if !d.created(w) {
		return PolicyStoreState{}, errors.New("サンドボックスがありません")
	}
	return d.openShell().PolicyStore(ctx, w)
}

func (d *Fake) ImportPolicy(ctx context.Context, w Sandbox) (PolicyStoreState, error) {
	return d.openShell().ImportPolicy(ctx, w)
}

func (d *Fake) OverwritePolicy(ctx context.Context, w Sandbox) (uint32, error) {
	return d.openShell().OverwritePolicy(ctx, w)
}

func (d *Fake) PolicyCommits(ctx context.Context, w Sandbox, limit int) ([]PolicyCommit, error) {
	return d.openShell().PolicyCommits(ctx, w, limit)
}

func (d *Fake) PolicyCommitDiff(ctx context.Context, w Sandbox, sha string) (PolicyCommit, string, error) {
	return d.openShell().PolicyCommitDiff(ctx, w, sha)
}

func (d *Fake) RevertPolicyCommit(ctx context.Context, w Sandbox, sha string) (uint32, error) {
	return d.openShell().RevertPolicyCommit(ctx, w, sha)
}

func (d *Fake) ForgetPolicyStore(name string) error {
	return d.openShell().ForgetPolicyStore(name)
}

func (d *Fake) ReplacePolicyRules(ctx context.Context, w Sandbox, version uint32, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error) {
	return d.openShell().ReplacePolicyRules(ctx, w, version, rules)
}

// ApplyPolicyChange merges as the gateway does.
func (d *Fake) ApplyPolicyChange(ctx context.Context, w Sandbox, ops []v1.PolicyMergeOperation) (uint32, error) {
	return d.openShell().ApplyPolicyChange(ctx, w, ops)
}

func (d *Fake) AddProviderRules(ctx context.Context, w Sandbox, provider string, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error) {
	return d.openShell().AddProviderRules(ctx, w, provider, rules)
}

func (d *Fake) RemoveProvider(ctx context.Context, w Sandbox, provider string) (PolicyChange, error) {
	return d.openShell().RemoveProvider(ctx, w, provider)
}

func (d *Fake) SetBinding(ctx context.Context, w Sandbox, b PolicyBinding) (PolicyChange, error) {
	return d.openShell().SetBinding(ctx, w, b)
}

// fakePreset is the fake's owner preset (a folder of its own each run).
const fakePreset = `# An owner's preset (the fake's): one repository of an in-house git
# server that takes no credential.
id: mirror
label: 社内のミラーに書き込む
note: git.example.com の 1 つのリポジトリへの git push
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
    binaries: [/usr/bin/git]
`

// seedFakePresets gives the fake a folder of the owner's presets.
func seedFakePresets() {
	dir, err := os.MkdirTemp("", "iwato-fake-presets-")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "mirror.yaml"), []byte(fakePreset), 0o644)
	setUserPresetDir(dir)
}
