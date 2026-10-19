package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
)

// Providers are OpenShell's: a named credential of a profile (provider
// type) that the gateway keeps, attached to sandboxes, which get it as
// placeholders that OpenShell's proxy replaces on the profile's endpoints.
// Iwato creates them from its own profiles (providers_profiles.go), lists
// them without values, and attaches them per sandbox: when it is created
// (Sandbox.Providers, which OpenShell gets when it creates the sandbox at
// the first start) or later.

// ProviderInfo is a provider as the gateway lists it: never its values.
type ProviderInfo struct {
	Name    string
	Profile string
	// Keys are the credentials' names (the environment variables).
	Keys      []string
	CreatedAt time.Time
}

// providerRuntime manages the gateway's providers. OpenShell and Fake
// implement it.
type providerRuntime interface {
	// ListProviders lists the providers and, for each sandbox OpenShell
	// has, the providers attached to it.
	ListProviders(context.Context) ([]ProviderInfo, map[string][]string, error)
	CreateProvider(ctx context.Context, name, profile string, credentials map[string]string) error
	UpdateProvider(ctx context.Context, name string, credentials map[string]string) error
	DeleteProvider(ctx context.Context, name string) error
	// AttachProvider and DetachProvider change a sandbox OpenShell has;
	// false when it has none of that name (not created yet). They return
	// once the gateway took the change, with its receipt when the sandbox
	// runs and applies it later.
	AttachProvider(ctx context.Context, sandbox, provider string) (bool, string, error)
	DetachProvider(ctx context.Context, sandbox, provider string) (bool, string, error)
	// ProviderReadiness follows a receipt: pending, ready, revoked,
	// superseded, withheld, failed (with a reason), or gone.
	ProviderReadiness(ctx context.Context, sandbox, provider, receipt string) (state, reason string, err error)
}

var providerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// maxProviders bounds a sandbox's providers: OpenShell's MAX_PROVIDERS.
const maxProviders = 32

func validProviderList(names []string) error {
	if len(names) > maxProviders {
		return fmt.Errorf("at most %d providers", maxProviders)
	}
	for i, n := range names {
		if !providerNamePattern.MatchString(n) {
			return fmt.Errorf("invalid provider name %q", n)
		}
		if slices.Contains(names[:i], n) {
			return fmt.Errorf("provider %q is listed twice", n)
		}
	}
	return nil
}

// credentialValue accepts a token: printable ASCII without spaces.
func credentialValue(v string) error {
	if v == "" {
		return errCode("provider_value_empty", nil, "値を入力してください")
	}
	if len(v) > 8192 {
		return errCode("provider_value_long", nil, "値が長すぎます")
	}
	for i := 0; i < len(v); i++ {
		if v[i] <= ' ' || v[i] > '~' {
			return errCode("provider_value_chars", nil, "値に空白や使えない文字が含まれています")
		}
	}
	return nil
}

// profileCredentials turns the values the owner entered (by the profile's
// credential names) into the provider's credentials (by environment
// variable). Every credential is required.
func profileCredentials(p providerProfile, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for name := range values {
		if !slices.ContainsFunc(p.Credentials, func(c profileCredential) bool { return c.Name == name }) {
			return nil, errCode("provider_value_unknown", map[string]any{"profile": p.Label, "name": name}, fmt.Sprintf("%s にない値です: %s", p.Label, name))
		}
	}
	for _, c := range p.Credentials {
		v := strings.TrimSpace(values[c.Name])
		if err := credentialValue(v); err != nil {
			var coded *codedError
			if errors.As(err, &coded) {
				return nil, errCode(coded.code, map[string]any{"label": c.Label}, c.Label+": "+coded.msg)
			}
			return nil, fmt.Errorf("%s: %w", c.Label, err)
		}
		for _, env := range c.Env {
			out[env] = v
		}
	}
	return out, nil
}

