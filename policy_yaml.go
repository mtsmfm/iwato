package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	sb "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
	"go.yaml.in/yaml/v3"
)

// The policy tab's YAML editor: a sandbox's network policy as YAML.
//
//	GET  /api/sandboxes/NAME/policy/yaml   {version, yaml, fixed}
//	POST /api/sandboxes/NAME/policy/yaml   {version, yaml, apply}
//
// Only network_policies change on a running sandbox: OpenShell's
// UpdateConfig takes a whole policy for a sandbox, but refuses one whose
// landlock or process differ from the creation policy, or whose
// filesystem drops a path (openshell-server grpc/validation.rs
// validate_static_fields_unchanged; added paths would only apply at the
// next start, as Landlock is fixed once the workload runs). So the YAML
// holds network_policies alone, and fixed shows the rest read-only; the
// providers' rules (_provider_*) are no revision's, and are not in it.
//
// Saving commits the rules and applies them as a whole
// (openshell_policy_store.go), so the rules are exactly the YAML's: the
// merge operations of an edit would fold a new rule into an existing one
// with the same endpoint (merge.rs add_rule). A save names the version it
// was loaded at and is refused once the policy moved on (an approval, an
// edit elsewhere). expected_resource_version does not see revisions made
// outside Iwato, so Iwato compares the versions itself, and serialises its
// own saves.
//
// Before saving, a rule added or changed is checked as an edit is: its
// shape, the endpoint ambiguity it would bring with the other rules and
// the providers' (OpenShell's refusal, predicted), and the programs of a
// rule bound to a credential (unchanged). Rules left as they were are sent as the
// gateway gave them. Without apply the answer is the difference (added,
// removed and changed rules), which the page shows before applying.
//
// Iwato's records stay as an edit leaves them: Sandbox.Presets keeps the
// presets (taking one out later removes only its rules that are still as
// it made them), and approved proposals stay approved (one whose rule is
// gone is listed to put back).

// policyYAMLLimit is the largest YAML a save takes.
const policyYAMLLimit = 256 << 10

// policyYAMLDoc is the editable part of a policy, as YAML.
type policyYAMLDoc struct {
	NetworkPolicies map[string]presetRuleDoc `yaml:"network_policies"`
}

// policyFixedDoc is the part of a policy fixed at creation.
type policyFixedDoc struct {
	Filesystem *struct {
		IncludeWorkdir bool     `yaml:"include_workdir"`
		ReadOnly       []string `yaml:"read_only,omitempty"`
		ReadWrite      []string `yaml:"read_write,omitempty"`
	} `yaml:"filesystem_policy,omitempty"`
	Landlock *struct {
		Compatibility string `yaml:"compatibility"`
	} `yaml:"landlock,omitempty"`
	Process *struct {
		RunAsUser  string `yaml:"run_as_user,omitempty"`
		RunAsGroup string `yaml:"run_as_group,omitempty"`
	} `yaml:"process,omitempty"`
	Middlewares map[string]any `yaml:"network_middlewares,omitempty"`
}

// PolicyYAMLRule is a rule in the difference a save makes.
type PolicyYAMLRule struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
}

// PolicyYAMLDiff is what a save changes.
type PolicyYAMLDiff struct {
	Added   []PolicyYAMLRule `json:"added"`
	Removed []PolicyYAMLRule `json:"removed"`
	Changed []PolicyYAMLRule `json:"changed"`
}

func (d PolicyYAMLDiff) empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Changed) == 0
}

// touched names the rules added and changed.
func (d PolicyYAMLDiff) touched() []string {
	var out []string
	for _, r := range append(slices.Clone(d.Added), d.Changed...) {
		out = append(out, r.Name)
	}
	return out
}

