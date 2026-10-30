package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Iwato is the source of truth for each sandbox's network policy (ADR
// 0022): every change Iwato makes is a commit in the sandbox's repository
// (policy_store.go) and is applied to OpenShell as a whole policy
// (UpdateConfig with policy), with the commit and the change's kind as the
// revision's provenance. The sections other than network_policies are sent
// as the latest revision has them (OpenShell refuses changing them on a
// live sandbox) and recorded in the commit too. The providers' rules
// (_provider_*) are no revision's: the gateway composes them on read, so a
// whole policy without them keeps them.
//
// OpenShell's policy is read to import it (a sandbox without a repository)
// and to see changes made outside Iwato (drift). Before a write, a drift is
// imported as its own commit, so the write starts from what OpenShell has.

const (
	provenanceCommit = "iwato/commit"
	provenanceChange = "iwato/change"
)

// policyStoreOf is the store (in memory when none was set).
func (o *OpenShell) policyStoreOf() *policyStore {
	o.storeOnce.Do(func() {
		if o.store == nil {
			o.store = &policyStore{}
		}
	})
	return o.store
}

// PolicyNote says why a change is made: its kind (approve, edit, yaml,
// preset, undo, revert, import, overwrite, recreate) and its reason, the
// commit message.
type PolicyNote struct {
	Kind    string
	Message string
}

type policyNoteKey struct{}

// withPolicyNote gives the writes made with ctx their reason.
func withPolicyNote(ctx context.Context, kind, message string) context.Context {
	return context.WithValue(ctx, policyNoteKey{}, PolicyNote{Kind: kind, Message: message})
}

func noteOf(ctx context.Context, fallback string) PolicyNote {
	n, _ := ctx.Value(policyNoteKey{}).(PolicyNote)
	if n.Kind == "" {
		n.Kind = "edit"
	}
	if n.Message == "" {
		n.Message = fallback
	}
	return n
}

// PolicyStoreState compares the repository with OpenShell: "synced", "none"
// (no repository yet), or "drift" (OpenShell's latest revision was made
// outside Iwato and differs).
type PolicyStoreState struct {
	State   string        `json:"state"`
	Head    *PolicyCommit `json:"head,omitempty"`
	Version uint32        `json:"version"`
	// Commit is the latest revision's provenance (empty: not Iwato's).
	Commit string `json:"commit,omitempty"`
}

// storeSync is the repository after syncing: its head and rules, and the
// latest revision they match.
type storeSync struct {
	repo   *git.Repository
	head   plumbing.Hash
	rules  map[string]v1.NetworkPolicyRule
	latest *v1.PolicyStatusResult
}

// policyCompare reads OpenShell's latest revision (once per change: the
// change holds the sandbox's policy lock) and compares it with the
// repository. It moves the branch to a commit OpenShell took but the
// branch missed (Iwato stopped between the two).
func (o *OpenShell) policyCompare(ctx context.Context, w Sandbox) (*storeSync, PolicyStoreState, error) {
	latest, err := o.latestPolicy(ctx, w)
	if err != nil {
		return nil, PolicyStoreState{}, err
	}
	st := PolicyStoreState{Version: latest.Revision.Version, Commit: latest.Revision.Provenance[provenanceCommit]}
	store := o.policyStoreOf()
	repo, err := store.open(w.Name, false)
	if err != nil {
		return nil, st, err
	}
	sync := &storeSync{repo: repo, latest: latest}
	if repo == nil {
		st.State = "none"
		return sync, st, nil
	}
	if sync.head, err = store.head(repo); err != nil {
		return nil, st, err
	}
	if sync.head.IsZero() {
		st.State = "none"
		return sync, st, nil
	}
	if st.Commit != "" && st.Commit != sync.head.String() {
		if c, err := repo.CommitObject(plumbing.NewHash(st.Commit)); err == nil && len(c.ParentHashes) == 1 && c.ParentHashes[0] == sync.head {
			if err := store.advance(repo, sync.head, c.Hash); err != nil {
				return nil, st, err
			}
			sync.head = c.Hash
		}
	}
	b, commit, err := store.content(repo, sync.head)
	if err != nil {
		return nil, st, err
	}
	if sync.rules, err = decodeStoreRules(b); err != nil {
		return nil, st, err
	}
	view := policyCommitView(commit)
	st.Head = &view
	st.State = "drift"
	if st.Commit == sync.head.String() || o.sameSince(ctx, w, latest, sync) {
		st.State = "synced"
	}
	return sync, st, nil
}

