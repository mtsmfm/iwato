package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// policyAdvisorSetting turns on OpenShell's Policy Advisor: agents get the
// policy.local API and a guide to propose the network rules they need.
// OpenShell also drafts proposals from blocked connections on its own.
const policyAdvisorSetting = "agent_policy_proposals_enabled"

// enablePolicyAdvisor sets the advisor on for every sandbox of the gateway.
func enablePolicyAdvisor(ctx context.Context, sdk *v1.Client) error {
	_, err := sdk.Config().Update(ctx, "", &v1.ConfigUpdate{
		Global:       true,
		SettingKey:   policyAdvisorSetting,
		SettingValue: &v1.SettingValue{Type: v1.SettingValueBool, BoolVal: true},
	})
	return err
}

// requestID maps an OpenShell proposal (chunk) ID to the 32 hex digits the
// UI and the notifier use for pending requests.
func requestID(chunkID string) string {
	h := sha256.Sum256([]byte(chunkID))
	return hex.EncodeToString(h[:16])
}

// chunkSummary is a proposal in one line: what may reach what, and why.
func chunkSummary(c v1.PolicyChunk) string {
	var dests []string
	if r := c.ProposedRule; r != nil {
		for _, e := range r.Endpoints {
			ports := []string{}
			if e.Port != 0 {
				ports = append(ports, fmt.Sprint(e.Port))
			}
			for _, p := range e.Ports {
				if p != e.Port {
					ports = append(ports, fmt.Sprint(p))
				}
			}
			dest := e.Host
			if dest == "" {
				dest = "すべてのホスト"
			}
			if len(ports) > 0 {
				dest += ":" + strings.Join(ports, ",")
			}
			switch e.Access {
			case v1.NetworkAccessPresetReadOnly:
				dest += " (読み取りだけ)"
			case v1.NetworkAccessPresetReadWrite:
				dest += " (読み書き)"
			case v1.NetworkAccessPresetFull:
				dest += " (すべて)"
			}
			for _, rule := range e.Rules {
				if a := rule.Allow; a != nil && !placeholderRoot(c) {
					dest += " " + strings.TrimSpace(a.Method+" "+a.Path)
				}
			}
			if len(e.AllowedIPs) > 0 {
				dest += " · " + allowedIPsLabel(e.Host, e.AllowedIPs)
			}
			dests = append(dests, dest)
		}
	}
	s := strings.Join(dests, ", ")
	if s == "" {
		s = c.RuleName
	}
	if c.Binary != "" {
		s = c.Binary + " → " + s
	}
	if c.Rationale != "" {
		s += ": " + c.Rationale
	}
	if c.SecurityNotes != "" {
		s += " [注意: " + c.SecurityNotes + "]"
	}
	return s
}

// allowedIPsLabel says what an endpoint's allowed_ips let through. OpenShell
// refuses internal addresses (private, CGNAT 100.64.0.0/10, ULA fc00::/7) by
// default; with allowed_ips the host's resolved addresses must be in the list
// instead, internal ones included. A host-less endpoint matches any host on
// its ports, so then every name that resolves into the list is reachable.
// web/app.js says the same (allowedIPsLabel).
func allowedIPsLabel(host string, ips []string) string {
	list := strings.Join(ips, ", ")
	if host == "" {
		return "ホストを問わず、解決先 IP が " + list + " なら許可 (内部アドレスも)"
	}
	return "解決先 IP を " + list + " に限定 (内部アドレスも許可)"
}

// Proposals reads the sandbox's policy as the policy tab shows it, all from
// OpenShell: the proposals (chunks) waiting for the owner (the policy tab,
// its badge and push notifications count these), the effective policy with
// each rule's origin (a provider, approved proposals, or by hand), the
// approved proposals whose rule is gone, and the rejected ones.
func (o *OpenShell) Proposals(ctx context.Context, w Sandbox) (ProposalView, error) {
	v := ProposalView{Pending: []PendingProposal{}, Rejected: []RejectedProposal{}, Unlinked: []UnlinkedProposal{}, Limits: proposalLimits, HostGateways: hostGateways}
	pol, err := o.policy(ctx)
	if err != nil {
		return v, err
	}
	chunks, err := o.draftChunks(ctx, pol, w.Name)
	if notFound(err) {
		o.forgetDraft(w.Name)
		return v, errCode("sandbox_missing", nil, "サンドボックスがありません")
	}
	if err != nil {
		return v, err
	}
	v.Available = true
	for _, c := range chunks {
		switch c.Status {
		case "pending":
			v.Pending = append(v.Pending, pendingProposal(requestID(c.ID), c))
		case "rejected":
			v.Rejected = append(v.Rejected, rejectedProposal(c))
		}
	}
	sort.Slice(v.Pending, func(i, j int) bool { return v.Pending[i].CreatedAt < v.Pending[j].CreatedAt })
	sort.SliceStable(v.Rejected, func(i, j int) bool { return v.Rejected[i].DecidedAt > v.Rejected[j].DecidedAt })
	if len(v.Rejected) > maxRejected {
		v.Rejected = v.Rejected[:maxRejected]
	}
	p, perr := o.currentPolicy(ctx, w)
	if perr == nil {
		// The rule each approval writes, with the credentials it carries by default.
		if p.effective != nil {
			composed := withProviderRules(p.base, p.effective)
			for _, c := range chunks {
				if c.ProposedRule == nil {
					continue
				}
				var pp *PendingProposal
				if j := slices.IndexFunc(v.Pending, func(x PendingProposal) bool { return x.ID == requestID(c.ID) }); c.Status == "pending" && j >= 0 {
					pp = &v.Pending[j]
				} else if j := slices.IndexFunc(v.Rejected, func(x RejectedProposal) bool { return x.ID == requestID(c.ID) }); c.Status == "rejected" && j >= 0 {
					pp = &v.Rejected[j].PendingProposal
				}
				if pp != nil {
					plan := inheritShadowed(approvedRule(c.RuleName, *c.ProposedRule), composed, nil)
					pp.Attach, pp.plan = approvalAttach(plan, composed), &plan
				}
			}
		}
		dropProposedDenials(p, chunks)
		v.Unlinked = linkChunks(p, chunks)
		v.Policy = p
		v.Error = p.composeError
	} else if !notFound(perr) {
		v.Error = "ポリシーを読めません: " + safeCLIError(perr)
	}
	return v, nil
}

