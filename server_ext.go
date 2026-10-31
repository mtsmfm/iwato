package main

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"rsc.io/qr"

	"iwato/internal/termsync"
)

// Optional runtime capabilities. OpenShell and Fake implement them; tests
// may not.
type proposalRuntime interface {
	Proposals(context.Context, Sandbox) (ProposalView, error)
	DecideProposal(ctx context.Context, sb Sandbox, action, request, digest string, scope *ProposalScope) (PolicyChange, error)
}
type deleteRuntime interface {
	Delete(context.Context, Sandbox, bool) error
}

// gpuRuntime tells whether a new sandbox can ask for a GPU (a runtime
// without it allows one).
type gpuRuntime interface {
	GPUAvailable(context.Context) bool
}

// gpuAvailable is gpuRuntime's answer.
func (s *Server) gpuAvailable(ctx context.Context) bool {
	rt, ok := s.rt.(gpuRuntime)
	return !ok || rt.GPUAvailable(ctx)
}

type stopRuntime interface {
	Stop(context.Context, Sandbox) error
}

// terminalsRuntime runs several terminal sessions per sandbox. outdated
// are sessions that a restart would update; multi is false when the
// sandbox can only have the main one.
type terminalsRuntime interface {
	Terminals(context.Context, Sandbox) (names, outdated []string, multi bool, e error)
	KillTerminal(context.Context, Sandbox, string) error
}

// sandboxRestartTerms tells which of a sandbox's terminals only a restart of
// the sandbox renews (OpenShell: a daemon older than Iwato's, or main
// without one).
type sandboxRestartTerms interface {
	SandboxRestartTerms(Sandbox, []string) []string
}

// screensRuntime shows the screens of a sandbox's running terminals, by
// terminal name, for the notifier.
type screensRuntime interface {
	TerminalScreens(Sandbox) map[string]termScreen
}

// termScreen is a terminal's size and the text of the rows it shows, when
// Iwato attached it (a new attachment's screen is the replayed output and a
// repaint, no news), and its clients' input and its alerts.
type termScreen struct {
	cols, rows int
	lines      []string
	attached   time.Time
	activity   termsync.Activity
}

func (s *Server) handleSandbox(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sandboxes/"), "/")
	if len(parts) < 2 {
		fail(w, 404, "unknown sandbox route")
		return
	}
	switch parts[1] {
	case "start":
		s.handleStart(w, r)
	case "proposals":
		s.handleProposals(w, r, parts)
	case "policy":
		s.handlePolicy(w, r, parts)
	case "providers":
		s.handleSandboxProviders(w, r, parts)
	case "presets":
		s.handleSandboxPresets(w, r, parts)
	case "terminals":
		s.handleTerminals(w, r, parts)
	case "stop":
		s.handleStop(w, r, parts)
	case "delete":
		s.handleDelete(w, r, parts)
	case "recreate":
		s.handleRecreate(w, r, parts)
	case "ports":
		s.handlePorts(w, r, parts)
	case "paste-image":
		s.handlePasteImage(w, r, parts)
	case "editor":
		s.handleEditor(w, r, parts)
	default:
		fail(w, 404, "unknown sandbox route")
	}
}

