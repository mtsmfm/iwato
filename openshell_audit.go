package main

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// OpenShell's audit mode lets every request through and only logs it: no
// proposal comes of it (OpenShell's denial aggregator never sees L7
// traffic). Iwato reads those logs back, so the owner can pick the
// requests an audited endpoint really needs and switch it to enforce with
// just those ("enforce" in PolicyEdit).
//
// An enforced endpoint that denies a request (its method or path is not
// allowed) makes no proposal either: the L7 relay (l7/relay.rs) sends no
// DenialEvent, and the program gets a policy_denied body that expects the
// agent to propose. The same logs name those requests, so the owner sees
// them under the endpoint and can allow them ("add-allow").

// ObservedRequest is one method and path seen on an inspected endpoint:
// let through by audit (Observed) or denied (Denied). A denied one has
// OpenShell's reason; Kind "binary" is a program the rule does not name,
// refused before any request (Method and Path empty).
type ObservedRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Binary string `json:"binary,omitempty"`
	Count  int    `json:"count"`
	Last   int64  `json:"last"` // Unix ms
	Reason string `json:"reason,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// maxObserved bounds the requests shown per endpoint.
const maxObserved = 50

// l7Request matches the shorthand of the OCSF HTTP event the sandbox pushes
// for every L7 decision (openshell-ocsf format_shorthand). The L7 relay
// (CONNECT tunnels, l7/relay.rs build_l7_request_event) names no process;
// it writes https without the default port for a TLS-terminated request
// (before v0.1.5: http with the port):
//
//	HTTP:GET [MED] DENIED GET https://api.github.com/repos/o/r [policy:gh engine:l7] [reason:L7_REQUEST deny GET api.github.com:443/repos/o/r reason=GET /repos/o/r not permitted by policy]
//
// the forward proxy (plain HTTP, proxy.rs FORWARD_L7) names it:
//
//	HTTP:PUT [MED] DENIED /usr/bin/curl(88) -> PUT http://api.example.com/v1 [policy:api engine:l7] [reason:FORWARD_L7 deny PUT api.example.com:80/v1 reason=...]
//
// An audited request is ALLOWED (decision "audit").
var l7Request = regexp.MustCompile(`^HTTP:\S+ \[\w+\] (ALLOWED|DENIED) (?:(\S+)\((?:\d+|-)\) -> )?([A-Z_]+) (\S+) \[policy:(\S+) engine:l7\](.*)$`)

// netOpen matches the shorthand of a CONNECT's OCSF network event
// (proxy.rs build_connect_allow_ocsf_event and the CONNECT deny):
//
//	NET:OPEN [INFO] ALLOWED /usr/bin/gh(42) -> api.github.com:443 [policy:gh engine:opa]
//	NET:OPEN [MED] DENIED /usr/bin/wget(7) -> api.github.com:443 [policy:- engine:opa] [reason:binary '/usr/bin/wget' not allowed in policy 'gh' (ancestors: ...]
var netOpen = regexp.MustCompile(`^NET:OPEN \[\w+\] (ALLOWED|DENIED) (\S+)\((?:\d+|-)\) -> (\S+):(\d+)(?:/\w+)? \[policy:(\S+) engine:opa\](.*)$`)

// binaryMiss is OpenShell's reason for a program a matching rule does not
// name (sandbox-policy.rego binary_misses), one per rule.
var binaryMiss = regexp.MustCompile(`binary '([^']+)' not allowed in policy '([^']+)'`)

type observedKey struct {
	rule, host string
	port       uint32
}

// loggedRequests are the requests in a sandbox's log, by rule, host and
// port: let through (audit) and denied.
type loggedRequests struct {
	allowed, denied map[observedKey][]ObservedRequest
}

// shorthandReason is the [reason:...] of a denied event's shorthand,
// without OpenShell's message prefix ("L7_REQUEST deny GET host:443/path
// reason=").
func shorthandReason(rest string) string {
	i := strings.Index(rest, "reason:")
	if i < 0 {
		return ""
	}
	r := strings.TrimSuffix(strings.TrimSpace(rest[i+len("reason:"):]), "]")
	if j := strings.LastIndex(r, " reason="); j >= 0 {
		r = r[j+len(" reason="):]
	}
	return strings.TrimSpace(r)
}

// procName is the process in a shorthand: OpenShell writes the
// executable's path, "-" when it could not tell.
func procName(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// parseLogged groups the logged L7 requests by rule, host and port, and
// the method and path (a denied program by the program). The L7 relay's
// lines name no process: the program is the one whose CONNECT to the same
// rule, host and port was logged last before it.
func parseLogged(lines []v1.LogLine) loggedRequests {
	lines = slices.Clone(lines)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Timestamp.Before(lines[j].Timestamp) })
	found := [2]map[observedKey]map[string]*ObservedRequest{{}, {}}
	add := func(denied bool, k observedKey, id string, r ObservedRequest, at time.Time) {
		set := found[0]
		if denied {
			set = found[1]
		}
		if set[k] == nil {
			set[k] = map[string]*ObservedRequest{}
		}
		x := set[k][id]
		if x == nil {
			x = &r
			set[k][id] = x
		}
		x.Count++
		if r.Binary != "" {
			x.Binary = r.Binary
		}
		if r.Reason != "" {
			x.Reason = r.Reason
		}
		if t := at.UnixMilli(); t > x.Last {
			x.Last = t
		}
	}
	connected := map[observedKey]string{}
	for _, l := range lines {
		if m := netOpen.FindStringSubmatch(l.Message); m != nil {
			p, err := strconv.ParseUint(m[4], 10, 16)
			if err != nil {
				continue
			}
			host, binary := strings.ToLower(m[3]), procName(m[2])
			if m[1] == "ALLOWED" {
				if m[5] != "-" && binary != "" {
					connected[observedKey{m[5], host, uint32(p)}] = binary
				}
				continue
			}
			for _, miss := range binaryMiss.FindAllStringSubmatch(m[6], -1) {
				add(true, observedKey{miss[2], host, uint32(p)}, "binary "+miss[1], ObservedRequest{Binary: miss[1], Kind: "binary", Reason: "binary '" + miss[1] + "' not allowed in policy '" + miss[2] + "'"}, l.Timestamp)
			}
			continue
		}
		m := l7Request.FindStringSubmatch(l.Message)
		if m == nil {
			continue
		}
		u, err := url.Parse(m[4])
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := map[string]uint32{"http": 80, "https": 443}[u.Scheme]
		if p, err := strconv.ParseUint(u.Port(), 10, 16); err == nil {
			port = uint32(p)
		}
		path := u.EscapedPath()
		if path == "" {
			path = "/"
		}
		k := observedKey{m[5], strings.ToLower(u.Hostname()), port}
		binary := procName(m[2])
		if binary == "" {
			binary = connected[k]
		}
		r := ObservedRequest{Method: m[3], Path: path, Binary: binary}
		denied := m[1] == "DENIED"
		if denied {
			r.Reason = shorthandReason(m[6])
		}
		add(denied, k, m[3]+" "+path, r, l.Timestamp)
	}
	var out [2]map[observedKey][]ObservedRequest
	for i, set := range found {
		out[i] = map[observedKey][]ObservedRequest{}
		for k, rs := range set {
			list := make([]ObservedRequest, 0, len(rs))
			for _, r := range rs {
				list = append(list, *r)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Last > list[j].Last })
			if len(list) > maxObserved {
				list = list[:maxObserved]
			}
			sort.Slice(list, func(i, j int) bool {
				if list[i].Path != list[j].Path {
					return list[i].Path < list[j].Path
				}
				if list[i].Method != list[j].Method {
					return list[i].Method < list[j].Method
				}
				return list[i].Binary < list[j].Binary
			})
			out[i][k] = list
		}
	}
	return loggedRequests{allowed: out[0], denied: out[1]}
}

// attachObserved adds the requests logged for the policy's inspected
// endpoints: let through by an audited one, and denied. The gateway keeps
// only the sandbox's last 2000 log lines; they are read only when an
// endpoint is inspected.
func (o *OpenShell) attachObserved(ctx context.Context, w Sandbox, p *CurrentPolicy) {
	inspected := false
	for _, r := range p.Rules {
		for _, e := range r.Endpoints {
			inspected = inspected || e.Inspect != ""
		}
	}
	if !inspected {
		return
	}
	lines, err := o.cachedLogs(ctx, w)
	if err != nil {
		return
	}
	fillObserved(p, lines)
}

// fillObserved puts the requests in lines on the endpoints: on an audited
// one what it let through; on an inspected one what it denied and still
// would (one allowed since drops out); on any what a program the rule
// does not name was refused.
func fillObserved(p *CurrentPolicy, lines []v1.LogLine) {
	seen := parseLogged(lines)
	for i, r := range p.Rules {
		for j, e := range r.Endpoints {
			var observed, denied []ObservedRequest
			for _, port := range e.Ports {
				k := observedKey{r.Name, strings.ToLower(e.Host), port}
				// A request goes on the rule's one endpoint for it (a
				// provider's may have one for /graphql besides).
				mine := func(d ObservedRequest) bool { return endpointFor(r, e.Host, port, d.Path) == j }
				if e.Enforcement == "audit" {
					for _, d := range seen.allowed[k] {
						if mine(d) {
							observed = append(observed, d)
						}
					}
				}
				for _, d := range seen.denied[k] {
					switch {
					case d.Kind == "binary":
						if endpointFor(r, e.Host, port, "") != j || programAllowed(p, e.Host, port, d.Binary) {
							continue
						}
					case e.Inspect == "" || !mine(d) || allowedNow(p, e.Host, port, d):
						continue
					}
					denied = append(denied, d)
				}
			}
			p.Rules[i].Endpoints[j].Observed = observed
			p.Rules[i].Endpoints[j].Denied = denied
		}
	}
}

// endpointFor is the index of the rule's endpoint for host and port that a
// request on path goes to: one whose path matches, else one without a
// path (the first; -1 when none). An empty path: the first for the host and port.
func endpointFor(r PolicyRuleView, host string, port uint32, path string) int {
	fallback := -1
	for i, e := range r.Endpoints {
		if !strings.EqualFold(e.Host, host) || !slices.Contains(e.Ports, port) {
			continue
		}
		if path == "" {
			return i
		}
		if e.Path != "" && pathGlobMatch(e.Path, path) {
			return i
		}
		if e.Path == "" && fallback < 0 {
			fallback = i
		}
	}
	return fallback
}

// allowedNow says whether a rule lets the denied request through now: an
// inspected endpoint for the host and port (of this rule or another one,
// as OpenShell evaluates every rule that matches) for its program (when
// the log named it).
func allowedNow(p *CurrentPolicy, host string, port uint32, d ObservedRequest) bool {
	for _, r := range p.Rules {
		if d.Binary != "" && len(r.Binaries) > 0 && !slices.ContainsFunc(r.Binaries, func(b string) bool { return binaryGlobMatch(b, d.Binary) }) {
			continue
		}
		for _, e := range r.Endpoints {
			if e.Inspect != "" && strings.EqualFold(e.Host, host) && slices.Contains(e.Ports, port) && endpointAllows(e, d.Method, d.Path) {
				return true
			}
		}
	}
	return false
}

// programAllowed says whether a rule for the host and port names the
// program now.
func programAllowed(p *CurrentPolicy, host string, port uint32, binary string) bool {
	for _, r := range p.Rules {
		if len(r.Binaries) > 0 && !slices.ContainsFunc(r.Binaries, func(b string) bool { return binaryGlobMatch(b, binary) }) {
			continue
		}
		if slices.ContainsFunc(r.Endpoints, func(e EndpointView) bool { return strings.EqualFold(e.Host, host) && slices.Contains(e.Ports, port) }) {
			return true
		}
	}
	return false
}

// dropProposedDenials takes out the refused programs that a pending
// proposal already asks for (the CONNECT deny goes to OpenShell's denial
// aggregator, which proposes it): the owner decides those there.
func dropProposedDenials(p *CurrentPolicy, chunks []v1.PolicyChunk) {
	pending := func(binary, host string, ports []uint32) bool {
		for _, c := range chunks {
			if c.Status != "pending" || c.ProposedRule == nil {
				continue
			}
			if !slices.Contains(proposalBinaries(c), binary) && c.Binary != binary {
				continue
			}
			for _, e := range c.ProposedRule.Endpoints {
				if strings.EqualFold(e.Host, host) && slices.ContainsFunc(endpointPorts(e), func(p uint32) bool { return slices.Contains(ports, p) }) {
					return true
				}
			}
		}
		return false
	}
	for i, r := range p.Rules {
		for j, e := range r.Endpoints {
			p.Rules[i].Endpoints[j].Denied = slices.DeleteFunc(e.Denied, func(d ObservedRequest) bool {
				return d.Kind == "binary" && pending(d.Binary, e.Host, e.Ports)
			})
		}
	}
}

// accessMethods are the methods an access preset allows on every path
// (openshell-policy-schema AccessPreset::methods, rest).
var accessMethods = map[string][]string{"read-only": readMethods, "read-write": {"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH"}, "full": {"*"}}

// endpointAllows says whether the endpoint lets method and path through
// now: an allow rule or its access preset matches, and no deny rule does
// (sandbox-policy.rego allow_request, deny_request; query matchers aside).
func endpointAllows(e EndpointView, method, path string) bool {
	match := func(r ProposalRule) bool {
		return (r.Method == "*" || r.Method == "" || strings.EqualFold(r.Method, method)) && pathGlobMatch(r.Path, path)
	}
	if slices.ContainsFunc(e.Deny, match) {
		return false
	}
	for _, m := range accessMethods[e.Access] {
		if match(ProposalRule{Method: m, Path: "**"}) {
			return true
		}
	}
	return slices.ContainsFunc(e.Rules, match)
}

// pathGlobMatch is Rego's glob.match(pattern, ["/"], path): "*" and "?"
// stay within a segment, "**" crosses them.
func pathGlobMatch(pattern, path string) bool {
	if pattern == "**" || pattern == "/**" {
		return true
	}
	return globRegexp(pattern).MatchString(path)
}

// binaryGlobMatch is how OpenShell compares a rule's program with the
// one connecting: as it is, or as a glob when it has "*".
func binaryGlobMatch(pattern, binary string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == binary
	}
	return globRegexp(pattern).MatchString(binary)
}

func globRegexp(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		case pattern[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^\b$`) // matches nothing
	}
	return re
}