// policyLookBack is how many revisions before the latest the drift check
// reads for the last one Iwato made.
const policyLookBack = 5

// sameSince says whether the network rules are unchanged since Iwato's
// last revision: revisions made since (the supervisor adding filesystem
// paths, an approval that changed nothing) do not count. Without such a
// revision nearby, the rules are compared with the head's.
func (o *OpenShell) sameSince(ctx context.Context, w Sandbox, latest *v1.PolicyStatusResult, sync *storeSync) bool {
	key := rulesKey(revisionRulesOf(latest))
	for v := latest.Revision.Version; v > 1 && latest.Revision.Version-v < policyLookBack; v-- {
		rev, err := o.latestPolicy(ctx, w, v1.WithVersion(v-1))
		if err != nil {
			break
		}
		if rev.Revision.Provenance[provenanceCommit] == sync.head.String() {
			return rulesKey(revisionRulesOf(rev)) == key
		}
	}
	return rulesKey(sync.rules) == key
}

func revisionRulesOf(st *v1.PolicyStatusResult) map[string]v1.NetworkPolicyRule {
	if st == nil || st.Revision.Policy == nil {
		return map[string]v1.NetworkPolicyRule{}
	}
	out := map[string]v1.NetworkPolicyRule{}
	for k, r := range st.Revision.Policy.NetworkPolicies {
		if !isProviderRule(k) {
			out[k] = r
		}
	}
	return out
}

// syncStore makes the repository match OpenShell before a write: a
// sandbox without one gets OpenShell's policy as its first commit, and a
// drift is imported as a commit.
func (o *OpenShell) syncStore(ctx context.Context, w Sandbox) (*storeSync, error) {
	sync, st, err := o.policyCompare(ctx, w)
	if err != nil {
		return nil, err
	}
	switch st.State {
	case "none":
		return o.importPolicy(ctx, w, sync, fmt.Sprintf("Import the policy from OpenShell (version %d)", st.Version))
	case "drift":
		return o.importPolicy(ctx, w, sync, fmt.Sprintf("Import changes made outside Iwato (OpenShell version %d)", st.Version))
	}
	return sync, nil
}

// importPolicy commits OpenShell's latest revision as it is (nothing is
// applied).
func (o *OpenShell) importPolicy(ctx context.Context, w Sandbox, sync *storeSync, message string) (*storeSync, error) {
	store := o.policyStoreOf()
	repo, err := store.open(w.Name, true)
	if err != nil {
		return nil, err
	}
	p := v1.SandboxPolicy{}
	if sync.latest.Revision.Policy != nil {
		p = *sync.latest.Revision.Policy
	}
	p.NetworkPolicies = revisionRulesOf(sync.latest)
	content, err := encodeStorePolicy(&p)
	if err != nil {
		return nil, err
	}
	c, err := store.write(repo, content, sync.head, commitMessage("import", message))
	if err != nil {
		return nil, err
	}
	if err := store.advance(repo, sync.head, c); err != nil {
		return nil, err
	}
	return &storeSync{repo: repo, head: c, rules: p.NetworkPolicies, latest: sync.latest}, nil
}

// errPolicyConflict: OpenShell's sandbox changed while Iwato applied.
func errPolicyConflict(err error) error {
	return errCode("policy_conflict", map[string]any{"detail": safeCLIError(err)}, "適用中にサンドボックスが変わりました。もう一度試してください: "+safeCLIError(err))
}