// handleProposals shows the sandbox's proposals (GET) and approves or rejects
// one (POST approve|reject with its request ID).
func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request, parts []string) {
	rt, hasRT := s.rt.(proposalRuntime)
	if r.Method == http.MethodGet && len(parts) == 2 {
		sb, ok := s.sandboxFor(w, parts, 2)
		if !ok || !s.require(w, r, false) {
			return
		}
		if !hasRT {
			jsonResponse(w, 200, ProposalView{Error: "proposals unavailable", Pending: []PendingProposal{}, Rejected: []RejectedProposal{}, Unlinked: []UnlinkedProposal{}})
			return
		}
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		v, e := rt.Proposals(ctx, sb)
		if e != nil {
			v = ProposalView{Error: safeCLIError(e), Pending: []PendingProposal{}, Rejected: []RejectedProposal{}, Unlinked: []UnlinkedProposal{}}
		}
		providers := map[string]RuleProvider{}
		var list []ProviderInfo
		var attached []string
		if prt, ok := s.rt.(providerRuntime); ok {
			if l, a, err := s.providerState(ctx, prt); err != nil {
				log.Printf("sandbox %s: providers for the proposals: %v", sb.Name, safeText(english(err)))
			} else {
				list, attached = l, a[sb.Name]
				providers = ruleProvidersOf(list, attached)
			}
		}
		nameProviderRules(v.Policy, providers)
		offerBindings(v.Policy, list, attached)
		for i := range v.Pending {
			if p := v.Pending[i].plan; p != nil {
				v.Pending[i].Offer = credentialOffers(*p, list, attached)
			}
		}
		v.Changes = s.policyChangesFor(sb.Name, v.Policy)
		v.Limits, v.HostGateways, v.ListenPort = proposalLimits, hostGateways, s.listenPort()
		jsonResponse(w, 200, v)
		return
	}
	if r.Method != http.MethodPost || len(parts) != 3 || !proposalOps[parts[2]] && parts[2] != "preview" {
		fail(w, 405, "method/operation not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, true) {
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		// Scope (approve only) narrows the proposal before it is approved.
		Scope *ProposalScope `json:"scope"`
		// Digest (approve, reject) is the proposal as the page showed it
		// (proposalDigest).
		Digest string `json:"digest"`
		// Reason (reject only) is the owner's, for the agent.
		Reason string `json:"reason"`
	}
	if decode(w, r, &body) != nil || !ownerID.MatchString(body.RequestID) {
		fail(w, 400, "invalid pending request ID")
		return
	}
	if parts[2] != "undo" && parts[2] != "restore" {
		if !digestPattern.MatchString(body.Digest) {
			failCode(w, 400, "proposal_digest_missing", nil, "表示した提案がわかりません。表示し直してください")
			return
		}
	}
	if body.Reason != "" && (parts[2] != "reject" || utf8.RuneCountInString(body.Reason) > 500) {
		fail(w, 400, "invalid reason")
		return
	}
	if body.Scope != nil {
		if parts[2] != "approve" && parts[2] != "preview" {
			fail(w, 400, "scope applies to approve only")
			return
		}
		if e := body.Scope.Validate(); e != nil {
			failErr(w, 400, e)
			return
		}
	}
	if !hasRT {
		fail(w, 409, "proposals unavailable")
		return
	}
	if parts[2] == "preview" {
		s.previewApproval(w, sb, body.RequestID, body.Digest, body.Scope)
		return
	}
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	ctx = withRejectNote(refuseDrift(ctx), body.Reason)
	defer s.lockPolicy(sb.Name)()
	if parts[2] == "undo" || parts[2] == "restore" {
		s.undoProposal(ctx, w, sb, body.RequestID, parts[2] == "restore")
		return
	}
	if parts[2] == "approve" && !s.approvable(ctx, w, sb, body.RequestID, body.Digest, body.Scope) {
		return
	}
	change, e := rt.DecideProposal(ctx, sb, parts[2], body.RequestID, body.Digest, body.Scope)
	if e != nil {
		s.failGateway(ctx, w, sb, e)
		return
	}
	s.recordPolicyChange(sb.Name, change)
	jsonResponse(w, 200, map[string]any{"ok": true, "version": change.Version})
}

// approvable checks the rule an approval would join the policy with (as
// proposed, or narrowed: programs alone keep the proposed endpoints): it
// may carry an attached provider's credential uninspected, which
// OpenShell refuses, or reach Iwato's own port. It writes the refusal.
func (s *Server) approvable(ctx context.Context, w http.ResponseWriter, sb Sandbox, request, shown string, scope *ProposalScope) bool {
	if pr, ok := s.rt.(proposedRuleRuntime); ok {
		rule, digest, e := pr.ProposedRule(ctx, sb, request)
		if e != nil {
			s.failGateway(ctx, w, sb, e)
			return false
		}
		if rule != nil {
			if digest != shown {
				failErr(w, 409, errProposalChanged())
				return false
			}
			merged := *rule
			if scope != nil {
				merged = scope.apply(merged)
			}
			if e := s.iwatoPortRefusal(map[string]v1.NetworkPolicyRule{orDefault(merged.Name, "proposal"): merged}); e != nil {
				failErr(w, 409, e)
				return false
			}
			scopes, e := s.attachedScopes(ctx, sb)
			if e != nil {
				failCode(w, 409, "provider_unverifiable", map[string]any{"detail": safeCLIError(e)}, "プロバイダーを確かめられません: "+safeCLIError(e))
				return false
			}
			name := merged.Name
			if name == "" {
				name = "proposal"
			}
			if cs := credentialConflicts(map[string]v1.NetworkPolicyRule{name: merged}, scopes); len(cs) > 0 {
				failCheck(w, conflictRefusal(cs))
				return false
			}
			// An endpoint already in the policy (a provider's, say) that
			// overlaps with other settings: OpenShell refuses the approval.
			if e := s.checkApproval(ctx, sb, name, *rule, scope); e != nil {
				failCheck(w, e)
				return false
			}
		}
	}
	return true
}

// approvalPreviewRuntime shows the rule an approval would write.
type approvalPreviewRuntime interface {
	PreviewApproval(ctx context.Context, sb Sandbox, request, digest string, scope *ProposalScope) (ApprovalPreview, error)
}

// previewApproval answers what an approval would add (ApprovalPreview),
// or why it would be refused.
func (s *Server) previewApproval(w http.ResponseWriter, sb Sandbox, request, digest string, scope *ProposalScope) {
	rt, ok := s.rt.(approvalPreviewRuntime)
	if !ok {
		fail(w, 409, "proposals unavailable")
		return
	}
	ctx, cancel := timeout(s.ctx, 20*time.Second)
	defer cancel()
	ctx = withGatewayReads(ctx)
	if !s.approvable(ctx, w, sb, request, digest, scope) {
		return
	}
	p, e := rt.PreviewApproval(ctx, sb, request, digest, scope)
	if e != nil {
		s.failGateway(ctx, w, sb, e)
		return
	}
	jsonResponse(w, 200, p)
}

// digestPattern is a proposal's digest (proposalDigest).
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// proposalOps are the owner's actions on a proposal: approve (pending or
// rejected), reject (pending), undo (approved: back to pending), restore
// (approved, its rule gone: put the rule back, then undo).
var proposalOps = map[string]bool{"approve": true, "reject": true, "undo": true, "restore": true}

// undoRuntime undoes approved proposals (openshell_policy_lists.go).
type undoRuntime interface {
	UndoProposal(ctx context.Context, sb Sandbox, request string, restore bool) (PolicyChange, error)
}

func (s *Server) undoProposal(ctx context.Context, w http.ResponseWriter, sb Sandbox, request string, restore bool) {
	rt, ok := s.rt.(undoRuntime)
	if !ok {
		fail(w, 409, "proposals unavailable")
		return
	}
	change, e := rt.UndoProposal(ctx, sb, request, restore)
	if e != nil {
		s.failGateway(ctx, w, sb, e)
		return
	}
	s.recordPolicyChange(sb.Name, change)
	jsonResponse(w, 200, map[string]any{"ok": true, "version": change.Version})
}

// nameProviderRules names the provider behind each _provider_* rule
// (Server.ruleProviders).
func nameProviderRules(p *CurrentPolicy, byRule map[string]RuleProvider) {
	if p == nil {
		return
	}
	for i, r := range p.Rules {
		if rp, ok := byRule[r.Name]; ok && r.Source == "provider" {
			p.Rules[i].Provider = &rp
		}
	}
}

// offerBindings names, per endpoint of the sandbox's own rules, the
// provider whose credential it could carry (EndpointView.Offer).
func offerBindings(p *CurrentPolicy, list []ProviderInfo, attached []string) {
	if p == nil {
		return
	}
	rules := p.effective
	if rules == nil {
		rules = p.base
	}
	for _, r := range p.Rules {
		if r.Source == "provider" {
			continue
		}
		for i := range r.Endpoints {
			if i < len(rules[r.Name].Endpoints) && r.Endpoints[i].Credential == "" {
				r.Endpoints[i].Offer, _ = offerProvider(rules[r.Name].Endpoints[i], list, attached)
			}
		}
	}
}

type policyRuntime interface {
	EditPolicy(context.Context, Sandbox, PolicyEdit) (PolicyChange, error)
	PolicyHistory(context.Context, Sandbox) ([]PolicyRevision, error)
	PolicyAt(context.Context, Sandbox, uint32) (*CurrentPolicy, error)
}

var policyOps = map[string]bool{"add": true, "add-allow": true, "remove": true, "remove-binary": true, "remove-endpoint": true, "remove-allow": true, "enforce": true}

// handlePolicy changes the sandbox's network policy (POST
// /api/sandboxes/NAME/policy/OP with a PolicyEdit), and lists its
// revisions (GET .../policy/history) or shows one (GET .../history/VERSION).
// .../policy/yaml is the YAML editor (policy_yaml.go).
func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "yaml" {
		s.handlePolicyYAML(w, r, parts)
		return
	}
	if len(parts) >= 3 && (parts[2] == "store" || parts[2] == "commits" || parts[2] == "binding") {
		s.handlePolicyStore(w, r, parts)
		return
	}
	rt, hasRT := s.rt.(policyRuntime)
	if r.Method == http.MethodGet && (len(parts) == 3 || len(parts) == 4) && parts[2] == "history" {
		var version uint64
		if len(parts) == 4 {
			v, err := strconv.ParseUint(parts[3], 10, 32)
			if err != nil || v == 0 {
				fail(w, 400, "invalid policy version")
				return
			}
			version = v
		}
		sb, ok := s.sandboxFor(w, parts, len(parts))
		if !ok || !s.require(w, r, false) {
			return
		}
		if !hasRT {
			fail(w, 409, "policy unavailable")
			return
		}
		ctx, cancel := timeout(r.Context(), 20*time.Second)
		defer cancel()
		if version != 0 {
			p, e := rt.PolicyAt(ctx, sb, uint32(version))
			if e != nil {
				fail(w, 409, safeCLIError(e))
				return
			}
			jsonResponse(w, 200, p)
			return
		}
		list, e := rt.PolicyHistory(ctx, sb)
		if e != nil {
			fail(w, 409, safeCLIError(e))
			return
		}
		jsonResponse(w, 200, map[string]any{"revisions": list})
		return
	}
	if r.Method != http.MethodPost || len(parts) != 3 || !policyOps[parts[2]] {
		fail(w, 405, "method/operation not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, true) {
		return
	}
	var edit PolicyEdit
	if decode(w, r, &edit) != nil {
		fail(w, 400, "invalid policy change")
		return
	}
	edit.Op = parts[2]
	if e := edit.Validate(); e != nil {
		failErr(w, 400, e)
		return
	}
	if !hasRT {
		fail(w, 409, "policy unavailable")
		return
	}
	if edit.Host != "" {
		if e := s.iwatoPortRefusal(map[string]v1.NetworkPolicyRule{orDefault(edit.Rule, "rule"): {Endpoints: []v1.PolicyNetworkEndpoint{{Host: edit.Host, Ports: edit.Ports}}}}); e != nil {
			failErr(w, 400, e)
			return
		}
	}
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	ctx = refuseDrift(withGatewayReads(ctx))
	defer s.lockPolicy(sb.Name)()
	// A rule by hand without inspection, to where an attached provider's
	// credential goes: OpenShell refuses it.
	if edit.Op == "add" && edit.Access == "" && len(edit.Rules) == 0 {
		scopes, e := s.attachedScopes(ctx, sb)
		if e != nil {
			failCode(w, 409, "provider_unverifiable", map[string]any{"detail": safeCLIError(e)}, "プロバイダーを確かめられません: "+safeCLIError(e))
			return
		}
		name := edit.Rule
		if name == "" {
			name = generatedRuleName(edit.Host, edit.Ports[0])
		}
		rule := v1.NetworkPolicyRule{Name: name, Endpoints: []v1.PolicyNetworkEndpoint{{Host: edit.Host, Port: edit.Ports[0], Ports: edit.Ports}}}
		for _, b := range edit.Binaries {
			rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b})
		}
		if cs := credentialConflicts(map[string]v1.NetworkPolicyRule{name: rule}, scopes); len(cs) > 0 {
			failCheck(w, conflictRefusal(cs))
			return
		}
	}
	if e := s.checkEdit(ctx, sb, edit); e != nil {
		failCheck(w, e)
		return
	}
	if e := s.checkEditBinaries(ctx, sb, edit); e != nil {
		failErr(w, 409, e)
		return
	}
	change, e := rt.EditPolicy(ctx, sb, edit)
	if e != nil {
		s.failGateway(ctx, w, sb, e)
		return
	}
	s.recordPolicyChange(sb.Name, change)
	jsonResponse(w, 200, map[string]any{"ok": true, "version": change.Version})
}

