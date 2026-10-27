package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// CurrentPolicy is a sandbox's network policy as the gateway keeps it: its
// latest version, whether the sandbox has loaded it, and its rules. The
// rules are the effective policy (GetSandboxConfig, as `openshell sandbox
// get` shows it): the latest revision with the attached providers' rules
// (_provider_*), which no revision holds.
type CurrentPolicy struct {
	Version uint32 `json:"version"`
	// Active is the revision the sandbox has loaded (OpenShell's
	// active_version); a change is applied once it reaches the change's.
	Active    uint32           `json:"active"`
	Loaded    bool             `json:"loaded"`
	LoadError string           `json:"loadError,omitempty"`
	Rules     []PolicyRuleView `json:"rules"`
	// composeError says why the providers' rules are missing (the
	// revision's rules shown instead).
	composeError string
	// base and effective are the revision's rules and the effective
	// ones (nil when the gateway could not compose them).
	base, effective map[string]v1.NetworkPolicyRule
}

// PolicyRuleView is one named network rule: which programs may reach which
// endpoints. Source is where it came from: "provider" (an attached
// provider's, read-only), "proposal" (approved proposals: Chunks) or
// "hand".
type PolicyRuleView struct {
	Name      string         `json:"name"`
	Binaries  []string       `json:"binaries"`
	Endpoints []EndpointView `json:"endpoints"`
	Source    string         `json:"source,omitempty"`
	// Provider is the provider a _provider_* rule is of (the server
	// names it from the attached providers).
	Provider *RuleProvider `json:"provider,omitempty"`
	Chunks   []ChunkLink   `json:"chunks,omitempty"`
	// ProviderHosts are its endpoints where an attached provider's
	// credential goes (the server marks them: markProviderHosts).
	ProviderHosts []ProviderHost `json:"providerHosts,omitempty"`
}

// RuleProvider is the provider behind a _provider_* rule and its profile.
type RuleProvider struct {
	Name    string `json:"name"`
	Profile string `json:"profile"`
	Label   string `json:"label,omitempty"`
}

// EndpointView is one endpoint of a rule. Inspect is "rest" when requests
// are inspected; then Enforcement ("enforce" or "audit") and Access or
// Rules say what passes, and Deny what never does. Path limits the
// endpoint to paths under it (a provider's, such as /v2/mcp/**).
type EndpointView struct {
	Host        string         `json:"host"`
	Ports       []uint32       `json:"ports"`
	Path        string         `json:"path,omitempty"`
	Inspect     string         `json:"inspect,omitempty"`
	Enforcement string         `json:"enforcement,omitempty"`
	Access      string         `json:"access,omitempty"`
	Rules       []ProposalRule `json:"rules,omitempty"`
	Deny        []ProposalRule `json:"deny,omitempty"`
	// AllowedIPs are the addresses the host may resolve to, internal ones
	// included (allowedIPsLabel).
	AllowedIPs []string `json:"allowedIps,omitempty"`
	// Observed are the requests logged for an audited endpoint
	// (openshell_audit.go).
	Observed []ObservedRequest `json:"observed,omitempty"`
	// Denied are the requests the endpoint denied (and still would), and
	// the programs its rule refused, from the same log.
	Denied []ObservedRequest `json:"denied,omitempty"`
}

var accessNames = map[v1.NetworkAccessPreset]string{v1.NetworkAccessPresetReadOnly: "read-only", v1.NetworkAccessPresetReadWrite: "read-write", v1.NetworkAccessPresetFull: "full"}

// latestPolicy reads the sandbox's latest policy revision.
func (o *OpenShell) latestPolicy(ctx context.Context, w Sandbox, opts ...v1.GetStatusOption) (*v1.PolicyStatusResult, error) {
	read := func() (*v1.PolicyStatusResult, error) {
		pol, err := o.policy(ctx)
		if err != nil {
			return nil, err
		}
		return pol.GetStatus(ctx, openshellWorkspace, w.Name, opts...)
	}
	if len(opts) > 0 {
		return read()
	}
	return readOnce(ctx, func(r *gatewayReads) map[string]*v1.PolicyStatusResult { return r.status }, w.Name, read)
}

