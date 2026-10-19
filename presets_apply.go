package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Presets on an existing sandbox (the policy tab): GET
// /api/sandboxes/NAME/presets lists the sandbox's, POST .../presets/apply
// and .../presets/remove (a PresetRef) put one into its policy or take it
// out. Both go through the merge operations a hand edit uses, checked as
// an edit is, and change Sandbox.Presets, so a recreate keeps them. A
// sandbox OpenShell does not have yet only changes Sandbox.Presets: the
// rules go into its first policy.

// presetRuntime is what applying presets needs from the runtime. OpenShell
// and Fake implement it.
type presetRuntime interface {
	policyLayersRuntime
	Exists(context.Context, Sandbox) (bool, error)
	// ApplyPolicyChange makes one policy update and says its version.
	ApplyPolicyChange(context.Context, Sandbox, []v1.PolicyMergeOperation) (uint32, error)
}

// ApplyPolicyChange makes one policy update of merge operations.
func (o *OpenShell) ApplyPolicyChange(ctx context.Context, w Sandbox, ops []v1.PolicyMergeOperation) (uint32, error) {
	res, err := o.applyOps(ctx, w, ops)
	if err != nil {
		return 0, err
	}
	c := PolicyChange{}
	if res != nil {
		c.Version = res.Version
	}
	return o.withVersion(ctx, w, c).Version, nil
}

// sandboxPresetView is one of a sandbox's presets.
type sandboxPresetView struct {
	// Not PresetRef embedded: its MarshalJSON would take over.
	ID     string            `json:"id"`
	Params map[string]string `json:"params,omitempty"`
	Label  string            `json:"label"`
	// Missing: the preset is gone (an owner's file removed or broken, or
	// one of Iwato's dropped in an update).
	Missing bool `json:"missing,omitempty"`
}

func (s *Server) handleSandboxPresets(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method == http.MethodGet && len(parts) == 2 {
		sb, ok := s.sandboxFor(w, parts, 2)
		if !ok || !s.require(w, r, false) {
			return
		}
		reloadUserPresets()
		out := []sandboxPresetView{}
		for _, ref := range sb.Presets {
			out = append(out, sandboxPresetView{ID: ref.ID, Params: ref.Params, Label: ref.label(), Missing: findPreset(ref.ID) == nil})
		}
		jsonResponse(w, 200, map[string]any{"presets": out})
		return
	}
	if r.Method != http.MethodPost || len(parts) != 3 || (parts[2] != "apply" && parts[2] != "remove") {
		fail(w, 405, "method/operation not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 3)
	if !ok || !s.require(w, r, true) {
		return
	}
	var ref PresetRef
	if decode(w, r, &ref) != nil {
		fail(w, 400, "invalid preset")
		return
	}
	rt, ok := s.rt.(presetRuntime)
	if !ok {
		fail(w, 501, "presets unavailable")
		return
	}
	s.mu.Lock()
	op := s.ops[sb.Name]
	s.mu.Unlock()
	if op != nil && op.kind != "start" {
		failBusy(w, opBusy[op.kind])
		return
	}
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	defer s.lockPolicy(sb.Name)()
	if parts[2] == "apply" {
		s.applyPreset(ctx, w, rt, sb, ref)
	} else {
		s.removePreset(ctx, w, rt, sb, ref)
	}
}

// setSandboxPresets changes the sandbox's presets in the configuration.
func (s *Server) setSandboxPresets(name string, change func([]PresetRef) []PresetRef) error {
	return s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for i := range list {
			if list[i].Name == name {
				list[i].Presets = change(slices.Clone(list[i].Presets))
				return list, nil
			}
		}
		return nil, errors.New("unknown sandbox")
	})
}