// appliedShown is how long a loaded change stays listed (as 反映済み).
const appliedShown = 20 * time.Second

// recordPolicyChange keeps a revision that an approval or edit made until
// the sandbox has loaded it, so every page (a reload too) shows it waiting.
func (s *Server) recordPolicyChange(name string, c PolicyChange) {
	if c.Version == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyChanges == nil {
		s.policyChanges = map[string][]PolicyChange{}
	}
	list := append(s.policyChanges[name], c)
	if len(list) > 20 {
		list = list[len(list)-20:]
	}
	s.policyChanges[name] = list
}

// policyChangesFor lists the sandbox's changes against its current policy:
// it notes when each was first seen loaded, and drops it a while after.
func (s *Server) policyChangesFor(name string, p *CurrentPolicy) []PolicyChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	kept := []PolicyChange{}
	for _, c := range s.policyChanges[name] {
		if p != nil && p.Active >= c.Version {
			if c.AppliedAt == 0 {
				c.AppliedAt = now.Unix()
			} else if now.Sub(time.Unix(c.AppliedAt, 0)) > appliedShown {
				continue
			}
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		delete(s.policyChanges, name)
	} else {
		s.policyChanges[name] = kept
	}
	return kept
}

func (s *Server) allowedOrigin(o string) bool {
	return o != "" && (o == s.origin || (s.publicOrigin != "" && o == s.publicOrigin))
}

