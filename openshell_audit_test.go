package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

func TestParseObserved(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	line := func(msg string, d time.Duration) v1.LogLine {
		return v1.LogLine{Timestamp: at.Add(d), Message: msg}
	}
	got := parseLogged([]v1.LogLine{
		line("HTTP:GET [INFO] ALLOWED gh(42) -> GET https://api.github.com/repos/o/r [policy:gh engine:l7]", 0),
		line("HTTP:GET [INFO] ALLOWED gh(43) -> GET https://api.github.com/repos/o/r [policy:gh engine:l7]", time.Second),
		line("HTTP:POST [INFO] ALLOWED POST http://api.example.com:8443/v1/items [policy:api engine:l7] [attempt:2]", 0),
		// Not L7, or not an HTTP event.
		line("HTTP:GET [INFO] ALLOWED curl(1) -> GET https://x.example/ [policy:x engine:mechanistic]", 0),
		line("NET:OPEN [INFO] ALLOWED curl(1) -> x.example:443/tcp [policy:x engine:opa]", 0),
	})
	gh := got.allowed[observedKey{"gh", "api.github.com", 443}]
	if len(gh) != 1 || gh[0] != (ObservedRequest{Method: "GET", Path: "/repos/o/r", Binary: "gh", Count: 2, Last: at.Add(time.Second).UnixMilli()}) {
		t.Fatalf("%+v", gh)
	}
	api := got.allowed[observedKey{"api", "api.example.com", 8443}]
	if len(api) != 1 || api[0].Method != "POST" || api[0].Path != "/v1/items" || api[0].Binary != "" {
		t.Fatalf("%+v", api)
	}
	if len(got.allowed) != 2 || len(got.denied) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestEnforceAnAuditedEndpoint(t *testing.T) {
	audited := v1.PolicyNetworkEndpoint{Host: "api.github.com", Port: 443, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeAudit,
		Rules: []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}}
	rules := map[string]v1.NetworkPolicyRule{"gh": {Name: "gh", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/gh"}}, Endpoints: []v1.PolicyNetworkEndpoint{audited}}}
	edit := PolicyEdit{Op: "enforce", Rule: "gh", Host: "api.github.com", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "GET", Path: "/repos/o/r/**"}}}
	if err := edit.Validate(); err != nil {
		t.Fatal(err)
	}
	ops, err := edit.operations(rules)
	if err != nil || len(ops) != 2 || ops[0].RemoveEndpoint == nil || ops[1].AddRule == nil {
		t.Fatal(ops, err)
	}
	e := ops[1].AddRule.Rule.Endpoints[0]
	if e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) != 1 || e.Rules[0].Allow.Path != "/repos/o/r/**" || ops[1].AddRule.Rule.Binaries[0].Path != "/usr/bin/gh" {
		t.Fatalf("%+v", ops[1].AddRule.Rule)
	}
	// Only an audited endpoint.
	audited.Enforcement = v1.NetworkEnforcementModeEnforce
	rules["gh"] = v1.NetworkPolicyRule{Name: "gh", Endpoints: []v1.PolicyNetworkEndpoint{audited}}
	if _, err := edit.operations(rules); err == nil {
		t.Fatal("enforced an enforced endpoint")
	}
	if err := (PolicyEdit{Op: "enforce", Rule: "gh", Host: "api.github.com", Ports: []uint32{443}}).Validate(); err == nil {
		t.Fatal("no rules accepted")
	}
}