// providerKeys are the environment variables a provider gives a sandbox:
// its credentials' names, or, when the gateway does not list them, its
// profile's.
func providerKeys(p ProviderInfo) []string {
	if len(p.Keys) > 0 {
		return p.Keys
	}
	if prof, ok := profileByID(p.Profile); ok {
		var keys []string
		for _, c := range prof.Credentials {
			keys = append(keys, c.Env...)
		}
		return keys
	}
	return nil
}

// providerConflict is the provider in attached that sets an environment
// variable of adding (OpenShell refuses two such on a sandbox): a sandbox
// gets at most one GitHub provider, one Claude provider.
func providerConflict(list []ProviderInfo, attached []string, adding string) string {
	find := func(name string) (ProviderInfo, bool) {
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
		if i < 0 {
			return ProviderInfo{}, false
		}
		return list[i], true
	}
	a, ok := find(adding)
	if !ok {
		return ""
	}
	keys := providerKeys(a)
	for _, name := range attached {
		if name == adding {
			continue
		}
		b, ok := find(name)
		if !ok {
			continue
		}
		if b.Profile == a.Profile || slices.ContainsFunc(providerKeys(b), func(k string) bool { return slices.Contains(keys, k) }) {
			return name
		}
	}
	return ""
}

// checkProviders accepts providers for one sandbox: each exists, and no two
// set the same variable.
func checkProviders(list []ProviderInfo, names []string) error {
	for i, n := range names {
		if !slices.ContainsFunc(list, func(p ProviderInfo) bool { return p.Name == n }) {
			return errCode("provider_missing", map[string]any{"name": n}, fmt.Sprintf("プロバイダー %s がありません", n))
		}
		if other := providerConflict(list, names[:i], n); other != "" {
			return conflictError(list, n, other)
		}
	}
	return nil
}

func conflictError(list []ProviderInfo, adding, other string) error {
	label := "同じ種類"
	for _, p := range list {
		if p.Name == adding {
			if prof, ok := profileByID(p.Profile); ok {
				label = prof.Label
			}
		}
	}
	return errCode("provider_one_per_sandbox", map[string]any{"profile": label, "other": other, "adding": adding}, fmt.Sprintf("%s のプロバイダーは 1 つのサンドボックスに 1 つまでです (%s と %s は同じ環境変数を使います)。先に %s を外してください", label, other, adding, other))
}

// ProviderView is a provider for the page.
type ProviderView struct {
	Name      string   `json:"name"`
	Profile   string   `json:"profile"`
	Keys      []string `json:"keys"`
	Attached  []string `json:"attached"`
	CreatedAt string   `json:"createdAt,omitempty"`
	// Refresh is the gateway's refresh, for the profiles Iwato connects
	// by OAuth.
	Refresh *RefreshView `json:"refresh,omitempty"`
	// Applying are the sandboxes that have not applied an attach or
	// detach of the provider yet, or could not.
	Applying []ApplyView `json:"applying"`
	// Base is the built-in profile of a provider with its own profile, and
	// Scope its access scope (providers_scope.go).
	Base  string     `json:"base,omitempty"`
	Scope *ScopeView `json:"scope,omitempty"`
}