// effectivePolicy reads the policy the sandbox gets: GetSandboxConfig,
// which composes the latest revision with the attached providers' rules
// (openshell-server grpc/policy.rs load_sandbox_config,
// compose_effective_policy).
func (o *OpenShell) effectivePolicy(ctx context.Context, w Sandbox) (*v1.SandboxConfig, error) {
	return readOnce(ctx, func(r *gatewayReads) map[string]*v1.SandboxConfig { return r.config }, w.Name, func() (*v1.SandboxConfig, error) {
		if o.sandboxConfig != nil {
			return o.sandboxConfig(ctx, w)
		}
		sdk, _, err := o.clients(ctx)
		if err != nil {
			return nil, err
		}
		return sdk.Config().GetSandbox(ctx, openshellWorkspace, w.Name)
	})
}

// currentPolicy reads the sandbox's latest policy revision (its version and
// load state) and its effective rules. When the gateway cannot compose the
// effective policy, the revision's rules stand in, with the reason.
func (o *OpenShell) currentPolicy(ctx context.Context, w Sandbox) (*CurrentPolicy, error) {
	st, err := o.latestPolicy(ctx, w)
	if err != nil {
		return nil, err
	}
	var rules map[string]v1.NetworkPolicyRule
	if st.Revision.Policy != nil {
		rules = st.Revision.Policy.NetworkPolicies
	}
	compose := ""
	base := rules
	var effective map[string]v1.NetworkPolicyRule
	if cfg, err := o.effectivePolicy(ctx, w); err != nil {
		compose = "プロバイダーの規則を含むポリシーを読めません: " + safeCLIError(err)
	} else if cfg.Policy != nil {
		rules = cfg.Policy.NetworkPolicies
		effective = rules
	}
	p := policyView(st, rules)
	p.composeError = compose
	p.base, p.effective = base, effective
	o.attachObserved(ctx, w, p)
	return p, nil
}

// policyView is the revision's version and load state with rules.
func policyView(st *v1.PolicyStatusResult, rules map[string]v1.NetworkPolicyRule) *CurrentPolicy {
	p := &CurrentPolicy{Version: st.Revision.Version, Active: st.ActiveVersion, Loaded: st.ActiveVersion >= st.Revision.Version && st.Revision.Status != v1.PolicyLoadStatusFailed, LoadError: st.Revision.LoadError, Rules: []PolicyRuleView{}}
	for name, rule := range rules {
		r := PolicyRuleView{Name: name, Binaries: []string{}, Endpoints: []EndpointView{}, Source: "hand"}
		if isProviderRule(name) {
			r.Source = "provider"
		}
		for _, b := range rule.Binaries {
			r.Binaries = append(r.Binaries, b.Path)
		}
		for _, e := range rule.Endpoints {
			ev := EndpointView{Host: e.Host, Ports: endpointPorts(e), Path: e.Path, Inspect: e.Protocol, AllowedIPs: slices.Clone(e.AllowedIPs)}
			if e.Protocol != "" {
				ev.Enforcement = map[v1.NetworkEnforcementMode]string{v1.NetworkEnforcementModeEnforce: "enforce", v1.NetworkEnforcementModeAudit: "audit"}[e.Enforcement]
				if ev.Enforcement == "" {
					ev.Enforcement = "audit" // OpenShell's default
				}
				ev.Access = accessNames[e.Access]
				ev.Rules = proposalRules(v1.PolicyChunk{ProposedRule: &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{e}}})
				for _, d := range e.DenyRules {
					if d.Path != "" {
						ev.Deny = append(ev.Deny, ProposalRule{Method: orDefault(d.Method, "*"), Path: d.Path})
					}
				}
			}
			r.Endpoints = append(r.Endpoints, ev)
		}
		p.Rules = append(p.Rules, r)
	}
	// The sandbox's own rules first, then the providers'.
	sort.Slice(p.Rules, func(i, j int) bool {
		a, b := p.Rules[i], p.Rules[j]
		if (a.Source == "provider") != (b.Source == "provider") {
			return b.Source == "provider"
		}
		return a.Name < b.Name
	})
	return p
}

