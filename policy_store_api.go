package main

import (
	"context"
	"net/http"
	"slices"
	"time"
)

// The policy repository's API (policy_store.go):
//
//	GET  /api/sandboxes/NAME/policy/store                  how it compares with OpenShell (PolicyStoreState)
//	POST /api/sandboxes/NAME/policy/store/import           commit OpenShell's policy (a drift accepted)
//	POST /api/sandboxes/NAME/policy/store/overwrite        apply the repository's policy over a drift
//	GET  /api/sandboxes/NAME/policy/commits                the history, newest first
//	GET  /api/sandboxes/NAME/policy/commits/SHA            one commit and its diff
//	POST /api/sandboxes/NAME/policy/commits/SHA/revert     undo one commit as a new one
//	POST /api/sandboxes/NAME/policy/binding                attach a provider's credential to an endpoint, or remove it (PolicyBinding)

// policyStoreRuntime is the repository's side of the runtime. OpenShell
// and Fake implement it.
type policyStoreRuntime interface {
	PolicyStore(context.Context, Sandbox) (PolicyStoreState, error)
	ImportPolicy(context.Context, Sandbox) (PolicyStoreState, error)
	OverwritePolicy(context.Context, Sandbox) (uint32, error)
	PolicyCommits(context.Context, Sandbox, int) ([]PolicyCommit, error)
	PolicyCommitDiff(context.Context, Sandbox, string) (PolicyCommit, string, error)
	RevertPolicyCommit(context.Context, Sandbox, string) (uint32, error)
	ForgetPolicyStore(string) error
}

// policyCommitsShown is how many commits the history lists.
const policyCommitsShown = 200

func (s *Server) handlePolicyStore(w http.ResponseWriter, r *http.Request, parts []string) {
	rt, ok := s.rt.(policyStoreRuntime)
	if !ok {
		fail(w, 409, "policy unavailable")
		return
	}
	post := r.Method == http.MethodPost
	var route string
	switch {
	case !post && len(parts) == 3 && parts[2] == "store":
		route = "state"
	case post && len(parts) == 4 && parts[2] == "store" && (parts[3] == "import" || parts[3] == "overwrite"):
		route = parts[3]
	case !post && len(parts) == 3 && parts[2] == "commits":
		route = "commits"
	case !post && len(parts) == 4 && parts[2] == "commits":
		route = "commit"
	case post && len(parts) == 5 && parts[2] == "commits" && parts[4] == "revert":
		route = "revert"
	case post && len(parts) == 3 && parts[2] == "binding":
		route = "binding"
	default:
		fail(w, 404, "unknown sandbox route")
		return
	}
	sb, ok := s.lookupSandbox(parts[0])
	if !ok {
		fail(w, 404, "unknown sandbox")
		return
	}
	if !s.require(w, r, post) {
		return
	}
	if post {
		defer s.lockPolicy(sb.Name)()
	}
	ctx, cancel := timeout(s.ctx, 30*time.Second)
	defer cancel()
	ctx = withGatewayReads(ctx)
	if route == "revert" || route == "binding" {
		ctx = refuseDrift(ctx)
	}
	switch route {
	case "state":
		st, err := rt.PolicyStore(ctx, sb)
		if err != nil {
			fail(w, 409, safeCLIError(err))
			return
		}
		jsonResponse(w, 200, st)
	case "import":
		st, err := rt.ImportPolicy(ctx, sb)
		if err != nil {
			s.failGateway(ctx, w, sb, err)
			return
		}
		jsonResponse(w, 200, st)
	case "commits":
		list, err := rt.PolicyCommits(ctx, sb, policyCommitsShown)
		if err != nil {
			failErr(w, 500, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"commits": list})
	case "commit":
		c, diff, err := rt.PolicyCommitDiff(ctx, sb, parts[3])
		if err != nil {
			failErr(w, 404, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"commit": c, "diff": diff})
	case "binding":
		s.setPolicyBinding(ctx, w, r, sb)
	case "overwrite", "revert":
		var version uint32
		var err error
		if route == "overwrite" {
			version, err = rt.OverwritePolicy(ctx, sb)
		} else {
			version, err = rt.RevertPolicyCommit(ctx, sb, parts[3])
		}
		if err != nil {
			s.failGateway(ctx, w, sb, err)
			return
		}
		s.recordPolicyChange(sb.Name, PolicyChange{Version: version, Kind: "edit", At: time.Now().Unix()})
		jsonResponse(w, 200, map[string]any{"ok": true, "version": version})
	}
}

// setPolicyBinding attaches an attached provider's credential to an
// endpoint (a provider of Iwato's profiles: the others' profiles have
// endpoints, and OpenShell refuses binding them), or removes it.
func (s *Server) setPolicyBinding(ctx context.Context, w http.ResponseWriter, r *http.Request, sb Sandbox) {
	var b PolicyBinding
	if decode(w, r, &b) != nil || !ruleNamePattern.MatchString(b.Rule) || !hostPattern.MatchString(b.Host) || len(b.Ports) == 0 || len(b.Path) > 512 ||
		b.Provider != "" && !providerNamePattern.MatchString(b.Provider) {
		fail(w, 400, "invalid binding")
		return
	}
	rt, ok := s.rt.(providerPolicyRuntime)
	prt, hasProviders := s.rt.(providerRuntime)
	if !ok || !hasProviders {
		fail(w, 409, "policy unavailable")
		return
	}
	if b.Provider != "" {
		list, attached, err := s.providerState(ctx, prt)
		if err != nil {
			failCode(w, 409, "provider_unverifiable", map[string]any{"detail": safeCLIError(err)}, "プロバイダーを確かめられません: "+safeCLIError(err))
			return
		}
		i := slices.IndexFunc(list, func(p ProviderInfo) bool { return p.Name == b.Provider })
		if !slices.Contains(attached[sb.Name], b.Provider) || i < 0 {
			failCode(w, 409, "binding_not_attached", map[string]any{"provider": b.Provider}, b.Provider+" はこのサンドボックスに付いていません")
			return
		}
		if _, ok := builtinProfile(list[i].Profile); !ok {
			failCode(w, 409, "binding_profile_endpoints", map[string]any{"provider": b.Provider}, b.Provider+" のプロファイルは宛先を持つので、OpenShell が自分で認証情報を付けます")
			return
		}
	}
	change, err := rt.SetBinding(ctx, sb, b)
	if err != nil {
		s.failGateway(ctx, w, sb, err)
		return
	}
	s.recordPolicyChange(sb.Name, change)
	jsonResponse(w, 200, map[string]any{"ok": true, "version": change.Version})
}
