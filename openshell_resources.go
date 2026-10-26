package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
)

// workloads caches each running sandbox's workload container id.
var workloads sync.Map // namespace/name → id

// workload finds the sandbox's running workload container by the labels
// both drivers give it (the WSLc driver's and upstream's Docker driver,
// whose sandbox_label is the namespace; reset.go relies on the same).
func (o *OpenShell) workload(ctx context.Context, name string) (string, error) {
	key := o.e.config.Namespace + "/" + name
	if id, ok := workloads.Load(key); ok {
		return id.(string), nil
	}
	out, err := o.e.run(ctx, "container", "list", "--quiet", "--no-trunc",
		"--filter", "label=openshell.ai/sandbox-name="+name,
		"--filter", "label=openshell.ai/isolation-role=sandbox",
		"--filter", "label=openshell.ai/sandbox-namespace="+o.e.config.Namespace)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 {
		return "", errors.New("no single running workload container")
	}
	workloads.Store(key, ids[0])
	return ids[0], nil
}

// workloadExec runs a command in the workload container; user "" is the
// container's own (root: the workload is created as 0:0 and OpenShell's
// supervisor drops privileges only for its own children).
func (o *OpenShell) workloadExec(ctx context.Context, name, user string, cmd ...string) ([]byte, error) {
	id, err := o.workload(ctx, name)
	if err != nil {
		return nil, err
	}
	// Never root, and a fixed PATH: the image's PATH may name folders the
	// sandbox can write, and these execs run outside OpenShell's Landlock.
	a := []string{"container", "exec", "--user", user, id, "/usr/bin/env", "-i", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	out, err := o.e.run(ctx, append(a, cmd...)...)
	if err != nil {
		// Restarted: a new container next time.
		workloads.Delete(o.e.config.Namespace + "/" + name)
	}
	return out, err
}

func (o *OpenShell) ResourceStats(ctx context.Context, sb Sandbox) (string, error) {
	arg := ""
	if sb.GPU {
		arg = "gpu"
	}
	out, err := o.workloadExec(ctx, sb.Name, nobody, "/bin/sh", "-c", statsScript, "iwato-stats", arg)
	return string(out), err
}

// nobody reads the cgroup files, /proc and the GPU device, all world-readable.
const nobody = "65534:65534"

var ownerPattern = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// HomeUsage walks the home as its owner, who can read all of it even where
// the root of a container without CAP_DAC_READ_SEARCH could not.
func (o *OpenShell) HomeUsage(ctx context.Context, sb Sandbox) (string, error) {
	owner, err := o.workloadExec(ctx, sb.Name, nobody, "stat", "-c", "%u:%g", homeDir)
	if err != nil {
		return "", err
	}
	user := strings.TrimSpace(string(owner))
	if !ownerPattern.MatchString(user) {
		return "", errors.New("unexpected home owner")
	}
	out, err := o.workloadExec(ctx, sb.Name, user, "/bin/sh", "-c", duScript, "iwato-du", homeDir)
	return string(out), err
}