// maxRejected bounds the rejected proposals listed (OpenShell keeps them
// all: ClearDraftChunks clears only pending ones).
const maxRejected = 200

// DecideProposal approves or rejects one pending proposal. The decision
// carries the digest of the proposal the owner was shown (proposalDigest):
// a proposal that changed since is refused. An approval then binds to that
// candidate (its review token), and applies only a rule the page can show
// whole (proposalUnshown).
//
// An approval answers the revision it made, which the sandbox loads at its
// next settings poll.
func (o *OpenShell) DecideProposal(ctx context.Context, w Sandbox, action, request, digest string, scope *ProposalScope) (PolicyChange, error) {
	change, err := o.decideProposal(ctx, w, action, request, digest, scope)
	if err != nil || action != "approve" {
		return PolicyChange{}, err
	}
	return o.withVersion(ctx, w, change), nil
}

func (o *OpenShell) decideProposal(ctx context.Context, w Sandbox, action, request, digest string, scope *ProposalScope) (PolicyChange, error) {
	none := PolicyChange{}
	if action != "approve" && action != "reject" {
		return none, errors.New("unsupported proposal decision")
	}
	pol, err := o.policy(ctx)
	if err != nil {
		return none, err
	}
	// Whatever the decision did, the next view reads the decided ones again.
	defer o.forgetDraft(w.Name)
	chunk, _, err := draftChunk(ctx, pol, w.Name, request, "pending", "rejected", "approved")
	if err != nil {
		return none, err
	}
	if chunk == nil {
		return none, errCode("request_not_found", nil, "この依頼は見つかりません")
	}
	// OpenShell approves a rejected chunk too (policy.rs: pending or
	// rejected), but edits only pending ones.
	rejected := chunk.Status == "rejected" && action == "approve"
	if chunk.Status != "pending" && !rejected {
		return none, errCode("request_decided", nil, "この依頼は決定済みです")
	}
	if digest != proposalDigest(*chunk) {
		return none, errProposalChanged()
	}
	if action == "approve" && chunk.ProposedRule != nil {
		var err error
		if scope, err = scope.against(*chunk.ProposedRule); err != nil {
			return none, err
		}
		rule := *chunk.ProposedRule
		if scope != nil {
			rule = scope.apply(rule)
		}
		if parts := proposalUnshown(rule); len(parts) > 0 {
			return none, errProposalUnshown(parts)
		}
	}
	if rejected && scope.narrows() {
		return none, errCode("rejected_as_proposed", nil, "却下した提案は OpenShell では提案どおりにしか承認できません")
	}
	if action == "reject" {
		// The reason (which the agent reads back through policy.local)
		// carries the count so far: the rejected list tells the hits
		// since from OpenShell's own data.
		var notes []string
		if owner, _ := ctx.Value(rejectNoteKey{}).(string); owner != "" {
			notes = append(notes, owner)
		}
		if chunk.ProposedRule != nil {
			if n := tooManyReason(proposalUnshown(*chunk.ProposedRule)); n != "" {
				notes = append(notes, n)
			}
		}
		note := strings.Join(notes, "; ")
		return none, pol.RejectDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, rejectReason(chunk.HitCount, note))
	}
	if chunk.ProposedRule == nil {
		return none, errCode("proposal_empty", nil, "この提案には規則がありません")
	}
	return o.approveChunk(ctx, w, pol, chunk, scope, rejected, digest)
}

