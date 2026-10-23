package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	sbv1 "github.com/NVIDIA/OpenShell/sdk/go/proto/sandboxv1"
)

// Iwato predicts OpenShell's endpoint ambiguity check
// (endpoint_ambiguity.go) before it asks the gateway: it applies the change
// to the sandbox's latest revision as OpenShell's merge does (simulateMerge),
// adds the providers' rules (as GetSandboxConfig composes them), and looks
// for pairs of endpoints the change made ambiguous. The gateway stays the
// judge: its refusal, when Iwato missed one, is parsed too
// (translateGatewayError). The page explains both in its language from
// the structured pairs (web/refusals.js); the Japanese text is the
// fallback.

// profileRule is the rule OpenShell composes for a provider of profile m
// under key (openshell-providers profiles.rs network_policy_rule).
func profileRule(m *pb.ProviderProfile, key string) v1.NetworkPolicyRule {
	rule := v1.NetworkPolicyRule{Name: key}
	for _, b := range m.GetBinaries() {
		rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b.GetPath()})
	}
	for _, e := range m.GetEndpoints() {
		rule.Endpoints = append(rule.Endpoints, profileEndpoint(e))
	}
	return rule
}

func profileEndpoint(e *sbv1.NetworkEndpoint) v1.PolicyNetworkEndpoint {
	x := v1.PolicyNetworkEndpoint{Host: e.GetHost(), Port: e.GetPort(), Ports: slices.Clone(e.GetPorts()), Path: e.GetPath(), Protocol: e.GetProtocol(),
		TLS: v1.NetworkTLSMode(e.GetTls()), Enforcement: v1.NetworkEnforcementMode(e.GetEnforcement()), Access: v1.NetworkAccessPreset(e.GetAccess()),
		AllowedIPs: slices.Clone(e.GetAllowedIps()), AllowEncodedSlash: e.GetAllowEncodedSlash(), PersistedQueries: e.GetPersistedQueries(),
		GraphqlMaxBodyBytes: e.GetGraphqlMaxBodyBytes(), WebsocketCredentialRewrite: e.GetWebsocketCredentialRewrite(),
		RequestBodyCredentialRewrite: e.GetRequestBodyCredentialRewrite(), AllowUninspectedCredentials: e.GetAllowUninspectedCredentials(),
		CredentialSigning: e.GetCredentialSigning(), SigningService: e.GetSigningService(), SigningRegion: e.GetSigningRegion(), JSONRPCMaxBodyBytes: e.GetJsonRpcMaxBodyBytes()}
	for _, r := range e.GetRules() {
		if a := r.GetAllow(); a != nil {
			x.Rules = append(x.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: a.GetMethod(), Path: a.GetPath(), Query: queryMatchers(a.GetQuery()), OperationType: a.GetOperationType(), OperationName: a.GetOperationName(), Fields: slices.Clone(a.GetFields()), Params: queryMatchers(a.GetParams())}})
		}
	}
	for _, d := range e.GetDenyRules() {
		x.DenyRules = append(x.DenyRules, v1.L7DenyRule{Method: d.GetMethod(), Path: d.GetPath(), Query: queryMatchers(d.GetQuery()), OperationType: d.GetOperationType(), OperationName: d.GetOperationName(), Fields: slices.Clone(d.GetFields())})
	}
	if len(e.GetGraphqlPersistedQueries()) > 0 {
		x.GraphqlPersistedQueries = map[string]v1.GraphqlOperation{}
		for k, op := range e.GetGraphqlPersistedQueries() {
			x.GraphqlPersistedQueries[k] = v1.GraphqlOperation{OperationType: op.GetOperationType(), OperationName: op.GetOperationName()}
		}
	}
	if m := e.GetMcp(); m != nil {
		x.Mcp = &types.McpOptions{StrictToolNames: m.StrictToolNames}
	}
	if b := e.GetCredentialBinding(); b != nil {
		x.CredentialBinding = &types.NetworkCredentialBinding{Provider: b.GetProvider()}
	}
	return x
}

func queryMatchers(in map[string]*sbv1.L7QueryMatcher) map[string]v1.L7QueryMatcher {
	if len(in) == 0 {
		return nil
	}
	out := map[string]v1.L7QueryMatcher{}
	for k, m := range in {
		out[k] = v1.L7QueryMatcher{Glob: m.GetGlob(), Any: slices.Clone(m.GetAny())}
	}
	return out
}

// composeProviderRule adds a provider's rule to rules as OpenShell does
// (compose.rs: _provider_NAME, then _2, _3 when taken) and returns its key.
func composeProviderRule(rules map[string]v1.NetworkPolicyRule, provider string, m *pb.ProviderProfile) string {
	key := providerRuleName(provider)
	for n := 2; ; n++ {
		if _, taken := rules[key]; !taken {
			break
		}
		key = providerRuleName(provider) + "_" + strconv.Itoa(n)
	}
	rules[key] = profileRule(m, key)
	return key
}

func cloneRule(r v1.NetworkPolicyRule) v1.NetworkPolicyRule {
	r.Binaries = slices.Clone(r.Binaries)
	r.Endpoints = slices.Clone(r.Endpoints)
	for i := range r.Endpoints {
		e := &r.Endpoints[i]
		e.Ports, e.Rules, e.DenyRules, e.AllowedIPs = slices.Clone(e.Ports), slices.Clone(e.Rules), slices.Clone(e.DenyRules), slices.Clone(e.AllowedIPs)
	}
	return r
}

