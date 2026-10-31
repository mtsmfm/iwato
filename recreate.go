package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// Recreating a sandbox (作り直す) deletes it in OpenShell and creates it
// again under the same name from the current image (an existing sandbox
// keeps its container's image and workspace). What it carries over:
//
//   - the home's volume: the delete keeps it, and its name comes from the
//     sandbox's name and the path (persistVolume), so the new sandbox
//     mounts the same one;
//   - the creation settings: the configuration entry (image, presets,
//     ingress) stays as it is, except the GPU, which the request may turn
//     on or off; the paths outside the home that a sandbox made before the
//     image convention kept (LegacyPersist) are dropped:
//     only the home persists, and their volumes stay unused;
//   - the providers: OpenShell's list for the sandbox goes into the
//     configuration, which the new sandbox is created with;
//   - the network rules: the latest policy revision (the effective policy
//     without the providers' _provider_* rules, which come back with the
//     providers) is saved in the configuration (Carry) and applied to the
//     new sandbox rule by rule, each checked as an edit is
//     (policy_prediction.go); a rule OpenShell refuses is reported and the
//     others still go in.
//
// The proposals (pending, approved, rejected) and the policy history are
// OpenShell's, per sandbox, and go with it.

// CarriedPolicy is the old sandbox's network rules, as OpenShell's SDK
// types in JSON. Raw, so that a change of those types never keeps the
// configuration from loading.
type CarriedPolicy struct {
	Rules json.RawMessage `json:"rules"`
	// Remove are the rules of the sandbox's presets that its policy no
	// longer had (taken out by hand): the new sandbox's first policy has
	// them, and they are taken out again. Only these are ever removed.
	Remove []string  `json:"remove,omitempty"`
	At     time.Time `json:"at"`
}

// newCarriedPolicy keeps the sandbox's own rules: not the providers' (they
// come back with the providers), and without the gateway's provenance. The
// presets' rules the old policy lacks are to be removed. (A preset rule
// that a newer Iwato added to a preset is among them: the recreated sandbox
// keeps the old one's access.)
func newCarriedPolicy(rules map[string]v1.NetworkPolicyRule, presets []PresetRef) (*CarriedPolicy, error) {
	keep := map[string]v1.NetworkPolicyRule{}
	for name, r := range rules {
		if isProviderRule(name) {
			continue
		}
		r = cloneRule(r)
		r.Name = name
		for i := range r.Endpoints {
			r.Endpoints[i].ProviderCredentialed = false
		}
		keep[name] = r
	}
	b, err := json.Marshal(keep)
	if err != nil {
		return nil, err
	}
	var remove []string
	for _, name := range slices.Sorted(maps.Keys(presetRules(presets))) {
		if _, ok := rules[name]; !ok {
			remove = append(remove, name)
		}
	}
	return &CarriedPolicy{Rules: b, Remove: remove, At: time.Now().UTC()}, nil
}