// approveChunk approves a proposal as its own rule (ADR 0022): the rule
// approvedRule makes of the (narrowed) proposal. The proposal is first
// edited into that rule and approved in OpenShell, which adds it under a
// rule name of its own (the gateway picks one the policy does not have) and
// marks the proposal approved, so the agent's /wait answers approved; then
// the repository's policy with the rule is committed and applied as a
// whole, which replaces OpenShell's merge with the same rule. A rejected
// proposal cannot be edited: it is approved as proposed, and the
// repository still gets approvedRule of it.
func (o *OpenShell) approveChunk(ctx context.Context, w Sandbox, pol v1.PolicyInterface, chunk *v1.PolicyChunk, scope *ProposalScope, rejected bool, digest string) (PolicyChange, error) {
	none := PolicyChange{}
	sync, err := o.syncStore(ctx, w)
	if err != nil {
		return none, err
	}
	plan, err := o.approvalPlan(ctx, w, sync.rules, chunk, scope)
	if err != nil {
		return none, err
	}
	planned := cloneRules(sync.rules)
	planned[plan.Name] = plan
	if err := o.checkWithProviders(ctx, w, sync.rules, planned); err != nil {
		return none, fmt.Errorf("proposal is not applicable: candidate invalid: %w", err)
	}
	host := ""
	if len(plan.Endpoints) > 0 {
		host = plan.Endpoints[0].Host
	}
	change := PolicyChange{Kind: "approve", Host: host, Summary: chunkSummary(*chunk), At: time.Now().Unix()}
	token := chunk.ReviewToken
	name := ""
	if rejected {
		// A rejected chunk's review token is from before the policy moved
		// on: OpenShell refreshes it on the first try ("evaluation
		// refreshed"); the proposal itself is unchanged, so try once more.
		_, err := pol.ApproveDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, token)
		if err != nil && strings.Contains(err.Error(), "refetch and review again") {
			fresh, ferr := pol.GetDraft(ctx, openshellWorkspace, w.Name, v1.WithStatusFilter("rejected"))
			if ferr != nil {
				return none, ferr
			}
			for _, c := range fresh.Chunks {
				if c.ID == chunk.ID && c.Status == "rejected" && proposalDigest(c) == digest {
					_, err = pol.ApproveDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, c.ReviewToken)
				}
			}
		}
		if err != nil {
			return none, err
		}
	} else {
		// The edit gives the chunk a new review token (and rule name),
		// which the approval then binds to.
		edit := cloneRule(plan)
		edit.Name = ""
		if err := pol.EditDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, &edit); err != nil {
			return none, err
		}
		draft, err := pol.GetDraft(ctx, openshellWorkspace, w.Name, v1.WithStatusFilter("pending"))
		if err != nil {
			return none, err
		}
		token = ""
		for _, c := range draft.Chunks {
			// The edited chunk, with the programs sent (not changed since).
			if c.ID == chunk.ID && c.Status == "pending" && c.ProposedRule != nil && slices.Equal(binaryPaths(*c.ProposedRule), binaryPaths(plan)) {
				token, name = c.ReviewToken, c.RuleName
			}
		}
		if token == "" {
			return none, errCode("narrowed_unverified", nil, "絞った提案を確認できません")
		}
		if _, err := pol.ApproveDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID, token); err != nil {
			return none, err
		}
	}
	// The name OpenShell gave the approved rule: the policy tab ties the
	// rule to the proposal by it.
	o.forgetDraft(w.Name)
	if rejected {
		if approved, _, err := draftChunk(ctx, pol, w.Name, requestID(chunk.ID), "approved"); err == nil && approved != nil {
			name = approved.RuleName
		}
	}
	if _, taken := sync.rules[name]; name != "" && name != plan.Name && !taken {
		delete(planned, plan.Name)
		plan.Name = name
		planned[name] = plan
	}
	change.Rule = plan.Name
	message := fmt.Sprintf("Approve proposal %s as rule %s\n\n%s", chunk.ID, plan.Name, change.Summary)
	v, err := o.commitPolicy(ctx, w, sync, planned, PolicyNote{Kind: "approve", Message: message})
	if err != nil {
		return none, fmt.Errorf("OpenShell は提案を承認しましたが、Iwato の規則として適用できませんでした: %w", err)
	}
	change.Version = v
	return change, nil
}

// approvalPlan is the rule approving chunk (narrowed by scope) adds to
// rules.
func (o *OpenShell) approvalPlan(ctx context.Context, w Sandbox, rules map[string]v1.NetworkPolicyRule, chunk *v1.PolicyChunk, scope *ProposalScope) (v1.NetworkPolicyRule, error) {
	proposed := *chunk.ProposedRule
	if scope != nil {
		proposed = scope.apply(proposed)
	}
	name := chunk.RuleName
	if name == "" && len(proposed.Endpoints) > 0 && len(endpointPorts(proposed.Endpoints[0])) > 0 {
		name = generatedRuleName(proposed.Endpoints[0].Host, endpointPorts(proposed.Endpoints[0])[0])
	}
	if name == "" {
		name = "approved"
	}
	composed, err := o.withProviders(ctx, w, rules)
	if err != nil {
		return v1.NetworkPolicyRule{}, err
	}
	var attach *bool
	if scope != nil {
		attach = scope.Attach
	}
	plan := inheritShadowed(approvedRule(freeRuleName(rules, name), proposed), composed, attach)
	if err := bindingRequired(plan, composed); err != nil {
		return v1.NetworkPolicyRule{}, err
	}
	if scope != nil && scope.Offer {
		// The credentials offered for the hosts they are meant for.
		list, attached, err := o.listProvidersFor(ctx)
		if err != nil {
			return v1.NetworkPolicyRule{}, err
		}
		plan = bindOffers(plan, list, attached[w.Name])
	}
	return plan, nil
}

// ApprovalPreview is the rule an approval would add, as policy.yaml has it,
// and the credentials its endpoints would carry by default (Attach) or
// could carry (Offer), whatever the scope's attach and offer say.
type ApprovalPreview struct {
	YAML   string           `json:"yaml"`
	Attach []ApprovalAttach `json:"attach"`
	Offer  []ApprovalAttach `json:"offer"`
}