// commitPolicy applies rules as the sandbox's whole network policy and,
// once OpenShell took it, moves the repository's branch to the commit
// that holds it. It answers OpenShell's new version.
func (o *OpenShell) commitPolicy(ctx context.Context, w Sandbox, sync *storeSync, rules map[string]v1.NetworkPolicyRule, note PolicyNote) (uint32, error) {
	p := v1.SandboxPolicy{}
	if sync.latest.Revision.Policy != nil {
		p = *sync.latest.Revision.Policy
	}
	p.NetworkPolicies = map[string]v1.NetworkPolicyRule{}
	for k, r := range rules {
		if !isProviderRule(k) {
			p.NetworkPolicies[k] = r
		}
	}
	store := o.policyStoreOf()
	content, err := encodeStorePolicy(&p)
	if err != nil {
		return 0, err
	}
	c, err := store.write(sync.repo, content, sync.head, commitMessage(note.Kind, note.Message))
	if err != nil {
		return 0, err
	}
	update := &v1.ConfigUpdate{Name: w.Name, Policy: &p, Annotations: map[string]string{provenanceCommit: c.String(), provenanceChange: note.Kind}}
	defer forgetPolicy(ctx, w.Name)
	var res *v1.ConfigUpdateResult
	if o.updateConfig != nil {
		res, err = o.updateConfig(ctx, update)
	} else {
		sdk, _, cerr := o.clients(ctx)
		if cerr != nil {
			return 0, cerr
		}
		// The sandbox object's version changes with each of Iwato's
		// revisions (their annotations go onto it), with providers
		// attached or detached, but not with revisions made elsewhere.
		sb, gerr := sdk.Sandboxes().Get(ctx, openshellWorkspace, w.Name)
		if gerr != nil {
			return 0, gerr
		}
		update.ExpectedResourceVersion = sb.ResourceVersion
		res, err = sdk.Config().Update(ctx, openshellWorkspace, update)
		if err != nil {
			if v1.IsConflict(err) && strings.Contains(strings.ToLower(err.Error()), "resource version") {
				return 0, errPolicyConflict(err)
			}
			err = fmt.Errorf("ポリシーを変えられませんでした: %w", err)
		}
	}
	if err != nil {
		return 0, err
	}
	if err := store.advance(sync.repo, sync.head, c); err != nil {
		return 0, err
	}
	sync.head, sync.rules = c, p.NetworkPolicies
	if res == nil {
		return 0, nil
	}
	return res.Version, nil
}

// changePolicy syncs the repository, changes its rules and applies them.
func (o *OpenShell) changePolicy(ctx context.Context, w Sandbox, note PolicyNote, change func(map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error)) (uint32, error) {
	sync, err := o.syncStore(ctx, w)
	if err != nil {
		return 0, err
	}
	rules, err := change(cloneRules(sync.rules))
	if err != nil {
		return 0, err
	}
	return o.commitPolicy(ctx, w, sync, rules, note)
}

// opsMessage describes merge operations for a commit message.
func opsMessage(ops []v1.PolicyMergeOperation) string {
	var lines []string
	for _, op := range ops {
		switch {
		case op.AddRule != nil:
			var hosts []string
			for _, e := range op.AddRule.Rule.Endpoints {
				hosts = append(hosts, endpointLabel(e.Host, endpointPorts(e)))
			}
			lines = append(lines, fmt.Sprintf("Add rule %s (%s)", op.AddRule.RuleName, strings.Join(hosts, ", ")))
		case op.RemoveRule != nil:
			lines = append(lines, "Remove rule "+op.RemoveRule.RuleName)
		case op.RemoveBinary != nil:
			lines = append(lines, fmt.Sprintf("Remove %s from rule %s", op.RemoveBinary.BinaryPath, op.RemoveBinary.RuleName))
		case op.RemoveEndpoint != nil:
			lines = append(lines, fmt.Sprintf("Remove %s:%d from rule %s", op.RemoveEndpoint.Host, op.RemoveEndpoint.Port, op.RemoveEndpoint.RuleName))
		case op.AddAllowRules != nil:
			lines = append(lines, fmt.Sprintf("Allow requests on %s in rule %s", op.AddAllowRules.Target.Host, op.AddAllowRules.Target.RuleName))
		}
	}
	return strings.Join(lines, "\n")
}