// simulateMerge applies merge operations to a revision's rules as
// OpenShell's merge_policy does (openshell-policy merge.rs: add_rule,
// merge_rules, merge_endpoint, remove_endpoint) as far as the endpoints'
// metadata go: an AddRule into an existing rule (or, unless its endpoints
// came from the advisor, into the first rule by name, not a provider's,
// with the same host, path and a shared port) keeps the existing
// endpoint's protocol, enforcement and tls, and joins the rest. OpenShell
// folds into that other rule only when no program would gain an endpoint
// it did not declare (ensure_authorization_inheritance_is_declared), else
// it keeps the rule under its own name; Iwato folds only rules for the
// same programs.
func simulateMerge(base map[string]v1.NetworkPolicyRule, ops []v1.PolicyMergeOperation) (map[string]v1.NetworkPolicyRule, error) {
	rules := map[string]v1.NetworkPolicyRule{}
	for k, r := range base {
		rules[k] = cloneRule(r)
	}
	for _, op := range ops {
		switch {
		case op.AddRule != nil:
			name, in := op.AddRule.RuleName, cloneRule(op.AddRule.Rule)
			if in.Name == "" {
				in.Name = name
			}
			target, exists := name, false
			if _, ok := rules[name]; ok {
				exists = true
			} else if !slices.ContainsFunc(in.Endpoints, func(e v1.PolicyNetworkEndpoint) bool { return e.AdvisorProposed }) {
				for _, k := range slices.Sorted(maps.Keys(rules)) {
					if !isProviderRule(k) && rulesShareEndpoint(rules[k], in) && sameBinaries(rules[k], in) {
						target, exists = k, true
						break
					}
				}
			}
			if !exists {
				rules[name] = in
				continue
			}
			rules[target] = mergeRule(rules[target], in)
		case op.RemoveRule != nil:
			delete(rules, op.RemoveRule.RuleName)
		case op.RemoveBinary != nil:
			x := op.RemoveBinary
			r, ok := rules[x.RuleName]
			if !ok {
				continue
			}
			if len(r.Binaries) == 0 {
				return nil, fmt.Errorf("cannot remove binary '%s' from rule '%s': the rule allows any binary", x.BinaryPath, x.RuleName)
			}
			before := len(r.Binaries)
			r.Binaries = slices.DeleteFunc(r.Binaries, func(b v1.PolicyNetworkBinary) bool { return b.Path == x.BinaryPath })
			if before != len(r.Binaries) && len(r.Binaries) == 0 {
				delete(rules, x.RuleName)
				continue
			}
			rules[x.RuleName] = r
		case op.RemoveEndpoint != nil:
			x := op.RemoveEndpoint
			r, ok := rules[x.RuleName]
			if !ok {
				continue
			}
			var kept []v1.PolicyNetworkEndpoint
			for _, e := range r.Endpoints {
				if strings.EqualFold(e.Host, x.Host) && slices.Contains(effectivePorts(e), x.Port) {
					ports := slices.DeleteFunc(effectivePorts(e), func(p uint32) bool { return p == x.Port })
					if len(ports) == 0 {
						continue
					}
					e.Port, e.Ports = ports[0], ports
				}
				kept = append(kept, e)
			}
			if len(kept) == 0 {
				delete(rules, x.RuleName)
				continue
			}
			r.Endpoints = kept
			rules[x.RuleName] = r
		case op.AddAllowRules != nil:
			t := op.AddAllowRules.Target
			r, ok := rules[t.RuleName]
			i := slices.IndexFunc(r.Endpoints, func(e v1.PolicyNetworkEndpoint) bool {
				return strings.EqualFold(e.Host, t.Host) && slices.Equal(effectivePorts(e), slices.Sorted(slices.Values(t.Ports)))
			})
			if !ok || i < 0 {
				return nil, fmt.Errorf("L7 target not found: rule '%s' %s:%s", t.RuleName, t.Host, joinPorts(t.Ports))
			}
			// An access preset turns into its rules first
			// (merge.rs expand_existing_access).
			e := &r.Endpoints[i]
			if name := accessNames[e.Access]; name != "" {
				for _, m := range accessMethods[name] {
					e.Rules = appendUniqueAllow(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: "**"}})
				}
				e.Access = v1.NetworkAccessPresetUnspecified
			}
			for _, x := range op.AddAllowRules.Rules {
				e.Rules = appendUniqueAllow(e.Rules, x)
			}
			rules[t.RuleName] = r
		}
	}
	return rules, nil
}

// appendUniqueAllow adds an allow rule unless the same method and path is
// there (merge.rs append_unique_l7_rules).
func appendUniqueAllow(rules []v1.L7Rule, x v1.L7Rule) []v1.L7Rule {
	if x.Allow != nil && slices.ContainsFunc(rules, func(r v1.L7Rule) bool {
		return r.Allow != nil && r.Allow.Method == x.Allow.Method && r.Allow.Path == x.Allow.Path
	}) {
		return rules
	}
	return append(rules, x)
}

func endpointsOverlapForMerge(a, b v1.PolicyNetworkEndpoint) bool {
	return strings.EqualFold(a.Host, b.Host) && a.Path == b.Path && len(overlappingPorts(a, b)) > 0
}

func rulesShareEndpoint(existing, in v1.NetworkPolicyRule) bool {
	for _, x := range in.Endpoints {
		for _, e := range existing.Endpoints {
			if endpointsOverlapForMerge(e, x) {
				return true
			}
		}
	}
	return false
}

