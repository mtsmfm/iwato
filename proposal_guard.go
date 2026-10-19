package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// What the approve dialog can show of a proposal, and what an approval
// binds to. An approval applies the whole rule, so the server approves only
// a rule the page could show in full (proposalUnshown), and only the
// version the owner was shown (proposalDigest).

// ProposalLimits are the most of each part of a proposal the page shows; the
// page reads them from the proposals list, the server refuses an approval
// beyond them.
type ProposalLimits struct {
	Binaries   int `json:"binaries"`
	Endpoints  int `json:"endpoints"`
	Ports      int `json:"ports"`
	Rules      int `json:"rules"`
	Deny       int `json:"deny"`
	AllowedIPs int `json:"allowedIps"`
}

var proposalLimits = ProposalLimits{Binaries: 8, Endpoints: 20, Ports: 16, Rules: 32, Deny: 16, AllowedIPs: 32}

// The longest strings the page shows whole.
const (
	shownBinary = 512
	shownHost   = 253
	shownPath   = 512
	shownMethod = 10
	shownIP     = 64
)

// proposalDigest is what the page sends back with a decision: a hash of the
// proposal as it was listed (program, rule name and rule). The server
// refuses the decision when the proposal is no longer that one.
func proposalDigest(c v1.PolicyChunk) string {
	b, _ := json.Marshal(struct {
		Binary   string
		Rule     string
		Proposed *v1.NetworkPolicyRule
	}{c.Binary, c.RuleName, c.ProposedRule})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func errProposalChanged() error {
	return errCode("proposal_changed", nil, "提案が表示したあとに変わりました。表示し直したものを確かめてください")
}

// proposalUnshown are the parts of a rule the approve dialog cannot show
// (empty: it shows all of it): more than proposalLimits, values longer than
// it shows, and endpoint settings it does not render (GraphQL, MCP,
// JSON-RPC, request signing, protocols other than rest and tcp, and rule
// matchers other than method and path).
func proposalUnshown(rule v1.NetworkPolicyRule) []string {
	var out []string
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	over := func(name string, n, max int) {
		if n > max {
			add(fmt.Sprintf("%s > %d", name, max))
		}
	}
	over("binaries", len(rule.Binaries), proposalLimits.Binaries)
	over("endpoints", len(rule.Endpoints), proposalLimits.Endpoints)
	for _, b := range rule.Binaries {
		if len(b.Path) > shownBinary {
			add("binary length")
		}
	}
	rules, deny := 0, 0
	matchers := func(m, p, cmd, ot, on string, q, params int, fields []string, deny bool) {
		name := "rules"
		if deny {
			name = "deny_rules"
		}
		if len(m) > shownMethod || len(p) > shownPath {
			add(name + " length")
		}
		if cmd != "" || ot != "" || on != "" || q > 0 || params > 0 || len(fields) > 0 {
			add(name + ": command, query, operation, fields, params")
		}
	}
	for _, e := range rule.Endpoints {
		over("ports", len(endpointPorts(e)), proposalLimits.Ports)
		over("allowed_ips", len(e.AllowedIPs), proposalLimits.AllowedIPs)
		if len(e.Host) > shownHost || len(e.Path) > shownPath {
			add("host length")
		}
		for _, ip := range e.AllowedIPs {
			if len(ip) > shownIP {
				add("allowed_ips length")
			}
		}
		if p := strings.ToLower(e.Protocol); p != "" && p != "rest" && p != "tcp" {
			add("protocol: " + e.Protocol)
		}
		if e.TLS != v1.NetworkTLSModeUnspecified && e.TLS != v1.NetworkTLSModeSkip {
			add("tls")
		}
		if e.PersistedQueries != "" || len(e.GraphqlPersistedQueries) > 0 || e.GraphqlMaxBodyBytes != 0 {
			add("graphql")
		}
		if e.Mcp != nil {
			add("mcp")
		}
		if e.JSONRPCMaxBodyBytes != 0 {
			add("json_rpc")
		}
		if e.CredentialSigning != "" || e.SigningService != "" || e.SigningRegion != "" {
			add("credential_signing")
		}
		for _, r := range e.Rules {
			if a := r.Allow; a != nil {
				rules++
				matchers(a.Method, a.Path, a.Command, a.OperationType, a.OperationName, len(a.Query), len(a.Params), a.Fields, false)
			}
		}
		for _, d := range e.DenyRules {
			deny++
			matchers(d.Method, d.Path, d.Command, d.OperationType, d.OperationName, len(d.Query), len(d.Params), d.Fields, true)
		}
	}
	over("rules", rules, proposalLimits.Rules)
	over("deny_rules", deny, proposalLimits.Deny)
	return out
}

func errProposalUnshown(parts []string) error {
	return errCode("proposal_unshown", map[string]any{"parts": parts}, "この提案には Iwato が表示できない内容があるため承認できません ("+strings.Join(parts, ", ")+")。却下するか、YAML で規則を書いてください")
}

// hostGateways are the names and addresses through which a container
// reaches the machine it runs on, and so that machine's loopback, where
// Iwato and the *.localhost publishing listen: WSLc's, Docker's (Desktop,
// and host-gateway on Linux) and Podman's. The page warns of rules to them
// (and of allowed_ips that include the addresses).
var hostGateways = []string{
	"host.wslc.internal", "host.docker.internal", "gateway.docker.internal", "host-gateway", "host.containers.internal",
	"192.168.65.254", "192.168.65.2", "192.168.65.1", "172.17.0.1", "10.0.2.2", "169.254.1.2",
}

// gatewayHit says which of hostGateways an endpoint reaches: its host (a
// glob too), or an allowed_ips entry that contains one of the addresses.
func gatewayHit(host string, allowedIPs []string) string {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, g := range hostGateways {
		if h == g || strings.Contains(h, "*") && hostGlob(h, g) {
			return g
		}
	}
	for _, g := range hostGateways {
		a, err := netip.ParseAddr(g)
		if err != nil {
			continue
		}
		for _, ip := range allowedIPs {
			if p, err := netip.ParsePrefix(ip); err == nil && p.Contains(a) {
				return g
			}
			if b, err := netip.ParseAddr(ip); err == nil && b == a {
				return g
			}
		}
	}
	return ""
}

// hostGlob matches a host glob loosely ("*" for any characters), which errs
// on the side of a warning.
func hostGlob(pattern, host string) bool {
	re, err := regexp.Compile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$")
	return err == nil && re.MatchString(host)
}

// listenPort is the port Iwato serves on (0 when unknown).
func (s *Server) listenPort() uint32 {
	_, p, err := net.SplitHostPort(s.host)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// iwatoPortRefusal refuses rules with an endpoint on Iwato's own port: a
// sandbox reaching it through the host's loopback would reach Iwato and
// the *.localhost publishing.
func (s *Server) iwatoPortRefusal(rules map[string]v1.NetworkPolicyRule) error {
	port := s.listenPort()
	if port == 0 {
		return nil
	}
	for _, name := range sortedKeys(rules) {
		for _, e := range rules[name].Endpoints {
			if slices.Contains(endpointPorts(e), port) {
				return errCode("iwato_port", map[string]any{"rule": name, "port": port}, fmt.Sprintf("規則 %s のポート %d は Iwato 自身のポートです。サンドボックスから Iwato には接続させません", name, port))
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// GatewayRule is a rule with an endpoint to one of hostGateways.
type GatewayRule struct {
	Rule    string `json:"rule"`
	Host    string `json:"host"`
	Gateway string `json:"gateway"`
}

// gatewayRules are the rules' endpoints that reach the host's loopback.
func gatewayRules(rules map[string]v1.NetworkPolicyRule) []GatewayRule {
	out := []GatewayRule{}
	for _, name := range sortedKeys(rules) {
		for _, e := range rules[name].Endpoints {
			if g := gatewayHit(e.Host, e.AllowedIPs); g != "" {
				out = append(out, GatewayRule{Rule: name, Host: e.Host, Gateway: g})
			}
		}
	}
	return out
}