// PreviewApproval is what approving a pending or rejected proposal would
// add (nothing is changed).
func (o *OpenShell) PreviewApproval(ctx context.Context, w Sandbox, request, digest string, scope *ProposalScope) (ApprovalPreview, error) {
	none := ApprovalPreview{}
	pol, err := o.policy(ctx)
	if err != nil {
		return none, err
	}
	chunk, _, err := draftChunk(ctx, pol, w.Name, request, "pending", "rejected")
	if err != nil {
		return none, err
	}
	if chunk == nil {
		return none, errCode("request_not_found", nil, "この依頼は見つかりません")
	}
	if digest != proposalDigest(*chunk) {
		return none, errProposalChanged()
	}
	if chunk.ProposedRule == nil {
		return none, errCode("proposal_empty", nil, "この提案には規則がありません")
	}
	if scope, err = scope.against(*chunk.ProposedRule); err != nil {
		return none, err
	}
	sync, st, err := o.policyCompare(ctx, w, false)
	if err != nil {
		return none, err
	}
	rules := sync.rules
	if st.State == "none" {
		rules = revisionRulesOf(sync.latest)
	}
	plan, err := o.approvalPlan(ctx, w, rules, chunk, scope)
	if err != nil {
		return none, err
	}
	out := ApprovalPreview{Attach: []ApprovalAttach{}, Offer: []ApprovalAttach{}}
	if out.YAML, err = encodeYAML(storeRules(map[string]v1.NetworkPolicyRule{plan.Name: plan})); err != nil {
		return none, err
	}
	var plain *ProposalScope
	if scope != nil {
		x := *scope
		x.Attach, x.Offer = nil, false
		plain = &x
	}
	if plan, err = o.approvalPlan(ctx, w, rules, chunk, plain); err != nil {
		return none, err
	}
	list, attached, err := o.listProvidersFor(ctx)
	if err != nil {
		return none, err
	}
	composed, err := o.withProviders(ctx, w, rules)
	if err != nil {
		return none, err
	}
	out.Attach = append(out.Attach, approvalAttach(plan, composed)...)
	out.Offer = append(out.Offer, credentialOffers(plan, list, attached[w.Name])...)
	return out, nil
}

func binaryPaths(r v1.NetworkPolicyRule) []string {
	out := make([]string, len(r.Binaries))
	for i, b := range r.Binaries {
		out[i] = b.Path
	}
	return out
}

// ProposalScope narrows a proposal before it is approved: OpenShell then
// inspects the HTTP requests (protocol rest, enforced) and lets only reads
// (GET, HEAD, OPTIONS) or the listed methods and paths through. Reads and
// paths combine: read-only with rules allows GET, HEAD and OPTIONS on each
// rule's path (the rules then name no method).
//
// Audit is the first step when the paths are not known yet (a blocked
// connection never sent a request): OpenShell lets every request through and
// only logs it. Iwato shows the logged methods and paths on the endpoint,
// for the owner to enforce it with the ones it needs (openshell_audit.go).
//
// Binaries, when set, replace the proposed rule's programs, for instance a
// versioned path widened to a glob (claude-*-linux-x64). Alone they leave the
// endpoints as proposed.
//
// Uninspected are the indexes of the proposed endpoints approved as
// proposed (uninspected, L4); the others are narrowed, and left out without
// rules of their own.
type ProposalScope struct {
	ReadOnly    bool           `json:"readOnly"`
	Audit       bool           `json:"audit"`
	Rules       []ProposalRule `json:"rules"`
	Binaries    []string       `json:"binaries"`
	Uninspected []int          `json:"uninspected,omitempty"`
	// Attach: whether the approved endpoints carry the credential of the
	// endpoints they shadow (nil: that default, PendingProposal.Attach).
	Attach *bool `json:"attach,omitempty"`
	// Offer binds the credentials offered (PendingProposal.Offer), off
	// unless the owner turns it on (ADR 0022).
	Offer bool `json:"offer,omitempty"`
}

// narrows: the scope changes the proposed endpoints (programs and
// credentials do not).
func (s *ProposalScope) narrows() bool {
	return s != nil && (s.ReadOnly || s.Audit || len(s.Rules) > 0 || len(s.Uninspected) > 0)
}

// ProposalRule allows one method ("*" for any) on paths matching a glob
// ("*" within a segment, "**" across segments). In a scope, Host limits it
// to the proposal's endpoints on that host; empty is the host-less ones, or
// every endpoint of a proposal with a single host.
type ProposalRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Host   string `json:"host,omitempty"`
}