func mergeRule(r, in v1.NetworkPolicyRule) v1.NetworkPolicyRule {
	for _, x := range in.Endpoints {
		i := slices.IndexFunc(r.Endpoints, func(e v1.PolicyNetworkEndpoint) bool { return endpointsOverlapForMerge(e, x) })
		if i < 0 {
			r.Endpoints = append(r.Endpoints, x)
			continue
		}
		r.Endpoints[i] = mergeEndpoint(r.Endpoints[i], x)
	}
	switch {
	case len(r.Binaries) == 0:
	case len(in.Binaries) == 0:
		r.Binaries = nil
	default:
		for _, b := range in.Binaries {
			if !slices.Contains(r.Binaries, b) {
				r.Binaries = append(r.Binaries, b)
			}
		}
	}
	return r
}

// mergeEndpoint is merge_endpoint: the existing endpoint's settings stay,
// unset ones are taken; ports, rules and allowed_ips join; flags or.
func mergeEndpoint(e, x v1.PolicyNetworkEndpoint) v1.PolicyNetworkEndpoint {
	if e.Host == "" {
		e.Host = x.Host
	}
	if e.Path == "" {
		e.Path = x.Path
	}
	ports := effectivePorts(e)
	for _, p := range effectivePorts(x) {
		if !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	slices.Sort(ports)
	if len(ports) > 0 {
		e.Port, e.Ports = ports[0], ports
	}
	if e.Protocol == "" {
		e.Protocol = x.Protocol
	}
	if e.Enforcement == v1.NetworkEnforcementModeUnspecified {
		e.Enforcement = x.Enforcement
	}
	if e.TLS == v1.NetworkTLSModeUnspecified {
		e.TLS = x.TLS
	}
	if len(x.Rules) > 0 {
		e.Rules = append(e.Rules, x.Rules...)
	} else if x.Access != v1.NetworkAccessPresetUnspecified && len(e.Rules) == 0 && e.Access == v1.NetworkAccessPresetUnspecified {
		e.Access = x.Access
	}
	e.DenyRules = append(e.DenyRules, x.DenyRules...)
	for _, ip := range x.AllowedIPs {
		if !slices.Contains(e.AllowedIPs, ip) {
			e.AllowedIPs = append(e.AllowedIPs, ip)
		}
	}
	e.AllowEncodedSlash = e.AllowEncodedSlash || x.AllowEncodedSlash
	e.WebsocketCredentialRewrite = e.WebsocketCredentialRewrite || x.WebsocketCredentialRewrite
	e.RequestBodyCredentialRewrite = e.RequestBodyCredentialRewrite || x.RequestBodyCredentialRewrite
	e.AllowUninspectedCredentials = e.AllowUninspectedCredentials || x.AllowUninspectedCredentials
	e.AdvisorProposed = e.AdvisorProposed && x.AdvisorProposed
	return e
}

// withProviderRules is a revision's rules with the providers' rules of the
// effective policy (the _provider_* ones), as the gateway composes them.
func withProviderRules(base, effective map[string]v1.NetworkPolicyRule) map[string]v1.NetworkPolicyRule {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]v1.NetworkPolicyRule{}
	}
	for k, r := range effective {
		if isProviderRule(k) {
			out[k] = r
		}
	}
	return out
}

// newAmbiguities are after's ambiguities that before does not have.
func newAmbiguities(before, after map[string]v1.NetworkPolicyRule) []EndpointAmbiguity {
	x, _ := indexAmbiguities(context.Background(), before)
	out, _ := x.newIn(context.Background(), after)
	return out
}

// predictAmbiguities: the ambiguities that ops would bring into the
// effective policy (base: the latest revision; effective: with the
// providers' rules).
func predictAmbiguities(base, effective map[string]v1.NetworkPolicyRule, ops []v1.PolicyMergeOperation) ([]EndpointAmbiguity, error) {
	merged, err := simulateMerge(base, ops)
	if err != nil {
		return nil, err
	}
	return newAmbiguities(withProviderRules(base, effective), withProviderRules(merged, effective)), nil
}

// approvalOps are the operations an approval makes: the chunk's rule added
// under its name, its endpoints the advisor's (OpenShell marks them so).
func approvalOps(name string, rule v1.NetworkPolicyRule) []v1.PolicyMergeOperation {
	rule = cloneRule(rule)
	rule.Name = name
	for i := range rule.Endpoints {
		rule.Endpoints[i].AdvisorProposed = true
	}
	return []v1.PolicyMergeOperation{{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: rule}}}
}

// approveChoices are the approve dialog's choices as scopes (the methods
// and paths do not matter to the check, nor do the programs).
var approveChoices = []struct {
	Name  string
	Label string
	Scope *ProposalScope
}{
	{"read", "読み取りだけ", &ProposalScope{ReadOnly: true}},
	{"rules", "メソッドとパスを指定", &ProposalScope{Rules: []ProposalRule{{Method: "*", Path: "/**"}}}},
	{"all", "提案どおり", nil},
	{"audit", "監査", &ProposalScope{Audit: true}},
}