func (c *CarriedPolicy) rules() (map[string]v1.NetworkPolicyRule, error) {
	out := map[string]v1.NetworkPolicyRule{}
	if err := json.Unmarshal(c.Rules, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// carryStep is one policy update of a carry-over: the merge operations
// that make one rule of the new sandbox the old one's.
type carryStep struct {
	Rule string
	Ops  []v1.PolicyMergeOperation
}

// carrySteps puts carried into current, one rule per update so that a
// refusal costs only that rule.
//
// fresh: current is the first policy of a sandbox just created (its
// presets' rules). The preset rules in remove go first, then each carried
// rule that differs is replaced or added; equal rules are left alone.
//
// Otherwise (a carry-over resumed: the sandbox may have rules approved or
// edited since) only the carried rules it lacks are added; nothing is
// removed or changed.
func carrySteps(current, carried map[string]v1.NetworkPolicyRule, remove []string, fresh bool) []carryStep {
	var steps []carryStep
	if fresh {
		for _, name := range remove {
			if _, ok := current[name]; ok && !isProviderRule(name) {
				steps = append(steps, carryStep{Rule: name, Ops: []v1.PolicyMergeOperation{{RemoveRule: &v1.RemoveNetworkRule{RuleName: name}}}})
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(carried)) {
		if isProviderRule(name) {
			continue
		}
		rule := cloneRule(carried[name])
		rule.Name = name
		var ops []v1.PolicyMergeOperation
		if cur, ok := current[name]; ok {
			if !fresh || sameRule(cur, rule) {
				continue
			}
			ops = append(ops, v1.PolicyMergeOperation{RemoveRule: &v1.RemoveNetworkRule{RuleName: name}})
		}
		ops = append(ops, v1.PolicyMergeOperation{AddRule: &v1.AddNetworkRule{RuleName: name, Rule: rule}})
		steps = append(steps, carryStep{Rule: name, Ops: ops})
	}
	return steps
}

// sameRule compares two rules as the gateway stores them: an empty list,
// a zero and an absent value are the same (proto3), and so is the name.
func sameRule(a, b v1.NetworkPolicyRule) bool {
	a.Name, b.Name = "", ""
	return reflect.DeepEqual(prunedJSON(a), prunedJSON(b))
}

// prunedJSON is v as JSON values without the zero ones.
func prunedJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var x any
	if json.Unmarshal(b, &x) != nil {
		return nil
	}
	return prune(x)
}

func prune(x any) any {
	switch t := x.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range t {
			if p := prune(v); p != nil {
				out[k] = p
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		var out []any
		for _, v := range t {
			out = append(out, prune(v))
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case bool:
		if !t {
			return nil
		}
	case float64:
		if t == 0 {
			return nil
		}
	case string:
		if t == "" {
			return nil
		}
	}
	return x
}

// CarryFailure is a rule a recreate could not put back, and why: Error
// in English (it goes to the console too), and for OpenShell's ambiguity
// check a code with the pairs the page explains (web/refusals.js).
type CarryFailure struct {
	Rule               string             `json:"rule"`
	Error              string             `json:"error"`
	Code               string             `json:"code,omitempty"`
	Params             map[string]any     `json:"params,omitempty"`
	Ambiguities        []AmbiguityView    `json:"ambiguities,omitempty"`
	GatewayAmbiguities []GatewayAmbiguity `json:"gatewayAmbiguities,omitempty"`
}

// recreateRuntime is what a recreate needs from the runtime besides
// Start. OpenShell and Fake implement it.
type recreateRuntime interface {
	deleteRuntime
	policyLayersRuntime
	// Exists says whether OpenShell has the sandbox.
	Exists(context.Context, Sandbox) (bool, error)
	// ApplyPolicyOps makes one policy update of merge operations.
	ApplyPolicyOps(context.Context, Sandbox, []v1.PolicyMergeOperation) error
}

// recreateSandbox recreates the sandbox and starts it: see the top of this
// file, with the GPU set to gpu. A previous carry-over that did not finish
// is applied first. The error says what state the sandbox was left in.
func (s *Server) recreateSandbox(ctx context.Context, sb Sandbox, gpu bool) ([]CarryFailure, error) {
	rt, ok := s.rt.(recreateRuntime)
	if !ok {
		return nil, errors.New("recreate unavailable")
	}
	exists, err := rt.Exists(ctx, sb)
	if err != nil {
		return nil, fmt.Errorf("サンドボックスを確かめられないので、作り直しません: %s", safeCLIError(err))
	}
	if !exists && sb.Carry == nil {
		return nil, errors.New("このサンドボックスはまだ OpenShell にありません。「起動」で作成してください")
	}
	if exists {
		// The rules of an earlier recreate go in before they are read.
		if sb.Carry != nil {
			if failed, err := s.applyCarried(ctx, sb.Name, false); err != nil {
				return failed, fmt.Errorf("前回の作り直しで引き継いだ規則をまだ戻せないので、作り直しません: %s", safeCLIError(err))
			}
		}
		providers := sb.Providers
		if prt, ok := s.rt.(providerRuntime); ok {
			_, attached, err := prt.ListProviders(ctx)
			if err != nil {
				return nil, fmt.Errorf("付いているプロバイダーを読めないので、作り直しません: %s", safeCLIError(err))
			}
			if list, ok := attached[sb.Name]; ok {
				providers = slices.Clone(list)
			}
		}
		base, _, err := rt.PolicyLayers(ctx, sb)
		if err != nil {
			return nil, fmt.Errorf("ポリシーを読めないので、作り直しません: %s", safeCLIError(err))
		}
		carry, err := newCarriedPolicy(base, sb.Presets)
		if err != nil {
			return nil, err
		}
		// Saved first: after the delete, this is all there is.
		err = s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
			i := slices.IndexFunc(list, func(x Sandbox) bool { return x.Name == sb.Name })
			if i < 0 {
				return nil, errors.New("サンドボックスが設定にありません")
			}
			if len(providers) == 0 {
				providers = nil
			}
			list[i].Providers, list[i].Carry, list[i].GPU = providers, carry, gpu
			// Only the home persists: the new sandbox does not mount the
			// volumes of the paths an older one kept outside it, which
			// stay in the volumes screen, no longer in use.
			list[i].LegacyPersist = nil
			// The new sandbox starts with all of them.
			list[i].ProvidersChangedAt, list[i].ProviderApplying = time.Time{}, nil
			return list, nil
		})
		if err != nil {
			return nil, fmt.Errorf("引き継ぐ設定を保存できないので、作り直しません: %w", err)
		}
		if err := rt.Delete(ctx, sb, false); err != nil {
			return nil, fmt.Errorf("サンドボックスを削除できませんでした: %s。ボリュームと設定は残っています。もう一度「作り直す」を試してください", safeCLIError(err))
		}
		s.forgetSandboxSessions(sb.Name)
	} else if sb.GPU != gpu {
		err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
			if i := slices.IndexFunc(list, func(x Sandbox) bool { return x.Name == sb.Name }); i >= 0 {
				list[i].GPU = gpu
			}
			return list, nil
		})
		if err != nil {
			return nil, err
		}
	}
	sb, ok = s.lookupSandbox(sb.Name)
	if !ok {
		return nil, errors.New("サンドボックスが設定にありません")
	}
	startErr := s.rt.Start(ctx, sb)
	if startErr != nil {
		if made, err := rt.Exists(context.WithoutCancel(ctx), sb); err != nil || !made {
			return nil, fmt.Errorf("サンドボックスは削除しましたが、作り直せませんでした: %s。ボリュームと引き継ぐ設定 (プロバイダー・ポリシーの規則) は残っているので、「作り直す」か「起動」でもう一度作れます", safeCLIError(startErr))
		}
	}
	// The rules go in even when the start failed: OpenShell has the
	// sandbox, and its policy is what a repair may need.
	failed, err := s.applyCarried(context.WithoutCancel(ctx), sb.Name, true)
	switch {
	case startErr != nil:
		return failed, fmt.Errorf("作り直したサンドボックスを起動できませんでした: %s。ボリューム・プロバイダー・ポリシーの規則は引き継いでいます", safeCLIError(startErr))
	case err != nil:
		return failed, fmt.Errorf("作り直して起動しましたが、ポリシーの規則を戻せませんでした: %s。次の「起動」か「作り直す」でもう一度戻します", safeCLIError(err))
	}
	return failed, nil
}

// forgetSandboxSessions drops what Iwato holds for a sandbox that OpenShell
// deleted and makes again: the file connections, the ingress's connections
// and tokens (not its node: the name and URLs stay) and the policy changes
// it followed.
func (s *Server) forgetSandboxSessions(name string) {
	if s.files != nil {
		s.files.Forget(name)
	}
	s.ingress.reset(name)
	s.mu.Lock()
	delete(s.policyChanges, name)
	s.mu.Unlock()
}

// applyCarried puts a recreate's carried rules into the sandbox and clears
// them from the configuration. Each rule is one update, checked as an edit
// is; the ones refused are returned. An error (the policy unreadable, the
// request given up) keeps them for the next try, which only adds the
// missing ones (carrySteps). fresh: the sandbox was just created for them.
func (s *Server) applyCarried(ctx context.Context, name string, fresh bool) ([]CarryFailure, error) {
	sb, ok := s.lookupSandbox(name)
	if !ok || sb.Carry == nil {
		return nil, nil
	}
	rt, ok := s.rt.(recreateRuntime)
	if !ok {
		return nil, errors.New("recreate unavailable")
	}
	defer s.lockPolicy(name)()
	carried, err := sb.Carry.rules()
	if err != nil {
		// Unreadable now, unreadable later: dropped with the reason.
		log.Printf("sandbox %s: carried policy unreadable: %v", name, english(err))
		s.clearCarry(name)
		return nil, errCodeEn("carry_unreadable", map[string]any{"detail": err.Error()}, "引き継いだ規則を読めません: "+err.Error(), "the carried rules are unreadable: "+err.Error(), err)
	}
	base, _, err := rt.PolicyLayers(ctx, sb)
	if err != nil {
		return nil, err
	}
	var failed []CarryFailure
	for _, step := range carrySteps(base, carried, sb.Carry.Remove, fresh) {
		if err := ctx.Err(); err != nil {
			return failed, err
		}
		if err := s.checkOps(ctx, sb, step.Ops, "ambiguous_carry", map[string]any{"rule": step.Rule}, "規則 "+step.Rule+" は OpenShell に拒否されるので戻しませんでした"); err != nil {
			f := CarryFailure{Rule: step.Rule, Error: english(err)}
			var a *errAmbiguity
			if errors.As(err, &a) {
				f.Error, f.Code, f.Params, f.Ambiguities = "OpenShell would refuse it: "+a.raw(), a.code, a.params, a.views
			} else if c := (*codedError)(nil); errors.As(err, &c) && c.code == "policy_unverifiable" {
				f.Error, f.Code, f.Params = fmt.Sprintf("cannot check the policy: %v", c.params["detail"]), c.code, c.params
			}
			failed = append(failed, f)
			continue
		}
		if err := rt.ApplyPolicyOps(withPolicyNote(ctx, "recreate", "Carry rule "+step.Rule+" over the recreate"), sb, step.Ops); err != nil {
			if ctx.Err() != nil {
				return failed, ctx.Err()
			}
			f := CarryFailure{Rule: step.Rule, Error: safeCLIError(err)}
			if j, ok := translateGatewayError(f.Error, s.ruleProviders(ctx, sb)); ok {
				f.Code, f.GatewayAmbiguities = "gateway_ambiguous", j.pairs
			}
			failed = append(failed, f)
		}
	}
	for _, f := range failed {
		noticef("sandbox %s: rule %s not carried over: %s", name, f.Rule, f.Error)
	}
	s.clearCarry(name)
	return failed, nil
}

func (s *Server) clearCarry(name string) {
	err := s.updateSandboxes(func(list []Sandbox) ([]Sandbox, error) {
		for i := range list {
			if list[i].Name == name {
				list[i].Carry = nil
			}
		}
		return list, nil
	})
	if err != nil {
		log.Printf("sandbox %s: %v", name, english(err))
	}
}

// handleRecreate recreates the sandbox (POST .../recreate); the body
// repeats its name as confirmation, as a delete's does, and may set the
// GPU (absent: kept). It answers once the new sandbox runs, with the rules
// that could not be carried over.
func (s *Server) handleRecreate(w http.ResponseWriter, r *http.Request, parts []string) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	sb, ok := s.sandboxFor(w, parts, 2)
	if !ok || !s.require(w, r, true) || !s.requireImages(w) {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
		GPU     *bool  `json:"gpu"`
	}
	if decode(w, r, &body) != nil || body.Confirm != sb.Name {
		fail(w, 400, "confirm must equal the sandbox name")
		return
	}
	gpu := sb.GPU
	if body.GPU != nil {
		gpu = *body.GPU
	}
	if gpu && !sb.GPU && !s.gpuAvailable(r.Context()) {
		failCode(w, 400, "gpu_unavailable", nil, "この環境では GPU を使えません")
		return
	}
	if _, ok := s.rt.(recreateRuntime); !ok {
		fail(w, 501, "recreate unavailable")
		return
	}
	// As a delete, it gives up a start in progress.
	op, _, _, busy := s.beginOp(sb.Name, "recreate")
	if busy != "" {
		failBusy(w, busy)
		return
	}
	var e error
	defer func() { s.endOp(sb.Name, op, e) }()
	failed, e := s.recreateSandbox(op.ctx, sb, gpu)
	if failed == nil {
		failed = []CarryFailure{}
	}
	if e != nil {
		jsonResponse(w, 500, map[string]any{"error": e.Error(), "failed": failed})
		return
	}
	s.cleanupImages()
	jsonResponse(w, 200, map[string]any{"ok": true, "failed": failed})
}