func (s *Server) applyPreset(ctx context.Context, w http.ResponseWriter, rt presetRuntime, sb Sandbox, ref PresetRef) {
	rules, err := presetRefRules(ref)
	if err != nil {
		failErr(w, 400, err)
		return
	}
	if slices.ContainsFunc(sb.Presets, ref.same) {
		failCode(w, 409, "preset_applied", map[string]any{"preset": ref.label()}, ref.label()+" はもう入っています")
		return
	}
	exists, err := rt.Exists(ctx, sb)
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	add := func(list []PresetRef) []PresetRef { return append(list, ref) }
	if !exists {
		// Created with it (sandboxPolicy), checked then by the gateway.
		if err := s.setSandboxPresets(sb.Name, add); err != nil {
			failErr(w, 409, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "onCreate": true})
		return
	}
	scopes, err := s.attachedScopes(ctx, sb)
	if err != nil {
		failCode(w, 409, "provider_unverifiable", map[string]any{"detail": safeCLIError(err)}, "プロバイダーを確かめられません: "+safeCLIError(err))
		return
	}
	if cs := credentialConflicts(rules, scopes); len(cs) > 0 {
		failCheck(w, conflictRefusal(cs))
		return
	}
	var ops []v1.PolicyMergeOperation
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		ops = append(ops, v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: rules[name]}})
	}
	if err := s.checkOps(ctx, sb, ops, "ambiguous_preset", map[string]any{"preset": ref.label()}, ref.label()+" は OpenShell に拒否されます"); err != nil {
		failCheck(w, err)
		return
	}
	// OpenShell folds an added rule whose name is new into an existing rule
	// with the same endpoint (merge.rs add_rule): the preset's access would
	// then live under another name, out of reach of a removal. Predicted
	// first, verified after.
	before, _, err := rt.PolicyLayers(ctx, sb)
	if err != nil {
		failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
		return
	}
	if merged, err := simulateMerge(before, ops); err == nil {
		if folded := foldedPresetRules(merged, rules); len(folded) > 0 {
			failCode(w, 409, "preset_would_fold", map[string]any{"preset": ref.label(), "rules": folded}, ref.label()+" を入れると、OpenShell が規則を既存の規則にまとめてしまい、あとで外せなくなるので入れません: "+strings.Join(folded, "、")+"。重なる規則を外してから入れてください。")
			return
		}
	}
	version, err := rt.ApplyPolicyChange(ctx, sb, ops)
	if err != nil {
		s.failGateway(ctx, w, sb, err)
		return
	}
	s.recordPolicyChange(sb.Name, PolicyChange{Version: version, Kind: "edit", Rule: opsRule(ops), At: time.Now().Unix()})
	after, _, err := rt.PolicyLayers(ctx, sb)
	if err != nil {
		fail(w, 409, "入れた後のポリシーを確かめられません ("+ref.label()+" はプリセットの一覧に入れていません。ポリシーのタブで確かめてください): "+safeCLIError(err))
		return
	}
	if folded := foldedPresetRules(after, rules); len(folded) > 0 {
		msg := ref.label() + " の規則を OpenShell が既存の規則にまとめたので、元に戻しました: " + strings.Join(folded, "、") + "。重なる規則を外してから入れてください。"
		if undo := rollbackOps(before, after); len(undo) > 0 {
			if v, err := rt.ApplyPolicyChange(ctx, sb, undo); err != nil {
				msg = ref.label() + " の規則を OpenShell が既存の規則にまとめ、元に戻せませんでした: " + strings.Join(folded, "、") + "。ポリシーのタブで、まとめられた規則から許可を外してください: " + safeCLIError(err)
			} else {
				s.recordPolicyChange(sb.Name, PolicyChange{Version: v, Kind: "edit", Rule: opsRule(undo), At: time.Now().Unix()})
				if again, _, err := rt.PolicyLayers(ctx, sb); err != nil || len(rollbackOps(before, again)) > 0 {
					msg = ref.label() + " の規則を OpenShell が既存の規則にまとめ、元に戻しきれませんでした: " + strings.Join(folded, "、") + "。ポリシーのタブで確かめてください。"
				}
			}
		}
		fail(w, 409, msg)
		return
	}
	if err := s.setSandboxPresets(sb.Name, add); err != nil {
		failErr(w, 409, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "version": version})
}

