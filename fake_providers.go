//go:build uitest

package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
)

// The fake's providers: values are dropped, as the gateway never returns
// them. A sandbox that was started has its providers here (as OpenShell
// keeps them); before that the configuration's list applies.

type fakeProviders struct {
	list     map[string]ProviderInfo
	attached map[string][]string
	// refresh is the gateway's refresh state (by provider).
	refresh map[string]RefreshInfo
	// receipts are the attach/detach and scope changes running sandboxes
	// apply.
	receipts map[string]fakeReceipt
}

func (d *Fake) seedProviders() {
	d.mu.Lock()
	defer d.mu.Unlock()
	at := time.Now().AddDate(0, 0, -20).UTC()
	d.prov.list = map[string]ProviderInfo{}
	d.prov.attached = map[string][]string{}
	registerScopes(fakeProviderScopes())
	for _, p := range []ProviderInfo{
		{Name: "github-personal", Profile: "iwato-github", CreatedAt: at},
		// With its own profile: the scope fakeProviderScopes gives it.
		{Name: "github-work", Profile: scopedProfileID(githubProfileID, "github-work"), CreatedAt: at.AddDate(0, 0, 3)},
		{Name: "claude", Profile: claudeSubscriptionProfileID, CreatedAt: at.AddDate(0, 0, 5)},
		// Of a type Iwato no longer ships (retiredBases): the gateway keeps its profile.
		{Name: "claude-token", Profile: scopedProfileID("iwato-claude", "claude-token"), Keys: []string{claudeOAuthCredentialKey}, CreatedAt: at.AddDate(0, 0, 6)},
		{Name: "atlassian-work", Profile: atlassianProfileID, CreatedAt: at.AddDate(0, 0, 8)},
		{Name: "atlassian-oss", Profile: atlassianProfileID, CreatedAt: at.AddDate(0, 0, 9)},
		{Name: "huggingface", Profile: "iwato-huggingface", CreatedAt: at.AddDate(0, 0, 10)},
		{Name: "openrouter", Profile: scopedProfileID("iwato-openrouter", "openrouter"), CreatedAt: at.AddDate(0, 0, 10)},
		{Name: "grok-subscription", Profile: grokSubscriptionProfileID, CreatedAt: at.AddDate(0, 0, 11)},
		{Name: "runpod", Profile: scopedProfileID("iwato-runpod", "runpod"), CreatedAt: at.AddDate(0, 0, 11)},
		// Of a profile imported with OpenShell's CLI (not Iwato's).
		{Name: "openai", Profile: "openai", Keys: []string{"OPENAI_API_KEY"}, CreatedAt: at.AddDate(0, 0, 12)},
	} {
		if p.Keys == nil {
			p.Keys = providerKeys(p)
		}
		d.prov.list[p.Name] = p
	}
	now := time.Now().UTC().Truncate(time.Second)
	d.prov.refresh = map[string]RefreshInfo{
		"claude":            {Status: "refreshed", LastRefreshAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(50 * time.Minute), NextRefreshAt: now.Add(45 * time.Minute)},
		"atlassian-work":    {Status: "refreshed", LastRefreshAt: now.Add(-20 * time.Minute), ExpiresAt: now.Add(40 * time.Minute), NextRefreshAt: now.Add(35 * time.Minute)},
		"grok-subscription": {Status: "refreshed", LastRefreshAt: now.Add(-5 * time.Minute), ExpiresAt: now.Add(10 * time.Minute), NextRefreshAt: now.Add(5 * time.Minute)},
		// The owner revoked it at Atlassian: 再接続.
		"atlassian-oss": {Status: "reauthorization_required", Recovery: "reauthorize", FailureCode: "oauth_invalid_grant", LastError: "refresh grant is no longer valid",
			LastRefreshAt: now.Add(-26 * time.Hour), ExpiresAt: now.Add(-25 * time.Hour), LastErrorAt: now.Add(-25*time.Hour - 5*time.Minute)},
	}
}