// parsePublicOrigin accepts exactly https://host[:port] for tailscale serve.
func parsePublicOrigin(raw string) (host, origin string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("public-origin must be https://HOST[:PORT] (the tailscale serve URL)")
	}
	return u.Host, "https://" + u.Host, nil
}

func (s *Server) sandboxFor(w http.ResponseWriter, parts []string, n int) (Sandbox, bool) {
	if len(parts) != n {
		fail(w, 404, "unknown sandbox route")
		return Sandbox{}, false
	}
	sb, ok := s.lookupSandbox(parts[0])
	if !ok {
		fail(w, 404, "unknown sandbox")
	}
	return sb, ok
}

// handleTerminals serves GET .../terminals (the running sessions) and
// POST .../terminals/{name}/kill.
func (s *Server) handleTerminals(w http.ResponseWriter, r *http.Request, parts []string) {
	kill := r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "kill"
	if !kill && (r.Method != http.MethodGet || len(parts) != 2) {
		fail(w, 404, "unknown sandbox route")
		return
	}
	sb, ok := s.lookupSandbox(parts[0])
	if !ok {
		fail(w, 404, "unknown sandbox")
		return
	}
	if !s.require(w, r, kill) {
		return
	}
	rt, hasRT := s.rt.(terminalsRuntime)
	ctx, cancel := timeout(r.Context(), 20*time.Second)
	defer cancel()
	if kill {
		if !validTermName(parts[2]) {
			fail(w, 400, "invalid terminal name")
			return
		}
		if !hasRT {
			fail(w, 501, "multiple terminals unavailable")
			return
		}
		if e := rt.KillTerminal(ctx, sb, parts[2]); e != nil {
			fail(w, 409, safeCLIError(e))
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if !hasRT {
		jsonResponse(w, 200, map[string]any{"terminals": []string{mainTerm}, "outdated": []string{}, "multi": false, "providerOutdated": s.providerOutdated(sb, []string{mainTerm}), "restartSandbox": []string{mainTerm}})
		return
	}
	names, outdated, multi, e := rt.Terminals(ctx, sb)
	if e != nil {
		fail(w, 409, safeCLIError(e))
		return
	}
	if outdated == nil {
		outdated = []string{}
	}
	// providerOutdated run since before a provider change (providers.go);
	// restartSandbox are renewed only by restarting the sandbox (the page's
	// notice offers that instead of restarting the terminal).
	restart := []string{}
	if r, ok := s.rt.(sandboxRestartTerms); ok {
		restart = r.SandboxRestartTerms(sb, names)
	}
	jsonResponse(w, 200, map[string]any{"terminals": names, "outdated": outdated, "multi": multi, "providerOutdated": s.providerOutdated(sb, names), "restartSandbox": restart})
}

// handlePair issues a fresh single-use launch URL from an authenticated
// session, so a phone can be added without restarting the server.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	token := randomHex(32)
	until := time.Now().Add(10 * time.Minute)
	s.mu.Lock()
	s.bootstrap, s.bootstrapUntil = token, until
	s.mu.Unlock()
	base := s.origin
	if s.publicOrigin != "" {
		base = s.publicOrigin
	}
	link := base + "/#token=" + token
	resp := map[string]any{"url": link, "token": token, "expiresAt": until.UTC()}
	// A phone camera reads the link off the PC screen: nothing to copy.
	if code, e := qr.Encode(link, qr.M); e == nil {
		code.Scale = 6
		resp["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
	}
	jsonResponse(w, 200, resp)
}

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/push"), "/")
	if r.Method == http.MethodGet && op == "" {
		if !s.require(w, r, false) {
			return
		}
		if s.push == nil {
			jsonResponse(w, 200, map[string]any{"available": false})
			return
		}
		jsonResponse(w, 200, map[string]any{"available": true, "publicKey": s.push.PublicKey(), "subscriptions": s.push.Count()})
		return
	}
	if r.Method != http.MethodPost || (op != "subscribe" && op != "unsubscribe") {
		fail(w, 405, "method/operation not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	if s.push == nil {
		fail(w, 409, "push notifications are unavailable")
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if decode(w, r, &body) != nil {
		fail(w, 400, "invalid subscription")
		return
	}
	var e error
	if op == "subscribe" {
		e = s.push.Subscribe(PushSubscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth})
	} else {
		e = s.push.Unsubscribe(body.Endpoint)
	}
	if e != nil {
		failErr(w, 400, e)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "subscriptions": s.push.Count()})
}

// handleStop stops a sandbox's containers; everything else is kept and
// start resumes it. It cancels a start in progress.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 2)
	if !ok || !s.require(w, r, true) {
		return
	}
	rt, ok := s.rt.(stopRuntime)
	if !ok {
		fail(w, 501, "stop unavailable")
		return
	}
	op, _, interrupted, busy := s.beginOp(sb.Name, "stop")
	if busy != "" {
		failBusy(w, busy)
		return
	}
	var e error
	defer func() { s.endOp(sb.Name, op, e) }()
	if e = rt.Stop(op.ctx, sb); e != nil {
		// Stopping a start that OpenShell is still provisioning gives up
		// waiting for it; OpenShell cannot stop it yet.
		if interrupted && errors.Is(e, errNotStoppable) {
			jsonResponse(w, 200, map[string]any{"ok": true, "canceled": true, "code": "start_canceled", "detail": "起動を待つのをやめました。" + e.Error()})
			return
		}
		fail(w, 500, safeCLIError(e))
		return
	}
	// Its next start gives every process the current providers.
	s.clearProvidersChanged(sb.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// handleDelete deletes the sandbox in OpenShell (its volumes too with purge)
// and removes it from the configuration. The body must repeat the sandbox
// name as confirmation.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 2)
	if !ok || !s.require(w, r, true) {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
		Purge   bool   `json:"purge"`
	}
	if decode(w, r, &body) != nil || body.Confirm != sb.Name {
		fail(w, 400, "confirm must equal the sandbox name")
		return
	}
	rt, ok := s.rt.(deleteRuntime)
	if !ok {
		fail(w, 501, "delete unavailable")
		return
	}
	// Deleting cancels a start in progress.
	op, _, _, busy := s.beginOp(sb.Name, "delete")
	if busy != "" {
		failBusy(w, busy)
		return
	}
	var e error
	defer func() { s.endOp(sb.Name, op, e) }()
	if e = rt.Delete(op.ctx, sb, body.Purge); e != nil {
		fail(w, 500, safeCLIError(e))
		return
	}
	s.cleanupImages()
	if s.files != nil {
		s.files.Forget(sb.Name)
	}
	s.ingress.forget(sb.Name)
	if st, ok := s.rt.(policyStoreRuntime); ok {
		if e := st.ForgetPolicyStore(sb.Name); e != nil {
			log.Printf("sandbox %s: could not delete its policy history: %v", sb.Name, e)
		}
	}
	s.mu.Lock()
	delete(s.policyChanges, sb.Name)
	s.mu.Unlock()
	if e := s.forgetSandbox(sb.Name); e != nil {
		failCode(w, 500, "delete_unlisted", map[string]any{"detail": e.Error()}, "サンドボックスは削除しましたが、設定から外せませんでした: "+e.Error())
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// requireImages answers 409 while images are being prepared or failed.
func (s *Server) requireImages(w http.ResponseWriter) bool {
	st, ok := s.imagesReady()
	if !ok {
		if err := s.checks.engineErr(); err != nil {
			failErr(w, 409, err)
		} else if st.Phase == "failed" {
			failCode(w, 409, "images_failed", nil, "イメージの準備に失敗しました。画面上部から再試行してください")
		} else {
			failCode(w, 409, "images_preparing", nil, "イメージを準備中です。完了するまでお待ちください")
		}
	}
	return ok
}

func (s *Server) handleImagesRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, true) {
		return
	}
	// Not while a reset runs: it prepares again itself (reset.go).
	retried := false
	resetting := !s.resets.unlessRunning(func() {
		if s.images != nil && s.images.get().Phase == "failed" && s.retryImages != nil {
			s.retryImages()
			retried = true
		} else if p, ok := s.rt.(preparer); ok && p.RetryPreparation(s.ctx) {
			retried = true
		}
	})
	switch {
	case resetting:
		failBusy(w, opBusy["reset"])
	case retried:
		jsonResponse(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 409, "nothing to retry")
	}
}