var scopeMethods = map[string]bool{"*": true, "GET": true, "HEAD": true, "OPTIONS": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// readMethods are what read-only allows (OpenShell's read-only preset).
var readMethods = []string{"GET", "HEAD", "OPTIONS"}

// Validate accepts read-only (with paths or not), audit, or 1 to 16 rules
// with a known method and an absolute path without spaces or control
// characters; and up to 8 programs (validBinary). Programs alone keep the
// proposed access.
func (s ProposalScope) Validate() error {
	if s.Audit && (s.ReadOnly || len(s.Rules) > 0) {
		return errCode("scope_audit_alone", nil, "監査は読み取りだけやメソッドとパスと一緒には指定できません")
	}
	if !s.narrows() && len(s.Binaries) == 0 && s.Attach == nil && !s.Offer {
		return errCode("scope_empty", nil, "読み取りだけ、監査、メソッドとパス、プログラムのどれかを指定してください")
	}
	if len(s.Binaries) > 8 {
		return errCode("scope_binaries_too_many", nil, "プログラムは 8 個までです")
	}
	if len(s.Uninspected) > proposalLimits.Endpoints || slices.ContainsFunc(s.Uninspected, func(i int) bool { return i < 0 }) {
		return errCode("scope_uninspected_invalid", nil, "検査せずに通す宛先の指定が正しくありません")
	}
	for _, b := range s.Binaries {
		if err := validBinary(b); err != nil {
			return err
		}
	}
	if !s.ReadOnly {
		return validateRules(s.Rules)
	}
	// Read-only rules are paths; the methods are the reads.
	paths := make([]ProposalRule, len(s.Rules))
	for i, r := range s.Rules {
		if r.Method != "" {
			return errCode("scope_read_no_method", nil, "読み取りだけではメソッドを指定しません")
		}
		paths[i] = ProposalRule{Method: "GET", Path: r.Path}
	}
	return validateRules(paths)
}

// validateRules accepts up to 16 rules with a known method and an absolute
// path (or OpenShell's "**") without spaces or control characters. The 16 is
// Iwato's own limit: OpenShell 0.1.6 sets none per endpoint (only the policy's
// size, 256 KiB).
func validateRules(rules []ProposalRule) error {
	if len(rules) > 16 {
		return errCode("scope_rules_too_many", nil, "規則は 16 個までです")
	}
	for _, r := range rules {
		if !scopeMethods[r.Method] {
			return errCode("scope_method_invalid", map[string]any{"method": fmt.Sprintf("%q", r.Method)}, fmt.Sprintf("メソッド %q は使えません", r.Method))
		}
		if r.Host != "" && !hostPattern.MatchString(r.Host) {
			return errCode("policy_host_invalid", map[string]any{"host": fmt.Sprintf("%q", r.Host)}, fmt.Sprintf("ホスト %q は使えません", r.Host))
		}
		if !strings.HasPrefix(r.Path, "/") && r.Path != "**" || len(r.Path) > 512 || strings.IndexFunc(r.Path, func(c rune) bool { return c <= ' ' || c == 0x7f }) >= 0 {
			return errCode("scope_path_invalid", map[string]any{"path": fmt.Sprintf("%q", r.Path)}, fmt.Sprintf("パス %q は / で始まり、空白を含まないようにしてください", r.Path))
		}
	}
	return nil
}

// validBinary accepts an absolute path or glob (OpenShell matches globs with
// "/" as the separator: "*" within a segment, "**" across segments) of
// printable characters without empty segments.
func validBinary(b string) error {
	ok := strings.HasPrefix(b, "/") && len(b) <= 512 && strings.IndexFunc(b, func(c rune) bool { return c <= ' ' || c == 0x7f || !unicode.IsPrint(c) }) < 0
	if ok && b != "/" {
		for _, seg := range strings.Split(b[1:], "/") {
			ok = ok && seg != ""
		}
	}
	if !ok || b == "/" {
		return errCode("scope_binary_invalid", map[string]any{"binary": fmt.Sprintf("%q", b)}, fmt.Sprintf("プログラム %q は / で始まる絶対パスで、空白や空の区切り (//) を含まないようにしてください", b))
	}
	return nil
}

// against checks a scope against the rule it narrows. The programs are the
// page's: a scope without them is refused when the rule names some (the
// rule's are never kept). A scope that only names the rule's own programs
// is no narrowing (nil: approved as proposed).
func (s *ProposalScope) against(rule v1.NetworkPolicyRule) (*ProposalScope, error) {
	if s == nil {
		return nil, nil
	}
	if len(s.Binaries) == 0 && len(rule.Binaries) > 0 {
		return nil, errCode("scope_binaries_missing", nil, "承認するプログラムがありません")
	}
	if slices.ContainsFunc(s.Uninspected, func(i int) bool { return i >= len(rule.Endpoints) }) {
		return nil, errCode("scope_uninspected_invalid", nil, "検査せずに通す宛先の指定が正しくありません")
	}
	if !s.narrows() && s.Attach == nil && !s.Offer {
		same := len(s.Binaries) == len(rule.Binaries)
		for i, b := range rule.Binaries {
			same = same && s.Binaries[i] == b.Path
		}
		if same {
			return nil, nil
		}
	}
	if len(s.apply(rule).Endpoints) == 0 {
		return nil, errCode("scope_no_endpoint", nil, "承認する宛先がありません")
	}
	return s, nil
}

// apply sets the programs and narrows the requests of every endpoint but
// the uninspected ones; everything else, allowed_ips included, stays as
// proposed.
func (s ProposalScope) apply(rule v1.NetworkPolicyRule) v1.NetworkPolicyRule {
	if len(s.Binaries) > 0 {
		rule.Binaries = make([]v1.PolicyNetworkBinary, len(s.Binaries))
		for i, b := range s.Binaries {
			rule.Binaries[i] = v1.PolicyNetworkBinary{Path: b}
		}
	}
	if !s.narrows() {
		return rule
	}
	hosts := map[string]bool{}
	for _, e := range rule.Endpoints {
		hosts[strings.ToLower(e.Host)] = true
	}
	endpoints := make([]v1.PolicyNetworkEndpoint, 0, len(rule.Endpoints))
	for i, e := range rule.Endpoints {
		if slices.Contains(s.Uninspected, i) {
			endpoints = append(endpoints, e)
			continue
		}
		if !s.ReadOnly && !s.Audit && len(s.Rules) == 0 {
			continue
		}
		e.Protocol, e.Enforcement = "rest", v1.NetworkEnforcementModeEnforce
		// Inspected: not tls: skip (OpenShell would refuse a provider's
		// credentials on it).
		if e.TLS == v1.NetworkTLSModeSkip {
			e.TLS = v1.NetworkTLSModeUnspecified
		}
		e.Access, e.Rules = v1.NetworkAccessPresetUnspecified, nil
		switch {
		case s.ReadOnly && len(s.Rules) == 0:
			e.Access = v1.NetworkAccessPresetReadOnly
		case s.ReadOnly:
			seen := map[string]bool{}
			for _, r := range s.Rules {
				if seen[r.Path] {
					continue
				}
				seen[r.Path] = true
				for _, m := range readMethods {
					e.Rules = append(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: m, Path: r.Path}})
				}
			}
		case s.Audit:
			// A REST endpoint needs rules; this one allows only GET / so
			// that every other request is logged with its method and path.
			e.Enforcement = v1.NetworkEnforcementModeAudit
			e.Rules = []v1.L7Rule{{Allow: &v1.L7Allow{Method: "GET", Path: "/"}}}
		default:
			for _, r := range s.Rules {
				if strings.EqualFold(r.Host, e.Host) || r.Host == "" && len(hosts) == 1 {
					e.Rules = append(e.Rules, v1.L7Rule{Allow: &v1.L7Allow{Method: r.Method, Path: r.Path}})
				}
			}
			// None of its rules kept: the endpoint is left out.
			if len(e.Rules) == 0 {
				continue
			}
		}
		endpoints = append(endpoints, e)
	}
	rule.Endpoints = endpoints
	return rule
}

