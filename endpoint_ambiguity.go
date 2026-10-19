package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// OpenShell (v0.1.4) refuses a policy in which two endpoints can match the
// same connection or request but disagree on how to handle it
// (crates/openshell-policy/src/ambiguity.rs, find_endpoint_ambiguities;
// the gateway runs it on every candidate effective policy, the providers'
// rules included: crates/openshell-server/src/grpc/policy.rs
// validate_endpoint_ambiguities and validate_candidate_effective_policy,
// on approvals, merges, provider attachments and profile updates):
//
//	network endpoint ambiguity validation failed:
//	network policies '_provider_github' endpoint[0] (api.github.com:443) and
//	'allow_api_github_com_443' endpoint[0] (api.github.com:443) overlap on
//	port(s) 443 with conflicting metadata: enforcement=1 vs 2
//
// Two endpoints of any rules (the programs do not matter) overlap when
// their ports intersect and their host globs can match the same host. Then
// the connection's metadata must agree (explicit protocol tcp or not, tls
// skip or not, allowed_ips as a set); and when both endpoints inspect
// requests (a protocol other than tcp, allowed_ips, tls, or a credential
// binding) and their paths can match the same request with the same
// specificity (the non-* characters of the path: a more specific path
// wins at runtime), so must the request's (protocol, enforcement,
// allow_encoded_slash, the credential rewrites and signing, the credential
// binding's provider, and per protocol the body limits, MCP's
// strict_tool_names and websocket's GraphQL classification). Access
// presets and rules may differ. Iwato runs the same check before it asks
// the gateway, to name the rules and offer a way out.

// EndpointAmbiguity is a pair of endpoints OpenShell would refuse, as its
// EndpointAmbiguity: the rules (as named in the policy), the endpoints'
// indexes and selectors (host:ports[path]), the ports they share and the
// conflicts ("enforcement=1 vs 2").
type EndpointAmbiguity struct {
	LeftRule      string   `json:"leftRule"`
	LeftIndex     int      `json:"leftIndex"`
	LeftSelector  string   `json:"leftSelector"`
	RightRule     string   `json:"rightRule"`
	RightIndex    int      `json:"rightIndex"`
	RightSelector string   `json:"rightSelector"`
	Ports         []uint32 `json:"ports"`
	Conflicts     []string `json:"conflicts"`
	// left and right are the endpoints (not sent).
	left, right v1.PolicyNetworkEndpoint
}

// String is OpenShell's message for it.
func (a EndpointAmbiguity) String() string {
	return fmt.Sprintf("network policies '%s' endpoint[%d] (%s) and '%s' endpoint[%d] (%s) overlap on port(s) %s with conflicting metadata: %s",
		a.LeftRule, a.LeftIndex, a.LeftSelector, a.RightRule, a.RightIndex, a.RightSelector, joinPorts(a.Ports), strings.Join(a.Conflicts, "; "))
}

// Fields are the names of the conflicting metadata.
func (a EndpointAmbiguity) Fields() []string {
	var out []string
	for _, c := range a.Conflicts {
		name, _, _ := strings.Cut(c, "=")
		out = append(out, name)
	}
	return out
}

// involves says whether the pair includes rule.
func (a EndpointAmbiguity) involves(rule string) bool {
	return a.LeftRule == rule || a.RightRule == rule
}

// key identifies the pair regardless of the endpoints' order.
func (a EndpointAmbiguity) key() string {
	l, r := a.LeftRule+"|"+a.LeftSelector, a.RightRule+"|"+a.RightSelector
	if r < l {
		l, r = r, l
	}
	return l + "|" + r + "|" + strings.Join(a.Conflicts, ";")
}

// findEndpointAmbiguities is OpenShell's find_endpoint_ambiguities over a
// policy's rules (the rules in name order, for a stable answer).
func findEndpointAmbiguities(rules map[string]v1.NetworkPolicyRule) []EndpointAmbiguity {
	type ref struct {
		rule  string
		index int
		e     v1.PolicyNetworkEndpoint
	}
	var eps []ref
	for _, key := range slices.Sorted(maps.Keys(rules)) {
		r := rules[key]
		name := r.Name
		if name == "" {
			name = key
		}
		for i, e := range r.Endpoints {
			eps = append(eps, ref{name, i, e})
		}
	}
	var out []EndpointAmbiguity
	for i := range eps {
		for j := i + 1; j < len(eps); j++ {
			l, r := eps[i], eps[j]
			ports := overlappingPorts(l.e, r.e)
			if len(ports) == 0 || !hostPatternsOverlap(l.e.Host, r.e.Host) {
				continue
			}
			conflicts := connectionConflicts(l.e, r.e)
			if contributesRequestMetadata(l.e) && contributesRequestMetadata(r.e) &&
				pathPatternsOverlap(l.e.Path, r.e.Path) && pathSpecificity(l.e.Path) == pathSpecificity(r.e.Path) {
				conflicts = append(conflicts, requestConflicts(l.e, r.e)...)
			}
			if len(conflicts) == 0 {
				continue
			}
			out = append(out, EndpointAmbiguity{LeftRule: l.rule, LeftIndex: l.index, LeftSelector: endpointSelector(l.e),
				RightRule: r.rule, RightIndex: r.index, RightSelector: endpointSelector(r.e), Ports: ports, Conflicts: conflicts, left: l.e, right: r.e})
		}
	}
	return out
}

