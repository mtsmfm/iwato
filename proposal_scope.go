package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// A proposal whose rule reaches an attached provider's credentialed
// endpoints is the provider's business (docs/adr/0006): the approve dialog
// offers to widen the provider's scope instead of adding a policy rule.
// For a provider with its own profile Iwato turns the proposal into scope
// fields (GitHub: a git push or REST writes under /repos/OWNER/REPO into a
// repository to write to, GraphQL mutations by field name into named
// mutations; Atlassian in mode tools: MCP tool calls by name into its
// tools; the other profiles: a write on the profile's own endpoints
// into mode full), saves the scope (changeScope) and then closes the
// proposal. When it cannot (a provider without a scope, the
// legacy shared iwato-github, the owner's YAML, a shape the scope cannot
// say), the page says why and approving adds the policy rule,
// with the warning that the provider's token rides along.
//
// Closing the proposal: OpenShell's approval always merges the chunk's
// rule into the sandbox policy (ApproveDraftChunk; an edit only changes the
// rule), which is what the scope replaces. So Iwato rejects the chunk, with
// a reason that tells the agent (it reads it from policy.local's
// /v1/proposals/ID/wait) that the request is allowed through the
// provider's scope and to retry it rather than propose again. The agent
// waits on /wait until then; Iwato rejects only after the running sandbox
// applied the new profile (the receipt of ObserveProvider, as 反映待ち on
// the provider tab), so the retry goes through. A sandbox that does not
// run gets the decision at once.

// ScopeWidening is what approving a proposal does to the provider whose
// credentialed endpoints it reaches.
type ScopeWidening struct {
	Provider string `json:"provider"`
	Label    string `json:"label,omitempty"`
	// Others are further providers the rule reaches.
	Others []string `json:"others,omitempty"`
	// Repos and Mutations are what the scope gains (both empty: it
	// allows the proposal already).
	Repos     []string `json:"repos,omitempty"`
	Mutations []string `json:"mutations,omitempty"`
	// Tools are MCP tools the scope gains (Atlassian).
	Tools []string `json:"tools,omitempty"`
	// Write: the scope becomes full (a profile other than GitHub's).
	Write bool `json:"write,omitempty"`
	// Reason is why the proposal cannot become the provider's scope:
	// approving it adds the policy rule, and the provider's credential
	// goes with the requests it allows.
	Reason string `json:"reason,omitempty"`
	// ReasonCode and ReasonParams say it for the page (web/i18n.js error.<code>).
	ReasonCode   string         `json:"reasonCode,omitempty"`
	ReasonParams map[string]any `json:"reasonParams,omitempty"`
	// Resolving: the scope was widened and Iwato closes the proposal once
	// the sandbox applied it.
	Resolving bool `json:"resolving,omitempty"`
}

// scopeTarget is a provider attached to the sandbox, as widenScope sees it.
type scopeTarget struct {
	name, label string
	// legacy: of a scoped built-in profile, but created before scopes
	// (the shared profile, which Iwato cannot change per provider).
	legacy bool
	// base is the scoped base of the provider's own profile ("" for
	// none), scope its configured scope.
	base     string
	scope    ProviderScope
	creds    []credScope
	binaries []string
}

// scopeTargets are the providers attached to the sandbox, with their
// credentialed endpoints.
func (s *Server) scopeTargets(ctx context.Context, sb Sandbox) ([]scopeTarget, error) {
	rt, ok := s.rt.(providerRuntime)
	if !ok {
		return nil, nil
	}
	list, attached, err := s.providerState(ctx, rt)
	if err != nil {
		return nil, err
	}
	var out []scopeTarget
	for _, p := range list {
		if !slices.Contains(attached[sb.Name], p.Name) {
			continue
		}
		t := scopeTarget{name: p.Name, label: p.Name, base: scopedBaseOf(p), creds: providerScopes(p.Name, p.Profile)}
		if len(t.creds) == 0 {
			continue
		}
		if prof, ok := profileByID(p.Profile); ok {
			t.label = prof.Label
			for _, b := range prof.proto().Binaries {
				t.binaries = append(t.binaries, b.Path)
			}
		}
		if bp, ok := builtinProfile(p.Profile); ok && bp.Scoped {
			t.legacy = true
		}
		if t.base != "" {
			sc, ok := s.providerScope(p.Name)
			if !ok {
				sc = ProviderScope{Base: t.base, Mode: defaultScopeMode(t.base)}
			}
			t.scope = sc
		}
		out = append(out, t)
	}
	return out, nil
}