// removePreset takes the preset's rules out where the policy still has
// them as the sandbox's presets made them (a rule two presets share is put
// back as the others make it); a rule changed since (approved into,
// edited, folded by OpenShell into another rule) is left alone and named.
func (s *Server) removePreset(ctx context.Context, w http.ResponseWriter, rt presetRuntime, sb Sandbox, ref PresetRef) {
	i := slices.IndexFunc(sb.Presets, ref.same)
	if i < 0 {
		failCode(w, 409, "preset_not_applied", map[string]any{"preset": ref.label()}, ref.label()+" は入っていません")
		return
	}
	rest := slices.Delete(slices.Clone(sb.Presets), i, i+1)
	drop := func(list []PresetRef) []PresetRef {
		return slices.DeleteFunc(list, ref.same)
	}
	exists, err := rt.Exists(ctx, sb)
	if err != nil {
		fail(w, 409, safeCLIError(err))
		return
	}
	mine, err := presetRefRules(ref)
	if !exists || err != nil {
		// Not created yet, or the preset is gone: nothing to take out.
		if err := s.setSandboxPresets(sb.Name, drop); err != nil {
			failErr(w, 409, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"ok": true, "kept": []string{}})
		return
	}
	base, _, err := rt.PolicyLayers(ctx, sb)
	if err != nil {
		failCode(w, 409, "policy_unverifiable", map[string]any{"detail": safeCLIError(err)}, "ポリシーを確かめられません: "+safeCLIError(err))
		return
	}
	ops, kept := presetRemoval(base, mine, presetRules(sb.Presets), presetRules(rest))
	var version uint32
	if len(ops) > 0 {
		if version, err = rt.ApplyPolicyChange(ctx, sb, ops); err != nil {
			s.failGateway(ctx, w, sb, err)
			return
		}
		s.recordPolicyChange(sb.Name, PolicyChange{Version: version, Kind: "edit", Rule: opsRule(ops), At: time.Now().Unix()})
		// A shared rule put back as the other presets make it can be folded
		// too: said, not hidden.
		if after, _, err := rt.PolicyLayers(ctx, sb); err != nil {
			kept = append(kept, "(外した後のポリシーを確かめられません: "+safeCLIError(err)+")")
		} else {
			for _, op := range ops {
				if a := op.AddRule; a != nil {
					if _, ok := after[a.RuleName]; !ok {
						kept = append(kept, a.RuleName+" (ほかのプリセットの分を戻したところ、OpenShell が "+strings.Join(rulesWithEndpoints(after, a.Rule), "・")+" にまとめました)")
					}
				}
			}
		}
	}
	if err := s.setSandboxPresets(sb.Name, drop); err != nil {
		failErr(w, 409, err)
		return
	}
	if kept == nil {
		kept = []string{}
	}
	jsonResponse(w, 200, map[string]any{"ok": true, "version": version, "kept": kept})
}

// presetRemoval: the operations that take mine (one preset's rules) out of
// base, the latest revision; all are the rules of every preset the sandbox
// has, rest those of the others. A rule that is not as all make it stays
// (kept).
func presetRemoval(base, mine, all, rest map[string]v1.NetworkPolicyRule) (ops []v1.PolicyMergeOperation, kept []string) {
	for _, name := range slices.Sorted(maps.Keys(mine)) {
		cur, ok := base[name]
		if !ok {
			// Taken out by hand, or folded into another rule: the rules
			// that have its endpoints may still allow what it did.
			if in := rulesWithEndpoints(base, mine[name]); len(in) > 0 {
				kept = append(kept, name+" (ポリシーにこの名前の規則がなく、同じ宛先が "+strings.Join(in, "・")+" にあります)")
			}
			continue
		}
		if !samePresetRule(cur, all[name]) {
			kept = append(kept, name)
			continue
		}
		ops = append(ops, v1.PolicyMergeOperation{RemoveRule: &v1.RemoveNetworkRule{RuleName: name}})
		if other, ok := rest[name]; ok {
			ops = append(ops, v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: other}})
		}
	}
	return ops, kept
}

// foldedPresetRules are the preset's rules that rules lacks under their
// own names, each with the rules that have its endpoints.
func foldedPresetRules(rules, mine map[string]v1.NetworkPolicyRule) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(mine)) {
		if _, ok := rules[name]; ok {
			continue
		}
		in := rulesWithEndpoints(rules, mine[name])
		if len(in) == 0 {
			in = []string{"(不明)"}
		}
		out = append(out, name+" → "+strings.Join(in, "・"))
	}
	return out
}