// endpointSelector is host:ports[path] (OpenShell's endpoint_selector).
func endpointSelector(e v1.PolicyNetworkEndpoint) string {
	host := e.Host
	if host == "" {
		host = "<any-host>"
	}
	return host + ":" + joinPorts(effectivePorts(e)) + e.Path
}

// effectivePorts are Ports (without 0) or else Port, sorted.
func effectivePorts(e v1.PolicyNetworkEndpoint) []uint32 {
	var ports []uint32
	if len(e.Ports) == 0 {
		if e.Port > 0 {
			ports = []uint32{e.Port}
		}
	} else {
		for _, p := range e.Ports {
			if p > 0 && !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
	}
	slices.Sort(ports)
	return ports
}

func overlappingPorts(l, r v1.PolicyNetworkEndpoint) []uint32 {
	rp := effectivePorts(r)
	var out []uint32
	for _, p := range effectivePorts(l) {
		if slices.Contains(rp, p) {
			out = append(out, p)
		}
	}
	return out
}

func conflict(field string, l, r any) string {
	return field + "=" + debugValue(l) + " vs " + debugValue(r)
}

// debugValue formats as Rust's Debug does for the values compared.
func debugValue(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%q", x)
	case []string:
		q := make([]string, len(x))
		for i, s := range x {
			q[i] = fmt.Sprintf("%q", s)
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	return fmt.Sprint(v)
}

func connectionConflicts(l, r v1.PolicyNetworkEndpoint) []string {
	var out []string
	if a, b := isExplicitTCP(l.Protocol), isExplicitTCP(r.Protocol); a != b {
		out = append(out, conflict("transparent_tcp_eligible", a, b))
	}
	if a, b := normalizedTLS(l.TLS), normalizedTLS(r.TLS); a != b {
		out = append(out, conflict("tls", a, b))
	}
	if a, b := normalizedStrings(l.AllowedIPs), normalizedStrings(r.AllowedIPs); !slices.Equal(a, b) {
		out = append(out, conflict("allowed_ips", a, b))
	}
	return out
}

func isExplicitTCP(p string) bool { return strings.EqualFold(p, "tcp") }

// contributesRequestMetadata is OpenShell's
// endpoint_contributes_request_pipeline_metadata: a plain L4 endpoint
// authorizes a destination but does not compete for request handling.
func contributesRequestMetadata(e v1.PolicyNetworkEndpoint) bool {
	return e.Protocol != "" && !isExplicitTCP(e.Protocol) || len(e.AllowedIPs) > 0 || e.TLS != v1.NetworkTLSModeUnspecified || e.CredentialBinding != nil
}

func requestConflicts(l, r v1.PolicyNetworkEndpoint) []string {
	var out []string
	push := func(field string, a, b any) {
		if a != b {
			out = append(out, conflict(field, a, b))
		}
	}
	push("protocol", normalizedRequestProtocol(l.Protocol), normalizedRequestProtocol(r.Protocol))
	push("enforcement", normalizedEnforcement(l.Enforcement), normalizedEnforcement(r.Enforcement))
	push("allow_encoded_slash", l.AllowEncodedSlash, r.AllowEncodedSlash)
	push("websocket_credential_rewrite", l.WebsocketCredentialRewrite, r.WebsocketCredentialRewrite)
	push("request_body_credential_rewrite", l.RequestBodyCredentialRewrite, r.RequestBodyCredentialRewrite)
	if strings.EqualFold(l.Protocol, "websocket") && strings.EqualFold(r.Protocol, "websocket") {
		push("websocket_graphql_policy", websocketGraphQLPolicy(l), websocketGraphQLPolicy(r))
	}
	push("credential_signing", l.CredentialSigning, r.CredentialSigning)
	push("signing_service", l.SigningService, r.SigningService)
	push("signing_region", l.SigningRegion, r.SigningRegion)
	push("credential_binding.provider", bindingProvider(l), bindingProvider(r))
	if strings.EqualFold(l.Protocol, "graphql") && strings.EqualFold(r.Protocol, "graphql") {
		push("graphql_max_body_bytes", bodyLimit(l.GraphqlMaxBodyBytes), bodyLimit(r.GraphqlMaxBodyBytes))
	}
	if strings.EqualFold(l.Protocol, r.Protocol) && isJSONRPCFamily(l.Protocol) {
		push("json_rpc_max_body_bytes", bodyLimit(l.JSONRPCMaxBodyBytes), bodyLimit(r.JSONRPCMaxBodyBytes))
	}
	if strings.EqualFold(l.Protocol, "mcp") && strings.EqualFold(r.Protocol, "mcp") {
		push("mcp.strict_tool_names", strictToolNames(l), strictToolNames(r))
	}
	return out
}

func normalizedRequestProtocol(p string) string {
	if isExplicitTCP(p) {
		return ""
	}
	return strings.ToLower(p)
}

// normalizedTLS: skip, or else unspecified (unknown values as they are).
func normalizedTLS(v v1.NetworkTLSMode) int32 {
	switch {
	case v == v1.NetworkTLSModeSkip:
		return int32(v1.NetworkTLSModeSkip)
	case v >= v1.NetworkTLSModeUnspecified && v <= v1.NetworkTLSModePassthrough:
		return int32(v1.NetworkTLSModeUnspecified)
	}
	return int32(v)
}

// normalizedEnforcement: enforce, or else audit (OpenShell's default).
func normalizedEnforcement(v v1.NetworkEnforcementMode) int32 {
	switch {
	case v == v1.NetworkEnforcementModeEnforce:
		return int32(v1.NetworkEnforcementModeEnforce)
	case v >= v1.NetworkEnforcementModeUnspecified && v <= v1.NetworkEnforcementModeAudit:
		return int32(v1.NetworkEnforcementModeAudit)
	}
	return int32(v)
}

func normalizedStrings(values []string) []string {
	out := []string{}
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func bindingProvider(e v1.PolicyNetworkEndpoint) string {
	if e.CredentialBinding == nil {
		return ""
	}
	return e.CredentialBinding.Provider
}

func bodyLimit(v uint32) uint32 {
	if v == 0 {
		return 65536
	}
	return v
}

func isJSONRPCFamily(p string) bool {
	return strings.EqualFold(p, "json-rpc") || strings.EqualFold(p, "mcp")
}

func strictToolNames(e v1.PolicyNetworkEndpoint) bool {
	if e.Mcp == nil || e.Mcp.StrictToolNames == nil {
		return true
	}
	return *e.Mcp.StrictToolNames
}

func websocketGraphQLPolicy(e v1.PolicyNetworkEndpoint) bool {
	for _, r := range e.Rules {
		if a := r.Allow; a != nil && (a.OperationType != "" || a.OperationName != "" || len(a.Fields) > 0) {
			return true
		}
	}
	for _, d := range e.DenyRules {
		if d.OperationType != "" || d.OperationName != "" || len(d.Fields) > 0 {
			return true
		}
	}
	return len(e.GraphqlPersistedQueries) > 0 || e.PersistedQueries != "" && e.PersistedQueries != "deny"
}

func hostPatternsOverlap(l, r string) bool {
	if l == "" || r == "" {
		return true
	}
	return globTokensOverlap(hostGlobTokens(strings.ToLower(l)), hostGlobTokens(strings.ToLower(r)), '.')
}

func pathPatternsOverlap(l, r string) bool {
	if l == "" || r == "" || l == "**" || l == "/**" || r == "**" || r == "/**" {
		return true
	}
	lt, lok := pathGlobTokens(l)
	rt, rok := pathGlobTokens(r)
	if !lok || !rok {
		return true // invalid globs: conservative, as OpenShell
	}
	return globTokensOverlap(lt, rt, '/')
}

// pathSpecificity is the runtime's route rank: the non-* characters.
func pathSpecificity(p string) int {
	n := 0
	for _, c := range p {
		if c != '*' {
			n++
		}
	}
	return n
}

type runeRange struct{ lo, hi rune }

// globToken is a literal, ?, a character class, or a star (crossing the
// delimiter or not).
type globToken struct {
	kind    byte // 'l' literal, '?' any, '[' class, '*' star
	lit     rune
	ranges  []runeRange
	negated bool
	cross   bool
}

// hostGlobTokens: * within a label, ** across labels; nothing else is
// special.
func hostGlobTokens(p string) []globToken {
	rs := []rune(p)
	var out []globToken
	for i := 0; i < len(rs); {
		if rs[i] != '*' {
			out = append(out, globToken{kind: 'l', lit: rs[i]})
			i++
			continue
		}
		start := i
		for i < len(rs) && rs[i] == '*' {
			i++
		}
		out = append(out, globToken{kind: '*', cross: i-start >= 2})
	}
	return out
}

// pathGlobTokens is the runtime's path glob (glob::Pattern without
// require_literal_separator: * and ** both cross /); false when invalid.
func pathGlobTokens(p string) ([]globToken, bool) {
	rs := []rune(p)
	var out []globToken
	for i := 0; i < len(rs); {
		switch rs[i] {
		case '?':
			out = append(out, globToken{kind: '?'})
			i++
		case '*':
			start := i
			for i < len(rs) && rs[i] == '*' {
				i++
			}
			n := i - start
			if n > 2 {
				return nil, false
			}
			if n == 2 {
				startsComponent := start == 0 || rs[start-1] == '/'
				endsComponent := i == len(rs) || rs[i] == '/'
				if !startsComponent || !endsComponent {
					return nil, false
				}
				if i < len(rs) && rs[i] == '/' {
					i++
				}
			}
			out = append(out, globToken{kind: '*', cross: true})
		case '[':
			negated := i+1 < len(rs) && rs[i+1] == '!'
			content := i + 1
			if negated {
				content++
			}
			end := -1
			for k := content; k < len(rs); k++ {
				if rs[k] == ']' {
					end = k
					break
				}
			}
			if end < 0 || end == content {
				return nil, false
			}
			out = append(out, globToken{kind: '[', ranges: classRanges(rs[content:end]), negated: negated})
			i = end + 1
		default:
			out = append(out, globToken{kind: 'l', lit: rs[i]})
			i++
		}
	}
	return out, true
}

func classRanges(cs []rune) []runeRange {
	var out []runeRange
	for i := 0; i < len(cs); {
		if i+2 < len(cs) && cs[i+1] == '-' {
			out = append(out, runeRange{cs[i], cs[i+2]})
			i += 3
		} else {
			out = append(out, runeRange{cs[i], cs[i]})
			i++
		}
	}
	return out
}

// globTokensOverlap decides whether two glob languages intersect: a
// breadth-first search of the product of their automata (OpenShell's
// token_languages_overlap).
func globTokensOverlap(l, r []globToken, delim rune) bool {
	type state struct{ i, j int }
	queue := []state{{0, 0}}
	seen := map[state]bool{}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		if seen[s] {
			continue
		}
		seen[s] = true
		if s.i == len(l) && s.j == len(r) {
			return true
		}
		if s.i < len(l) && l[s.i].kind == '*' {
			queue = append(queue, state{s.i + 1, s.j})
		}
		if s.j < len(r) && r[s.j].kind == '*' {
			queue = append(queue, state{s.i, s.j + 1})
		}
		if s.i >= len(l) || s.j >= len(r) {
			continue
		}
		if tokensShare(l[s.i], r[s.j], delim) {
			next := s
			if l[s.i].kind != '*' {
				next.i++
			}
			if r[s.j].kind != '*' {
				next.j++
			}
			queue = append(queue, next)
		}
	}
	return false
}

func tokensShare(a, b globToken, delim rune) bool {
	ar, br := tokenRanges(a, delim), tokenRanges(b, delim)
	for _, x := range ar {
		for _, y := range br {
			if x.lo <= y.hi && y.lo <= x.hi {
				return true
			}
		}
	}
	return false
}

var allRunes = []runeRange{{0, 0xD7FF}, {0xE000, 0x10FFFF}}

func tokenRanges(t globToken, delim rune) []runeRange {
	switch t.kind {
	case 'l':
		return []runeRange{{t.lit, t.lit}}
	case '?':
		return allRunes
	case '*':
		if t.cross {
			return allRunes
		}
		return complementRanges([]runeRange{{delim, delim}})
	}
	var rs []runeRange
	for _, r := range t.ranges {
		if r.lo <= r.hi {
			rs = append(rs, r)
		}
	}
	if t.negated {
		return complementRanges(rs)
	}
	return rs
}

func complementRanges(rs []runeRange) []runeRange {
	rs = slices.Clone(rs)
	slices.SortFunc(rs, func(a, b runeRange) int { return int(a.lo - b.lo) })
	var out []runeRange
	for _, u := range allRunes {
		cur := u.lo
		for _, r := range rs {
			if r.hi < u.lo || r.lo > u.hi {
				continue
			}
			lo, hi := max(r.lo, u.lo), min(r.hi, u.hi)
			if cur < lo {
				out = append(out, runeRange{cur, lo - 1})
			}
			if hi+1 > cur {
				cur = hi + 1
			}
			if cur > u.hi {
				break
			}
		}
		if cur <= u.hi {
			out = append(out, runeRange{cur, u.hi})
		}
	}
	return out
}
