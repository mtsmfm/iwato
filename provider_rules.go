package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// A provider's credential reaches only the policy's endpoints bound to it
// (credential_binding: ADR 0022): Iwato's profiles have no endpoints.
// Attaching a provider of one of them adds its initial rule
// (presets/providers/PROFILE.yaml, a preset with the parameter provider)
// with every endpoint bound to the provider; detaching removes every
// endpoint bound to it. Approvals and the policy screen bind more.
//
// OpenShell refuses a binding to a provider that is not attached, and a
// detach while the policy binds the provider: the attach goes first and
// the rules after it, the rules go first and the detach after them.

//go:embed presets/providers/*.yaml
var providerRuleFiles embed.FS

// providerRulePresets are the initial rules, by profile.
func providerRulePresets() map[string]networkPreset {
	providerRules.once.Do(func() {
		list, errs := readPresetDir(providerRuleFiles, "presets/providers", "provider")
		if len(errs) > 0 {
			panic(fmt.Sprintf("presets/providers: %+v", errs))
		}
		providerRules.m = map[string]networkPreset{}
		for _, p := range list {
			if _, ok := builtinProfile(p.ID); !ok {
				panic("presets/providers/" + p.ID + ".yaml: no such profile")
			}
			providerRules.m[p.ID] = p
		}
	})
	return providerRules.m
}

var providerRules struct {
	once sync.Once
	m    map[string]networkPreset
}

// providerInitialRules are the rules attaching provider name of profile
// adds, every endpoint bound to it: none for a profile Iwato has none for
// (the owner's, ones imported with OpenShell's CLI, the per-provider ones
// made before ADR 0022), whose endpoints OpenShell binds itself.
func providerInitialRules(profile, name string) (map[string]v1.NetworkPolicyRule, error) {
	p, ok := providerRulePresets()[profile]
	if !ok {
		return nil, nil
	}
	rules, err := p.expand(map[string]string{"provider": name})
	if err != nil {
		return nil, err
	}
	for k, r := range rules {
		for i := range r.Endpoints {
			r.Endpoints[i].CredentialBinding = &types.NetworkCredentialBinding{Provider: name}
		}
		rules[k] = r
	}
	return rules, nil
}

// withProviderInitialRules adds the initial rules of the providers to
// rules (under a free name when a rule has the name already).
func withProviderInitialRules(rules map[string]v1.NetworkPolicyRule, list []ProviderInfo, names []string) (map[string]v1.NetworkPolicyRule, error) {
	out := cloneRules(rules)
	if out == nil {
		out = map[string]v1.NetworkPolicyRule{}
	}
	for _, name := range names {
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
		if i < 0 {
			continue
		}
		add, err := providerInitialRules(list[i].Profile, name)
		if err != nil {
			return nil, err
		}
		for _, k := range slices.Sorted(maps.Keys(add)) {
			r := add[k]
			r.Name = freeRuleName(out, k)
			out[r.Name] = r
		}
	}
	return out, nil
}

// withoutProvider removes the endpoints bound to provider, and the rules
// left without endpoints. changed is false when there were none.
func withoutProvider(rules map[string]v1.NetworkPolicyRule, provider string) (out map[string]v1.NetworkPolicyRule, changed bool) {
	out = cloneRules(rules)
	for k, r := range out {
		kept := slices.DeleteFunc(slices.Clone(r.Endpoints), func(e v1.PolicyNetworkEndpoint) bool {
			return e.CredentialBinding != nil && e.CredentialBinding.Provider == provider
		})
		if len(kept) == len(r.Endpoints) {
			continue
		}
		changed = true
		if len(kept) == 0 {
			delete(out, k)
			continue
		}
		r.Endpoints = kept
		out[k] = r
	}
	return out, changed
}

// boundProviders are the providers the rules bind.
func boundProviders(rules map[string]v1.NetworkPolicyRule) []string {
	var out []string
	for _, r := range rules {
		for _, e := range r.Endpoints {
			if e.CredentialBinding != nil && !slices.Contains(out, e.CredentialBinding.Provider) {
				out = append(out, e.CredentialBinding.Provider)
			}
		}
	}
	slices.Sort(out)
	return out
}