func encodeYAML(v any) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// policyRulesYAML renders a revision's rules as the editor shows them.
func policyRulesYAML(rules map[string]v1.NetworkPolicyRule) (string, error) {
	doc := policyYAMLDoc{NetworkPolicies: map[string]presetRuleDoc{}}
	for name, r := range rules {
		d := presetRuleDoc{Endpoints: []endpointDoc{}, Binaries: []binaryDoc{}}
		for _, e := range r.Endpoints {
			d.Endpoints = append(d.Endpoints, policyEndpointDoc(e))
		}
		for _, b := range r.Binaries {
			d.Binaries = append(d.Binaries, binaryDoc{Path: b.Path})
		}
		doc.NetworkPolicies[name] = d
	}
	return encodeYAML(doc)
}

// policyFixedYAML renders the parts of a policy fixed at creation ("" for
// none).
func policyFixedYAML(p *v1.SandboxPolicy) (string, error) {
	if p == nil {
		return "", nil
	}
	var d policyFixedDoc
	if f := p.Filesystem; f != nil {
		d.Filesystem = &struct {
			IncludeWorkdir bool     `yaml:"include_workdir"`
			ReadOnly       []string `yaml:"read_only,omitempty"`
			ReadWrite      []string `yaml:"read_write,omitempty"`
		}{f.IncludeWorkdir, f.ReadOnly, f.ReadWrite}
	}
	if l := p.Landlock; l != nil {
		d.Landlock = &struct {
			Compatibility string `yaml:"compatibility"`
		}{l.Compatibility}
	}
	if pr := p.Process; pr != nil {
		d.Process = &struct {
			RunAsUser  string `yaml:"run_as_user,omitempty"`
			RunAsGroup string `yaml:"run_as_group,omitempty"`
		}{pr.RunAsUser, pr.RunAsGroup}
	}
	if len(p.NetworkMiddlewares) > 0 {
		d.Middlewares = map[string]any{}
		for k, m := range p.NetworkMiddlewares {
			d.Middlewares[k] = prunedJSON(m)
		}
	}
	if d.Filesystem == nil && d.Landlock == nil && d.Process == nil && d.Middlewares == nil {
		return "", nil
	}
	return encodeYAML(d)
}

// policyEndpointDoc is an endpoint in the YAML format of presets and
// profiles (endpointDoc). The gateway's markers (advisor_proposed,
// provider_credentialed) and a provider's binding are not in it.
func policyEndpointDoc(e v1.PolicyNetworkEndpoint) endpointDoc {
	d := endpointDoc{Host: e.Host, Path: e.Path, Protocol: e.Protocol,
		TLS:         enumName(tlsModes, sb.NetworkTlsMode(e.TLS), int32(e.TLS)),
		Access:      enumName(accessPresets, sb.NetworkAccessPreset(e.Access), int32(e.Access)),
		Enforcement: enumName(enforcementModes, sb.NetworkEnforcementMode(e.Enforcement), int32(e.Enforcement)),
		AllowedIPs:  e.AllowedIPs, AllowEncodedSlash: e.AllowEncodedSlash, WebsocketCredentialRewrite: e.WebsocketCredentialRewrite,
		RequestBodyCredentialRewrite: e.RequestBodyCredentialRewrite, AllowUninspectedCredentials: e.AllowUninspectedCredentials,
		PersistedQueries: e.PersistedQueries, GraphqlMaxBodyBytes: e.GraphqlMaxBodyBytes, JSONRPCMaxBodyBytes: e.JSONRPCMaxBodyBytes,
		CredentialSigning: e.CredentialSigning, SigningService: e.SigningService, SigningRegion: e.SigningRegion}
	if ports := endpointPorts(e); len(ports) == 1 {
		d.Port = ports[0]
	} else {
		d.Ports = ports
	}
	matchers := func(in map[string]v1.L7QueryMatcher) map[string]matcherDoc {
		if len(in) == 0 {
			return nil
		}
		out := map[string]matcherDoc{}
		for k, m := range in {
			out[k] = matcherDoc{Glob: m.Glob, Any: m.Any}
		}
		return out
	}
	if len(e.Rules) > 0 {
		rules := []ruleDoc{}
		for _, r := range e.Rules {
			rd := ruleDoc{}
			if a := r.Allow; a != nil {
				rd.Allow = &matchDoc{Method: a.Method, Path: a.Path, Command: a.Command, Query: matchers(a.Query), OperationType: a.OperationType,
					OperationName: a.OperationName, Fields: a.Fields, Params: matchers(a.Params)}
			}
			rules = append(rules, rd)
		}
		d.Rules = &rules
	}
	if len(e.DenyRules) > 0 {
		deny := []matchDoc{}
		for _, a := range e.DenyRules {
			deny = append(deny, matchDoc{Method: a.Method, Path: a.Path, Command: a.Command, Query: matchers(a.Query), OperationType: a.OperationType,
				OperationName: a.OperationName, Fields: a.Fields, Params: matchers(a.Params)})
		}
		d.DenyRules = &deny
	}
	if len(e.GraphqlPersistedQueries) > 0 {
		d.GraphqlPersistedQueries = map[string]graphqlOpDoc{}
		for k, op := range e.GraphqlPersistedQueries {
			d.GraphqlPersistedQueries[k] = graphqlOpDoc{OperationType: op.OperationType, OperationName: op.OperationName, Fields: op.Fields}
		}
	}
	if m := e.Mcp; m != nil && (len(m.Versions) > 0 || m.StrictToolNames != nil || m.AllowAllKnownMcpMethods != nil) {
		d.MCP = &mcpDoc{Versions: m.Versions, StrictToolNames: m.StrictToolNames, AllowAllKnownMCPMethods: m.AllowAllKnownMcpMethods}
	}
	return d
}

