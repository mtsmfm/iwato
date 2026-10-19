//go:build uitest

package main

import (
	"context"
	"sync/atomic"

	"iwato/internal/openshell/wslc"
)

// newFakeChecks is the fake's startup checks. With down (serve -fake
// -fake-engine-down) the engine check fails until the page checks again
// (the banner's 「再確認」, or opening the settings), so the blocking banner
// can be seen in a browser.
func newFakeChecks(down bool) *checkState {
	var up atomic.Bool
	up.Store(!down)
	return newCheckState(func(ctx context.Context) []check {
		if ctx.Value(pageCheck{}) != nil {
			up.Store(true)
		}
		if !up.Load() {
			return []check{{Level: "fail", Name: "docker", Detail: "docker_down_error", Hint: "docker_start", Params: map[string]any{"error": "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}}}
		}
		return []check{
			{Level: "ok", Name: "docker", Detail: "docker_version_context", Params: map[string]any{"version": "28.3.0", "api": "1.51", "context": "colima"}},
			{Level: "ok", Name: "engine", Detail: "engine_vm", Hint: "engine_resources", Params: map[string]any{"os": "Ubuntu 24.04 LTS", "arch": "aarch64", "cpus": 4, "memory": "7.7"}},
			{Level: "warn", Name: "buildx", Detail: "buildx_missing", Hint: "buildx_install"},
			{Level: "ok", Name: "config", Detail: "config", Params: map[string]any{"path": "/fake/iwato.json", "count": 2}},
			{Level: "ok", Name: "openshell", Detail: "version", Params: map[string]any{"version": wslc.OpenShellVersion}},
		}
	})
}
