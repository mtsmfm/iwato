package main

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
)

// OpenShell (v0.1.4) puts a provider's credentials only into traffic it
// inspects. A sandbox policy endpoint that matches an attached provider's
// credentialed endpoint (a profile's endpoint, or a policy endpoint bound
// to the provider: boundScopes) (host and port overlap, and the provider's path
// covers the endpoint's) without L7 inspection (no protocol: "L4-only";
// or tls: skip) and without allow_uninspected_credentials makes the
// gateway refuse the sandbox's effective policy
// (crates/openshell-server/src/grpc/policy.rs,
// find_uninspected_credentialed_endpoint): "credentialed endpoint
// 'api.anthropic.com:443' in rule 'allow_api_anthropic_com_443' uses
// L4-only". OpenShell checks it when a policy is merged and when a
// sandbox is created, but its attach checks only the sandbox's creation
// policy, not the revisions approved since: the attach succeeds and the
// sandbox then fails to provision (the 300 s repair window). Iwato checks
// the latest revision before an attach, and refuses approvals and rules
// that would create such an endpoint while the provider is attached. It
// never sets allow_uninspected_credentials.

// credScope is one credentialed endpoint of a provider.
type credScope struct {
	Provider, Label, Host, Path string
	Port                        uint32
	// Profile is set for Iwato's profiles (the page translates their
	// labels).
	Profile string
}

// providerScopes are the credentialed endpoints of a provider of profile.
// Profiles Iwato does not ship are left to OpenShell's own check.
func providerScopes(provider, profile string) []credScope {
	p, ok := profileByID(profile)
	if !ok {
		return nil
	}
	out := profileScopes(provider, p.Label, p.proto())
	for i := range out {
		out[i].Profile = profile
	}
	return out
}

// profileScopes are the credentialed endpoints of a provider of the
// profile m.
func profileScopes(provider, label string, m *pb.ProviderProfile) []credScope {
	if len(m.Credentials) == 0 {
		return nil
	}
	var out []credScope
	for _, e := range m.Endpoints {
		ports := slices.Clone(e.Ports)
		if len(ports) == 0 && e.Port != 0 {
			ports = []uint32{e.Port}
		}
		for _, port := range ports {
			out = append(out, credScope{Provider: provider, Label: label, Host: strings.ToLower(e.Host), Path: e.Path, Port: port})
		}
	}
	return out
}

// CredentialConflict is a policy endpoint that would carry a provider's
// credential without inspection.
type CredentialConflict struct {
	Rule     string   `json:"rule"`
	Host     string   `json:"host"`
	Ports    []uint32 `json:"ports"`
	Mode     string   `json:"mode"`
	Provider string   `json:"provider"`
	Label    string   `json:"label"`
	Profile  string   `json:"profile,omitempty"`
	// Binaries lose the endpoint when it is removed.
	Binaries []string `json:"binaries"`
}

