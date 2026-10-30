package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// The policy tab's lists come from OpenShell: an approved proposal (chunk)
// added its rule under its rule name (approveChunk), so a rule "comes from"
// the approved chunks of its name whose program it still holds.

// ChunkLink is an approved proposal a rule came from. ID is its request ID.
// Binary is the program that asked; Binaries the ones approved (Widened:
// the approve dialog widened it to a glob). Undoing it takes out Binary
// and the approved ones no other approved proposal of the rule holds.
// RemovesRule says the rule goes with them. Drops are the endpoints that
// then go too without having come from this proposal.
type ChunkLink struct {
	ID          string   `json:"id"`
	Binary      string   `json:"binary"`
	Binaries    []string `json:"binaries"`
	Widened     bool     `json:"widened,omitempty"`
	Source      string   `json:"source"`
	Intent      string   `json:"intent,omitempty"`
	DecidedAt   int64    `json:"decidedAt"`
	RemovesRule bool     `json:"removesRule"`
	Drops       []string `json:"drops,omitempty"`
}

// chunkBinaries are the programs an approved chunk put into its rule: its
// (edited) proposed rule's, or the one that asked when it names none.
func chunkBinaries(c v1.PolicyChunk) []string {
	var out []string
	if c.ProposedRule != nil {
		for _, b := range c.ProposedRule.Binaries {
			if b.Path != "" && !slices.Contains(out, b.Path) {
				out = append(out, b.Path)
			}
		}
	}
	if len(out) == 0 && c.Binary != "" {
		out = append(out, c.Binary)
	}
	return out
}

func ruleHasBinary(rule v1.NetworkPolicyRule, path string) bool {
	return slices.ContainsFunc(rule.Binaries, func(b v1.PolicyNetworkBinary) bool { return b.Path == path })
}

// linked says whether an approved chunk's rule is in rules: the rule of its
// name still holds one of its programs.
func linked(c v1.PolicyChunk, rules map[string]v1.NetworkPolicyRule) bool {
	rule, ok := rules[c.RuleName]
	if !ok || c.RuleName == "" || isProviderRule(c.RuleName) {
		return false
	}
	if ruleHasBinary(rule, c.Binary) {
		return true
	}
	return slices.ContainsFunc(chunkBinaries(c), func(b string) bool { return ruleHasBinary(rule, b) })
}

// undoPlan is what undoing approved chunk c does to its rule: OpenShell
// takes out c.Binary; leftovers are the other programs c put in that no
// other approved chunk of the rule holds (a glob the dialog widened it to),
// which Iwato takes out in the same action. removesRule says no program is
// left, so the rule goes with its endpoints.
func undoPlan(rule v1.NetworkPolicyRule, c v1.PolicyChunk, approved []v1.PolicyChunk) (leftovers []string, removesRule bool) {
	claimed := map[string]bool{}
	for _, o := range approved {
		if o.ID == c.ID || o.RuleName != c.RuleName {
			continue
		}
		claimed[o.Binary] = true
		for _, b := range chunkBinaries(o) {
			claimed[b] = true
		}
	}
	for _, b := range chunkBinaries(c) {
		if b != c.Binary && !claimed[b] && ruleHasBinary(rule, b) && !slices.Contains(leftovers, b) {
			leftovers = append(leftovers, b)
		}
	}
	left := 0
	for _, b := range rule.Binaries {
		if b.Path != c.Binary && !slices.Contains(leftovers, b.Path) {
			left++
		}
	}
	return leftovers, len(rule.Binaries) > 0 && left == 0
}

// endpointLabel is host:ports.
func endpointLabel(host string, ports []uint32) string {
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = fmt.Sprint(p)
	}
	if host == "" {
		host = "すべてのホスト"
	}
	return host + ":" + strings.Join(ps, ",")
}

// drops are the rule's endpoints that did not come from c.
func drops(rule v1.NetworkPolicyRule, c v1.PolicyChunk) []string {
	var out []string
	for _, e := range rule.Endpoints {
		from := c.ProposedRule != nil && slices.ContainsFunc(c.ProposedRule.Endpoints, func(x v1.PolicyNetworkEndpoint) bool {
			return strings.EqualFold(x.Host, e.Host) && slices.Equal(endpointPorts(x), endpointPorts(e))
		})
		if !from {
			out = append(out, endpointLabel(e.Host, endpointPorts(e)))
		}
	}
	return out
}

// rulesOf turns the view's rules back into what linking needs (programs and
// endpoints).
func rulesOf(p *CurrentPolicy) map[string]v1.NetworkPolicyRule {
	out := map[string]v1.NetworkPolicyRule{}
	for _, r := range p.Rules {
		rule := v1.NetworkPolicyRule{Name: r.Name}
		for _, b := range r.Binaries {
			rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b})
		}
		for _, e := range r.Endpoints {
			ports := slices.Clone(e.Ports)
			first := uint32(0)
			if len(ports) > 0 {
				first = ports[0]
			}
			rule.Endpoints = append(rule.Endpoints, v1.PolicyNetworkEndpoint{Host: e.Host, Port: first, Ports: ports})
		}
		out[r.Name] = rule
	}
	return out
}

