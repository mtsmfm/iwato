package main

import (
	"runtime/debug"
	"strings"
)

// buildCommit and buildTime are set by scripts/build-exe.sh (-ldflags -X).
// Plain `go build` falls back to the VCS stamp Go records, when there is one.
var buildCommit, buildTime string

// buildRelease is set by scripts/build-exe.sh unless it is given --debug.
// Without it (go run, tests, debug exes) the debug options work: the page's
// ?font=fallback.
var buildRelease string

func debugBuild() bool { return buildRelease == "" }

// imageRegistry, imageTag and imageDigests are set by scripts/build-exe.sh.
// With --images REGISTRY (releases, and the edge exes with --image-tag
// edge) the exe pulls both of its images from REGISTRY, tagged with
// imageTag or else its version, and with imageDigests
// ("name=sha256:...,name=sha256:...") as tag@digest, so that it uses only
// the images it was built with even if the tag moves. By default (other
// exes) imageTag is "edge" without digests: the exe pulls the sandbox
// image main last published (.github/workflows/image.yml) when it has
// none, pulls it again only on request (UpdateImage), and builds the
// gateway image itself. With --local-images, and in go run and tests,
// imageRegistry is empty and both images are built from the embedded
// sources.
var imageRegistry, imageTag, imageDigests string

// imageRef names one of Iwato's images.
func imageRef(name string) string {
	if imageRegistry == "" {
		return name + ":" + version
	}
	tag := imageTag
	if tag == "" {
		tag = version
	}
	ref := imageRegistry + "/" + name + ":" + tag
	for _, p := range strings.Split(imageDigests, ",") {
		if n, d, ok := strings.Cut(p, "="); ok && n == name {
			return ref + "@" + d
		}
	}
	return ref
}

// pullsGatewayImage reports whether the exe pulls the gateway image too
// (a release or pinned edge exe) instead of building it.
func pullsGatewayImage() bool {
	return imageRegistry != "" && (imageTag == "" || imageDigests != "")
}

// imageMoves reports whether the pulled sandbox image's tag moves (edge
// without a digest), so that a pulled one may be outdated.
func imageMoves() bool { return imageRegistry != "" && imageTag != "" && imageDigests == "" }

// BuildInfo identifies the running server on the page.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"` // RFC 3339, UTC
	Debug   bool   `json:"debug,omitempty"`
}

func currentBuild() BuildInfo {
	b := BuildInfo{Version: version, Commit: buildCommit, Built: buildTime, Debug: debugBuild()}
	if b.Commit != "" {
		return b
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		dirty := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value[:min(len(s.Value), 7)]
			case "vcs.time":
				if b.Built == "" {
					b.Built = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if dirty && b.Commit != "" && !strings.HasSuffix(b.Commit, "-dirty") {
			b.Commit += "-dirty"
		}
	}
	return b
}