// ApplyView is a provider change a sandbox is applying: state pending,
// withheld or failed.
type ApplyView struct {
	Sandbox string `json:"sandbox"`
	Attach  bool   `json:"attach"`
	Scope   bool   `json:"scope,omitempty"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// providerApplying asks OpenShell about the pending provider changes and
// forgets those the sandboxes applied (or that a later change replaced).
// It returns what remains, by provider.
func (s *Server) providerApplying(ctx context.Context, rt providerRuntime) map[string][]ApplyView {
	type result struct {
		sandbox, provider, receipt, state, reason string
	}
	var results []result
	for _, sb := range s.sandboxList() {
		for _, a := range sb.ProviderApplying {
			if a.State != "" {
				continue
			}
			state, reason, err := rt.ProviderReadiness(ctx, sb.Name, a.Provider, a.Receipt)
			if err != nil {
				log.Printf("sandbox %s: provider %s: readiness: %v", sb.Name, a.Provider, english(err))
				continue
			}
			switch state {
			case "ready", "revoked", "superseded", "gone":
				results = append(results, result{sb.Name, a.Provider, a.Receipt, "", ""})
			case "withheld", "failed":
				log.Printf("sandbox %s: provider %s: OpenShell could not apply the change (%s, %s)", sb.Name, a.Provider, state, reason)
				results = append(results, result{sb.Name, a.Provider, a.Receipt, state, safeCode(reason)})
			}
		}
	}
	if len(results) > 0 {
		err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
			for i := range list {
				for _, r := range results {
					if list[i].Name != r.sandbox {
						continue
					}
					j := slices.IndexFunc(list[i].ProviderApplying, func(a ProviderApply) bool { return a.Provider == r.provider && a.Receipt == r.receipt })
					if j < 0 {
						continue
					}
					if r.state == "" {
						list[i].ProviderApplying = slices.Delete(list[i].ProviderApplying, j, j+1)
					} else {
						list[i].ProviderApplying[j].State, list[i].ProviderApplying[j].Reason = r.state, r.reason
					}
				}
				if len(list[i].ProviderApplying) == 0 {
					list[i].ProviderApplying = nil
				}
			}
			return list, nil
		})
		if err != nil {
			log.Printf("provider changes: %v", english(err))
		}
	}
	out := map[string][]ApplyView{}
	for _, sb := range s.sandboxList() {
		for _, a := range sb.ProviderApplying {
			st := a.State
			if st == "" {
				st = "pending"
			}
			out[a.Provider] = append(out[a.Provider], ApplyView{Sandbox: sb.Name, Attach: a.Attach, Scope: a.Scope, State: st, Reason: a.Reason})
		}
	}
	return out
}

// sandboxProviders are the providers attached to each sandbox: OpenShell's
// for the sandboxes it has, the configuration's for those not created yet.
func sandboxProviders(sandboxes []Sandbox, inOpenShell map[string][]string) map[string][]string {
	out := map[string][]string{}
	for name, list := range inOpenShell {
		out[name] = list
	}
	for _, sb := range sandboxes {
		if _, ok := inOpenShell[sb.Name]; !ok {
			out[sb.Name] = sb.Providers
		}
	}
	return out
}

func (s *Server) providerState(ctx context.Context, rt providerRuntime) ([]ProviderInfo, map[string][]string, error) {
	list, inOpenShell, err := rt.ListProviders(ctx)
	if err != nil {
		return nil, nil, err
	}
	return list, sandboxProviders(s.sandboxList(), inOpenShell), nil
}

// handleProviders serves GET /api/providers (the profiles and the
// providers, without values), POST /api/providers (create),
// POST /api/providers/NAME/update, POST /api/providers/NAME/delete and,
// for the gateway-refreshed ones, POST /api/providers/NAME/rotate.
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/providers"), "/")
	rt, hasRT := s.rt.(providerRuntime)
	if rest == "" && r.Method == http.MethodGet {
		if !s.require(w, r, false) {
			return
		}
		resp := map[string]any{"profiles": append(slices.Clone(providerProfiles), customProfiles()...), "providers": []ProviderView{}}
		if !hasRT {
			resp["error"] = "providers unavailable"
			jsonResponse(w, 200, resp)
			return
		}
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		list, attached, err := s.providerState(ctx, rt)
		if err != nil {
			resp["error"] = safeCLIError(err)
			jsonResponse(w, 200, resp)
			return
		}
		applying := s.providerApplying(ctx, rt)
		views := []ProviderView{}
		for _, p := range list {
			v := ProviderView{Name: p.Name, Profile: p.Profile, Keys: append([]string{}, p.Keys...), Attached: []string{}, Applying: append([]ApplyView{}, applying[p.Name]...)}
			if base := scopedBaseOf(p); base != "" {
				sc, ok := s.providerScope(p.Name)
				v.Base, v.Scope = base, scopeView(base, sc, ok)
			}
			if !p.CreatedAt.IsZero() {
				v.CreatedAt = p.CreatedAt.UTC().Format(time.RFC3339)
			}
			for sb, names := range attached {
				if slices.Contains(names, p.Name) {
					v.Attached = append(v.Attached, sb)
				}
			}
			sort.Strings(v.Attached)
			sort.Strings(v.Keys)
			if base := providerBase(p); profileOAuth(base) || base == s.oauthProfile() || base == s.xaiProfile() {
				if ref, ok := rt.(providerRefresher); ok {
					if info, err := ref.RefreshStatus(ctx, p.Name, s.refreshKey(providerBase(p))); err != nil {
						v.Refresh = &RefreshView{Status: "unknown", Error: truncate(safeCLIError(err), 300)}
					} else {
						v.Refresh = refreshView(info)
					}
				}
			}
			views = append(views, v)
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
		resp["providers"] = views
		jsonResponse(w, 200, resp)
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	var name, op string
	if rest != "" {
		var ok bool
		if name, op, ok = strings.Cut(rest, "/"); !ok || (op != "update" && op != "delete" && op != "rotate" && op != "scope") {
			fail(w, 404, "unknown provider route")
			return
		}
		if !providerNamePattern.MatchString(name) {
			fail(w, 400, "invalid provider name")
			return
		}
	}
	if op == "scope" {
		s.handleProviderScope(w, r, name)
		return
	}
	var body struct {
		Name    string            `json:"name"`
		Profile string            `json:"profile"`
		Values  map[string]string `json:"values"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid request")
		return
	}
	if !hasRT {
		fail(w, 501, "providers unavailable")
		return
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	switch op {
	case "":
		body.Name = strings.TrimSpace(body.Name)
		if body.Name != "" && !providerNamePattern.MatchString(body.Name) {
			failCode(w, 400, "name_invalid", map[string]any{"max": 63}, "名前は英小文字で始まり、英小文字・数字・ハイフンの 63 文字以内にしてください")
			return
		}
		prof, ok := profileByID(body.Profile)
		if !ok {
			fail(w, 400, "unknown profile")
			return
		}
		if prof.OAuth {
			connect := "Atlassian に接続"
			if prof.OAuthFlow == "xai" {
				connect = "xAI に接続"
			} else if prof.OAuthFlow == "claude" {
				connect = "Claude にログイン"
			}
			fail(w, 400, prof.Label+" は「"+connect+"」で追加します")
			return
		}
		creds, err := profileCredentials(prof, body.Values)
		if err != nil {
			failErr(w, 400, err)
			return
		}
		var created string
		if store, ok := s.rt.(profileStore); ok && prof.Scoped {
			// Its own profile, for its scope (providers_scope.go).
			created, err = s.createScopedProvider(ctx, rt, store, body.Name, prof, creds)
		} else {
			created, err = createProviderNamed(ctx, rt, body.Name, prof.ID, creds)
		}
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "name": created})
		return
	case "update":
		list, _, err := rt.ListProviders(ctx)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
		if i < 0 {
			fail(w, 404, "unknown provider")
			return
		}
		prof, ok := profileByID(list[i].Profile)
		if !ok {
			failCode(w, 409, "provider_not_iwato", nil, "Iwato のプロファイルではないプロバイダーは変更できません (openshell provider update を使ってください)")
			return
		}
		if body.Name != "" || body.Profile != "" {
			fail(w, 400, "only the values can be changed")
			return
		}
		if prof.OAuth {
			failCode(w, 409, "provider_value_refreshed", nil, "ゲートウェイが更新している値は変更できません。「再接続」を使ってください")
			return
		}
		creds, err := profileCredentials(prof, body.Values)
		if err != nil {
			failErr(w, 400, err)
			return
		}
		if err := rt.UpdateProvider(ctx, name, creds); err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
	case "rotate":
		// The gateway mints a new access token now (as `openshell provider
		// refresh rotate`): checks the grant without waiting for the expiry.
		list, _, err := rt.ListProviders(ctx)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name })
		if i < 0 {
			fail(w, 404, "unknown provider")
			return
		}
		ref, ok := rt.(providerRefresher)
		if base := providerBase(list[i]); !ok || !(profileOAuth(base) || base == s.oauthProfile() || base == s.xaiProfile()) {
			failCode(w, 409, "provider_not_refreshed", nil, "ゲートウェイが更新するプロバイダーではありません")
			return
		}
		key := s.refreshKey(providerBase(list[i]))
		info, err := ref.RotateRefresh(ctx, name, key)
		if err != nil {
			log.Printf("provider %s: rotate: %v", name, english(err))
			if st, serr := ref.RefreshStatus(ctx, name, key); serr == nil {
				state := strings.Trim(safeCode(st.Status)+" "+safeCode(st.FailureCode), " ")
				jsonResponse(w, 409, map[string]any{"error": "更新できませんでした (" + state + ")", "code": "provider_refresh_failed", "params": map[string]any{"state": state}, "refresh": refreshView(st)})
				return
			}
			failCode(w, 409, "provider_refresh_error", map[string]any{"detail": safeCLIError(err)}, "更新できませんでした: "+safeCLIError(err))
			return
		}
		if !info.ExpiresAt.IsZero() && !info.LastRefreshAt.IsZero() {
			log.Printf("provider %s: refreshed; access token lifetime %s", name, info.ExpiresAt.Sub(info.LastRefreshAt).Round(time.Second))
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "refresh": refreshView(info)})
		return
	case "delete":
		// As OpenShell: a provider attached to a sandbox is not deleted;
		// the owner detaches it first, knowing which sandboxes lose it.
		list, attached, err := s.providerState(ctx, rt)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		var users []string
		for sb, names := range attached {
			if slices.Contains(names, name) {
				users = append(users, sb)
			}
		}
		if len(users) > 0 {
			sort.Strings(users)
			failCode(w, 409, "provider_attached", map[string]any{"sandboxes": users}, "サンドボックス "+strings.Join(users, ", ")+" に付いています。先に外してください")
			return
		}
		if err := rt.DeleteProvider(ctx, name); err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		if i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == name }); i >= 0 {
			s.forgetScope(ctx, list[i])
		}
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// profileOAuth: the gateway refreshes the credential of the profile's
// providers.
func profileOAuth(id string) bool {
	p, _ := profileByID(id)
	return p.OAuth
}