// policy is the gateway's policy API (tests replace it).
func (o *OpenShell) policy(ctx context.Context) (v1.PolicyInterface, error) {
	if o.pol != nil {
		return o.pol, nil
	}
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	return sdk.Policy(), nil
}

// ProposalView is a sandbox's network policy as the policy tab shows it:
// the proposals waiting for the owner, the policy (each rule with where it
// came from), the approved proposals whose rule is gone, and the rejected
// ones. Iwato keeps none of it: it is all OpenShell's.
type ProposalView struct {
	Available bool               `json:"available"`
	Pending   []PendingProposal  `json:"pending"`
	Rejected  []RejectedProposal `json:"rejected"`
	Unlinked  []UnlinkedProposal `json:"unlinked"`
	Error     string             `json:"error,omitempty"`
	// Policy is the sandbox's effective network policy.
	Policy *CurrentPolicy `json:"policy,omitempty"`
	// Changes are the revisions approvals and edits in Iwato made, until
	// a while after the sandbox loaded them (the server keeps them).
	Changes []PolicyChange `json:"changes"`
	// Limits are the most the approve dialog shows of a proposal
	// (proposal_guard.go); HostGateways the names and addresses that reach
	// the host's loopback, and ListenPort Iwato's port (the server fills
	// it): the page warns of rules to them.
	Limits       ProposalLimits `json:"limits"`
	HostGateways []string       `json:"hostGateways"`
	ListenPort   uint32         `json:"listenPort,omitempty"`
}

// PendingProposal is a proposal (OpenShell's draft chunk) as the policy tab
// shows it; the rejected and unlinked lists show theirs the same way.
type PendingProposal struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	State     string `json:"state"`
	CreatedAt int64  `json:"createdAt"`
	// Source is "agent" (the agent proposed it through policy.local, with
	// its intent) or "auto" (OpenShell drafted it from blocked
	// connections, one per host, port and program).
	Source string `json:"source"`
	Intent string `json:"intent,omitempty"`
	// Notes are OpenShell's security notes.
	Notes string `json:"notes,omitempty"`
	// Rule is the rule the proposal joins (or makes) when approved.
	Rule string `json:"rule,omitempty"`
	// Hits, FirstSeen and LastSeen are OpenShell's count and times.
	Hits      int32 `json:"hits"`
	FirstSeen int64 `json:"firstSeen,omitempty"`
	LastSeen  int64 `json:"lastSeen,omitempty"`
	// Rules are the methods and paths the proposal allows when it inspects
	// requests (a refined proposal lists those OpenShell saw).
	Rules []ProposalRule `json:"rules,omitempty"`
	// Placeholder: the proposal's only rule is a GET / nobody saw
	// (placeholderRoot), left out of Rules.
	Placeholder bool `json:"placeholder,omitempty"`
	// AllowedIPs are the endpoints that carry allowed_ips.
	AllowedIPs []AllowedIPs `json:"allowedIps,omitempty"`
	// Binaries are the programs the rule names, the one that asked first.
	Binaries []string `json:"binaries,omitempty"`
	// Endpoints are where the rule goes (the approve dialog's summary, and
	// whether it looks like HTTP).
	Endpoints []ProposalEndpoint `json:"endpoints,omitempty"`
	// Digest is what a decision sends back (proposalDigest): the server
	// refuses it when the proposal changed since.
	Digest string `json:"digest"`
	// Unshown are the parts of the proposed rule the page cannot show
	// (proposalUnshown): it cannot be approved as proposed.
	Unshown []string `json:"unshown,omitempty"`
	// Attach are the approved rule's endpoints that carry a credential by
	// default, approved as proposed (inheritShadowed).
	Attach []ApprovalAttach `json:"attach,omitempty"`
	// Offer are its endpoints without one that could carry an attached
	// provider's (credentialOffers): scope.offer binds them.
	Offer []ApprovalAttach `json:"offer,omitempty"`
	plan  *v1.NetworkPolicyRule
	rule  *v1.NetworkPolicyRule
}

// mechanisticRationale is how OpenShell's own proposals explain themselves
// (openshell-supervisor mechanistic_mapper.rs); the chunk does not keep
// who proposed it.
var mechanisticRationale = regexp.MustCompile(`^Allow .+ to connect to \S+:\d+`)

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// placeholderRoot says whether the chunk's only rule, GET /, is no request
// anyone saw. OpenShell's own proposals (mechanistic_mapper.rs) carry the
// methods and paths the denials sampled, and say so in their rationale
// ("... with L7 inspection. Allowed paths: GET /x."); without samples they
// propose an endpoint without inspection. A REST one with a lone GET / and
// no samples got it elsewhere. The gateway gives a proposal for a known
// host and port the existing endpoint's whole contract (grpc/policy.rs
// canonicalize_advisor_add_rule). An endpoint Iwato audits allows GET /
// only to be valid (ProposalScope.apply). Read-only on exactly / would
// deny every real request. An agent's proposal that asks
// for / keeps it.
func placeholderRoot(c v1.PolicyChunk) bool {
	if !mechanisticRationale.MatchString(c.Rationale) || strings.Contains(c.Rationale, "with L7 inspection") {
		return false
	}
	rules := proposalRules(c)
	return len(rules) == 1 && rules[0].Method == "GET" && rules[0].Path == "/"
}