// proposalFit says, per approve choice, which ambiguities approving the
// proposed rule with it would bring. before indexes
// withProviderRules(base, effective).
func proposalFit(ctx context.Context, before *ambiguityIndex, base, effective map[string]v1.NetworkPolicyRule, name string, rule v1.NetworkPolicyRule) (map[string][]EndpointAmbiguity, error) {
	out := map[string][]EndpointAmbiguity{}
	for _, c := range approveChoices {
		r := rule
		if c.Scope != nil {
			r = c.Scope.apply(cloneRule(rule))
		}
		merged, err := simulateMerge(base, approvalOps(name, r))
		if err != nil {
			continue
		}
		a, err := before.newIn(ctx, withProviderRules(merged, effective))
		if err != nil {
			return nil, err
		}
		if len(a) > 0 {
			out[c.Name] = a
		}
	}
	return out, nil
}

// AmbiguityView is an ambiguity for the owner: the endpoint already in the
// policy (Rule, maybe a provider's) and the one the change brings (With),
// what they disagree on (Diffs; Details is the Japanese text of each) and
// OpenShell's own words (Raw). For an attach, Host, EndpointPorts,
// Binaries and Fix say how the existing endpoint can be fixed: "enforce"
// when switching it from audit to enforce (keeping its rules) is enough,
// else only removing it.
type AmbiguityView struct {
	Rule          string          `json:"rule"`
	Endpoint      string          `json:"endpoint"`
	Provider      *RuleProvider   `json:"provider,omitempty"`
	With          string          `json:"with"`
	WithEndpoint  string          `json:"withEndpoint"`
	WithProvider  *RuleProvider   `json:"withProvider,omitempty"`
	Ports         []uint32        `json:"ports"`
	Fields        []string        `json:"fields"`
	Diffs         []AmbiguityDiff `json:"diffs"`
	Details       []string        `json:"details"`
	Raw           string          `json:"raw"`
	Host          string          `json:"host"`
	EndpointPorts []uint32        `json:"endpointPorts"`
	Binaries      []string        `json:"binaries"`
	Audited       bool            `json:"audited"`
	Fix           string          `json:"fix,omitempty"`
	// Access and Allows are what the existing endpoint lets through.
	Access string         `json:"access,omitempty"`
	Allows []ProposalRule `json:"allows,omitempty"`
	// want says what the changed endpoint has to match.
	want []string
}

// AmbiguityDiff is one setting two endpoints disagree on: Field as
// OpenShell names it, and the values of the Rule side and the With side
// as tokens (fieldValue) the page puts in words.
type AmbiguityDiff struct {
	Field     string `json:"field"`
	Value     string `json:"value"`
	WithValue string `json:"withValue"`
}

// detail is the diff in Japanese.
func (d AmbiguityDiff) detail(rule, with string) string {
	return fieldLabel(d.Field) + ": " + rule + " は " + valueLabel(d.Field, d.Value) + "、" + with + " は " + valueLabel(d.Field, d.WithValue)
}

// ambiguityViews turn ambiguities into views: changed names the rules the
// change touched (the With side), providers the providers' rules.
func ambiguityViews(list []EndpointAmbiguity, changed []string, rules map[string]v1.NetworkPolicyRule, providers map[string]RuleProvider) []AmbiguityView {
	out := []AmbiguityView{}
	for _, a := range list {
		rule, ep, sel := a.LeftRule, a.left, a.LeftSelector
		with, wep, wsel := a.RightRule, a.right, a.RightSelector
		if slices.Contains(changed, rule) && !slices.Contains(changed, with) {
			rule, ep, sel, with, wep, wsel = with, wep, wsel, rule, ep, sel
		}
		v := AmbiguityView{Rule: rule, Endpoint: sel, With: with, WithEndpoint: wsel, Ports: a.Ports, Fields: a.Fields(), Raw: a.String(),
			Host: ep.Host, EndpointPorts: effectivePorts(ep), Binaries: []string{}, Diffs: []AmbiguityDiff{}, Details: []string{}}
		if p, ok := providers[rule]; ok {
			v.Provider = &p
		}
		if p, ok := providers[with]; ok {
			v.WithProvider = &p
		}
		for _, b := range rules[rule].Binaries {
			v.Binaries = append(v.Binaries, b.Path)
		}
		v.Access = accessNames[ep.Access]
		v.Allows = proposalRules(v1.PolicyChunk{ProposedRule: &v1.NetworkPolicyRule{Endpoints: []v1.PolicyNetworkEndpoint{ep}}})
		v.Audited = ep.Protocol != "" && normalizedEnforcement(ep.Enforcement) == int32(v1.NetworkEnforcementModeAudit)
		// Audit → enforce is safe (it only stops more) and enough when
		// the other side enforces and nothing else differs.
		if slices.Equal(v.Fields, []string{"enforcement"}) && v.Audited && !isProviderRule(rule) {
			v.Fix = "enforce"
		}
		for _, f := range v.Fields {
			d := AmbiguityDiff{Field: f, Value: fieldValue(f, ep), WithValue: fieldValue(f, wep)}
			v.Diffs = append(v.Diffs, d)
			v.Details = append(v.Details, d.detail(rule, with))
			v.want = append(v.want, fieldLabel(f)+"を "+valueLabel(f, d.Value))
		}
		out = append(out, v)
	}
	return out
}

func fieldLabel(f string) string {
	switch f {
	case "enforcement":
		return "検査のモード"
	case "protocol":
		return "検査の種類 (protocol)"
	case "tls":
		return "TLS の扱い"
	case "allowed_ips":
		return "解決先 IP の限定 (allowed_ips)"
	case "transparent_tcp_eligible":
		return "protocol: tcp の指定"
	case "credential_binding.provider":
		return "値を付けるプロバイダー"
	}
	return f
}

