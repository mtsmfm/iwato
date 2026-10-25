#!/bin/sh
# Builds the Windows exes and the macOS and Linux binaries into dist/
# (windows-<arch>/iwato.exe, darwin-arm64/iwato, linux-<arch>/iwato).
# Release build by default; --debug keeps the debug option
# (/?font=fallback) for investigating a problem. Neither has the fake
# backend of the browser checks (serve -fake, build tag uitest).
# Images: by default the exes pull the sandbox image main last published,
# ghcr.io/mtsmfm/iwato-sandbox:edge (.github/workflows/image.yml), when they
# have none (a newer one only on request), and build the gateway image themselves.
# --local-images makes them build both from the embedded sources instead (to
# try a change to sandboximage/ before it reaches main). --images REGISTRY
# makes them pull both from REGISTRY, tagged with the version or
# --image-tag TAG; with --image-digests iwato-gateway=sha256:...,iwato-sandbox=sha256:...
# they pull them as tag@digest (release.yml, and build.yml's edge exes).
# --channel edge|release|pr-N --repo OWNER/REPO: the published exes, which
# offer updates from that repository's releases (update.go; pr-N: pull
# request N's pre-release).
set -eu
release="-X main.buildRelease=1"
images="-X main.imageRegistry=ghcr.io/mtsmfm -X main.imageTag=edge"
registry="" tag="" digests="" channel="" repo=""
while [ $# -gt 0 ]; do
  case "$1" in
    --debug) release="" ;;
    --images) [ $# -ge 2 ] || { echo "--images needs a registry" >&2; exit 2; }; registry=$2; images="-X main.imageRegistry=$2"; shift ;;
    --image-tag) [ $# -ge 2 ] || { echo "--image-tag needs a tag" >&2; exit 2; }; tag=$2; shift ;;
    --image-digests) [ $# -ge 2 ] || { echo "--image-digests needs a list" >&2; exit 2; }; digests=$2; shift ;;
    --local-images) images="" ;;
    --channel) [ $# -ge 2 ] || { echo "--channel needs edge, release or pr-N" >&2; exit 2; }; channel=$2; shift ;;
    --repo) [ $# -ge 2 ] || { echo "--repo needs OWNER/REPO" >&2; exit 2; }; repo=$2; shift ;;
    *) echo "usage: $0 [--debug] [--images REGISTRY [--image-tag TAG] [--image-digests NAME=sha256:HEX,...] [--channel edge|release|pr-N --repo OWNER/REPO] | --local-images]" >&2; exit 2 ;;
  esac
  shift
done
if [ -n "$tag" ]; then
  [ -n "$registry" ] || { echo "--image-tag needs --images" >&2; exit 2; }
  echo "$tag" | grep -Eqx '[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}' || { echo "bad image tag: $tag" >&2; exit 2; }
  images="$images -X main.imageTag=$tag"
fi
if [ -n "$digests" ]; then
  [ -n "$registry" ] || { echo "--image-digests needs --images" >&2; exit 2; }
  for d in $(echo "$digests" | tr ',' ' '); do
    echo "$d" | grep -Eqx 'iwato-(gateway|sandbox)=sha256:[0-9a-f]{64}' || { echo "bad image digest: $d" >&2; exit 2; }
  done
  for n in iwato-gateway iwato-sandbox; do
    echo ",$digests" | grep -q ",$n=" || { echo "--image-digests has no $n" >&2; exit 2; }
  done
  images="$images -X main.imageDigests=$digests"
fi
if [ -n "$channel$repo" ]; then
  [ -n "$digests" ] || { echo "--channel needs --image-digests" >&2; exit 2; }
  echo "$channel" | grep -Eqx 'edge|release|pr-[1-9][0-9]{0,8}' || { echo "bad channel: $channel" >&2; exit 2; }
  echo "$repo" | grep -Eqx '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+' || { echo "bad repo: $repo" >&2; exit 2; }
  images="$images -X main.buildChannel=$channel -X main.buildRepo=$repo"
fi
cd "$(dirname "$0")/.."
# The page shows the commit and build time of the running server.
commit=$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)
[ -z "$(git status --porcelain 2>/dev/null)" ] || commit="$commit-dirty"
built=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# Each exe carries iwato-entrypoint (the sandboxes' daemon) for Linux on
# its own architecture (internal/termbin). The targets build in
# parallel, so each exe sees its own iwato-entrypoint in place of
# internal/termbin/bin/iwato-entrypoint through a go build -overlay, and the file
# itself is never written. Each pipeline logs to its own file, printed when it
# ends, so the output does not interleave.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"; rm -f internal/termbin/bin/iwato-entrypoint' EXIT
# The macOS build carries the arm64 one: the engine's VM runs Linux on the
# Mac's architecture.
build() {
  os=${1%-*} a=${1#*-}
  out=dist/$1/iwato
  [ "$os" = windows ] && out=$out.exe
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -buildvcs=false -trimpath -ldflags="-s -w" -o "$tmp/iwato-entrypoint-$1" ./cmd/iwato-entrypoint &&
    printf '{"Replace":{"%s":"%s"}}\n' "$PWD/internal/termbin/bin/iwato-entrypoint" "$tmp/iwato-entrypoint-$1" >"$tmp/overlay-$1.json" &&
    CGO_ENABLED=0 GOOS=$os GOARCH=$a go build -overlay "$tmp/overlay-$1.json" -buildvcs=false -trimpath -ldflags="-s -w -X main.buildCommit=$commit -X main.buildTime=$built $release $images" -o "$out" .
}
targets="windows-amd64 windows-arm64 darwin-arm64 linux-amd64 linux-arm64"
for t in $targets; do
  build $t >"$tmp/log-$t" 2>&1 &
  eval "pid_${t%-*}_${t#*-}=\$!"
done
failed=""
for t in $targets; do
  eval "pid=\$pid_${t%-*}_${t#*-}"
  wait "$pid" || failed="$failed $t"
  cat "$tmp/log-$t" >&2
done
[ -z "$failed" ] || { echo "build failed for:$failed" >&2; exit 1; }