// providerRulePrefix marks the rules OpenShell composes from the attached
// providers (openshell-policy compose.rs).
const providerRulePrefix = "_provider_"

func isProviderRule(name string) bool { return strings.HasPrefix(name, providerRulePrefix) }

// providerRuleName is the rule OpenShell composes for a provider
// (compose.rs provider_rule_name; a second one of the same name gets _2).
func providerRuleName(provider string) string {
	s := strings.Trim(strings.Map(func(c rune) rune {
		switch {
		case c >= 'A' && c <= 'Z':
			return c + ('a' - 'A')
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_':
			return c
		}
		return '_'
	}, provider), "_")
	if s == "" {
		s = "unnamed"
	}
	return providerRulePrefix + s
}

func orDefault(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// endpointPorts are an endpoint's ports, sorted (OpenShell keeps the first
// in Port and all of them in Ports).
func endpointPorts(e v1.PolicyNetworkEndpoint) []uint32 {
	ports := []uint32{}
	for _, p := range append([]uint32{e.Port}, e.Ports...) {
		if p != 0 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	slices.Sort(ports)
	return ports
}

// PolicyEdit is one change to a sandbox's network policy, made as
// `openshell policy update` makes it: merge operations that the gateway
// applies to its latest revision. Op is
//
//   - "add": an endpoint for Binaries (or any binary) into rule Rule
//     (allow_HOST_PORT when empty, as the CLI names it); an existing rule
//     keeps its binaries. Access or Rules inspect the requests, Audit only
//     logs what they would block.
//   - "add-allow": methods and paths into an inspected endpoint.
//   - "remove", "remove-binary", "remove-endpoint": a rule, one of its
//     binaries, or one of its endpoints.
//   - "remove-allow": one method and path from an endpoint.
//   - "enforce": an audited endpoint to enforce, allowing only Rules (picked
//     from the requests its audit logged).
//
// The endpoint is the rule's one with Host and exactly Ports.
type PolicyEdit struct {
	Op        string         `json:"-"`
	Rule      string         `json:"rule"`
	Binaries  []string       `json:"binaries,omitempty"`
	AnyBinary bool           `json:"anyBinary,omitempty"`
	Binary    string         `json:"binary,omitempty"`
	Host      string         `json:"host,omitempty"`
	Ports     []uint32       `json:"ports,omitempty"`
	Access    string         `json:"access,omitempty"`
	Audit     bool           `json:"audit,omitempty"`
	Rules     []ProposalRule `json:"rules,omitempty"`
	// Keep (enforce) keeps the endpoint's access and rules: it only
	// switches it from audit to enforce.
	Keep bool `json:"keep,omitempty"`
	// Confirm is the token of the owner's confirmation that providers'
	// credentials go with the rule (askProviderHosts).
	Confirm string `json:"confirm,omitempty"`
}

var (
	ruleNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9*]([A-Za-z0-9*._-]{0,251}[A-Za-z0-9])?$`)
)

func printable(s string, max int) bool {
	return s != "" && len(s) <= max && strings.IndexFunc(s, func(c rune) bool { return c < ' ' || c == 0x7f }) < 0
}

// Validate checks the edit's shape; the gateway checks it against the policy.
func (e PolicyEdit) Validate() error {
	if !printable(e.Rule, 200) && (e.Op != "add" || e.Rule != "") {
		return errCode("policy_rule_missing", nil, "規則名を指定してください")
	}
	if e.Op != "add" && e.Op != "remove" && e.Op != "remove-binary" {
		if !hostPattern.MatchString(e.Host) {
			return errCode("policy_host_invalid", map[string]any{"host": fmt.Sprintf("%q", e.Host)}, fmt.Sprintf("ホスト %q は使えません", e.Host))
		}
		if len(e.Ports) == 0 || len(e.Ports) > 16 {
			return errCode("policy_ports_count", nil, "ポートを 1 個から 16 個指定してください")
		}
		for i, p := range e.Ports {
			if p == 0 || p > 65535 || slices.Contains(e.Ports[:i], p) {
				return errCode("policy_port_invalid", map[string]any{"port": p}, fmt.Sprintf("ポート %d は使えません", p))
			}
		}
	}
	switch e.Op {
	case "remove":
		return nil
	case "remove-binary":
		if !strings.HasPrefix(e.Binary, "/") || !printable(e.Binary, 512) {
			return errCode("policy_binary_missing", nil, "プログラムのパスを指定してください")
		}
		return nil
	case "remove-endpoint":
		return nil
	case "remove-allow":
		if len(e.Rules) != 1 || !printable(e.Rules[0].Method, 10) || !printable(e.Rules[0].Path, 512) {
			return errCode("policy_allow_one", nil, "外すメソッドとパスを 1 つ指定してください")
		}
		return nil
	case "enforce":
		if e.Keep {
			if len(e.Rules) > 0 {
				return errCode("policy_keep_no_rules", nil, "今の許可のまま切り替えるときは、メソッドとパスを指定しません")
			}
			return nil
		}
		if len(e.Rules) == 0 {
			return errCode("policy_rules_missing", nil, "メソッドとパスを指定してください")
		}
		return validateRules(e.Rules)
	case "add-allow":
		if len(e.Rules) == 0 {
			return errCode("policy_rules_missing", nil, "メソッドとパスを指定してください")
		}
		return validateRules(e.Rules)
	case "add":
	default:
		return errors.New("unsupported policy operation")
	}
	if e.Rule != "" && (!ruleNamePattern.MatchString(e.Rule) || strings.HasPrefix(e.Rule, "_provider_")) {
		return errCode("policy_rule_name_invalid", nil, "規則名は英数字と _ . - で 100 文字までにしてください")
	}
	if e.AnyBinary && len(e.Binaries) > 0 {
		return errCode("policy_binaries_or_any", nil, "プログラムを指定するか、すべてのプログラムにするかのどちらかです")
	}
	if len(e.Binaries) > 16 {
		return errCode("policy_binaries_too_many", nil, "プログラムは 16 個までです")
	}
	for i, b := range e.Binaries {
		if !strings.HasPrefix(b, "/") || !printable(b, 512) || slices.Contains(e.Binaries[:i], b) {
			return errCode("policy_binary_invalid", map[string]any{"binary": fmt.Sprintf("%q", b)}, fmt.Sprintf("プログラム %q は / で始まる絶対パスにしてください", b))
		}
	}
	if _, ok := map[string]bool{"": true, "read-only": true, "read-write": true, "full": true}[e.Access]; !ok {
		return errCode("policy_access_invalid", map[string]any{"access": fmt.Sprintf("%q", e.Access)}, fmt.Sprintf("アクセス %q は使えません", e.Access))
	}
	if e.Access != "" && len(e.Rules) > 0 {
		return errCode("policy_access_or_rules", nil, "アクセスの種類か、メソッドとパスのどちらかを指定してください")
	}
	if e.Audit && e.Access == "" && len(e.Rules) == 0 {
		return errCode("policy_audit_needs_inspect", nil, "監査は要求を検査するときだけ指定できます")
	}
	// The endpoint, as in the other operations.
	return PolicyEdit{Op: "remove-endpoint", Rule: "x", Host: e.Host, Ports: e.Ports}.Validate()
}

// generatedRuleName names a rule as `openshell policy update
// --add-endpoint` does.
func generatedRuleName(host string, port uint32) string {
	host = strings.Map(func(c rune) rune {
		switch {
		case c == '.' || c == '-' || c == '_':
			return '_'
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			return c
		}
		return -1
	}, host)
	return fmt.Sprintf("allow_%s_%d", host, port)
}

// operations turns the edit into merge operations on rules, the network
// policy it was made against.
func (e PolicyEdit) operations(rules map[string]v1.NetworkPolicyRule) ([]v1.PolicyMergeOperation, error) {
	name := e.Rule
	if e.Op == "add" && name == "" {
		name = generatedRuleName(e.Host, e.Ports[0])
	}
	rule, exists := rules[name]
	if !exists && e.Op != "add" {
		return nil, errCode("policy_rule_not_found", nil, "この規則はポリシーにありません")
	}
	var endpoint v1.PolicyNetworkEndpoint
	if e.Op != "add" && e.Op != "remove" && e.Op != "remove-binary" {
		ports := slices.Sorted(slices.Values(e.Ports))
		i := slices.IndexFunc(rule.Endpoints, func(x v1.PolicyNetworkEndpoint) bool {
			return strings.EqualFold(x.Host, e.Host) && slices.Equal(endpointPorts(x), ports)
		})
		if i < 0 {
			return nil, errCode("policy_endpoint_not_found", nil, "この宛先は規則にありません")
		}
		endpoint = rule.Endpoints[i]
	}
	removeEndpoint := func() []v1.PolicyMergeOperation {
		var ops []v1.PolicyMergeOperation
		for _, p := range endpointPorts(endpoint) {
			ops = append(ops, v1.PolicyMergeOperation{RemoveEndpoint: &v1.RemoveNetworkEndpoint{RuleName: name, Host: endpoint.Host, Port: p}})
		}
		return ops
	}
	switch e.Op {
	case "remove":
		return []v1.PolicyMergeOperation{{RemoveRule: &v1.RemoveNetworkRule{RuleName: name}}}, nil
	case "remove-binary":
		if !slices.ContainsFunc(rule.Binaries, func(b v1.PolicyNetworkBinary) bool { return b.Path == e.Binary }) {
			return nil, errCode("policy_binary_not_found", nil, "このプログラムは規則にありません")
		}
		return []v1.PolicyMergeOperation{{RemoveBinary: &v1.RemoveNetworkBinary{RuleName: name, BinaryPath: e.Binary}}}, nil
	case "remove-endpoint":
		return removeEndpoint(), nil
	case "add-allow":
		if endpoint.Protocol == "" {
			return nil, errCode("policy_endpoint_not_inspected", nil, "この宛先は要求を検査していません")
		}
		path := endpoint.Path
		target := &v1.L7RuleTarget{RuleName: name, Host: endpoint.Host, Ports: endpointPorts(endpoint), Path: &path, Binaries: rule.Binaries, AnyBinary: len(rule.Binaries) == 0}
		var allow []v1.L7Rule
		for _, r := range e.Rules {
			allow = append(allow, v1.L7Rule{Allow: &v1.L7Allow{Method: r.Method, Path: r.Path}})
		}
		return []v1.PolicyMergeOperation{{AddAllowRules: &v1.AddAllowRules{Target: target, Rules: allow}}}, nil
	case "enforce":
		// Taken out and put back enforcing, in one update (the rest of the
		// endpoint, allowed_ips included, stays).
		if endpoint.Protocol == "" || endpoint.Enforcement == v1.NetworkEnforcementModeEnforce {
			return nil, errCode("policy_endpoint_not_audited", nil, "この宛先は監査モードではありません")
		}
		endpoint.Enforcement = v1.NetworkEnforcementModeEnforce
		if !e.Keep {
			endpoint.Access, endpoint.Rules = v1.NetworkAccessPresetUnspecified, nil
		}
		for _, r := range e.Rules {
			endpoint.Rules = append(endpoint.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: r.Method, Path: r.Path}})
		}
		return append(removeEndpoint(), v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: v1.NetworkPolicyRule{Name: name, Binaries: rule.Binaries, Endpoints: []v1.PolicyNetworkEndpoint{endpoint}}}}), nil
	case "remove-allow":
		// OpenShell has no operation for this: the endpoint is taken out and
		// put back without the rule, in one update (the rest of the
		// endpoint, allowed_ips included, stays).
		want := e.Rules[0]
		i := slices.IndexFunc(endpoint.Rules, func(r v1.L7Rule) bool {
			return r.Allow != nil && orDefault(r.Allow.Method, "*") == want.Method && r.Allow.Path == want.Path
		})
		if i < 0 {
			return nil, errCode("policy_allow_not_found", nil, "このメソッドとパスは宛先にありません")
		}
		if len(endpoint.Rules) == 1 && endpoint.Access == v1.NetworkAccessPresetUnspecified {
			return nil, errCode("policy_allow_last", nil, "宛先に残る最後の許可です。宛先ごと外してください")
		}
		endpoint.Rules = slices.Delete(slices.Clone(endpoint.Rules), i, i+1)
		ops := removeEndpoint()
		return append(ops, v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: v1.NetworkPolicyRule{Name: name, Binaries: rule.Binaries, Endpoints: []v1.PolicyNetworkEndpoint{endpoint}}}}), nil
	}
	// add
	binaries := rule.Binaries
	if len(e.Binaries) > 0 || e.AnyBinary {
		binaries = nil
		for _, b := range e.Binaries {
			binaries = append(binaries, v1.PolicyNetworkBinary{Path: b})
		}
	} else if !exists {
		return nil, errCode("policy_binary_required", nil, "プログラムを指定してください")
	}
	endpoint = v1.PolicyNetworkEndpoint{Host: e.Host, Port: e.Ports[0], Ports: slices.Clone(e.Ports)}
	if e.Access != "" || len(e.Rules) > 0 {
		endpoint.Protocol, endpoint.Enforcement = "rest", v1.NetworkEnforcementModeEnforce
		if e.Audit {
			endpoint.Enforcement = v1.NetworkEnforcementModeAudit
		}
		for preset, s := range accessNames {
			if s == e.Access {
				endpoint.Access = preset
			}
		}
		for _, r := range e.Rules {
			endpoint.Rules = append(endpoint.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: r.Method, Path: r.Path}})
		}
	}
	return []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: v1.NetworkPolicyRule{Name: name, Binaries: binaries, Endpoints: []v1.PolicyNetworkEndpoint{endpoint}}}}}, nil
}