// pendingProposal is the chunk as the policy tab shows it.
func pendingProposal(id string, c v1.PolicyChunk) PendingProposal {
	p := PendingProposal{ID: id, Summary: chunkSummary(c), State: "pending-approval", CreatedAt: c.CreatedAt.Unix(), Source: "agent", Intent: c.Rationale, Notes: c.SecurityNotes,
		Rule: c.RuleName, Hits: c.HitCount, FirstSeen: unixOrZero(c.FirstSeen), LastSeen: unixOrZero(c.LastSeen),
		Rules: proposalRules(c), AllowedIPs: proposalAllowedIPs(c), Binaries: proposalBinaries(c), Endpoints: proposalEndpoints(c), Digest: proposalDigest(c)}
	if c.ProposedRule != nil {
		p.Unshown = proposalUnshown(*c.ProposedRule)
	}
	if mechanisticRationale.MatchString(c.Rationale) {
		p.Source, p.Intent = "auto", ""
	}
	if placeholderRoot(c) {
		p.Rules, p.Placeholder = nil, true
	}
	p.rule = c.ProposedRule
	return p
}

// RejectedProposal is a rejected proposal. OpenShell keeps counting it
// after: HitsAfter is how many times since (-1 when unknown), SeenAfter
// whether it came again at all. Auto names OpenShell's own rejections:
// "superseded" (a newer proposal for the same host, port and program) or
// "covered" (an approved proposal already covers it).
type RejectedProposal struct {
	PendingProposal
	DecidedAt int64  `json:"decidedAt"`
	HitsAfter int32  `json:"hitsAfter"`
	SeenAfter bool   `json:"seenAfter"`
	Auto      string `json:"auto,omitempty"`
}

type rejectNoteKey struct{}

// withRejectNote gives a rejection made with ctx the owner's reason, on
// one line.
func withRejectNote(ctx context.Context, reason string) context.Context {
	reason = strings.Join(strings.FieldsFunc(reason, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) }), " ")
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, rejectNoteKey{}, reason)
}

// rejectReason is the reason Iwato gives a rejection: a note for the agent,
// and the count so far (rejectedHits reads it back).
func rejectReason(hits int32, note string) string {
	if note != "" {
		note = ": " + note
	}
	return fmt.Sprintf("the owner rejected this rule in Iwato%s (hits: %d)", note, hits)
}

// tooManyReason tells the agent how to propose again a rule over the
// approve dialog's counts (empty when it is not).
func tooManyReason(unshown []string) string {
	var over []string
	for _, p := range unshown {
		if strings.Contains(p, " > ") {
			over = append(over, p)
		}
	}
	if len(over) == 0 {
		return ""
	}
	return fmt.Sprintf("too many items for Iwato to show (%s); propose again with fewer, e.g. combine paths with * or **, or split it into several proposals", strings.Join(over, ", "))
}

var rejectedHits = regexp.MustCompile(`\(hits: (\d+)\)$`)

// seenSlack is how much later than the decision OpenShell's last-seen may
// be without a hit since: a status change stamps it too.
const seenSlack = 2 * time.Second

func rejectedProposal(c v1.PolicyChunk) RejectedProposal {
	r := RejectedProposal{PendingProposal: pendingProposal(requestID(c.ID), c), DecidedAt: unixOrZero(c.DecidedAt), HitsAfter: -1}
	r.State = "rejected"
	switch {
	case strings.HasPrefix(c.RejectionReason, "superseded by chunk "):
		r.Auto = "superseded"
	case strings.HasPrefix(c.RejectionReason, "already covered by approved chunk "):
		r.Auto = "covered"
	}
	if m := rejectedHits.FindStringSubmatch(c.RejectionReason); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 32); err == nil && c.HitCount >= int32(n) {
			r.HitsAfter = c.HitCount - int32(n)
		}
	}
	r.SeenAfter = r.HitsAfter > 0 || r.HitsAfter < 0 && !c.DecidedAt.IsZero() && c.LastSeen.After(c.DecidedAt.Add(seenSlack))
	return r
}

// UnlinkedProposal is an approved proposal whose rule is gone (taken out by
// hand): OpenShell keeps it approved, so the same request is not proposed
// again. RuleExists says a rule of its name is there, without its program.
type UnlinkedProposal struct {
	PendingProposal
	DecidedAt  int64 `json:"decidedAt"`
	RuleExists bool  `json:"ruleExists"`
}