// logCacheFor is how long a sandbox's log lines are reused: the policy
// tab reads the policy every 10 seconds (every 2 while following), and
// each read would fetch 2000 lines from the gateway. Between one and two
// polls: every other poll reads them, and they are at most 20 seconds old.
const logCacheFor = 15 * time.Second

type cachedLogLines struct {
	at    time.Time
	lines []v1.LogLine
}

// cachedLogs are the sandbox's log lines, read again after logCacheFor.
func (o *OpenShell) cachedLogs(ctx context.Context, w Sandbox) ([]v1.LogLine, error) {
	o.logMu.Lock()
	c, ok := o.logCache[w.Name]
	o.logMu.Unlock()
	if ok && time.Since(c.at) < logCacheFor {
		return c.lines, nil
	}
	lines, err := o.sandboxLogs(ctx, w)
	if err != nil {
		return nil, err
	}
	o.logMu.Lock()
	if o.logCache == nil {
		o.logCache = map[string]cachedLogLines{}
	}
	o.logCache[w.Name] = cachedLogLines{at: time.Now(), lines: lines}
	o.logMu.Unlock()
	return lines, nil
}

// sandboxLogs reads the sandbox's own log lines from the gateway.
func (o *OpenShell) sandboxLogs(ctx context.Context, w Sandbox) ([]v1.LogLine, error) {
	if o.logs != nil {
		return o.logs(ctx, w)
	}
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	res, err := sdk.Sandboxes().GetLogs(ctx, openshellWorkspace, w.Name, v1.WithLogSources("sandbox"), v1.WithLogLines(2000))
	if err != nil {
		return nil, err
	}
	return res.Lines, nil
}