// refreshKey is the credential the gateway refreshes for a provider of
// the profile (the tests' copies of the OAuth profiles included).
func (s *Server) refreshKey(profile string) string {
	if profile == s.xaiProfile() {
		return grokOAuthCredentialKey
	}
	return oauthCredentialKey(profile)
}

// handleSandboxProviders attaches a provider to the sandbox or detaches it
// (POST /api/sandboxes/NAME/providers/attach|detach with {"provider"}).
// It answers once the gateway took the change; a running sandbox applies
// it on its next settings poll, which the provider list follows
// (ProviderView.Applying, 反映待ち). Processes that run already keep their
// environment: the change reaches terminals and processes started
// afterwards.
func (s *Server) handleSandboxProviders(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost || len(parts) != 3 || (parts[2] != "attach" && parts[2] != "detach" && parts[2] != "fix") {
		fail(w, 405, "method/operation not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, true) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
		// Mode (fix only): "remove" the conflicting endpoints, or
		// "enforce" the audited ones (keeping their rules).
		Mode string `json:"mode"`
		// Confirm (attach only): the token of the owner's confirmation
		// that rules let the credential go beyond the provider's scope.
		Confirm string `json:"confirm"`
	}
	if decode(w, r, &body) != nil || !providerNamePattern.MatchString(body.Provider) || (body.Mode != "" && body.Mode != "remove" && body.Mode != "enforce") {
		fail(w, 400, "invalid provider name")
		return
	}
	rt, hasRT := s.rt.(providerRuntime)
	if !hasRT {
		fail(w, 501, "providers unavailable")
		return
	}
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	defer s.lockPolicy(sb.Name)()
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	list, attached, err := s.providerState(ctx, rt)
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	current := attached[sb.Name]
	if parts[2] == "fix" {
		s.fixCredentialConflicts(w, ctx, sb, list, body.Provider, body.Mode == "enforce")
		return
	}
	attach := parts[2] == "attach"
	running := s.rt.Status(ctx, sb).State == "running"
	var applied bool
	var receipt string
	if attach {
		if err := checkProviders(list, []string{body.Provider}); err != nil {
			failErr(w, 409, err)
			return
		}
		if other := providerConflict(list, current, body.Provider); other != "" {
			failErr(w, 409, conflictError(list, body.Provider, other))
			return
		}
		if !slices.Contains(current, body.Provider) && len(current) >= maxProviders {
			failCode(w, 409, "provider_too_many", map[string]any{"max": maxProviders}, fmt.Sprintf("プロバイダーは %d 個までです", maxProviders))
			return
		}
		// A rule approved before that carries the credential uninspected
		// would keep the sandbox from provisioning (credential_conflicts.go).
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == body.Provider })
		conflicts, cerr := s.conflictsForAttach(ctx, sb, list[i])
		if cerr != nil {
			failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(cerr)}, "ポリシーを確かめられません: "+safeCLIError(cerr))
			return
		}
		// An endpoint the provider's rule would make ambiguous (another
		// enforcement, say) makes OpenShell refuse the attach.
		ambiguities, aerr := s.attachAmbiguities(ctx, sb, list[i])
		if aerr != nil {
			failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(aerr)}, "ポリシーを確かめられません: "+safeCLIError(aerr))
			return
		}
		if len(conflicts) > 0 || len(ambiguities) > 0 {
			failCheck(w, attachRefusal(body.Provider, conflicts, ambiguities))
			return
		}
		// ADR 0006: a rule that lets the credential reach more than the
		// provider's own rule would widen its scope once attached: only
		// with the owner's confirmation.
		if beyond, berr := s.beyondScopeForAttach(ctx, sb, list[i]); berr != nil {
			failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(berr)}, "ポリシーを確かめられません: "+safeCLIError(berr))
			return
		} else if token := confirmToken(sb.Name, "attach", body.Provider, beyond); len(beyond) > 0 && body.Confirm != token {
			var where []string
			for _, b := range beyond {
				where = append(where, b.Rule+": "+b.Host)
			}
			jsonResponse(w, 409, map[string]any{"code": "provider_attach_beyond_scope", "params": map[string]any{"provider": body.Provider, "rules": where},
				"error":   body.Provider + " を付けると、ポリシーの規則がその認証情報をプロバイダーの範囲より広く届けます: " + strings.Join(where, "、"),
				"confirm": token})
			return
		}
		applied, receipt, err = rt.AttachProvider(ctx, sb.Name, body.Provider)
	} else {
		applied, receipt, err = rt.DetachProvider(ctx, sb.Name, body.Provider)
	}
	// Saved but not applied in time still changes what restarts get.
	if err != nil && !applied {
		s.failGateway(ctx, w, sb, err)
		return
	}
	changed := applied && running
	// The configuration keeps the list for creating the sandbox in
	// OpenShell (its first start, or again after a failure).
	serr := s.updateSandboxes(func(sandboxes []Sandbox) ([]Sandbox, error) {
		for i := range sandboxes {
			if sandboxes[i].Name != sb.Name {
				continue
			}
			p := slices.DeleteFunc(slices.Clone(sandboxes[i].Providers), func(n string) bool { return n == body.Provider })
			if attach {
				p = append(p, body.Provider)
			}
			if len(p) == 0 {
				p = nil
			}
			sandboxes[i].Providers = p
			if changed {
				sandboxes[i].ProvidersChangedAt = time.Now().UTC()
			}
			// The running sandbox applies it on its own: follow the receipt
			// (a newer change of the provider replaces the older).
			pending := slices.DeleteFunc(slices.Clone(sandboxes[i].ProviderApplying), func(a ProviderApply) bool { return a.Provider == body.Provider })
			if changed && receipt != "" {
				pending = append(pending, ProviderApply{Provider: body.Provider, Attach: attach, Receipt: receipt, At: time.Now().UTC()})
			}
			if len(pending) == 0 {
				pending = nil
			}
			sandboxes[i].ProviderApplying = pending
		}
		return sandboxes, nil
	})
	if serr != nil {
		failCode(w, 500, "provider_config_unsaved", map[string]any{"detail": serr.Error()}, "OpenShell では変更しましたが、設定に保存できませんでした: "+serr.Error())
		return
	}
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	// restart: the running sandbox's processes from before lack the change.
	jsonResponse(w, 200, map[string]bool{"ok": true, "restart": changed})
}