// reaches says whether one of the rule's endpoints reaches the provider's
// credential.
func (t scopeTarget) reaches(e v1.PolicyNetworkEndpoint) bool {
	return slices.ContainsFunc(t.creds, func(sc credScope) bool { return credentialReaches(e, sc) })
}

var (
	// git push: POST /OWNER/REPO(.git)/git-receive-pack (fetch:
	// git-upload-pack, which the read-only scope allows).
	gitPackPath = regexp.MustCompile(`^/([^/]+)/([^/]+)/git-(receive|upload)-pack$`)
	// A REST call under a repository: /repos/OWNER/REPO/... .
	repoAPIPath = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/.+$`)
)

// widenScope is what the proposal's rule does to the attached providers
// (nil when it reaches none of their credentials).
func widenScope(rule v1.NetworkPolicyRule, targets []scopeTarget) *ScopeWidening {
	var hit []scopeTarget
	for _, t := range targets {
		if slices.ContainsFunc(rule.Endpoints, t.reaches) {
			hit = append(hit, t)
		}
	}
	if len(hit) == 0 {
		return nil
	}
	t := hit[0]
	w := &ScopeWidening{Provider: t.name, Label: t.label}
	for _, o := range hit[1:] {
		w.Others = append(w.Others, o.name)
	}
	repos, mutations, tools, write, reason := scopeFields(rule, t)
	if len(hit) > 1 {
		names := append([]string{t.name}, w.Others...)
		reason = errCode("scope_many_providers", map[string]any{"providers": names}, "複数のプロバイダー ("+strings.Join(names, "、")+") の宛先に当たります")
	}
	if reason == nil {
		if _, err := addToScope(t.scope, scopeAdd{repos: repos, mutations: mutations, tools: tools, write: write}); err != nil {
			reason = err
		}
	}
	if reason != nil {
		// The page shows the reason in its language (ADR 0013).
		w.Reason = reason.Error()
		if c := (*codedError)(nil); errors.As(reason, &c) && c.msg == w.Reason {
			w.ReasonCode, w.ReasonParams = c.code, c.params
		}
		return w
	}
	w.Repos, w.Mutations, w.Tools, w.Write = repos, mutations, tools, write
	return w
}

// scopeFields turns the rule into what the provider's scope lacks for it:
// repositories to write to and GraphQL mutations (GitHub), MCP tools
// (Atlassian), or write (the other profiles); or says why it cannot.
func scopeFields(rule v1.NetworkPolicyRule, t scopeTarget) (repos, mutations, tools []string, write bool, reason error) {
	fail := func(err error) ([]string, []string, []string, bool, error) { return nil, nil, nil, false, err }
	switch {
	case t.base == "" && t.legacy:
		return fail(errCode("scope_legacy", map[string]any{"provider": t.name, "label": t.label}, t.name+" は範囲を設定できるようになる前に作ったので、範囲を広げられません ("+t.label+" のプロバイダーを追加し直して付け替えると、範囲を持てます)"))
	case t.base == "":
		return fail(errCode("scope_none", map[string]any{"provider": t.name, "label": t.label}, t.name+" ("+t.label+") には Iwato が広げられる範囲がありません"))
	case t.scope.YAML != "":
		return fail(errCode("scope_yaml", map[string]any{"provider": t.name}, t.name+" の範囲は YAML で編集してあるので、Iwato は自動では広げません (プロバイダーの画面の YAML で変えられます)"))
	}
	if len(rule.Binaries) == 0 {
		return fail(errCode("scope_any_binary", map[string]any{"binaries": strings.Join(t.binaries, ", ")}, "提案がすべてのプログラムに許可するものなので、プロバイダーのプログラム ("+strings.Join(t.binaries, ", ")+") に限れません"))
	}
	for _, b := range rule.Binaries {
		if !slices.Contains(t.binaries, b.Path) {
			return fail(errCode("scope_other_binary", map[string]any{"binary": b.Path, "provider": t.name, "binaries": strings.Join(t.binaries, ", ")}, "プログラム "+b.Path+" は "+t.name+" の範囲のプログラム ("+strings.Join(t.binaries, ", ")+") ではありません"))
		}
	}
	if t.base != githubProfileID {
		if tools, write, ok, err := toolsField(rule, t); ok {
			if err != nil {
				return fail(err)
			}
			return nil, nil, tools, write, nil
		}
		write, err := writeField(rule, t)
		if err != nil {
			return fail(err)
		}
		return nil, nil, nil, write, nil
	}
	add := func(list *[]string, v string) {
		if !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
	for _, e := range rule.Endpoints {
		if !t.reaches(e) {
			return fail(errCode("scope_other_host", map[string]any{"provider": t.name, "host": e.Host}, "提案に "+t.name+" の宛先でないもの ("+e.Host+") も含まれます"))
		}
		if t.scope.Mode == scopeFull {
			continue
		}
		where := e.Host + ":" + joinPorts(effectivePorts(e))
		switch {
		case strings.TrimSpace(e.Protocol) == "":
			return fail(errCode("scope_uninspected", map[string]any{"where": where}, where+" の通信を検査せずに許可する提案なので、どの要求か分かりません"))
		case normalizedEnforcement(e.Enforcement) != int32(v1.NetworkEnforcementModeEnforce):
			return fail(errCode("scope_audit", map[string]any{"where": where}, where+" を監査 (すべて通す) で許可する提案なので、範囲に直せません"))
		case e.Access == v1.NetworkAccessPresetReadOnly:
			continue
		case e.Access != v1.NetworkAccessPresetUnspecified:
			return fail(errCode("scope_all_writes", map[string]any{"where": where}, where+" のすべての書き込みを許可する提案なので、リポジトリに限れません (要るならプロバイダーの画面で「すべてに書き込む」に)"))
		case len(e.Rules) == 0:
			return fail(errCode("scope_no_rules", map[string]any{"where": where}, where+" の提案にメソッドとパスがありません"))
		}
		graphql := strings.EqualFold(e.Protocol, "graphql")
		for _, r := range e.Rules {
			a := r.Allow
			if a == nil {
				continue
			}
			if graphql {
				switch {
				case a.OperationType == "query":
				case a.OperationType == "mutation" && len(a.Fields) > 0:
					for _, f := range a.Fields {
						if !graphqlNamePattern.MatchString(f) {
							return fail(errCode("scope_mutation_name", map[string]any{"name": truncate(f, 80)}, "GraphQL のミューテーションの名前ではありません: "+truncate(f, 80)))
						}
						add(&mutations, f)
					}
				default:
					return fail(errCode("scope_graphql_fields", nil, "GraphQL の許可は、ミューテーションの名前 (fields) を挙げたものだけを範囲に直せます"))
				}
				continue
			}
			method := strings.ToUpper(a.Method)
			if slices.Contains(readMethods, method) {
				// The scope reads every path of the provider's hosts.
				continue
			}
			path := a.Path
			switch e.Host {
			case "github.com":
				if m := gitPackPath.FindStringSubmatch(path); m != nil && (method == "POST" || method == "*" || method == "") {
					if m[3] == "upload" {
						continue
					}
					repo, err := normalizeRepo(m[1] + "/" + m[2])
					if err != nil {
						return fail(errCode("scope_push_repo", map[string]any{"path": truncate(path, 120)}, "push の宛先 "+truncate(path, 120)+" のリポジトリが 1 つに決まりません"))
					}
					add(&repos, repo)
					continue
				}
			case "api.github.com", "uploads.github.com":
				if path == "/graphql" {
					return fail(errCode("scope_graphql_rest", map[string]any{"method": method}, "GraphQL を REST の規則 ("+method+" /graphql) で許可すると、すべてのミューテーションが通ります。ミューテーションの名前で提案し直してもらうか、プロバイダーの画面で名前を足してください"))
				}
				if m := repoAPIPath.FindStringSubmatch(path); m != nil {
					repo, err := normalizeRepo(m[1] + "/" + m[2])
					if err != nil {
						return fail(errCode("scope_api_repo", map[string]any{"host": e.Host, "path": truncate(path, 120)}, e.Host+" の "+truncate(path, 120)+" のリポジトリが 1 つに決まりません"))
					}
					add(&repos, repo)
					continue
				}
			}
			return fail(errCode("scope_not_repo_write", map[string]any{"host": e.Host, "request": strings.TrimSpace(orDefault(a.Method, "*") + " " + truncate(path, 120))}, e.Host+" の "+strings.TrimSpace(orDefault(a.Method, "*")+" "+truncate(path, 120))+" は、リポジトリへの書き込みとして範囲に直せません"))
		}
	}
	// What the scope has already is no change.
	repos = slices.DeleteFunc(repos, func(r string) bool { return t.scope.Mode == scopeRepos && slices.Contains(t.scope.Repos, r) })
	mutations = slices.DeleteFunc(mutations, func(m string) bool { return slices.Contains(t.scope.Mutations, m) })
	return repos, mutations, nil, false, nil
}

// writeField says whether the rule needs write (mode full) on a profile
// other than GitHub's: each endpoint's requests are within the provider's
// rule as it is, or within it in mode full; anything else (other paths,
// hosts or ports, uninspected or audited traffic) cannot be its scope.
func writeField(rule v1.NetworkPolicyRule, t scopeTarget) (bool, error) {
	render := func(mode string) []v1.PolicyNetworkEndpoint {
		m, err := renderScope(t.name, ProviderScope{Base: t.base, Mode: mode})
		if err != nil {
			return nil
		}
		return profileRule(m, "").Endpoints
	}
	cur, full := render(t.scope.Mode), render(scopeFull)
	write := false
	for _, e := range rule.Endpoints {
		where := e.Host + ":" + joinPorts(effectivePorts(e))
		switch {
		case !t.reaches(e):
			return false, errCode("scope_other_host", map[string]any{"provider": t.name, "host": e.Host}, "提案に "+t.name+" の宛先でないもの ("+e.Host+") も含まれます")
		case strings.TrimSpace(e.Protocol) == "" || e.TLS == v1.NetworkTLSModeSkip:
			return false, errCode("scope_uninspected", map[string]any{"where": where}, where+" の通信を検査せずに許可する提案なので、どの要求か分かりません")
		case normalizedEnforcement(e.Enforcement) != int32(v1.NetworkEnforcementModeEnforce):
			return false, errCode("scope_audit", map[string]any{"where": where}, where+" を監査 (すべて通す) で許可する提案なので、範囲に直せません")
		case withinProvider(e, cur):
		case withinProvider(e, full):
			write = true
		default:
			return false, errCode("scope_beyond_endpoints", map[string]any{"where": where, "provider": t.name}, where+" の提案は "+t.name+" の宛先 (パス) の外にも及ぶので、範囲に直せません")
		}
	}
	return write, nil
}

// scopeAdd is what a proposal adds to a scope.
type scopeAdd struct {
	repos, mutations, tools []string
	write                   bool
}

// addToScope is the scope with the repositories and mutations added
// (read-only becomes writing to them), the tools added, or with write
// (mode full).
func addToScope(sc ProviderScope, add scopeAdd) (ProviderScope, error) {
	if sc.YAML != "" {
		return sc, errCode("scope_custom_yaml", nil, "範囲は YAML で編集してあります")
	}
	out := sc
	out.Repos = slices.Clone(sc.Repos)
	out.Mutations = slices.Clone(sc.Mutations)
	out.Tools = slices.Clone(sc.Tools)
	if sc.Mode == scopeFull {
		return out, nil
	}
	if add.write {
		out.Mode = scopeFull
		return out.normalize()
	}
	repos, mutations := add.repos, add.mutations
	for _, n := range add.tools {
		if !slices.Contains(out.Tools, n) {
			out.Tools = append(out.Tools, n)
		}
	}
	if len(repos) > 0 && out.Mode != scopeRepos {
		out.Mode, out.Repos = scopeRepos, nil
	}
	for _, r := range repos {
		if !slices.Contains(out.Repos, r) {
			out.Repos = append(out.Repos, r)
		}
	}
	for _, m := range mutations {
		if !slices.Contains(out.Mutations, m) {
			out.Mutations = append(out.Mutations, m)
		}
	}
	return out.normalize()
}

// scopeChangeText says what the widening adds, for the agent (English).
func scopeChangeText(w *ScopeWidening) string {
	var parts []string
	if len(w.Repos) > 0 {
		parts = append(parts, "writes to "+strings.Join(w.Repos, ", "))
	}
	if len(w.Mutations) > 0 {
		parts = append(parts, "GraphQL mutations "+strings.Join(w.Mutations, ", "))
	}
	if len(w.Tools) > 0 {
		parts = append(parts, "MCP tools "+strings.Join(w.Tools, ", "))
	}
	if w.Write {
		parts = append(parts, "writes on its endpoints")
	}
	if len(parts) == 0 {
		return "it allows this already"
	}
	return "added " + strings.Join(parts, " and ")
}

// scopeReasonPrefix starts the reason Iwato rejects a proposal with when it
// widened the provider's scope instead (rejectedProposal reads it back).
const scopeReasonPrefix = "Not added to the policy: the owner allowed this through provider "

var scopeReason = regexp.MustCompile(`^` + regexp.QuoteMeta(scopeReasonPrefix) + `([a-z][a-z0-9-]*)`)

// scopeRejectReason is the reason the agent reads on /wait.
func scopeRejectReason(provider, change, applied string) string {
	retry := "Do not propose this rule again; retry the original request now."
	if applied != "" {
		retry = "Do not propose this rule again. The sandbox has not applied the change yet (" + applied + "): retry the original request in a minute, and tell the user if it is still denied."
	}
	return scopeReasonPrefix + provider + "'s scope (" + change + ") instead, so its own rules allow the request with its credential. " + retry
}

// proposalCloser rejects a pending proposal with a reason (the scope's).
type proposalCloser interface {
	CloseProposal(ctx context.Context, sb Sandbox, request, reason string) error
}

// CloseProposal rejects the pending proposal with the reason, which keeps
// the count so far as Iwato's rejections do (rejectedHits).
func (o *OpenShell) CloseProposal(ctx context.Context, w Sandbox, request, reason string) error {
	pol, err := o.policy(ctx)
	if err != nil {
		return err
	}
	defer o.forgetDraft(w.Name)
	chunk, _, err := draftChunk(ctx, pol, w.Name, request, "pending")
	if err != nil {
		return err
	}
	if chunk == nil {
		return errCode("request_not_pending", nil, "この依頼は承認待ちではありません")
	}
	return pol.RejectDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, fmt.Sprintf("%s (hits: %d)", reason, chunk.HitCount))
}

// The proposals whose scope was widened and that Iwato closes once the
// sandbox applied it, by sandbox and request.
type scopeClosings struct {
	sync.Mutex
	m map[string]bool
}

func (c *scopeClosings) has(sandbox, request string) bool {
	c.Lock()
	defer c.Unlock()
	return c.m[sandbox+"/"+request]
}

// start marks it (false when it is marked already).
func (c *scopeClosings) start(sandbox, request string) bool {
	c.Lock()
	defer c.Unlock()
	if c.m == nil {
		c.m = map[string]bool{}
	}
	if c.m[sandbox+"/"+request] {
		return false
	}
	c.m[sandbox+"/"+request] = true
	return true
}

func (c *scopeClosings) done(sandbox, request string) {
	c.Lock()
	defer c.Unlock()
	delete(c.m, sandbox+"/"+request)
}

// How the closing follows the sandbox (tests shorten them).
var (
	scopeApplyPoll    = 2 * time.Second
	scopeApplyTimeout = 3 * time.Minute
)

// widenProposal handles POST /api/sandboxes/NAME/proposals/scope: the
// pending proposal's rule becomes its provider's scope (refused with the
// reason when it cannot), then Iwato closes the proposal.
func (s *Server) widenProposal(ctx context.Context, w http.ResponseWriter, sb Sandbox, request, digest string) {
	pr, ok := s.rt.(proposedRuleRuntime)
	closer, ok2 := s.rt.(proposalCloser)
	rt, ok3 := s.rt.(providerRuntime)
	store, ok4 := s.rt.(profileStore)
	if !ok || !ok2 || !ok3 || !ok4 {
		fail(w, 501, "provider scopes unavailable")
		return
	}
	if s.closings.has(sb.Name, request) {
		failCode(w, 409, "scope_widening", nil, "範囲は広げてあります。サンドボックスが反映したら提案を閉じます")
		return
	}
	rule, current, err := pr.ProposedRule(ctx, sb, request)
	if err != nil {
		s.failGateway(ctx, w, sb, err)
		return
	}
	// The proposal as the owner was shown it.
	if current != digest {
		failErr(w, 409, errProposalChanged())
		return
	}
	targets, err := s.scopeTargets(ctx, sb)
	if err != nil {
		failCode(w, 409, "provider_unverifiable", map[string]any{"detail": safeCLIError(err)}, "プロバイダーを確かめられません: "+safeCLIError(err))
		return
	}
	var wd *ScopeWidening
	if rule != nil {
		wd = widenScope(*rule, targets)
	}
	switch {
	case wd == nil:
		failCode(w, 409, "scope_not_provider", nil, "この提案はプロバイダーの宛先への通信ではありません")
		return
	case wd.Reason != "":
		failCode(w, 409, "scope_impossible", map[string]any{"reason": wd.Reason}, "プロバイダーの範囲にできません: "+wd.Reason)
		return
	}
	if !s.closings.start(sb.Name, request) {
		failCode(w, 409, "scope_widening", nil, "範囲は広げてあります。サンドボックスが反映したら提案を閉じます")
		return
	}
	pending := []string{}
	if len(wd.Repos) > 0 || len(wd.Mutations) > 0 || len(wd.Tools) > 0 || wd.Write {
		var code int
		_, pending, code, err = s.changeScope(ctx, rt, store, wd.Provider, func(old ProviderScope, _ string) (ProviderScope, error) {
			return addToScope(old, scopeAdd{repos: wd.Repos, mutations: wd.Mutations, tools: wd.Tools, write: wd.Write})
		})
		if err != nil {
			s.closings.done(sb.Name, request)
			failScope(w, code, err)
			return
		}
	}
	receipt := ""
	if slices.Contains(pending, sb.Name) {
		if cur, ok := s.lookupSandbox(sb.Name); ok {
			for _, a := range cur.ProviderApplying {
				if a.Provider == wd.Provider && a.Scope && a.State == "" {
					receipt = a.Receipt
				}
			}
		}
	}
	change := scopeChangeText(wd)
	if receipt == "" {
		defer s.closings.done(sb.Name, request)
		if err := closer.CloseProposal(ctx, sb, request, scopeRejectReason(wd.Provider, change, "")); err != nil {
			failCode(w, 409, "scope_close_failed", map[string]any{"detail": safeCLIError(err)}, "範囲は広げましたが、提案を閉じられませんでした (却下してください): "+safeCLIError(err))
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "provider": wd.Provider, "pending": pending, "closed": true})
		return
	}
	go s.closeWhenApplied(sb, request, wd.Provider, receipt, change, rt, closer)
	jsonResponse(w, 200, map[string]any{"ok": true, "provider": wd.Provider, "pending": pending, "closed": false})
}

// closeWhenApplied closes the proposal once the running sandbox applied
// the provider's new scope (or gave up on it, or it took too long: the
// reason then says so).
func (s *Server) closeWhenApplied(sb Sandbox, request, provider, receipt, change string, rt providerRuntime, closer proposalCloser) {
	defer s.closings.done(sb.Name, request)
	deadline := time.Now().Add(scopeApplyTimeout)
	applied := "still pending"
	for {
		ctx, cancel := timeout(s.ctx, 20*time.Second)
		state, reason, err := rt.ProviderReadiness(ctx, sb.Name, provider, receipt)
		cancel()
		if err == nil && state != "pending" {
			applied = ""
			if !slices.Contains([]string{"ready", "superseded", "gone"}, state) {
				applied = state + " " + reason
			}
			break
		}
		if err != nil {
			log.Printf("sandbox %s: provider %s: readiness: %v", sb.Name, provider, english(err))
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(scopeApplyPoll):
		}
	}
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := closer.CloseProposal(ctx, sb, request, scopeRejectReason(provider, change, strings.TrimSpace(applied))); err != nil {
		log.Printf("sandbox %s: close the proposal after widening provider %s: %v", sb.Name, provider, safeText(english(err)))
	}
}

// ProviderHost is a policy endpoint where an attached provider's
// credential goes: by ADR 0006 the provider's scope's business.
type ProviderHost struct {
	Provider string `json:"provider"`
	Label    string `json:"label,omitempty"`
	Host     string `json:"host"`
}

// markProviderHosts marks the policy's rules (not the providers' own)
// whose endpoints reach an attached provider's credential.
func markProviderHosts(p *CurrentPolicy, targets []scopeTarget) {
	if p == nil || len(targets) == 0 {
		return
	}
	rules := p.effective
	if rules == nil {
		rules = p.base
	}
	for i, r := range p.Rules {
		raw, ok := rules[r.Name]
		if !ok || r.Source == "provider" {
			continue
		}
		for _, e := range raw.Endpoints {
			for _, t := range targets {
				if t.reaches(e) && !slices.ContainsFunc(p.Rules[i].ProviderHosts, func(h ProviderHost) bool { return h.Provider == t.name && h.Host == e.Host }) {
					p.Rules[i].ProviderHosts = append(p.Rules[i].ProviderHosts, ProviderHost{Provider: t.name, Label: t.label, Host: e.Host})
				}
			}
		}
	}
}