func (d *Fake) ConfigureRefresh(_ context.Context, provider, key string, material map[string]string, _ []string, expiresAt time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.prov.list[provider]; !ok || key != oauthCredentialKey(p.Profile) {
		return fmt.Errorf("provider %s not found", provider)
	}
	if material["client_id"] == "" || material["refresh_token"] == "" {
		return fmt.Errorf("missing refresh material")
	}
	if d.prov.refresh == nil {
		d.prov.refresh = map[string]RefreshInfo{}
	}
	d.prov.refresh[provider] = RefreshInfo{Status: "configured", ExpiresAt: expiresAt}
	return nil
}

func (d *Fake) DeleteRefresh(_ context.Context, provider, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.prov.refresh, provider)
	return nil
}

func (d *Fake) RotateRefresh(_ context.Context, provider, _ string) (RefreshInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.prov.refresh[provider]; !ok {
		return RefreshInfo{}, fmt.Errorf("provider refresh state not found")
	}
	now := time.Now().UTC().Truncate(time.Second)
	i := RefreshInfo{Status: "refreshed", LastRefreshAt: now, ExpiresAt: now.Add(time.Hour), NextRefreshAt: now.Add(55 * time.Minute)}
	d.prov.refresh[provider] = i
	return i, nil
}

func (d *Fake) RefreshStatus(_ context.Context, provider, _ string) (RefreshInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i, ok := d.prov.refresh[provider]
	if !ok {
		return RefreshInfo{Status: "not_configured"}, nil
	}
	return i, nil
}

func (d *Fake) ListProviders(context.Context) ([]ProviderInfo, map[string][]string, error) {
	// ListProviders and the sandboxes' ListAll.
	d.gatewayCall("ListProviders")
	d.gatewayCall("ListSandboxes")
	d.mu.Lock()
	defer d.mu.Unlock()
	var list []ProviderInfo
	for _, p := range d.prov.list {
		list = append(list, p)
	}
	attached := map[string][]string{}
	for name, l := range d.prov.attached {
		attached[name] = slices.Clone(l)
	}
	return list, attached, nil
}

func (d *Fake) CreateProvider(_ context.Context, name, profile string, credentials map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.prov.list == nil {
		d.prov.list = map[string]ProviderInfo{}
	}
	if _, ok := d.prov.list[name]; ok {
		return fmt.Errorf("provider %s already exists", name)
	}
	p := ProviderInfo{Name: name, Profile: profile, CreatedAt: time.Now().UTC()}
	for k := range credentials {
		p.Keys = append(p.Keys, k)
	}
	d.prov.list[name] = p
	return nil
}

func (d *Fake) UpdateProvider(_ context.Context, name string, _ map[string]string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.prov.list[name]; !ok {
		return fmt.Errorf("provider %s not found", name)
	}
	return nil
}

func (d *Fake) DeleteProvider(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for sb, l := range d.prov.attached {
		if slices.Contains(l, name) {
			return fmt.Errorf("provider %s is attached to sandbox %s", name, sb)
		}
	}
	delete(d.prov.list, name)
	delete(d.prov.refresh, name)
	return nil
}

func (d *Fake) AttachProvider(_ context.Context, sandbox, provider string) (bool, string, error) {
	return d.changeProvider(sandbox, provider, true)
}

func (d *Fake) DetachProvider(_ context.Context, sandbox, provider string) (bool, string, error) {
	return d.changeProvider(sandbox, provider, false)
}

// fakeApplyDelay is how long a running fake sandbox takes to apply a
// provider change (OpenShell's supervisor polls about every 10 s).
var fakeApplyDelay = 4 * time.Second

func (d *Fake) changeProvider(sandbox, provider string, attach bool) (bool, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started[sandbox] && !d.stopped[sandbox] {
		return false, "", nil
	}
	if d.prov.attached == nil {
		d.prov.attached = map[string][]string{}
	}
	l := slices.DeleteFunc(slices.Clone(d.prov.attached[sandbox]), func(n string) bool { return n == provider })
	if attach {
		l = append(l, provider)
	}
	prev := d.prov.attached[sandbox]
	d.prov.attached[sandbox] = l
	if attach {
		// As the gateway's attachment preflight: the composed policy may
		// not be ambiguous.
		revs := d.fakePolicy(sandbox)
		if err := fakeValidate(d.fakeCompose(sandbox, revs[len(revs)-1].rules)); err != nil {
			d.prov.attached[sandbox] = prev
			return false, "", err
		}
	}
	if !d.started[sandbox] {
		return true, "", nil
	}
	if d.prov.receipts == nil {
		d.prov.receipts = map[string]fakeReceipt{}
	}
	id := "fake-" + randomHex(8)
	d.prov.receipts[id] = fakeReceipt{at: time.Now(), attach: attach}
	return true, id, nil
}