// linkChunks marks the policy's rules with the approved chunks they came
// from, and answers the approved chunks whose rule is gone.
func linkChunks(p *CurrentPolicy, chunks []v1.PolicyChunk) []UnlinkedProposal {
	rules := rulesOf(p)
	var approved []v1.PolicyChunk
	for _, c := range chunks {
		if c.Status == "approved" {
			approved = append(approved, c)
		}
	}
	sort.SliceStable(approved, func(i, j int) bool { return approved[i].DecidedAt.Before(approved[j].DecidedAt) })
	unlinked := []UnlinkedProposal{}
	for _, c := range approved {
		if !linked(c, rules) {
			_, exists := rules[c.RuleName]
			u := UnlinkedProposal{PendingProposal: pendingProposal(requestID(c.ID), c), DecidedAt: unixOrZero(c.DecidedAt), RuleExists: exists}
			u.State = "approved"
			unlinked = append(unlinked, u)
			continue
		}
		rule := rules[c.RuleName]
		_, removes := undoPlan(rule, c, approved)
		view := pendingProposal("", c)
		link := ChunkLink{ID: requestID(c.ID), Binary: c.Binary, Binaries: chunkBinaries(c), Source: view.Source, Intent: view.Intent, DecidedAt: unixOrZero(c.DecidedAt), RemovesRule: removes}
		link.Widened = c.Binary != "" && !slices.Contains(link.Binaries, c.Binary)
		if removes {
			link.Drops = drops(rule, c)
		}
		for i := range p.Rules {
			if p.Rules[i].Name == c.RuleName {
				p.Rules[i].Chunks = append(p.Rules[i].Chunks, link)
				p.Rules[i].Source = "proposal"
			}
		}
	}
	return unlinked
}

// UndoProposal undoes an approved proposal: its program goes out of its
// rule with the programs it was approved with that no other approved
// proposal of the rule holds (the rule goes with its last program), as a
// commit applied as a whole; then OpenShell puts it back to pending
// (UndoDraftChunk, whose own removal of the program then finds nothing to
// remove).
//
// With restore, the proposal's rule is gone already (taken out by hand):
// only the proposal goes back to pending. A rule of the same name that is
// still there (without the proposal's programs) is left as it is.
func (o *OpenShell) UndoProposal(ctx context.Context, w Sandbox, request string, restore bool) (PolicyChange, error) {
	none := PolicyChange{}
	pol, err := o.policy(ctx)
	if err != nil {
		return none, err
	}
	defer o.forgetDraft(w.Name)
	chunk, approved, err := draftChunk(ctx, pol, w.Name, request, "approved")
	if err != nil {
		return none, err
	}
	if chunk == nil {
		// Found elsewhere, it is not approved (any more).
		if other, _, err := draftChunk(ctx, pol, w.Name, request, "pending", "rejected"); err != nil {
			return none, err
		} else if other != nil {
			return none, errCode("proposal_not_approved", nil, "この提案は承認済みではありません")
		}
		return none, errCode("proposal_not_found", nil, "この提案は見つかりません")
	}
	sync, err := o.syncStore(ctx, w)
	if err != nil {
		return none, err
	}
	change := PolicyChange{Kind: "edit", Rule: chunk.RuleName, At: time.Now().Unix()}
	if chunk.ProposedRule != nil && len(chunk.ProposedRule.Endpoints) > 0 {
		change.Host = chunk.ProposedRule.Endpoints[0].Host
	}
	isLinked := linked(*chunk, sync.rules)
	if restore && isLinked {
		return none, errCode("proposal_rule_exists", nil, "この提案の規則はあります。「取り消す」を使ってください")
	}
	if !restore && !isLinked {
		return none, errCode("proposal_rule_missing", nil, "この提案の規則がありません。「規則を戻して取り消す」を使ってください")
	}
	if rule := sync.rules[chunk.RuleName]; !restore {
		leftovers, _ := undoPlan(rule, *chunk, approved)
		var ops []v1.PolicyMergeOperation
		for _, b := range append([]string{chunk.Binary}, leftovers...) {
			if ruleHasBinary(rule, b) {
				ops = append(ops, v1.PolicyMergeOperation{RemoveBinary: &v1.RemoveNetworkBinary{RuleName: chunk.RuleName, BinaryPath: b}})
			}
		}
		rules, err := simulateMerge(sync.rules, ops)
		if err != nil {
			return none, err
		}
		message := fmt.Sprintf("Undo proposal %s\n\n%s\n%s", chunk.ID, chunkSummary(*chunk), opsMessage(ops))
		if len(ops) > 0 {
			if change.Version, err = o.commitPolicy(ctx, w, sync, rules, PolicyNote{Kind: "undo", Message: message}); err != nil {
				return none, err
			}
		}
	}
	if _, err := pol.UndoDraftChunk(ctx, openshellWorkspace, w.Name, chunk.ID); err != nil {
		return none, err
	}
	if v, err := o.reassert(ctx, w, "undo"); err != nil {
		return none, err
	} else if v > change.Version {
		change.Version = v
	}
	return o.withVersion(ctx, w, change), nil
}

// revisionRules are the latest revision's rules (the ones edits apply to;
// the providers' are not among them).
func (o *OpenShell) revisionRules(ctx context.Context, w Sandbox) (map[string]v1.NetworkPolicyRule, error) {
	st, err := o.latestPolicy(ctx, w)
	if err != nil {
		return nil, err
	}
	if st.Revision.Policy == nil {
		return map[string]v1.NetworkPolicyRule{}, nil
	}
	return st.Revision.Policy.NetworkPolicies, nil
}