// fieldValue is an endpoint's value of a conflicting field as a token:
// enforcement enforce or audit, tls skip or default, protocol its name,
// allowed_ips the addresses, transparent_tcp_eligible tcp, and
// credential_binding.provider the provider; "" is none (or, for other
// fields, a value not shown).
func fieldValue(f string, e v1.PolicyNetworkEndpoint) string {
	switch f {
	case "enforcement":
		if normalizedEnforcement(e.Enforcement) == int32(v1.NetworkEnforcementModeEnforce) {
			return "enforce"
		}
		return "audit"
	case "protocol":
		return normalizedRequestProtocol(e.Protocol)
	case "tls":
		if e.TLS == v1.NetworkTLSModeSkip {
			return "skip"
		}
		return "default"
	case "allowed_ips":
		return strings.Join(normalizedStrings(e.AllowedIPs), ", ")
	case "transparent_tcp_eligible":
		if isExplicitTCP(e.Protocol) {
			return "tcp"
		}
		return ""
	case "credential_binding.provider":
		return bindingProvider(e)
	}
	return ""
}

// valueLabel is a fieldValue token in Japanese.
func valueLabel(f, v string) string {
	switch {
	case f == "enforcement" && v == "enforce":
		return "遮断 (enforce)"
	case f == "enforcement" && v == "audit":
		return "監査 (audit)"
	case f == "tls" && v == "skip":
		return "tls: skip (中を見ない)"
	case f == "tls" && v == "default":
		return "既定 (中を見る)"
	case v != "":
		return v
	case f == "protocol":
		return "検査なし"
	case f == "allowed_ips" || f == "transparent_tcp_eligible":
		return "指定なし"
	case f == "credential_binding.provider":
		return "なし"
	}
	return "別の値"
}

// ruleOwner names a rule for the owner: a provider's with the provider.
func ruleOwner(rule string, p *RuleProvider) string {
	if p != nil {
		label := p.Name
		if p.Label != "" {
			label += " (" + p.Label + ")"
		}
		return "プロバイダー " + label + " の規則 " + rule
	}
	return "規則 " + rule
}

// ambiguityMessage explains ambiguities: lead says what is refused, fix
// what to do.
func ambiguityMessage(lead string, vs []AmbiguityView, fix string) string {
	var parts []string
	for _, v := range vs {
		parts = append(parts, v.Endpoint+" で "+ruleOwner(v.Rule, v.Provider)+" と "+ruleOwner(v.With, v.WithProvider)+" の設定が食い違います ("+strings.Join(v.Details, "、")+")")
	}
	msg := lead + ": " + strings.Join(parts, "。") + "。OpenShell は、同じ宛先 (ホスト・ポート・パス) に当たる規則どうしで、検査のしかたがそろっていることを求めます (network endpoint ambiguity)。"
	if fix != "" {
		msg += fix
	}
	return msg
}

// gatewayAmbiguity is one line of OpenShell's refusal.
var gatewayAmbiguity = regexp.MustCompile(`network policies '([^']*)' endpoint\[(\d+)\] \((.*?)\) and '([^']*)' endpoint\[(\d+)\] \((.*?)\) overlap on port\(s\) ([\d,]+) with conflicting metadata: `)

// GatewayAmbiguity is a pair of OpenShell's refusal: the rules (with their
// providers), the endpoint of the first and what they disagree on.
type GatewayAmbiguity struct {
	Rule         string          `json:"rule"`
	Provider     *RuleProvider   `json:"provider,omitempty"`
	With         string          `json:"with"`
	WithProvider *RuleProvider   `json:"withProvider,omitempty"`
	Endpoint     string          `json:"endpoint"`
	Diffs        []AmbiguityDiff `json:"diffs"`
}

// gatewayRefusal is OpenShell's endpoint ambiguity refusal, read: the
// pairs (none when the message could not be read) and the Japanese text.
type gatewayRefusal struct {
	text  string
	pairs []GatewayAmbiguity
}

// translateGatewayError reads OpenShell's endpoint ambiguity refusal (from
// any operation: the newlines may have become "; "), naming the rules; ok
// is false for other errors.
func translateGatewayError(msg string, providers map[string]RuleProvider) (gatewayRefusal, bool) {
	if !strings.Contains(msg, "network endpoint ambiguity validation failed") {
		return gatewayRefusal{}, false
	}
	idx := gatewayAmbiguity.FindAllStringSubmatchIndex(msg, -1)
	if len(idx) == 0 {
		return gatewayRefusal{text: "OpenShell が拒否しました: 同じ宛先に当たる規則どうしで、検査のしかたが食い違います (network endpoint ambiguity)。承認一覧で同じホストの規則を確かめ、検査のモード (遮断・監査) や種類をそろえるか、どちらかを外してください。", pairs: []GatewayAmbiguity{}}, true
	}
	var parts []string
	var pairs []GatewayAmbiguity
	for i, m := range idx {
		end := len(msg)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		conflicts := strings.TrimRight(strings.TrimSpace(msg[m[1]:end]), ";")
		g := GatewayAmbiguity{Rule: msg[m[2]:m[3]], With: msg[m[8]:m[9]], Endpoint: msg[m[6]:m[7]], Diffs: []AmbiguityDiff{}}
		if p, ok := providers[g.Rule]; ok {
			g.Provider = &p
		}
		if p, ok := providers[g.With]; ok {
			g.WithProvider = &p
		}
		var details []string
		for _, c := range strings.Split(conflicts, "; ") {
			field, values, _ := strings.Cut(strings.TrimSpace(c), "=")
			a, b, _ := strings.Cut(values, " vs ")
			d := AmbiguityDiff{Field: field, Value: gatewayValue(field, a), WithValue: gatewayValue(field, b)}
			g.Diffs = append(g.Diffs, d)
			details = append(details, d.detail(g.Rule, g.With))
		}
		pairs = append(pairs, g)
		parts = append(parts, g.Endpoint+" で "+ruleOwner(g.Rule, g.Provider)+" と "+ruleOwner(g.With, g.WithProvider)+" の設定が食い違います ("+strings.Join(details, "、")+")")
	}
	return gatewayRefusal{text: "OpenShell が拒否しました: " + strings.Join(parts, "。") + "。同じ宛先 (ホスト・ポート・パス) に当たる規則どうしで、検査のしかたがそろっている必要があります。承認一覧で、プロバイダーではない方の規則の宛先をもう一方に合わせる (監査なら「遮断に切り替え」) か、外してください。", pairs: pairs}, true
}