// ProposalEndpoint is a proposed endpoint: host, ports and the protocol
// OpenShell inspects ("rest", or empty for none), with what widens it
// beyond its rules, so the page can tell what approving as proposed
// allows: the access preset ("read-only", "read-write", "full", or
// "unknown") and, when inspected, the enforcement ("enforce", or "audit",
// OpenShell's default, which lets every request through).
//
// The rest is the endpoint's other settings, for the dialog to show all of
// the rule: tls ("skip": not inspected), the path it is scoped to, its
// allow and deny rules (Path empty: none given), allowed_ips, the provider
// whose credential it binds, and the settings that widen it.
type ProposalEndpoint struct {
	Host                         string         `json:"host"`
	Ports                        []uint32       `json:"ports"`
	Protocol                     string         `json:"protocol,omitempty"`
	Access                       string         `json:"access,omitempty"`
	Enforcement                  string         `json:"enforcement,omitempty"`
	TLS                          string         `json:"tls,omitempty"`
	Path                         string         `json:"path,omitempty"`
	Rules                        []ProposalRule `json:"rules,omitempty"`
	Deny                         []ProposalRule `json:"deny,omitempty"`
	AllowedIPs                   []string       `json:"allowedIps,omitempty"`
	Credential                   string         `json:"credential,omitempty"`
	AllowEncodedSlash            bool           `json:"allowEncodedSlash,omitempty"`
	WebsocketCredentialRewrite   bool           `json:"websocketCredentialRewrite,omitempty"`
	RequestBodyCredentialRewrite bool           `json:"requestBodyCredentialRewrite,omitempty"`
	AllowUninspectedCredentials  bool           `json:"allowUninspectedCredentials,omitempty"`
}

var tlsNames = map[v1.NetworkTLSMode]string{v1.NetworkTLSModeSkip: "skip", v1.NetworkTLSModeTerminate: "terminate", v1.NetworkTLSModePassthrough: "passthrough"}

// methodOrAny is a rule's method as the page shows it ("*" when none).
func methodOrAny(m string) string {
	if m == "" {
		return "*"
	}
	return m
}

// proposalBinaries are the chunk's program first, then the rule's others.
func proposalBinaries(c v1.PolicyChunk) []string {
	var out []string
	if c.Binary != "" {
		out = append(out, c.Binary)
	}
	if c.ProposedRule != nil {
		for _, b := range c.ProposedRule.Binaries {
			if b.Path != "" && !slices.Contains(out, b.Path) {
				out = append(out, b.Path)
			}
		}
	}
	return out
}

func proposalEndpoints(c v1.PolicyChunk) []ProposalEndpoint {
	if c.ProposedRule == nil {
		return nil
	}
	var out []ProposalEndpoint
	for _, e := range c.ProposedRule.Endpoints {
		pe := ProposalEndpoint{Host: e.Host, Ports: endpointPorts(e), Protocol: e.Protocol, TLS: tlsNames[e.TLS], Path: e.Path, AllowedIPs: slices.Clone(e.AllowedIPs),
			AllowEncodedSlash: e.AllowEncodedSlash, WebsocketCredentialRewrite: e.WebsocketCredentialRewrite,
			RequestBodyCredentialRewrite: e.RequestBodyCredentialRewrite, AllowUninspectedCredentials: e.AllowUninspectedCredentials}
		if e.CredentialBinding != nil {
			pe.Credential = e.CredentialBinding.Provider
		}
		for _, r := range e.Rules {
			if r.Allow != nil {
				pe.Rules = append(pe.Rules, ProposalRule{Method: methodOrAny(r.Allow.Method), Path: r.Allow.Path})
			}
		}
		for _, d := range e.DenyRules {
			pe.Deny = append(pe.Deny, ProposalRule{Method: methodOrAny(d.Method), Path: d.Path})
		}
		if e.Access != v1.NetworkAccessPresetUnspecified {
			if pe.Access = accessNames[e.Access]; pe.Access == "" {
				pe.Access = "unknown"
			}
		}
		if e.Protocol != "" {
			pe.Enforcement = "audit"
			if e.Enforcement == v1.NetworkEnforcementModeEnforce {
				pe.Enforcement = "enforce"
			}
		}
		out = append(out, pe)
	}
	return out
}

// AllowedIPs is one endpoint's allowed_ips (allowedIPsLabel); Host is empty
// for a host-less endpoint.
type AllowedIPs struct {
	Host string   `json:"host"`
	IPs  []string `json:"ips"`
}

// proposalAllowedIPs are a proposal's endpoints with allowed_ips.
func proposalAllowedIPs(c v1.PolicyChunk) []AllowedIPs {
	var out []AllowedIPs
	if c.ProposedRule == nil {
		return nil
	}
	for _, e := range c.ProposedRule.Endpoints {
		if len(e.AllowedIPs) > 0 {
			out = append(out, AllowedIPs{Host: e.Host, IPs: slices.Clone(e.AllowedIPs)})
		}
	}
	return out
}

// proposalRules are a proposal's allowed methods and paths.
func proposalRules(c v1.PolicyChunk) []ProposalRule {
	var rules []ProposalRule
	if c.ProposedRule == nil {
		return nil
	}
	for _, e := range c.ProposedRule.Endpoints {
		for _, r := range e.Rules {
			if r.Allow != nil && r.Allow.Path != "" {
				method := r.Allow.Method
				if method == "" {
					method = "*"
				}
				rules = append(rules, ProposalRule{Method: method, Path: r.Allow.Path})
			}
		}
	}
	return rules
}

// ProposedRule is the rule a proposal adds (or added, or would add again),
// with the proposal's digest: the server checks it against the attached
// providers' credentials.
func (o *OpenShell) ProposedRule(ctx context.Context, w Sandbox, request string) (*v1.NetworkPolicyRule, string, error) {
	pol, err := o.policy(ctx)
	if err != nil {
		return nil, "", err
	}
	c, _, err := draftChunk(ctx, pol, w.Name, request, "pending", "rejected", "approved")
	if err != nil {
		return nil, "", err
	}
	if c == nil {
		return nil, "", errCode("request_not_found", nil, "この依頼は見つかりません")
	}
	if c.ProposedRule != nil && c.ProposedRule.Name == "" {
		r := *c.ProposedRule
		r.Name = c.RuleName
		return &r, proposalDigest(*c), nil
	}
	return c.ProposedRule, proposalDigest(*c), nil
}