// credentialConflicts finds the endpoints of rules that OpenShell would
// refuse with scopes attached. The providers' own rules (_provider_*)
// are inspected and left out.
func credentialConflicts(rules map[string]v1.NetworkPolicyRule, scopes []credScope) []CredentialConflict {
	var out []CredentialConflict
	for name, rule := range rules {
		if strings.HasPrefix(name, "_provider_") {
			continue
		}
		for _, e := range rule.Endpoints {
			mode := ""
			switch {
			case strings.TrimSpace(e.Protocol) == "":
				mode = "L4-only"
			case e.TLS == v1.NetworkTLSModeSkip:
				mode = "tls: skip"
			default:
				continue
			}
			if e.AllowUninspectedCredentials {
				continue
			}
			ports := endpointPorts(e)
			for _, sc := range scopes {
				if !hostPatternMatches(e.Host, sc.Host) || (len(ports) > 0 && !slices.Contains(ports, sc.Port)) || !scopePathCovers(sc.Path, e.Path) {
					continue
				}
				c := CredentialConflict{Rule: name, Host: e.Host, Ports: ports, Mode: mode, Provider: sc.Provider, Label: sc.Label, Profile: sc.Profile, Binaries: []string{}}
				for _, b := range rule.Binaries {
					c.Binaries = append(c.Binaries, b.Path)
				}
				out = append(out, c)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// hostPatternMatches: an endpoint host (maybe a pattern: * within a label,
// ** as the whole first label) admits the literal host.
func hostPatternMatches(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if pattern == "" || pattern == host {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	var b strings.Builder
	b.WriteString("^")
	for i, label := range strings.Split(pattern, ".") {
		if i > 0 {
			b.WriteString(`\.`)
		}
		if i == 0 && label == "**" {
			b.WriteString(`[^.]+(\.[^.]+)*`)
			continue
		}
		b.WriteString(strings.ReplaceAll(regexp.QuoteMeta(label), `\*`, `[^.]*`))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(host)
}

// scopePathCovers: the provider's endpoint path covers the policy
// endpoint's (as OpenShell's path_pattern_covers): every path when the
// provider's is empty or **; a policy path with a glob only when equal;
// else the provider's pattern matches it (/v1/** matches /v1 and below).
func scopePathCovers(scope, path string) bool {
	canon := func(p string) string {
		if p = strings.TrimSpace(p); p == "" || p == "**" || p == "/**" {
			return "/**"
		}
		return p
	}
	scope, path = canon(scope), canon(path)
	if scope == path || scope == "/**" {
		return true
	}
	if strings.ContainsAny(path, "*?[") {
		return false
	}
	if prefix, ok := strings.CutSuffix(scope, "/**"); ok && !strings.ContainsAny(prefix, "*?[") {
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	re, err := regexp.Compile("^" + strings.ReplaceAll(regexp.QuoteMeta(scope), `\*`, `.*`) + "$")
	return err == nil && re.MatchString(path)
}

// conflictRefusal refuses a change that would bring conflicts with an
// attached provider (the page explains it from the conflicts).
func conflictRefusal(cs []CredentialConflict) *errAmbiguity {
	return errRefused("credential_uninspected", nil, conflictMessage(cs, false), nil, cs)
}

// conflictMessage explains conflicts for the owner (in Japanese; the page
// words them itself: web/refusals.js).
func conflictMessage(cs []CredentialConflict, attaching bool) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("規則 %s の %s:%s (%s)", c.Rule, c.Host, joinPorts(c.Ports), c.Mode))
	}
	what := cs[0].Provider + " (" + cs[0].Label + ")"
	if attaching {
		return what + " の値が送られる宛先を、ポリシーが検査なしで許可しています: " + strings.Join(parts, "、") +
			"。OpenShell はプロバイダーの値を検査する通信にしか付けないので、このまま付けるとサンドボックスを起動できなくなります。この宛先を規則から外してから付けてください (プロバイダーが検査付きの規則を足します)。"
	}
	return "このサンドボックスに付いている " + what + " の値が送られる宛先を、検査なしで許可することになります: " + strings.Join(parts, "、") +
		"。OpenShell はこれを受け付けません。メソッドとパスを選んで検査付きにしてください。"
}

func joinPorts(ports []uint32) string {
	var s []string
	for _, p := range ports {
		s = append(s, fmt.Sprint(p))
	}
	return strings.Join(s, ",")
}

// policyRulesRuntime reads a sandbox's latest network policy (nil when
// OpenShell has no such sandbox). OpenShell and Fake implement it.
type policyRulesRuntime interface {
	PolicyRules(context.Context, Sandbox) (map[string]v1.NetworkPolicyRule, error)
}

// proposedRuleRuntime reads the rule a pending proposal would add, with the
// proposal's digest (proposalDigest).
type proposedRuleRuntime interface {
	ProposedRule(ctx context.Context, sb Sandbox, request string) (*v1.NetworkPolicyRule, string, error)
}

// attachedScopes are the credentialed endpoints of the providers attached
// to the sandbox.
func (s *Server) attachedScopes(ctx context.Context, sb Sandbox) ([]credScope, error) {
	rt, ok := s.rt.(providerRuntime)
	if !ok {
		return nil, nil
	}
	list, attached, err := s.providerState(ctx, rt)
	if err != nil {
		return nil, err
	}
	var scopes []credScope
	for _, p := range list {
		if slices.Contains(attached[sb.Name], p.Name) {
			scopes = append(scopes, providerScopes(p.Name, p.Profile)...)
		}
	}
	if rr, ok := s.rt.(policyRulesRuntime); ok {
		rules, err := rr.PolicyRules(ctx, sb)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, boundScopes(rules, list)...)
	}
	return scopes, nil
}

// boundScopes are the credentialed endpoints the policy's bindings make
// (OpenShell: extend_credentialed_scopes_from_policy_bindings).
func boundScopes(rules map[string]v1.NetworkPolicyRule, list []ProviderInfo) []credScope {
	var out []credScope
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		for _, e := range rules[name].Endpoints {
			if e.CredentialBinding == nil {
				continue
			}
			sc := credScope{Provider: e.CredentialBinding.Provider, Label: e.CredentialBinding.Provider, Host: strings.ToLower(e.Host), Path: e.Path}
			if i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == sc.Provider }); i >= 0 {
				if prof, ok := profileByID(list[i].Profile); ok {
					sc.Label, sc.Profile = prof.Label, prof.ID
				}
			}
			for _, port := range effectivePorts(e) {
				sc.Port = port
				out = append(out, sc)
			}
		}
	}
	return out
}

// conflictsForAttach are the latest policy's endpoints that would conflict
// with the provider.
func (s *Server) conflictsForAttach(ctx context.Context, sb Sandbox, provider ProviderInfo) ([]CredentialConflict, error) {
	rt, ok := s.rt.(policyRulesRuntime)
	if !ok {
		return nil, nil
	}
	scopes := providerScopes(provider.Name, provider.Profile)
	initial, err := providerInitialRules(provider.Profile, provider.Name)
	if err != nil {
		return nil, err
	}
	scopes = append(scopes, boundScopes(initial, []ProviderInfo{provider})...)
	if len(scopes) == 0 {
		return nil, nil
	}
	rules, err := rt.PolicyRules(ctx, sb)
	if err != nil {
		return nil, err
	}
	return credentialConflicts(rules, scopes), nil
}