// applyOps makes merge operations the way `openshell policy update` does
// (simulateMerge), on the repository's rules, and applies the result as a
// whole: OpenShell's merge would leave the repository behind.
func (o *OpenShell) applyOps(ctx context.Context, w Sandbox, ops []v1.PolicyMergeOperation) (*v1.ConfigUpdateResult, error) {
	note := noteOf(ctx, opsMessage(ops))
	if detail := opsMessage(ops); detail != note.Message {
		note.Message += "\n\n" + detail
	}
	version, err := o.changePolicy(ctx, w, note, func(rules map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		return simulateMerge(rules, ops)
	})
	if err != nil {
		return nil, err
	}
	return &v1.ConfigUpdateResult{Version: version}, nil
}

// reassert applies the repository's policy again when OpenShell's latest
// revision is no longer it (an operation on a proposal merged into it).
func (o *OpenShell) reassert(ctx context.Context, w Sandbox, kind string) (uint32, error) {
	forgetPolicy(ctx, w.Name)
	sync, st, err := o.policyCompare(ctx, w)
	if err != nil || st.State != "drift" {
		return st.Version, err
	}
	return o.commitPolicy(ctx, w, sync, sync.rules, PolicyNote{Kind: kind, Message: "Put back the policy after OpenShell changed it"})
}

// approvedRule is the rule an approval writes (ADR 0022): every program
// (program matching is no boundary), and per inspected endpoint with
// explicit methods and paths, one endpoint per distinct path with that
// path set, so that the rule reaches only what was approved. Its endpoints
// stay the advisor's, as OpenShell marks them.
func approvedRule(name string, in v1.NetworkPolicyRule) v1.NetworkPolicyRule {
	out := v1.NetworkPolicyRule{Name: name, Binaries: []v1.PolicyNetworkBinary{{Path: "/**"}}}
	for _, e := range cloneRule(in).Endpoints {
		out.Endpoints = append(out.Endpoints, splitByPath(e)...)
	}
	for i := range out.Endpoints {
		out.Endpoints[i].AdvisorProposed = true
	}
	return out
}

// splitByPath makes an endpoint with REST allow rules one endpoint per
// distinct path. An endpoint with a path already, an access preset, audit,
// or a rule for any path stays as it is.
func splitByPath(e v1.PolicyNetworkEndpoint) []v1.PolicyNetworkEndpoint {
	if e.Protocol != "rest" || e.Path != "" || e.Access != v1.NetworkAccessPresetUnspecified || e.Enforcement != v1.NetworkEnforcementModeEnforce || len(e.Rules) == 0 {
		return []v1.PolicyNetworkEndpoint{e}
	}
	var paths []string
	for _, r := range e.Rules {
		if r.Allow == nil || r.Allow.Path == "" || r.Allow.Path == "**" || r.Allow.Path == "/**" {
			return []v1.PolicyNetworkEndpoint{e}
		}
		if !slices.Contains(paths, r.Allow.Path) {
			paths = append(paths, r.Allow.Path)
		}
	}
	var out []v1.PolicyNetworkEndpoint
	for _, p := range paths {
		x := e
		x.Path = p
		x.Rules = nil
		for _, r := range e.Rules {
			if r.Allow.Path == p {
				x.Rules = append(x.Rules, r)
			}
		}
		x.DenyRules = slices.Clone(e.DenyRules)
		x.AllowedIPs = slices.Clone(e.AllowedIPs)
		x.Ports = slices.Clone(e.Ports)
		out = append(out, x)
	}
	return out
}

