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

// imageRegistry, imageTag and imageDigests name the images Iwato pulls
// (it never builds them: docs/adr/0024). scripts/build-exe.sh sets them:
// releases pull REGISTRY/NAME:VERSION@DIGEST (imageTag empty), the edge
// and pull request exes :edge or :pr-N @DIGEST, so that each uses only
// the images it was built with even if the tag moves. Other exes, go run
// and tests pull :edge, the images main last published (image.yml), when
// they have none, and again only on request (UpdateImage).
var (
	imageRegistry = "ghcr.io/mtsmfm"
	imageTag      = "edge"
	imageDigests  string
)

// imageRef names one of Iwato's images.
func imageRef(name string) string {
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

// imageMoves reports whether the pulled sandbox image's tag moves (edge
// without a digest), so that a pulled one may be outdated.
func imageMoves() bool { return imageTag != "" && imageDigests == "" }

// BuildInfo identifies the running server on the page.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"` // RFC 3339, UTC
	Debug   bool   `json:"debug,omitempty"`
	PR      int    `json:"pr,omitempty"` // a pull request's build (buildChannel pr-N)
}

func currentBuild() BuildInfo {
	b := BuildInfo{Version: version, Commit: buildCommit, Built: buildTime, Debug: debugBuild(), PR: prOfTag(buildChannel)}
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