// credentialHost: the endpoint is on one of the hosts the profile's
// credential is meant for.
func credentialHost(prof providerProfile, e v1.PolicyNetworkEndpoint) bool {
	for _, port := range effectivePorts(e) {
		if slices.Contains(prof.CredentialHosts, strings.ToLower(e.Host)+":"+fmt.Sprint(port)) {
			return true
		}
	}
	return false
}

// bindable: OpenShell binds a credential only to an endpoint it inspects
// (L7, TLS terminated).
func bindable(e v1.PolicyNetworkEndpoint) bool {
	p := strings.ToLower(strings.TrimSpace(e.Protocol))
	return p != "" && !isExplicitTCP(p) && e.TLS != v1.NetworkTLSModeSkip && !e.AllowUninspectedCredentials
}

// offerProvider is the attached provider that can be bound on an
// endpoint: of one of Iwato's profiles whose credential is meant for the
// endpoint's host (the first by name when several are). ok is false when
// none is.
func offerProvider(e v1.PolicyNetworkEndpoint, list []ProviderInfo, attached []string) (string, bool) {
	if !bindable(e) {
		return "", false
	}
	for _, name := range slices.Sorted(slices.Values(attached)) {
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
		if i < 0 {
			continue
		}
		if prof, ok := builtinProfile(list[i].Profile); ok && credentialHost(prof, e) {
			return name, true
		}
	}
	return "", false
}

// credentialOffers are the endpoints of an approval's rule that carry no
// credential by default but could carry an attached provider's: their
// host is one the provider's credential is meant for (ADR 0022: offered,
// off by default).
func credentialOffers(plan v1.NetworkPolicyRule, list []ProviderInfo, attached []string) []ApprovalAttach {
	var out []ApprovalAttach
	for _, e := range plan.Endpoints {
		if e.CredentialBinding != nil {
			continue
		}
		if p, ok := offerProvider(e, list, attached); ok {
			out = append(out, ApprovalAttach{Host: e.Host, Ports: endpointPorts(e), Path: e.Path, Provider: p})
		}
	}
	return out
}

// bindOffers binds the offered endpoints of plan (credentialOffers).
func bindOffers(plan v1.NetworkPolicyRule, list []ProviderInfo, attached []string) v1.NetworkPolicyRule {
	plan = cloneRule(plan)
	for i, e := range plan.Endpoints {
		if e.CredentialBinding != nil {
			continue
		}
		if p, ok := offerProvider(e, list, attached); ok {
			plan.Endpoints[i].CredentialBinding = &types.NetworkCredentialBinding{Provider: p}
		}
	}
	return plan
}

// PolicyBinding attaches a provider's credential to an endpoint of the
// policy, or removes it (Provider empty): POST .../policy/binding.
type PolicyBinding struct {
	Rule     string   `json:"rule"`
	Host     string   `json:"host"`
	Ports    []uint32 `json:"ports"`
	Path     string   `json:"path"`
	Provider string   `json:"provider"`
}

// setBinding changes the binding of the endpoint b names.
func setBinding(rules map[string]v1.NetworkPolicyRule, b PolicyBinding) (map[string]v1.NetworkPolicyRule, error) {
	r, ok := rules[b.Rule]
	if !ok {
		return nil, errCode("policy_rule_not_found", nil, "この規則はポリシーにありません")
	}
	for i, e := range r.Endpoints {
		if !strings.EqualFold(e.Host, b.Host) || !slices.Equal(slices.Sorted(slices.Values(effectivePorts(e))), slices.Sorted(slices.Values(b.Ports))) || e.Path != b.Path {
			continue
		}
		if b.Provider == "" {
			r.Endpoints[i].CredentialBinding = nil
		} else {
			if !bindable(e) {
				return nil, errCode("policy_endpoint_not_inspected", nil, "この宛先は要求を検査していません")
			}
			r.Endpoints[i].CredentialBinding = &types.NetworkCredentialBinding{Provider: b.Provider}
		}
		rules[b.Rule] = r
		return rules, nil
	}
	return nil, errCode("policy_endpoint_not_found", nil, "この宛先は規則にありません")
}