// clearProvidersChanged forgets a provider change once the sandbox
// stopped or started again.
func (s *Server) clearProvidersChanged(name string) {
	if sb, ok := s.lookupSandbox(name); !ok || (sb.ProvidersChangedAt.IsZero() && len(sb.ProviderApplying) == 0) {
		return
	}
	err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for i := range list {
			if list[i].Name == name {
				// A start gives the sandbox all its providers at once.
				list[i].ProvidersChangedAt = time.Time{}
				list[i].ProviderApplying = nil
			}
		}
		return list, nil
	})
	if err != nil {
		log.Printf("sandbox %s: %v", name, english(err))
	}
}

// terminalStarts tells when each of a sandbox's terminals started (false
// when unknown: main without a terminal daemon, the sandbox's main
// process). OpenShell and Fake implement it.
type terminalStarts interface {
	TerminalStarted(Sandbox, string) (time.Time, bool)
}

// providerOutdated are the terminals that run since before a provider
// change: those started before it, or at an unknown time.
func (s *Server) providerOutdated(sb Sandbox, names []string) []string {
	out := []string{}
	if sb.ProvidersChangedAt.IsZero() {
		return out
	}
	starts, _ := s.rt.(terminalStarts)
	for _, n := range names {
		if starts != nil {
			if at, ok := starts.TerminalStarted(sb, n); ok && !at.Before(sb.ProvidersChangedAt) {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// fixCredentialConflicts removes from the sandbox's policy the endpoints
// that conflict with the provider (POST .../providers/fix), so that it can
// be attached: the provider adds its own inspected rule for its programs.
// Programs of the removed endpoints lose them; the page says which. An
// endpoint the provider's rule would make ambiguous is removed too. With
// enforce, one that only audits where the provider enforces is switched
// to enforce instead, with the requests it allows now.
func (s *Server) fixCredentialConflicts(w http.ResponseWriter, ctx context.Context, sb Sandbox, list []ProviderInfo, provider string, enforce bool) {
	i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == provider })
	if i < 0 {
		failCode(w, 404, "provider_missing", map[string]any{"name": provider}, "プロバイダー "+provider+" がありません")
		return
	}
	pol, ok := s.rt.(policyRuntime)
	if !ok {
		fail(w, 501, "policy unavailable")
		return
	}
	conflicts, err := s.conflictsForAttach(ctx, sb, list[i])
	if err != nil {
		failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
		return
	}
	ambiguities, err := s.attachAmbiguities(ctx, sb, list[i])
	if err != nil {
		failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
		return
	}
	if enforce && slices.ContainsFunc(ambiguities, func(v AmbiguityView) bool { return v.Fix != "enforce" }) {
		failCode(w, 409, "provider_fix_enforce_insufficient", nil, "遮断に切り替えるだけでは直らない宛先があります。規則から外してください")
		return
	}
	var edits []PolicyEdit
	for _, c := range conflicts {
		edits = append(edits, PolicyEdit{Op: "remove-endpoint", Rule: c.Rule, Host: c.Host, Ports: c.Ports})
	}
	for _, v := range ambiguities {
		if isProviderRule(v.Rule) {
			failCode(w, 409, "provider_rules_disagree", map[string]any{"detail": v.Raw}, "プロバイダーどうしの規則が食い違います: "+v.Raw)
			return
		}
		e := PolicyEdit{Op: "remove-endpoint", Rule: v.Rule, Host: v.Host, Ports: v.EndpointPorts}
		if enforce {
			e.Op, e.Keep = "enforce", true
		}
		if !slices.ContainsFunc(edits, func(x PolicyEdit) bool {
			return x.Rule == e.Rule && strings.EqualFold(x.Host, e.Host) && slices.Equal(x.Ports, e.Ports)
		}) {
			edits = append(edits, e)
		}
	}
	for _, edit := range edits {
		change, err := pol.EditPolicy(ctx, sb, edit)
		if err != nil {
			// The gateway's ambiguity refusal: its pairs, which the page
			// puts in detail's place.
			msg, extra := safeCLIError(err), map[string]any{}
			if j, ok := translateGatewayError(msg, s.ruleProviders(ctx, sb)); ok {
				msg, extra["gatewayAmbiguities"], extra["raw"] = j.text, j.pairs, msg
			}
			params := map[string]any{"rule": edit.Rule, "host": edit.Host, "detail": msg}
			if edit.Op == "enforce" {
				failCodeWith(w, 409, "provider_fix_enforce_failed", params, "規則 "+edit.Rule+" の "+edit.Host+" を遮断に切り替えられませんでした: "+msg, extra)
			} else {
				failCodeWith(w, 409, "provider_fix_remove_failed", params, "規則 "+edit.Rule+" の "+edit.Host+" を外せませんでした: "+msg, extra)
			}
			return
		}
		s.recordPolicyChange(sb.Name, change)
		log.Printf("sandbox %s: %s %s:%s of rule %s (for provider %s)", sb.Name, edit.Op, edit.Host, joinPorts(edit.Ports), edit.Rule, provider)
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "removed": conflicts, "fixed": ambiguities})
}

// providerProfileReader reads a profile the gateway holds (in Iwato's
// workspace, or the platform's). OpenShell and Fake implement it; nil
// when there is none of that ID.
type providerProfileReader interface {
	ProviderProfile(ctx context.Context, id string) (*pb.ProviderProfile, error)
}

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// handleProviderProfile serves GET /api/provider-profiles/ID: the profile
// as YAML in OpenShell's format. Iwato's own are its files as shipped
// (source "iwato"); others, imported with OpenShell's CLI, are read from
// the gateway and rendered (source "gateway").
// A scoped provider's own profile is source "scope", a custom one
// "custom". POSTs add, replace or delete custom profiles
// (handleCustomProfile).
func (s *Server) handleProviderProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleCustomProfile(w, r, strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/provider-profiles"), "/"))
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, false) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/provider-profiles/")
	if !profileIDPattern.MatchString(id) {
		fail(w, 400, "invalid profile id")
		return
	}
	if p, ok := builtinProfile(id); ok {
		jsonResponse(w, 200, map[string]string{"id": id, "source": "iwato", "file": "profiles/" + id + ".yaml", "yaml": string(p.yaml)})
		return
	}
	if p, ok := profileByID(id); ok {
		// A scoped provider's (rendered from its scope, or the owner's
		// YAML), or a custom profile (a file in the config folder).
		resp := map[string]any{"id": id, "yaml": string(p.yaml)}
		if p.Custom {
			resp["source"], resp["file"] = "custom", s.customProfileFile(id)
		} else {
			resp["source"], resp["base"] = "scope", p.Base
		}
		jsonResponse(w, 200, resp)
		return
	}
	reader, ok := s.rt.(providerProfileReader)
	if !ok {
		fail(w, 501, "providers unavailable")
		return
	}
	ctx, cancel := timeout(r.Context(), 20*time.Second)
	defer cancel()
	m, err := reader.ProviderProfile(ctx, id)
	if err != nil {
		fail(w, 502, safeCLIError(err))
		return
	}
	if m == nil {
		failCode(w, 404, "profile_missing", map[string]any{"id": id}, "ゲートウェイにプロファイル "+id+" がありません")
		return
	}
	y, err := profileYAML(m)
	if err != nil {
		failErr(w, 500, err)
		return
	}
	jsonResponse(w, 200, map[string]string{"id": id, "source": "gateway", "scope": m.GetScope(), "yaml": y})
}