// policyEndpoint is the endpoint the proto (endpointDoc.proto) says, in
// the SDK's types, with every field the format has.
func policyEndpoint(e *sb.NetworkEndpoint) v1.PolicyNetworkEndpoint {
	x := profileEndpoint(e)
	// profileEndpoint keeps the allow rules' request fields only.
	x.Rules = nil
	for _, r := range e.GetRules() {
		rule := v1.L7Rule{}
		if a := r.GetAllow(); a != nil {
			rule.Allow = &v1.L7Allow{Method: a.GetMethod(), Path: a.GetPath(), Command: a.GetCommand(), Query: queryMatchers(a.GetQuery()), OperationType: a.GetOperationType(),
				OperationName: a.GetOperationName(), Fields: slices.Clone(a.GetFields()), Params: queryMatchers(a.GetParams())}
		}
		x.Rules = append(x.Rules, rule)
	}
	for i, d := range e.GetDenyRules() {
		x.DenyRules[i].Command, x.DenyRules[i].Params = d.GetCommand(), queryMatchers(d.GetParams())
	}
	for k, op := range e.GetGraphqlPersistedQueries() {
		x.GraphqlPersistedQueries[k] = v1.GraphqlOperation{OperationType: op.GetOperationType(), OperationName: op.GetOperationName(), Fields: slices.Clone(op.GetFields())}
	}
	if m := e.GetMcp(); m != nil {
		x.Mcp = &types.McpOptions{Versions: slices.Clone(m.GetVersions()), StrictToolNames: m.StrictToolNames, AllowAllKnownMcpMethods: m.AllowAllKnownMcpMethods}
	}
	if len(x.Ports) == 0 && x.Port != 0 {
		x.Ports = []uint32{x.Port}
	}
	if len(x.Ports) > 0 {
		x.Port = x.Ports[0]
	}
	return x
}