// providerPolicyRuntime changes a sandbox's policy for its providers.
// OpenShell and Fake implement it.
type providerPolicyRuntime interface {
	// AddProviderRules commits and applies the rules (a provider's
	// initial rules) under free names, unless an endpoint of the policy is
	// bound to the provider already (nothing then).
	AddProviderRules(ctx context.Context, w Sandbox, provider string, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error)
	// RemoveProvider commits and applies the policy without the endpoints
	// bound to the provider (nothing when there are none).
	RemoveProvider(ctx context.Context, w Sandbox, provider string) (PolicyChange, error)
	// SetBinding commits and applies one endpoint's binding.
	SetBinding(ctx context.Context, w Sandbox, b PolicyBinding) (PolicyChange, error)
}

// listProvidersFor lists the providers (the fake's in the fake).
func (o *OpenShell) listProvidersFor(ctx context.Context) ([]ProviderInfo, map[string][]string, error) {
	if o.providerList != nil {
		return o.providerList(ctx)
	}
	return o.ListProviders(ctx)
}

// errNoChange: the change leaves the policy as it is.
var errNoChange = errors.New("no change")

func (o *OpenShell) AddProviderRules(ctx context.Context, w Sandbox, provider string, rules map[string]v1.NetworkPolicyRule) (PolicyChange, error) {
	change, err := o.providerChange(ctx, w, "provider", "Attach provider "+provider, func(cur map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		if _, bound := withoutProvider(cur, provider); bound {
			return nil, errNoChange
		}
		for _, k := range slices.Sorted(maps.Keys(rules)) {
			r := cloneRule(rules[k])
			r.Name = freeRuleName(cur, k)
			cur[r.Name] = r
		}
		return cur, nil
	})
	if errors.Is(err, errNoChange) {
		return PolicyChange{}, nil
	}
	return change, err
}

func (o *OpenShell) RemoveProvider(ctx context.Context, w Sandbox, provider string) (PolicyChange, error) {
	change, err := o.providerChange(ctx, w, "provider", "Detach provider "+provider, func(cur map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		out, changed := withoutProvider(cur, provider)
		if !changed {
			return nil, errNoChange
		}
		return out, nil
	})
	if errors.Is(err, errNoChange) {
		return PolicyChange{}, nil
	}
	return change, err
}

func (o *OpenShell) SetBinding(ctx context.Context, w Sandbox, b PolicyBinding) (PolicyChange, error) {
	msg := fmt.Sprintf("Remove the credential from %s:%s%s of rule %s", b.Host, joinPorts(b.Ports), b.Path, b.Rule)
	if b.Provider != "" {
		msg = fmt.Sprintf("Attach provider %s's credential to %s:%s%s of rule %s", b.Provider, b.Host, joinPorts(b.Ports), b.Path, b.Rule)
	}
	return o.providerChange(ctx, w, "binding", msg, func(cur map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		return setBinding(cur, b)
	})
}

// providerChange commits and applies change, checked with the providers'
// rules first as an approval is.
func (o *OpenShell) providerChange(ctx context.Context, w Sandbox, kind, message string, change func(map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error)) (PolicyChange, error) {
	note := noteOf(ctx, message)
	note.Kind = kind
	version, err := o.changePolicy(ctx, w, note, func(cur map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		next, err := change(cloneRules(cur))
		if err != nil {
			return nil, err
		}
		if err := o.checkWithProviders(ctx, w, cur, next); err != nil {
			return nil, err
		}
		return next, nil
	})
	if err != nil {
		return PolicyChange{}, err
	}
	return o.withVersion(ctx, w, PolicyChange{Kind: kind, Version: version, At: time.Now().Unix()}), nil
}