// gatewayValue reads a value of OpenShell's message (Rust's Debug) as a
// fieldValue token.
func gatewayValue(field, v string) string {
	v = strings.TrimSpace(v)
	switch field {
	case "enforcement":
		switch v {
		case "1":
			return "enforce"
		case "2":
			return "audit"
		}
	case "tls":
		switch v {
		case "1":
			return "skip"
		case "0":
			return "default"
		}
	case "transparent_tcp_eligible":
		if v == "true" {
			return "tcp"
		}
		return ""
	}
	return strings.Trim(v, `"`)
}

// policyLayersRuntime reads a sandbox's latest revision's rules and its
// effective rules (with the providers'; nil when the gateway cannot
// compose them). OpenShell and Fake implement it.
type policyLayersRuntime interface {
	PolicyLayers(context.Context, Sandbox) (base, effective map[string]v1.NetworkPolicyRule, err error)
}

// PolicyLayers reads the latest revision (none when OpenShell has no such
// sandbox) and the effective policy.
func (o *OpenShell) PolicyLayers(ctx context.Context, w Sandbox) (base, effective map[string]v1.NetworkPolicyRule, err error) {
	st, err := o.latestPolicy(ctx, w)
	if notFound(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if st.Revision.Policy != nil {
		base = st.Revision.Policy.NetworkPolicies
	}
	if cfg, err := o.effectivePolicy(ctx, w); err == nil && cfg.Policy != nil {
		effective = cfg.Policy.NetworkPolicies
	}
	return base, effective, nil
}

// providerProfile is the profile of a provider: Iwato's, or the gateway's.
func (s *Server) providerProfile(ctx context.Context, p ProviderInfo) *pb.ProviderProfile {
	if prof, ok := profileByID(p.Profile); ok {
		return prof.proto()
	}
	if r, ok := s.rt.(providerProfileReader); ok {
		if m, err := r.ProviderProfile(ctx, p.Profile); err == nil {
			return m
		}
	}
	return nil
}

// policyLayers reads the sandbox's revision and effective rules; when the
// gateway cannot compose the latter, Iwato composes the attached
// providers' rules from their profiles. ok is false without a policy
// runtime.
func (s *Server) policyLayers(ctx context.Context, sb Sandbox) (base, effective map[string]v1.NetworkPolicyRule, ok bool, err error) {
	rt, ok := s.rt.(policyLayersRuntime)
	if !ok {
		return nil, nil, false, nil
	}
	base, effective, err = rt.PolicyLayers(ctx, sb)
	if err != nil {
		return nil, nil, true, err
	}
	if effective == nil {
		effective = maps.Clone(base)
		if effective == nil {
			effective = map[string]v1.NetworkPolicyRule{}
		}
		if prt, ok := s.rt.(providerRuntime); ok {
			list, attached, err := s.providerState(ctx, prt)
			if err != nil {
				return nil, nil, true, err
			}
			for _, name := range attached[sb.Name] {
				i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
				if i < 0 {
					continue
				}
				if m := s.providerProfile(ctx, list[i]); m != nil {
					composeProviderRule(effective, name, m)
				}
			}
		}
	}
	return base, effective, true, nil
}

// ruleProviders names the provider behind each _provider_* rule of the
// sandbox: its attached providers, as OpenShell names their rules (in
// their order; a name taken gets _2, _3).
func (s *Server) ruleProviders(ctx context.Context, sb Sandbox) map[string]RuleProvider {
	byRule := map[string]RuleProvider{}
	rt, ok := s.rt.(providerRuntime)
	if !ok {
		return byRule
	}
	list, attached, err := s.providerState(ctx, rt)
	if err != nil {
		return byRule
	}
	for _, name := range attached[sb.Name] {
		i := slices.IndexFunc(list, func(x ProviderInfo) bool { return x.Name == name })
		if i < 0 {
			continue
		}
		key := providerRuleName(name)
		for n := 2; ; n++ {
			if _, taken := byRule[key]; !taken {
				break
			}
			key = providerRuleName(name) + "_" + strconv.Itoa(n)
		}
		byRule[key] = providerLabel(list[i])
	}
	return byRule
}

func providerLabel(p ProviderInfo) RuleProvider {
	rp := RuleProvider{Name: p.Name, Profile: p.Profile}
	if prof, ok := profileByID(p.Profile); ok {
		rp.Label = prof.Label
	}
	return rp
}

// errAmbiguity is a change OpenShell would refuse, for a 409: the
// ambiguities and the credential conflicts it would bring, with a code and
// params the page explains them by (web/refusals.js) and the Japanese text.
type errAmbiguity struct {
	code      string
	params    map[string]any
	msg       string
	views     []AmbiguityView
	conflicts []CredentialConflict
}

func (e *errAmbiguity) Error() string { return e.msg }

// errRefused makes an errAmbiguity. Its codes are error codes
// (TestErrorCodesHaveStrings): ambiguous_* name what is refused.
func errRefused(code string, params map[string]any, msg string, views []AmbiguityView, conflicts []CredentialConflict) *errAmbiguity {
	return &errAmbiguity{code: code, params: params, msg: msg, views: views, conflicts: conflicts}
}

// raw is OpenShell's own words for the ambiguities (English, for the
// console).
func (e *errAmbiguity) raw() string {
	var out []string
	for _, v := range e.views {
		out = append(out, v.Raw)
	}
	return strings.Join(out, "; ")
}

// checkOps refuses merge operations that OpenShell would refuse as
// ambiguous: with what conflicts, and what to do. code (ambiguous_*) and
// params say what is refused; lead says it in Japanese.
func (s *Server) checkOps(ctx context.Context, sb Sandbox, ops []v1.PolicyMergeOperation, code string, params map[string]any, lead string) error {
	base, effective, ok, err := s.policyLayers(ctx, sb)
	if !ok {
		return nil
	}
	if err != nil {
		return errCode("policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
	}
	list, err := predictAmbiguities(base, effective, ops)
	if err != nil || len(list) == 0 {
		return nil // a merge error is the gateway's to report
	}
	changed := []string{opsRule(ops)}
	merged, _ := simulateMerge(base, ops)
	views := ambiguityViews(list, changed, withProviderRules(merged, effective), s.ruleProviders(ctx, sb))
	return &errAmbiguity{code: code, params: params, msg: ambiguityMessage(lead, views, editFix(views)), views: views}
}

// editFix says how to get past an ambiguity a hand edit would bring.
func editFix(vs []AmbiguityView) string {
	v := vs[0]
	if v.Provider != nil {
		return "プロバイダーの規則は変えられないので、" + strings.Join(v.want, "、") + " にそろえてください。プロバイダーのプログラムには、プロバイダーの規則が既に許可しています。"
	}
	return strings.Join(v.want, "、") + " にそろえるか、規則 " + v.Rule + " の宛先を外してから変えてください。"
}

// failCheck answers a change Iwato refused: 409, with the ambiguities
// and conflicts when that is why.
func failCheck(w http.ResponseWriter, e error) {
	var a *errAmbiguity
	if errors.As(e, &a) {
		body := map[string]any{"error": a.msg, "code": a.code}
		if len(a.params) > 0 {
			body["params"] = a.params
		}
		if a.views != nil {
			body["ambiguities"] = a.views
		}
		if a.conflicts != nil {
			body["conflicts"] = a.conflicts
		}
		jsonResponse(w, 409, body)
		return
	}
	failErr(w, 409, e)
}

// failGateway answers the gateway's refusal: the ambiguity check read
// (gateway_ambiguous, the pairs for the page, the gateway's words in raw).
func (s *Server) failGateway(ctx context.Context, w http.ResponseWriter, sb Sandbox, e error) {
	// Iwato's own refusals (the proposal is gone, the rule is not there)
	// carry codes.
	if c := (*codedError)(nil); errors.As(e, &c) && c.msg == e.Error() {
		failErr(w, 409, e)
		return
	}
	msg := safeCLIError(e)
	if j, ok := translateGatewayError(msg, s.ruleProviders(ctx, sb)); ok {
		failCodeWith(w, 409, "gateway_ambiguous", nil, j.text, map[string]any{"gatewayAmbiguities": j.pairs, "raw": msg})
		return
	}
	fail(w, 409, msg)
}

// choiceLabel is an approve choice as the dialog names it.
func choiceLabel(name string) string {
	for _, c := range approveChoices {
		if c.Name == name {
			return c.Label
		}
	}
	return name
}

// blockedChoices says which approve choices OpenShell would refuse and why.
func blockedChoices(fit map[string][]EndpointAmbiguity, rule string, providers map[string]RuleProvider) map[string]BlockedChoice {
	if len(fit) == 0 {
		return nil
	}
	out := map[string]BlockedChoice{}
	for choice, list := range fit {
		views := ambiguityViews(list, []string{rule}, nil, providers)
		var parts []string
		for _, v := range views {
			parts = append(parts, v.Endpoint+" には "+ruleOwner(v.Rule, v.Provider)+" があり、設定が食い違います ("+strings.Join(v.Details, "、")+")")
		}
		out[choice] = BlockedChoice{Reason: "「" + choiceLabel(choice) + "」は選べません: " + strings.Join(parts, "。") + "。OpenShell は同じ宛先の規則どうしで検査のしかたがそろっていることを求めます。", Ambiguities: views}
	}
	return out
}

// checkApproval refuses an approval (the proposed rule, narrowed by scope)
// that OpenShell would refuse as ambiguous, saying which choices work.
func (s *Server) checkApproval(ctx context.Context, sb Sandbox, name string, rule v1.NetworkPolicyRule, scope *ProposalScope) error {
	base, effective, ok, err := s.policyLayers(ctx, sb)
	if !ok {
		return nil
	}
	if err != nil {
		return errCode("policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
	}
	r := rule
	if scope != nil {
		r = scope.apply(cloneRule(rule))
	}
	list, err := predictAmbiguities(base, effective, approvalOps(name, r))
	if err != nil || len(list) == 0 {
		return nil
	}
	views := ambiguityViews(list, []string{name}, nil, s.ruleProviders(ctx, sb))
	before, err := indexAmbiguities(ctx, withProviderRules(base, effective))
	if err != nil {
		return err
	}
	fit, err := proposalFit(ctx, before, base, effective, name, rule)
	if err != nil {
		return err
	}
	ok2, labels := []string{}, []string{}
	for _, c := range approveChoices {
		if _, blocked := fit[c.Name]; !blocked {
			ok2 = append(ok2, c.Name)
			labels = append(labels, "「"+c.Label+"」")
		}
	}
	fix := "どの選び方でも承認できません。食い違う規則の宛先を外す (プロバイダーの規則ならプロバイダーを外す) か、この提案を却下してください。"
	if len(ok2) > 0 {
		fix = strings.Join(labels, "") + "なら承認できます。"
	}
	return errRefused("ambiguous_approve", map[string]any{"choices": ok2}, ambiguityMessage("この承認は OpenShell に拒否されます", views, fix), views, nil)
}

// checkEdit refuses a hand edit that OpenShell would refuse as ambiguous.
func (s *Server) checkEdit(ctx context.Context, sb Sandbox, edit PolicyEdit) error {
	switch edit.Op {
	case "add", "enforce", "remove-allow", "add-allow":
	default:
		return nil // removing never makes endpoints ambiguous
	}
	base, _, ok, err := s.policyLayers(ctx, sb)
	if !ok || err != nil {
		return nil
	}
	ops, err := edit.operations(base)
	if err != nil {
		return nil // the runtime says why
	}
	if edit.Op == "enforce" {
		return s.checkOps(ctx, sb, ops, "ambiguous_enforce", nil, "遮断への切り替えは OpenShell に拒否されます")
	}
	return s.checkOps(ctx, sb, ops, "ambiguous_edit", nil, "この変更は OpenShell に拒否されます")
}

// attachAmbiguities are the endpoints of the sandbox's policy that the
// provider's rule would make ambiguous (OpenShell refuses the attach:
// grpc/policy.rs validate_candidate_provider_attachments). None when it
// is attached already.
func (s *Server) attachAmbiguities(ctx context.Context, sb Sandbox, p ProviderInfo) ([]AmbiguityView, error) {
	_, effective, ok, err := s.policyLayers(ctx, sb)
	if !ok || err != nil {
		return nil, err
	}
	m := s.providerProfile(ctx, p)
	if m == nil {
		return nil, nil
	}
	providers := s.ruleProviders(ctx, sb)
	for _, rp := range providers {
		if rp.Name == p.Name {
			return nil, nil
		}
	}
	candidate := maps.Clone(effective)
	if candidate == nil {
		candidate = map[string]v1.NetworkPolicyRule{}
	}
	key := composeProviderRule(candidate, p.Name, m)
	providers[key] = providerLabel(p)
	list := newAmbiguities(effective, candidate)
	if len(list) == 0 {
		return nil, nil
	}
	return ambiguityViews(list, []string{key}, candidate, providers), nil
}

// providersAmbiguities are the ambiguities among providers chosen
// together (a new sandbox's).
func (s *Server) providersAmbiguities(ctx context.Context, list []ProviderInfo) []AmbiguityView {
	rules := map[string]v1.NetworkPolicyRule{}
	providers := map[string]RuleProvider{}
	for _, p := range list {
		if m := s.providerProfile(ctx, p); m != nil {
			providers[composeProviderRule(rules, p.Name, m)] = providerLabel(p)
		}
	}
	if a := findEndpointAmbiguities(rules); len(a) > 0 {
		return ambiguityViews(a, nil, rules, providers)
	}
	return nil
}

// attachRefusal explains why a provider cannot be attached yet.
func attachRefusal(provider string, cs []CredentialConflict, vs []AmbiguityView) *errAmbiguity {
	var parts []string
	if len(cs) > 0 {
		parts = append(parts, conflictMessage(cs, true))
	}
	if len(vs) > 0 {
		fix := "付ける前に、この宛先を規則から外してください。"
		if !slices.ContainsFunc(vs, func(v AmbiguityView) bool { return v.Fix != "enforce" }) {
			fix = "付ける前に、この宛先を遮断に切り替える (監査で記録した要求から選ぶか、今の許可のまま) か、規則から外してください。"
		}
		parts = append(parts, ambiguityMessage(provider+" を付けると OpenShell に拒否されます", vs, fix))
	}
	if cs == nil {
		cs = []CredentialConflict{}
	}
	if vs == nil {
		vs = []AmbiguityView{}
	}
	return errRefused("provider_attach_refused", map[string]any{"provider": provider}, strings.Join(parts, " "), vs, cs)
}

// sameBinaries: the two rules are for the same programs (any, or the same
// set).
func sameBinaries(a, b v1.NetworkPolicyRule) bool {
	set := func(r v1.NetworkPolicyRule) []string {
		var out []string
		for _, x := range r.Binaries {
			if !slices.Contains(out, x.Path) {
				out = append(out, x.Path)
			}
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(set(a), set(b))
}