// freeRuleName is name, or name_N when the rules have it.
func freeRuleName(rules map[string]v1.NetworkPolicyRule, name string) string {
	out := name
	for i := 2; ; i++ {
		if _, ok := rules[out]; !ok {
			return out
		}
		out = fmt.Sprintf("%s_%d", name, i)
	}
}

// ambiguityError is OpenShell's refusal of ambiguous endpoints, in its
// words (failGateway reads it).
func ambiguityError(list []EndpointAmbiguity) error {
	if len(list) == 0 {
		return nil
	}
	var lines []string
	for _, a := range list {
		lines = append(lines, a.String())
	}
	return errors.New("network endpoint ambiguity validation failed:\n" + strings.Join(lines, "\n"))
}

// checkWithProviders refuses rules that OpenShell would refuse with the
// attached providers' rules: endpoints that overlap with the same path
// specificity but differ in how requests are handled.
func (o *OpenShell) checkWithProviders(ctx context.Context, w Sandbox, before, after map[string]v1.NetworkPolicyRule) error {
	effective := map[string]v1.NetworkPolicyRule{}
	if cfg, err := o.effectivePolicy(ctx, w); err != nil {
		return err
	} else if cfg.Policy != nil {
		effective = cfg.Policy.NetworkPolicies
	}
	return ambiguityError(newAmbiguities(withProviderRules(before, effective), withProviderRules(after, effective)))
}

// PolicyStore says how the repository compares with OpenShell.
func (o *OpenShell) PolicyStore(ctx context.Context, w Sandbox) (PolicyStoreState, error) {
	_, st, err := o.policyCompare(ctx, w)
	return st, err
}

// PolicyCommits lists the repository's history, newest first.
func (o *OpenShell) PolicyCommits(_ context.Context, w Sandbox, limit int) ([]PolicyCommit, error) {
	store := o.policyStoreOf()
	repo, err := store.open(w.Name, false)
	if err != nil || repo == nil {
		return []PolicyCommit{}, err
	}
	return store.log(repo, limit)
}

// PolicyCommitDiff is one commit: its message and what it changed in
// policy.yaml (a unified diff).
func (o *OpenShell) PolicyCommitDiff(_ context.Context, w Sandbox, sha string) (PolicyCommit, string, error) {
	repo, c, err := o.storeCommit(w, sha)
	if err != nil {
		return PolicyCommit{}, "", err
	}
	view := policyCommitView(c)
	if len(c.ParentHashes) == 0 {
		b, _, err := o.policyStoreOf().content(repo, c.Hash)
		if err != nil {
			return view, "", err
		}
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		return view, "--- /dev/null\n+++ b/" + policyFile + "\n+" + strings.Join(lines, "\n+") + "\n", nil
	}
	parent, err := repo.CommitObject(c.ParentHashes[0])
	if err != nil {
		return view, "", err
	}
	patch, err := parent.Patch(c)
	if err != nil {
		return view, "", err
	}
	return view, patch.String(), nil
}

// storeCommit finds a commit of the sandbox's repository by its hash.
func (o *OpenShell) storeCommit(w Sandbox, sha string) (*git.Repository, *object.Commit, error) {
	if !commitPattern.MatchString(sha) {
		return nil, nil, errCode("policy_commit_not_found", nil, "この変更は見つかりません")
	}
	repo, err := o.policyStoreOf().open(w.Name, false)
	if err != nil {
		return nil, nil, err
	}
	if repo == nil {
		return nil, nil, errCode("policy_commit_not_found", nil, "この変更は見つかりません")
	}
	c, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return nil, nil, errCode("policy_commit_not_found", nil, "この変更は見つかりません")
	}
	return repo, c, nil
}

