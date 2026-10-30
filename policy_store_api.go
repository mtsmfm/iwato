package main

import (
	"context"
	"net/http"
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