// rulesWithEndpoints names the rules (not the providers') with an endpoint
// of rule (the same host, path and a shared port: where OpenShell folds).
func rulesWithEndpoints(rules map[string]v1.NetworkPolicyRule, rule v1.NetworkPolicyRule) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		if !isProviderRule(name) && rulesShareEndpoint(rules[name], rule) {
			out = append(out, name)
		}
	}
	return out
}

// rollbackOps put after back to before, rule by rule (rules added are
// taken out, rules changed are replaced by their old selves).
func rollbackOps(before, after map[string]v1.NetworkPolicyRule) []v1.PolicyMergeOperation {
	var ops []v1.PolicyMergeOperation
	for _, name := range slices.Sorted(maps.Keys(after)) {
		if isProviderRule(name) {
			continue
		}
		old, existed := before[name]
		if existed && sameRule(old, after[name]) {
			continue
		}
		ops = append(ops, v1.PolicyMergeOperation{RemoveRule: &v1.RemoveNetworkRule{RuleName: name}})
		if existed {
			old = cloneRule(old)
			old.Name = name
			ops = append(ops, v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: old}})
		}
	}
	return ops
}

// samePresetRule compares a rule of the policy with a preset's, as sets:
// the order of programs, endpoints and requests, the port fields and the
// gateway's provenance do not matter.
func samePresetRule(a, b v1.NetworkPolicyRule) bool {
	canon := func(r v1.NetworkPolicyRule) any {
		r = cloneRule(r)
		r.Name = ""
		slices.SortFunc(r.Binaries, func(x, y v1.PolicyNetworkBinary) int { return strings.Compare(x.Path, y.Path) })
		r.Binaries = slices.CompactFunc(r.Binaries, func(x, y v1.PolicyNetworkBinary) bool { return x.Path == y.Path })
		for i := range r.Endpoints {
			e := &r.Endpoints[i]
			e.Ports = effectivePorts(*e)
			e.Port = 0
			e.ProviderCredentialed, e.AdvisorProposed = false, false
			sortJSON(e.Rules)
			sortJSON(e.DenyRules)
		}
		sortJSON(r.Endpoints)
		return prunedJSON(r)
	}
	return reflect.DeepEqual(canon(a), canon(b))
}

// sortJSON sorts a list by its elements' JSON and drops repeats.
func sortJSON[T any](list []T) {
	key := func(x T) string { b, _ := json.Marshal(prunedJSON(x)); return string(b) }
	slices.SortFunc(list, func(x, y T) int { return strings.Compare(key(x), key(y)) })
}

// presetsAmbiguities are what a new sandbox's presets and providers would
// make OpenShell refuse: endpoints that disagree, and endpoints of the
// presets that would carry a provider's credential without inspection.
func (s *Server) presetsAmbiguities(ctx context.Context, refs []PresetRef, providers []ProviderInfo) error {
	rules := presetRules(refs)
	if len(rules) == 0 {
		return nil
	}
	var scopes []credScope
	for _, p := range providers {
		scopes = append(scopes, providerScopes(p.Name, p.Profile)...)
	}
	if cs := credentialConflicts(rules, scopes); len(cs) > 0 {
		return errRefused("credential_uninspected_presets", nil, "通信のプリセット: "+conflictMessage(cs, false), nil, cs)
	}
	byRule := map[string]RuleProvider{}
	for _, p := range providers {
		if m := s.providerProfile(ctx, p); m != nil {
			byRule[composeProviderRule(rules, p.Name, m)] = providerLabel(p)
		}
	}
	if list := findEndpointAmbiguities(rules); len(list) > 0 {
		views := ambiguityViews(list, nil, rules, byRule)
		return errRefused("ambiguous_presets", nil, ambiguityMessage("この組み合わせの通信のプリセットとプロバイダーは OpenShell に拒否されます", views, "どれかを外してください。"), views, nil)
	}
	return nil
}