// PolicyChange is a revision that an approval or an edit in Iwato made,
// until the sandbox has loaded it (the supervisor picks a new revision up
// at its settings poll, about every 10 seconds). Rule names the rule it
// touched; Summary is the approved proposal's.
type PolicyChange struct {
	Version uint32 `json:"version"`
	Kind    string `json:"kind"` // approve or edit
	Rule    string `json:"rule,omitempty"`
	Host    string `json:"host,omitempty"`
	Summary string `json:"summary,omitempty"`
	At      int64  `json:"at"`
	// AppliedAt is when Iwato first saw the sandbox load it.
	AppliedAt int64 `json:"appliedAt,omitempty"`
}

// opsRule is the rule that merge operations touch.
func opsRule(ops []v1.PolicyMergeOperation) string {
	for _, op := range ops {
		switch {
		case op.AddRule != nil:
			return op.AddRule.RuleName
		case op.RemoveRule != nil:
			return op.RemoveRule.RuleName
		case op.RemoveBinary != nil:
			return op.RemoveBinary.RuleName
		case op.RemoveEndpoint != nil:
			return op.RemoveEndpoint.RuleName
		case op.AddAllowRules != nil:
			return op.AddAllowRules.Target.RuleName
		}
	}
	return ""
}

// EditPolicy changes the sandbox's network policy; the sandbox picks the
// new revision up at its next settings poll.
func (o *OpenShell) EditPolicy(ctx context.Context, w Sandbox, edit PolicyEdit) (PolicyChange, error) {
	st, err := o.latestPolicy(ctx, w)
	if err != nil {
		return PolicyChange{}, err
	}
	var rules map[string]v1.NetworkPolicyRule
	if st.Revision.Policy != nil {
		rules = st.Revision.Policy.NetworkPolicies
	}
	ops, err := edit.operations(rules)
	if err != nil {
		return PolicyChange{}, err
	}
	change := PolicyChange{Kind: "edit", Rule: opsRule(ops), Host: edit.Host, At: time.Now().Unix()}
	res, err := o.applyOps(ctx, w, ops)
	if err != nil {
		return PolicyChange{}, err
	}
	if res != nil {
		change.Version = res.Version
	}
	return o.withVersion(ctx, w, change), nil
}