// Shorthand lines as OpenShell v0.1.2 writes them: the L7 relay's deny
// (l7/relay.rs unmatched_route_path_builds_denied_http_activity_event,
// which names no process), the forward proxy's (with the process), and a
// CONNECT refused for a program the rule does not name.
func TestParseDenied(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_000)
	line := func(msg string, d time.Duration) v1.LogLine {
		return v1.LogLine{Timestamp: at.Add(d), Message: msg}
	}
	got := parseLogged([]v1.LogLine{
		line("HTTP:GET [MED] DENIED GET http://gateway.example.test:443/other [policy:route_api engine:l7] [reason:L7_REQUEST deny GET gateway.example.test:443/other reason=no L7 endpoint path matched request]", 0),
		// The relay names no process: the CONNECT before it does.
		line("NET:OPEN [INFO] ALLOWED /usr/local/lib/mise/mise(123) -> api.github.com:443 [policy:allow_api_github_com_443 engine:opa]", time.Second),
		line("HTTP:GET [MED] DENIED GET http://api.github.com:443/repos/anthropics/claude-code/releases/assets/616117246 [policy:allow_api_github_com_443 engine:l7] [reason:L7_REQUEST deny GET api.github.com:443/repos/anthropics/claude-code/releases/assets/616117246 reason=GET /repos/anthropics/claude-code/releases/assets/616117246 not permitted by policy]", 2*time.Second),
		line("HTTP:GET [MED] DENIED GET http://api.github.com:443/repos/anthropics/claude-code/releases/assets/616117246 [policy:allow_api_github_com_443 engine:l7] [reason:L7_REQUEST deny GET api.github.com:443/repos/anthropics/claude-code/releases/assets/616117246 reason=GET /repos/anthropics/claude-code/releases/assets/616117246 not permitted by policy]", 3*time.Second),
		line("HTTP:DELETE [MED] DENIED /usr/bin/curl(88) -> DELETE http://api.example.com/v1/items/7 [policy:api engine:l7] [reason:FORWARD_L7 deny DELETE api.example.com:80/v1/items/7 reason=DELETE /v1/items/7 blocked by deny rule]", 0),
		line("NET:OPEN [MED] DENIED /usr/bin/wget(7) -> api.github.com:443 [policy:- engine:opa] [reason:binary '/usr/bin/wget' not allowed in policy 'gh' (ancestors: [/usr/bin/bash], cmdline: []). SYMLINK HINT: the binary path is the kernel-resolved target from /proc/<pid>/exe, not the symlink. If your policy specifies a symlink (e.g., /usr/bin/python3) but the actual...]", 0),
		// Audited (let through) and other engines are not denials.
		line("HTTP:GET [INFO] ALLOWED GET http://api.github.com:443/user [policy:allow_api_github_com_443 engine:l7]", 0),
		line("HTTP:PUT [MED] DENIED PUT http://169.254.169.254/latest/api/token [policy:aws_iam engine:ssrf] [reason:not in allowed_ips]", 0),
	})
	if r := got.denied[observedKey{"route_api", "gateway.example.test", 443}]; len(r) != 1 || r[0].Method != "GET" || r[0].Path != "/other" || r[0].Reason != "no L7 endpoint path matched request" || r[0].Binary != "" {
		t.Fatalf("%+v", r)
	}
	mise := got.denied[observedKey{"allow_api_github_com_443", "api.github.com", 443}]
	if len(mise) != 1 || mise[0] != (ObservedRequest{Method: "GET", Path: "/repos/anthropics/claude-code/releases/assets/616117246", Binary: "/usr/local/lib/mise/mise", Count: 2, Last: at.Add(3 * time.Second).UnixMilli(), Reason: "GET /repos/anthropics/claude-code/releases/assets/616117246 not permitted by policy"}) {
		t.Fatalf("%+v", mise)
	}
	if r := got.denied[observedKey{"api", "api.example.com", 80}]; len(r) != 1 || r[0].Binary != "/usr/bin/curl" || r[0].Reason != "DELETE /v1/items/7 blocked by deny rule" {
		t.Fatalf("%+v", r)
	}
	if r := got.denied[observedKey{"gh", "api.github.com", 443}]; len(r) != 1 || r[0].Kind != "binary" || r[0].Binary != "/usr/bin/wget" || r[0].Method != "" {
		t.Fatalf("%+v", r)
	}
	if len(got.denied) != 4 || len(got.allowed) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestFillDeniedKeepsWhatStillFails(t *testing.T) {
	lines := []v1.LogLine{
		{Message: "HTTP:POST [MED] DENIED POST http://ci.example:443/api/builds [policy:ci engine:l7] [reason:L7_REQUEST deny POST ci.example:443/api/builds reason=POST /api/builds not permitted by policy]"},
		{Message: "HTTP:GET [MED] DENIED GET http://ci.example:443/old [policy:ci engine:l7] [reason:L7_REQUEST deny GET ci.example:443/old reason=GET /old not permitted by policy]"},
		{Message: "HTTP:DELETE [MED] DENIED DELETE http://ci.example:443/api/x [policy:ci engine:l7] [reason:L7_REQUEST deny DELETE ci.example:443/api/x reason=DELETE /api/x blocked by deny rule]"},
		{Message: "NET:OPEN [MED] DENIED /usr/bin/wget(7) -> ci.example:443 [policy:- engine:opa] [reason:binary '/usr/bin/wget' not allowed in policy 'ci' (ancestors: [])]"},
		{Message: "NET:OPEN [MED] DENIED /usr/bin/nc(9) -> ci.example:443 [policy:- engine:opa] [reason:binary '/usr/bin/nc' not allowed in policy 'ci' (ancestors: [])]"},
		{Message: "NET:OPEN [MED] DENIED /opt/node/22/bin/node(7) -> ci.example:443 [policy:- engine:opa] [reason:binary '/opt/node/22/bin/node' not allowed in policy 'ci' (ancestors: [])]"},
	}
	// Read-write: POST allowed since; DELETE denied by rule; node was
	// added as a glob since.
	p := &CurrentPolicy{Rules: []PolicyRuleView{{Name: "ci", Binaries: []string{"/usr/bin/curl", "/opt/node/*/bin/node"}, Endpoints: []EndpointView{
		{Host: "ci.example", Ports: []uint32{443}, Inspect: "rest", Enforcement: "enforce", Access: "read-write", Deny: []ProposalRule{{Method: "DELETE", Path: "/api/**"}}},
	}}}}
	// Another rule for the host allows /old to curl, and DELETE to wget
	// only (which it names, so wget is no longer refused).
	p.Rules = append(p.Rules, PolicyRuleView{Name: "more", Binaries: []string{"/usr/bin/curl"}, Endpoints: []EndpointView{{Host: "CI.example", Ports: []uint32{443}, Inspect: "rest", Rules: []ProposalRule{{Method: "GET", Path: "/old"}}}}},
		PolicyRuleView{Name: "wget", Binaries: []string{"/usr/bin/wget"}, Endpoints: []EndpointView{{Host: "ci.example", Ports: []uint32{443}, Inspect: "rest", Rules: []ProposalRule{{Method: "DELETE", Path: "/api/x"}}}}})
	lines[1].Message = strings.Replace(lines[1].Message, "GET http", "/usr/bin/curl(3) -> GET http", 1)
	lines[2].Message = strings.Replace(lines[2].Message, "DELETE http", "/usr/bin/curl(3) -> DELETE http", 1)
	fillObserved(p, lines)
	var got []string
	for _, d := range p.Rules[0].Endpoints[0].Denied {
		got = append(got, d.Kind+":"+d.Method+" "+d.Path+" "+d.Binary)
	}
	if !slices.Equal(got, []string{"binary:  /usr/bin/nc", ":DELETE /api/x /usr/bin/curl"}) {
		t.Fatalf("%q", got)
	}
	if p.Rules[0].Endpoints[0].Observed != nil {
		t.Fatal("an enforced endpoint lists observed requests")
	}
	// A pending proposal for nc there: it is decided there.
	dropProposedDenials(p, []v1.PolicyChunk{{Status: "pending", Binary: "/usr/bin/nc", ProposedRule: &v1.NetworkPolicyRule{Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/nc"}}, Endpoints: []v1.PolicyNetworkEndpoint{{Host: "CI.example", Port: 443}}}}})
	if d := p.Rules[0].Endpoints[0].Denied; len(d) != 1 || d[0].Method != "DELETE" {
		t.Fatalf("%+v", d)
	}
}

func TestEndpointAllows(t *testing.T) {
	e := EndpointView{Access: "read-only", Rules: []ProposalRule{{Method: "POST", Path: "/repos/*/*/issues"}, {Method: "*", Path: "/v1/**"}}, Deny: []ProposalRule{{Method: "*", Path: "/v1/admin/**"}}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/anything/at/all", true},
		{"POST", "/repos/o/r/issues", true},
		{"POST", "/repos/o/r/x/issues", false},
		{"PUT", "/v1/a/b", true},
		{"PUT", "/v1/admin/x", false},
		{"GET", "/v1/admin/x", false},
		{"DELETE", "/x", false},
	} {
		if got := endpointAllows(e, c.method, c.path); got != c.want {
			t.Errorf("%s %s: %v", c.method, c.path, got)
		}
	}
	if !binaryGlobMatch("/sandbox/.local/share/claude/versions/*", "/sandbox/.local/share/claude/versions/2.1.3") || binaryGlobMatch("/usr/bin/*", "/usr/bin/x/y") {
		t.Fatal("binary glob")
	}
}

func TestAttachObservedReadsLogsForInspectedEndpoints(t *testing.T) {
	calls := 0
	o := &OpenShell{logs: func(context.Context, Sandbox) ([]v1.LogLine, error) {
		calls++
		return []v1.LogLine{
			{Message: "HTTP:GET [INFO] ALLOWED GET https://api.github.com/user [policy:gh engine:l7]"},
			{Message: "HTTP:POST [MED] DENIED POST http://api.github.com:443/user/repos [policy:gh engine:l7] [reason:L7_REQUEST deny POST api.github.com:443/user/repos reason=POST /user/repos not permitted by policy]"},
		}, nil
	}}
	view := func(inspect, enforcement string) *CurrentPolicy {
		return &CurrentPolicy{Rules: []PolicyRuleView{{Name: "gh", Endpoints: []EndpointView{{Host: "api.github.com", Ports: []uint32{443}, Inspect: inspect, Enforcement: enforcement, Rules: []ProposalRule{{Method: "GET", Path: "/user"}}}}}}}
	}
	p := view("", "")
	o.attachObserved(context.Background(), Sandbox{Name: "dev"}, p)
	if calls != 0 {
		t.Fatal("read logs without an inspected endpoint")
	}
	p = view("rest", "enforce")
	o.attachObserved(context.Background(), Sandbox{Name: "dev"}, p)
	if e := p.Rules[0].Endpoints[0]; calls != 1 || len(e.Observed) != 0 || len(e.Denied) != 1 || e.Denied[0].Path != "/user/repos" {
		t.Fatalf("%d %+v", calls, e)
	}
	// The policy tab polls: the lines are reused for a few seconds.
	p = view("rest", "audit")
	o.attachObserved(context.Background(), Sandbox{Name: "dev"}, p)
	if e := p.Rules[0].Endpoints[0]; calls != 1 || len(e.Observed) != 1 || e.Observed[0].Path != "/user" || len(e.Denied) != 1 {
		t.Fatalf("%d %+v", calls, e)
	}
	o.attachObserved(context.Background(), Sandbox{Name: "other"}, view("rest", "audit"))
	if calls != 2 {
		t.Fatal("another sandbox's logs came from the cache")
	}
	o.logMu.Lock()
	c := o.logCache["dev"]
	c.at = c.at.Add(-logCacheFor)
	o.logCache["dev"] = c
	o.logMu.Unlock()
	o.attachObserved(context.Background(), Sandbox{Name: "dev"}, view("rest", "audit"))
	if calls != 3 {
		t.Fatal("the cache did not expire")
	}
}

// Adding methods and paths to an endpoint with an access preset: OpenShell
// turns the preset into its rules first (expand_existing_access); the
// prediction (and the fake) does the same.
func TestAddAllowToAPresetEndpoint(t *testing.T) {
	ci := v1.PolicyNetworkEndpoint{Host: "ci.example", Port: 443, Ports: []uint32{443}, Protocol: "rest", Enforcement: v1.NetworkEnforcementModeEnforce, Access: v1.NetworkAccessPresetReadOnly}
	rules := map[string]v1.NetworkPolicyRule{"ci": {Name: "ci", Binaries: []v1.PolicyNetworkBinary{{Path: "/usr/bin/curl"}}, Endpoints: []v1.PolicyNetworkEndpoint{ci}}}
	edit := PolicyEdit{Op: "add-allow", Rule: "ci", Host: "ci.example", Ports: []uint32{443}, Rules: []ProposalRule{{Method: "POST", Path: "/api/builds"}, {Method: "GET", Path: "**"}}}
	if err := edit.Validate(); err != nil {
		t.Fatal(err)
	}
	ops, err := edit.operations(rules)
	if err != nil || len(ops) != 1 || ops[0].AddAllowRules == nil {
		t.Fatal(ops, err)
	}
	after, err := simulateMerge(rules, ops)
	if err != nil {
		t.Fatal(err)
	}
	e := after["ci"].Endpoints[0]
	var got []string
	for _, r := range e.Rules {
		got = append(got, r.Allow.Method+" "+r.Allow.Path)
	}
	if e.Access != v1.NetworkAccessPresetUnspecified || !slices.Equal(got, []string{"GET **", "HEAD **", "OPTIONS **", "POST /api/builds"}) {
		t.Fatalf("%v %q", e.Access, got)
	}
	if rules["ci"].Endpoints[0].Access != v1.NetworkAccessPresetReadOnly {
		t.Fatal("the base changed")
	}
}

// noLogs stands in for a sandbox whose log is empty.
func noLogs(context.Context, Sandbox) ([]v1.LogLine, error) { return nil, nil }

// A provider's rule may have two endpoints for one host: the request goes
// on the one whose path it matches.
func TestDeniedGoesOnItsEndpoint(t *testing.T) {
	p := &CurrentPolicy{Rules: []PolicyRuleView{{Name: "_provider_github", Binaries: []string{"/usr/bin/gh"}, Endpoints: []EndpointView{
		{Host: "api.github.com", Ports: []uint32{443}, Inspect: "rest", Enforcement: "enforce", Access: "read-only"},
		{Host: "api.github.com", Ports: []uint32{443}, Path: "/graphql", Inspect: "graphql", Enforcement: "enforce", Access: "read-only"},
	}}}}
	fillObserved(p, []v1.LogLine{
		{Message: "HTTP:POST [MED] DENIED POST http://api.github.com:443/repos/o/r/releases [policy:_provider_github engine:l7] [reason:L7_REQUEST deny POST api.github.com:443/repos/o/r/releases reason=POST /repos/o/r/releases not permitted by policy]"},
		{Message: "HTTP:PUT [MED] DENIED PUT http://api.github.com:443/graphql [policy:_provider_github engine:l7] [reason:L7_REQUEST deny PUT api.github.com:443/graphql reason=PUT /graphql not permitted by policy]"},
		{Message: "NET:OPEN [MED] DENIED /usr/bin/nc(9) -> api.github.com:443 [policy:- engine:opa] [reason:binary '/usr/bin/nc' not allowed in policy '_provider_github' (ancestors: [])]"},
	})
	a, g := p.Rules[0].Endpoints[0].Denied, p.Rules[0].Endpoints[1].Denied
	if len(a) != 2 || a[0].Kind != "binary" || a[1].Path != "/repos/o/r/releases" || len(g) != 1 || g[0].Path != "/graphql" {
		t.Fatalf("%+v %+v", a, g)
	}
}
