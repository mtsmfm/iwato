#!/bin/sh
# Writes dist/iwato-build.json for a published build (update.go reads it):
# the version, commit and channel, the image digests the exes pin, and the
# SHA-256 of each binary under its release asset name. pr-N: pull request
# N's build (channel pr), titled $PR_TITLE.
# Usage: scripts/build-meta.sh edge|release|pr-N iwato-gateway=sha256:...,iwato-sandbox=sha256:...
set -eu
[ $# -eq 2 ] || { echo "usage: $0 edge|release|pr-N DIGESTS" >&2; exit 2; }
echo "$1" | grep -Eqx 'edge|release|pr-[1-9][0-9]{0,8}' || { echo "bad channel: $1" >&2; exit 2; }
cd "$(dirname "$0")/.."
version=$(sed -n 's/^const version = "\(.*\)"$/\1/p' config.go)
images=$(echo "$2" | tr ',' '\n' | jq -Rn '[inputs | select(length > 0) | split("=") | {(.[0]): .[1]}] | add')
assets=$(for t in windows-amd64:.exe windows-arm64:.exe darwin-arm64: linux-amd64: linux-arm64:; do
  p=${t%:*} ext=${t#*:}
  printf '%s %s\n' "iwato-$p$ext" "$(sha256sum "dist/$p/iwato$ext" | cut -d' ' -f1)"
done | jq -Rn '[inputs | split(" ") | {(.[0]): .[1]}] | add')
jq -n --arg version "$version" --arg commit "$(git rev-parse HEAD)" --arg built "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg channel "$1" \
  --arg title "${PR_TITLE:-}" --argjson images "$images" --argjson assets "$assets" \
  '{version: $version, commit: $commit, built: $built, channel: $channel, images: $images, assets: $assets}
   + if ($channel | startswith("pr-")) then {channel: "pr", pr: ($channel[3:] | tonumber), title: ($title[:300])} else {} end' >dist/iwato-build.json
cat dist/iwato-build.json
