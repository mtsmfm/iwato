package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// minDockerMajor is the Docker Engine OpenShell supports (its
// docs/about/support-matrix.mdx: 28.0 or later).
const minDockerMajor = 28

// dockerVersion is the part of `docker version` read here.
type dockerVersion struct {
	Client *struct{ Version, Context string }
	Server *struct{ Version, APIVersion, Arch string }
}

// dockerInfo is the part of `docker info` read here.
type dockerInfo struct {
	Name, OperatingSystem, Architecture, DefaultRuntime, ServerVersion string
	NCPU                                                               int
	MemTotal                                                           int64
}

// diagnoseDocker checks a Docker-compatible engine. The first check is a
// running engine (serve waits for one).
func diagnoseDocker(ctx context.Context, e *Engine) []check {
	var out []check
	add := func(level, name, detail, hint string, params map[string]any) {
		out = append(out, check{Level: level, Name: name, Detail: detail, Hint: hint, Params: params})
	}
	raw, err := e.run(ctx, "version", "--format", "{{json .}}")
	var v dockerVersion
	_ = json.Unmarshal(raw, &v) // the client's part comes even when the engine is down
	switch {
	case v.Client == nil && err != nil:
		add("fail", "docker", "docker_missing", "docker_install", map[string]any{"error": cliLine(err)})
		return out
	case v.Server == nil && err != nil:
		add("fail", "docker", "docker_down_error", "docker_start", map[string]any{"error": cliLine(err)})
		return out
	case v.Server == nil:
		add("fail", "docker", "docker_down", "docker_start", nil)
		return out
	}
	params := map[string]any{"version": v.Server.Version, "api": v.Server.APIVersion}
	detail := "docker_version"
	if v.Client != nil && v.Client.Context != "" {
		detail, params["context"] = "docker_version_context", v.Client.Context
	}
	if major, _, _ := strings.Cut(v.Server.Version, "."); atoiOr(major, 0) < minDockerMajor {
		params["min"] = minDockerMajor
		add("warn", "docker", detail, "docker_old", params)
	} else {
		add("ok", "docker", detail, "", params)
	}

	raw, err = e.run(ctx, "info", "--format", "{{json .}}")
	var info dockerInfo
	if err != nil || json.Unmarshal(raw, &info) != nil {
		reason := "unexpected output"
		if err != nil {
			reason = cliLine(err)
		}
		add("warn", "engine", "docker_info_failed", "", map[string]any{"error": reason})
	} else {
		params := map[string]any{"os": info.OperatingSystem, "arch": info.Architecture, "cpus": info.NCPU, "memory": fmt.Sprintf("%.1f", float64(info.MemTotal)/(1<<30))}
		switch {
		case !sameArch(info.Architecture, runtime.GOARCH):
			params["want"] = map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
			add("warn", "engine", "engine_vm", "engine_arch", params)
		case runtime.GOOS == "darwin":
			add("ok", "engine", "engine_vm", "engine_resources", params)
		default:
			add("ok", "engine", "engine_vm", "", params)
		}
		if strings.Contains(strings.ToLower(info.DefaultRuntime), "sysbox") {
			add("fail", "engine_config", "eci", "eci", map[string]any{"runtime": info.DefaultRuntime})
		}
		if strings.Contains(info.OperatingSystem, "Docker Desktop") {
			add("warn", "engine_config", "docker_desktop", "host_networking", nil)
		}
	}

	// A local build (the gateway's image when it is not pulled, the
	// sandbox image with --local-images) uses BuildKit features.
	if !pullsGatewayImage() {
		if _, err := e.run(ctx, "buildx", "version"); err != nil {
			add("warn", "buildx", "buildx_missing", "buildx_install", nil)
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// sameArch compares the engine's architecture (`uname -m` style, or Go's)
// with a GOARCH.
func sameArch(engine, goarch string) bool { return goArchName(engine) == goarch }

func goArchName(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return a
}