// parsePolicyYAML reads the editor's YAML: one document with
// network_policies. Unknown fields are errors. The rules' contents are
// checked by checkPolicyYAMLRule (the changed ones).
func parsePolicyYAML(b []byte) (map[string]v1.NetworkPolicyRule, error) {
	bad := func(err error) error {
		return errCode("policy_yaml_invalid", map[string]any{"detail": err.Error()}, "YAML を読めません: "+err.Error())
	}
	if len(b) > policyYAMLLimit {
		return nil, errCode("policy_yaml_too_large", nil, "256 KiB を超えています")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var doc policyYAMLDoc
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, bad(errors.New("network_policies がありません"))
		}
		return nil, bad(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, bad(errors.New("YAML 文書は 1 つだけにしてください"))
	}
	if len(doc.NetworkPolicies) > 500 {
		return nil, bad(errors.New("規則は 500 個までです"))
	}
	rules := map[string]v1.NetworkPolicyRule{}
	for key, d := range doc.NetworkPolicies {
		if d.Name != "" && d.Name != key {
			return nil, bad(fmt.Errorf("規則 %s: name は書かないか、キーと同じにしてください", key))
		}
		rule := v1.NetworkPolicyRule{Name: key}
		for _, b := range d.Binaries {
			rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b.Path})
		}
		for i, e := range d.Endpoints {
			pe, err := e.proto()
			if err != nil {
				return nil, bad(fmt.Errorf("規則 %s endpoints[%d]: %w", key, i, err))
			}
			rule.Endpoints = append(rule.Endpoints, policyEndpoint(pe))
		}
		rules[key] = rule
	}
	return rules, nil
}

// checkPolicyYAMLRule checks an added or changed rule's shape, as a
// preset's rules are checked.
func checkPolicyYAMLRule(name string, r v1.NetworkPolicyRule) error {
	bad := func(msg string) error {
		return errCode("policy_yaml_rule_invalid", map[string]any{"rule": name, "detail": msg}, "規則 "+name+": "+msg)
	}
	if !ruleNamePattern.MatchString(name) || isProviderRule(name) {
		return bad("規則名は英数字と _ . - で 100 文字までにし、_provider_ で始めないでください")
	}
	if len(r.Endpoints) == 0 || len(r.Binaries) == 0 {
		return bad("endpoints と binaries が要ります (すべてのプログラムなら \"/**\")")
	}
	for _, b := range r.Binaries {
		if !strings.HasPrefix(b.Path, "/") || !printable(b.Path, 512) {
			return bad(fmt.Sprintf("プログラム %q は / で始まる絶対パスにしてください", b.Path))
		}
	}
	for _, e := range r.Endpoints {
		if !hostPattern.MatchString(e.Host) {
			return bad(fmt.Sprintf("ホスト %q は使えません", e.Host))
		}
		ports := endpointPorts(e)
		if len(ports) == 0 || len(ports) > 16 {
			return bad(e.Host + ": port を 1 個から 16 個書いてください")
		}
		for _, p := range ports {
			if p > 65535 {
				return bad(fmt.Sprintf("ポート %d は使えません", p))
			}
		}
	}
	return nil
}

// canonEndpoint compares endpoints as the editor shows them: without the
// gateway's markers and with the ports as a set.
// A credential binding is not in the YAML either: a changed rule keeps it
// on the endpoints it kept. MCP's flags are compared as written (unset
// means true to OpenShell, which prunedJSON would not tell from false).
func canonEndpoint(e v1.PolicyNetworkEndpoint) string {
	e.Ports, e.Port = effectivePorts(e), 0
	e.AdvisorProposed, e.ProviderCredentialed, e.CredentialBinding = false, false, nil
	flag := func(b *bool) string {
		if b == nil {
			return "unset"
		}
		return fmt.Sprint(*b)
	}
	mcp := ""
	if m := e.Mcp; m != nil {
		mcp = flag(m.StrictToolNames) + "/" + flag(m.AllowAllKnownMcpMethods)
	}
	b, _ := json.Marshal(prunedJSON(e))
	return string(b) + mcp
}

// sameYAMLRule: the rule reads the same in the editor (samePresetRule,
// with the endpoints compared as canonEndpoint does too).
func sameYAMLRule(a, b v1.NetworkPolicyRule) bool {
	// sameYAMLRuleJSON is slow over a whole policy.
	return reflect.DeepEqual(yamlNorm(a), yamlNorm(b)) || sameYAMLRuleJSON(a, b)
}