// RevertPolicyCommit undoes one commit as a new commit: the rules it
// changed go back to how they were before it, unless a later change
// changed them again.
func (o *OpenShell) RevertPolicyCommit(ctx context.Context, w Sandbox, sha string) (uint32, error) {
	repo, c, err := o.storeCommit(w, sha)
	if err != nil {
		return 0, err
	}
	store := o.policyStoreOf()
	after, _, err := store.content(repo, c.Hash)
	if err != nil {
		return 0, err
	}
	afterRules, err := decodeStoreRules(after)
	if err != nil {
		return 0, err
	}
	beforeRules := map[string]v1.NetworkPolicyRule{}
	if len(c.ParentHashes) > 0 {
		b, _, err := store.content(repo, c.ParentHashes[0])
		if err != nil {
			return 0, err
		}
		if beforeRules, err = decodeStoreRules(b); err != nil {
			return 0, err
		}
	}
	view := policyCommitView(c)
	note := PolicyNote{Kind: "revert", Message: fmt.Sprintf("Revert %q\n\nThis reverts commit %s.", view.Subject, c.Hash)}
	return o.changePolicy(ctx, w, note, func(rules map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
		return revertRules(rules, beforeRules, afterRules)
	})
}

// revertRules puts the rules a commit changed (before → after) back in
// rules as they were before it. A rule changed again since is a conflict.
func revertRules(rules, before, after map[string]v1.NetworkPolicyRule) (map[string]v1.NetworkPolicyRule, error) {
	key := func(m map[string]v1.NetworkPolicyRule, name string) string {
		r, ok := m[name]
		if !ok {
			return ""
		}
		return rulesKey(map[string]v1.NetworkPolicyRule{name: r})
	}
	names := slices.Collect(maps.Keys(before))
	for k := range after {
		if _, ok := before[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	var conflicts []string
	changed := false
	for _, name := range names {
		was, now := key(before, name), key(after, name)
		if was == now {
			continue
		}
		changed = true
		if key(rules, name) != now {
			conflicts = append(conflicts, name)
			continue
		}
		if r, ok := before[name]; ok {
			rules[name] = r
		} else {
			delete(rules, name)
		}
	}
	if len(conflicts) > 0 {
		return nil, errCode("policy_revert_conflict", map[string]any{"rules": conflicts}, "後の変更が同じ規則を変えているので、取り消せません: "+strings.Join(conflicts, "、"))
	}
	if !changed {
		return nil, errCode("policy_revert_empty", nil, "この変更は規則を変えていません")
	}
	return rules, nil
}

// ImportPolicy commits OpenShell's latest policy (a drift accepted).
func (o *OpenShell) ImportPolicy(ctx context.Context, w Sandbox) (PolicyStoreState, error) {
	sync, st, err := o.policyCompare(ctx, w)
	if err != nil {
		return st, err
	}
	if st.State != "synced" {
		if _, err := o.importPolicy(ctx, w, sync, fmt.Sprintf("Import changes made outside Iwato (OpenShell version %d)", st.Version)); err != nil {
			return st, err
		}
	}
	_, st, err = o.policyCompare(ctx, w)
	return st, err
}

// OverwritePolicy applies the repository's policy over a drift.
func (o *OpenShell) OverwritePolicy(ctx context.Context, w Sandbox) (uint32, error) {
	sync, st, err := o.policyCompare(ctx, w)
	if err != nil {
		return 0, err
	}
	if st.State == "none" {
		return 0, errCode("policy_store_missing", nil, "このサンドボックスのポリシーはまだ記録されていません")
	}
	return o.commitPolicy(ctx, w, sync, sync.rules, PolicyNote{Kind: "overwrite", Message: fmt.Sprintf("Overwrite changes made outside Iwato (OpenShell version %d)", st.Version)})
}

// ForgetPolicyStore deletes the sandbox's repository (it was deleted).
func (o *OpenShell) ForgetPolicyStore(name string) error {
	return o.policyStoreOf().remove(name)
}