type fakeReceipt struct {
	at     time.Time
	attach bool
}

func (d *Fake) ProviderReadiness(_ context.Context, _, _, receipt string) (string, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.prov.receipts[receipt]
	switch {
	case !ok:
		return "gone", "", nil
	case time.Since(r.at) < fakeApplyDelay:
		return "pending", "", nil
	case r.attach:
		return "ready", "", nil
	default:
		return "revoked", "", nil
	}
}

// TerminalStarted tells when the fake terminal started.
func (d *Fake) TerminalStarted(w Sandbox, name string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.termStarted[w.Name+"/"+name]
	return at, ok
}

// fakeGatewayProfile is a profile imported with OpenShell's CLI:
// providers/openai.yaml of OpenShell v0.1.4 (Apache-2.0).
const fakeGatewayProfile = `id: openai
display_name: OpenAI
description: OpenAI inference API
category: inference
inference_capable: true
credentials:
  - name: api_key
    description: OpenAI API key
    env_vars: [OPENAI_API_KEY]
    required: true
    auth_style: bearer
    header_name: authorization
discovery:
  credentials: [api_key]
endpoints:
  - host: api.openai.com
    port: 443
    protocol: rest
    access: read-write
    enforcement: enforce
binaries: [/usr/bin/curl, /usr/local/bin/curl]
`

// ProviderProfile is the fake gateway's profile: Iwato's (as imported)
// and the CLI-imported openai.
func (d *Fake) ProviderProfile(_ context.Context, id string) (*pb.ProviderProfile, error) {
	d.gatewayCall("GetProviderProfile")
	if p, ok := profileByID(id); ok {
		return importedProfile(p), nil
	}
	if id != "openai" {
		return nil, nil
	}
	m, err := parseProfileYAML([]byte(fakeGatewayProfile))
	if err != nil {
		return nil, err
	}
	m.Source, m.Scope, m.ResourceVersion = "providers/openai.yaml", "platform", 1
	return m, nil
}

// fakeProviderScopes are the fake configuration's scopes: github-work
// writes to one repository, openrouter and runpod are read-only (the
// others have no scope: they use the shared built-in profiles).
func fakeProviderScopes() map[string]ProviderScope {
	return map[string]ProviderScope{
		"github-work":  {Base: githubProfileID, Mode: scopeRepos, Repos: []string{"mtsmfm/iwato"}},
		"openrouter":   {Base: "iwato-openrouter", Mode: scopeReadOnly},
		"runpod":       {Base: "iwato-runpod", Mode: scopeReadOnly},
		"claude-token": {Base: "iwato-claude", Mode: scopeReadOnly},
	}
}

// PutProviderProfile takes the profile as the gateway would (Iwato's
// registry holds what ProviderProfile returns); a profile with the
// description "fake: reject" is refused, as the gateway refuses one that
// does not fit an attached sandbox.
func (d *Fake) PutProviderProfile(_ context.Context, m *pb.ProviderProfile) error {
	if m.Description == "fake: reject" {
		return fmt.Errorf("endpoints: update would leave static provider credentials without an authorized endpoint on sandbox 'dev'")
	}
	return nil
}

func (d *Fake) DeleteProviderProfile(context.Context, string) error { return nil }

// ObserveProvider follows a scope change: applied after fakeApplyDelay.
func (d *Fake) ObserveProvider(_ context.Context, sandbox, _ string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.prov.receipts == nil {
		d.prov.receipts = map[string]fakeReceipt{}
	}
	id := "fake-" + randomHex(8)
	d.prov.receipts[id] = fakeReceipt{at: time.Now(), attach: true}
	return id, nil
}