func sameYAMLRuleJSON(a, b v1.NetworkPolicyRule) bool {
	canon := func(r v1.NetworkPolicyRule) []string {
		var out []string
		for _, e := range r.Endpoints {
			out = append(out, canonEndpoint(e))
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	strip := func(r v1.NetworkPolicyRule) v1.NetworkPolicyRule {
		r = cloneRule(r)
		for i := range r.Endpoints {
			r.Endpoints[i].CredentialBinding = nil
		}
		return r
	}
	return samePresetRule(strip(a), strip(b)) && slices.Equal(canon(a), canon(b))
}

// yamlNorm takes out of a rule what sameYAMLRule ignores, in the same way,
// but keeps the order: rules equal after it read the same.
func yamlNorm(r v1.NetworkPolicyRule) v1.NetworkPolicyRule {
	r = cloneRule(r)
	r.Name = ""
	slices.SortFunc(r.Binaries, func(x, y v1.PolicyNetworkBinary) int { return strings.Compare(x.Path, y.Path) })
	r.Binaries = slices.CompactFunc(r.Binaries, func(x, y v1.PolicyNetworkBinary) bool { return x.Path == y.Path })
	for i := range r.Endpoints {
		e := &r.Endpoints[i]
		e.Ports, e.Port = effectivePorts(*e), 0
		e.AdvisorProposed, e.ProviderCredentialed, e.CredentialBinding = false, false, nil
	}
	return r
}

// policyYAMLPlan is the policy a save makes from the YAML's rules: the
// rules left as they were are the gateway's, as it gave them; a changed
// rule keeps the gateway's markers on the endpoints it did not change.
func policyYAMLPlan(old, edited map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, PolicyYAMLDiff) {
	out := map[string]v1.NetworkPolicyRule{}
	diff := PolicyYAMLDiff{Added: []PolicyYAMLRule{}, Removed: []PolicyYAMLRule{}, Changed: []PolicyYAMLRule{}}
	view := func(name string, r v1.NetworkPolicyRule) PolicyYAMLRule {
		v := PolicyYAMLRule{Name: name, Hosts: []string{}}
		for _, e := range r.Endpoints {
			h := e.Host + ":" + joinPorts(endpointPorts(e))
			if !slices.Contains(v.Hosts, h) {
				v.Hosts = append(v.Hosts, h)
			}
		}
		return v
	}
	for _, name := range slices.Sorted(maps.Keys(edited)) {
		r := edited[name]
		was, ok := old[name]
		switch {
		case !ok:
			diff.Added = append(diff.Added, view(name, r))
		case sameYAMLRule(was, r):
			out[name] = was
			continue
		default:
			diff.Changed = append(diff.Changed, view(name, r))
			r = cloneRule(r)
			for i, e := range r.Endpoints {
				if j := slices.IndexFunc(was.Endpoints, func(x v1.PolicyNetworkEndpoint) bool { return canonEndpoint(x) == canonEndpoint(e) }); j >= 0 {
					r.Endpoints[i].AdvisorProposed, r.Endpoints[i].CredentialBinding = was.Endpoints[j].AdvisorProposed, was.Endpoints[j].CredentialBinding
				}
			}
		}
		out[name] = r
	}
	for _, name := range slices.Sorted(maps.Keys(old)) {
		if _, ok := edited[name]; !ok {
			diff.Removed = append(diff.Removed, view(name, old[name]))
		}
	}
	return out, diff
}

// checkBoundBinaries refuses changing the programs of a rule with an
// endpoint bound to a provider's credential (credential_binding): the
// credential would go to other programs.
func checkBoundBinaries(old, rules map[string]v1.NetworkPolicyRule, names []string) error {
	paths := func(r v1.NetworkPolicyRule) []string {
		var out []string
		for _, b := range r.Binaries {
			out = append(out, b.Path)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	for _, name := range names {
		was, ok := old[name]
		if !ok || !slices.ContainsFunc(was.Endpoints, func(e v1.PolicyNetworkEndpoint) bool { return e.CredentialBinding != nil }) {
			continue
		}
		if !slices.Equal(paths(was), paths(rules[name])) {
			return errCode("policy_bound_binaries", map[string]any{"rule": name}, "規則 "+name+" にはプロバイダーの認証情報に結び付いた宛先があるので、プログラムは変えられません")
		}
	}
	return nil
}

// checkEditBinaries refuses a hand edit that changes the programs of a
// rule bound to a credential (checkBoundBinaries).
func (s *Server) checkEditBinaries(ctx context.Context, sb Sandbox, edit PolicyEdit) error {
	if edit.Op != "add" && edit.Op != "add-allow" {
		return nil
	}
	base, _, ok, err := s.policyLayers(ctx, sb)
	if !ok {
		return nil
	}
	if err != nil {
		return errCode("policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
	}
	ops, err := edit.operations(base)
	if err != nil {
		return err
	}
	merged, err := simulateMerge(base, ops)
	if err != nil {
		return err
	}
	var touched []string
	for _, name := range slices.Sorted(maps.Keys(merged)) {
		if was, ok := base[name]; !ok || !sameYAMLRule(was, merged[name]) {
			touched = append(touched, name)
		}
	}
	return checkBoundBinaries(base, merged, touched)
}

// policyYAMLRuntime reads and replaces a sandbox's network policy as a
// whole. OpenShell and Fake implement it.
type policyYAMLRuntime interface {
	// LatestPolicy is the latest revision, with its policy.
	LatestPolicy(context.Context, Sandbox) (*v1.PolicyStatusResult, error)
	// ReplacePolicyRules makes a revision of the latest one with rules as
	// its network_policies, unless the latest is no longer version.
	ReplacePolicyRules(ctx context.Context, sb Sandbox, version uint32, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error)
}

// errPolicyStale: the policy moved on since the YAML was loaded.
func errPolicyStale(loaded, now uint32) error {
	return errCode("policy_yaml_stale", map[string]any{"loaded": loaded, "version": now},
		fmt.Sprintf("読み込んだ後でポリシーが変わりました (版 %d → 版 %d)。読み込み直してください。", loaded, now))
}

func (o *OpenShell) LatestPolicy(ctx context.Context, w Sandbox) (*v1.PolicyStatusResult, error) {
	return o.latestPolicy(ctx, w)
}

func (o *OpenShell) ReplacePolicyRules(ctx context.Context, w Sandbox, version uint32, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error) {
	sync, err := o.syncStore(ctx, w)
	if err != nil {
		return PolicyChange{}, err
	}
	if now := sync.latest.Revision.Version; now != version {
		return PolicyChange{}, errPolicyStale(version, now)
	}
	v, err := o.commitPolicy(ctx, w, sync, rules, noteOf(ctx, "Edit the policy as YAML"))
	if err != nil {
		return PolicyChange{}, err
	}
	return o.withVersion(ctx, w, PolicyChange{Kind: "edit", Version: v, At: time.Now().Unix()}), nil
}

// yamlNote is a YAML save's commit message.
func yamlNote(d PolicyYAMLDiff) string {
	lines := []string{"Edit the policy as YAML", ""}
	for _, x := range []struct {
		verb  string
		rules []PolicyYAMLRule
	}{{"Add", d.Added}, {"Change", d.Changed}, {"Remove", d.Removed}} {
		for _, r := range x.rules {
			lines = append(lines, fmt.Sprintf("%s rule %s (%s)", x.verb, r.Name, strings.Join(r.Hosts, ", ")))
		}
	}
	return strings.Join(lines, "\n")
}

// handlePolicyYAML serves the editor (GET) and checks or applies a save
// (POST).
func (s *Server) handlePolicyYAML(w http.ResponseWriter, r *http.Request, parts []string) {
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, r.Method != http.MethodGet) {
		return
	}
	rt, ok := s.rt.(policyYAMLRuntime)
	if !ok {
		fail(w, 409, "policy unavailable")
		return
	}
	if r.Method == http.MethodGet {
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		st, err := rt.LatestPolicy(ctx, sb)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		var rules map[string]v1.NetworkPolicyRule
		if st.Revision.Policy != nil {
			rules = st.Revision.Policy.NetworkPolicies
		}
		doc, err := policyRulesYAML(rules)
		if err == nil {
			var fixed string
			if fixed, err = policyFixedYAML(st.Revision.Policy); err == nil {
				jsonResponse(w, 200, map[string]any{"version": st.Revision.Version, "yaml": doc, "fixed": fixed})
				return
			}
		}
		failErr(w, 500, err)
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	var body struct {
		Version uint32 `json:"version"`
		YAML    string `json:"yaml"`
		Apply   bool   `json:"apply"`
	}
	if decodeLimit(w, r, &body, policyYAMLLimit+(16<<10)) != nil || body.Version == 0 {
		fail(w, 400, "invalid policy")
		return
	}
	edited, err := parsePolicyYAML([]byte(body.YAML))
	if err != nil {
		failErr(w, 400, err)
		return
	}
	defer s.lockPolicy(sb.Name)()
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	ctx = withGatewayReads(ctx)
	st, err := rt.LatestPolicy(ctx, sb)
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	if st.Revision.Version != body.Version {
		failErr(w, 409, errPolicyStale(body.Version, st.Revision.Version))
		return
	}
	var old map[string]v1.NetworkPolicyRule
	if st.Revision.Policy != nil {
		old = st.Revision.Policy.NetworkPolicies
	}
	rules, diff := policyYAMLPlan(old, edited)
	if diff.empty() {
		failCode(w, 409, "policy_yaml_unchanged", nil, "変更がありません")
		return
	}
	touched := diff.touched()
	changed := map[string]v1.NetworkPolicyRule{}
	for _, name := range touched {
		if err := checkPolicyYAMLRule(name, rules[name]); err != nil {
			failErr(w, 400, err)
			return
		}
		if r, ok := rules[name]; ok {
			changed[name] = r
		}
	}
	if err := s.iwatoPortRefusal(changed); err != nil {
		failErr(w, 400, err)
		return
	}
	if err := checkBoundBinaries(old, rules, touched); err != nil {
		failErr(w, 409, err)
		return
	}
	// OpenShell's endpoint ambiguity, with the providers' rules.
	if _, effective, ok, err := s.policyLayers(ctx, sb); ok {
		if err != nil {
			failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
			return
		}
		after := withProviderRules(rules, effective)
		if list := newAmbiguities(withProviderRules(old, effective), after); len(list) > 0 {
			views := ambiguityViews(list, touched, after, s.ruleProviders(ctx, sb))
			failCheck(w, errRefused("ambiguous_yaml", nil, ambiguityMessage("この YAML は OpenShell に拒否されます", views, editFix(views)), views, nil))
			return
		}
	}
	if !body.Apply {
		// Rules that reach the host's loopback, for the confirmation.
		jsonResponse(w, 200, map[string]any{"diff": diff, "gateways": gatewayRules(changed)})
		return
	}
	change, err := rt.ReplacePolicyRules(withPolicyNote(ctx, "yaml", yamlNote(diff)), sb, body.Version, rules)
	if err != nil {
		s.failGateway(ctx, w, sb, err)
		return
	}
	if len(touched) > 0 {
		change.Rule = touched[0]
	}
	s.recordPolicyChange(sb.Name, change)
	resp := map[string]any{"ok": true, "version": change.Version, "diff": diff}
	// Another change (made outside Iwato) between the version check and
	// the update was replaced by this one.
	latest := change.Version
	if st, err := rt.LatestPolicy(ctx, sb); err == nil {
		latest = st.Revision.Version
	}
	if change.Version != body.Version+1 || latest != body.Version+1 {
		resp["warningCode"] = "policy_yaml_overwritten"
		resp["warning"] = fmt.Sprintf("保存の間に、ほかで変えたポリシー (版 %d から 版 %d) を上書きしたかもしれません。承認一覧と履歴を確かめてください。", body.Version, latest)
		resp["params"] = map[string]any{"loaded": body.Version, "version": latest}
	}
	jsonResponse(w, 200, resp)
}