// withVersion fills in the revision a change made when the gateway's answer
// did not carry it: the latest one right after.
func (o *OpenShell) withVersion(ctx context.Context, w Sandbox, c PolicyChange) PolicyChange {
	if c.Version == 0 {
		if st, err := o.latestPolicy(ctx, w); err == nil {
			c.Version = st.Revision.Version
		}
	}
	return c
}

// PolicyRevision is one stored version of a sandbox's policy. Status is
// OpenShell's: pending, loaded, failed or superseded.
type PolicyRevision struct {
	Version   uint32 `json:"version"`
	Status    string `json:"status"`
	Active    bool   `json:"active"`
	CreatedAt int64  `json:"createdAt"`
	LoadedAt  int64  `json:"loadedAt,omitempty"`
	LoadError string `json:"loadError,omitempty"`
}

// policyHistorySize is how many revisions the history shows (the first page
// of `openshell policy list`).
const policyHistorySize = 20

// PolicyHistory lists the sandbox's latest policy revisions, newest first.
func (o *OpenShell) PolicyHistory(ctx context.Context, w Sandbox) ([]PolicyRevision, error) {
	pol, err := o.policy(ctx)
	if err != nil {
		return nil, err
	}
	st, err := pol.GetStatus(ctx, openshellWorkspace, w.Name)
	if err != nil {
		return nil, err
	}
	pager, err := pol.List(openshellWorkspace, w.Name, v1.WithPageSize(policyHistorySize))
	if err != nil {
		return nil, err
	}
	page, err := pager.NextPage(ctx)
	if err != nil {
		return nil, err
	}
	list := []PolicyRevision{}
	if page == nil {
		return list, nil
	}
	for _, r := range page.Items {
		p := PolicyRevision{Version: r.Version, Status: strings.ToLower(r.Status.String()), Active: r.Version == st.ActiveVersion, CreatedAt: r.CreatedAt.Unix(), LoadError: r.LoadError}
		if !r.LoadedAt.IsZero() {
			p.LoadedAt = r.LoadedAt.Unix()
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Version > list[j].Version })
	if len(list) > policyHistorySize {
		list = list[:policyHistorySize]
	}
	return list, nil
}

// PolicyAt reads one stored revision of the sandbox's policy.
func (o *OpenShell) PolicyAt(ctx context.Context, w Sandbox, version uint32) (*CurrentPolicy, error) {
	st, err := o.latestPolicy(ctx, w, v1.WithVersion(version))
	if err != nil {
		return nil, err
	}
	var rules map[string]v1.NetworkPolicyRule
	if st.Revision.Policy != nil {
		rules = st.Revision.Policy.NetworkPolicies
	}
	return policyView(st, rules), nil
}

// PolicyRules is the sandbox's latest network policy (nil when OpenShell
// has no such sandbox yet).
func (o *OpenShell) PolicyRules(ctx context.Context, w Sandbox) (map[string]v1.NetworkPolicyRule, error) {
	st, err := o.latestPolicy(ctx, w)
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Revision.Policy == nil {
		return nil, nil
	}
	return st.Revision.Policy.NetworkPolicies, nil
}
