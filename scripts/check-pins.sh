#!/bin/sh
# Checks the pinned downloads against their sources
# (docs/dev/development.md#bumping-pinned-versions):
#  - OpenShell's runtime images (internal/openshell/wslc/driver.go and
#    internal/openshell/gateway/gateway-docker.toml): the pinned digest is the
#    one ghcr.io serves for the tag of OpenShellVersion;
#  - OpenShell's release tarballs (internal/openshell/gateway/Dockerfile): the
#    pinned SHA-256 are those of the release's *-checksums-sha256.txt;
#  - Selkies' .deb (sandboximage/Dockerfile), when gh is there: the pinned
#    SHA-256 are the release assets' digests.
# It also prints the digest ubuntu:24.04 has now next to the pinned one (the
# tag moves with Ubuntu's updates: a difference is an update to take, not an
# error). Needs curl and jq. Exits 1 on a mismatch.
set -eu
cd "$(dirname "$0")/.."
fail=0
bad() { echo "MISMATCH: $*" >&2; fail=1; }
accept='application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'
# digest TOKEN_URL MANIFEST_URL: the registry's digest of a tag (its index).
digest() {
  token=$(curl -fsSL "$1" | jq -r .token)
  curl -fsSI -H "Authorization: Bearer $token" -H "Accept: $accept" "$2" | tr -d '\r' | sed -n 's/^[Dd]ocker-[Cc]ontent-[Dd]igest: *//p'
}

ver=$(sed -n 's/^\s*OpenShellVersion\s*= "\(.*\)"$/\1/p' internal/openshell/wslc/driver.go)
[ -n "$ver" ] || { echo "no OpenShellVersion in driver.go" >&2; exit 1; }
for name in supervisor sandbox; do
  pinned=$(sed -n "s/^\s*${name}ImageDigest\s*= \"\(.*\)\"$/\1/p" internal/openshell/wslc/driver.go)
  got=$(digest "https://ghcr.io/token?scope=repository:nvidia/openshell/$name:pull" "https://ghcr.io/v2/nvidia/openshell/$name/manifests/$ver")
  if [ "$got" = "$pinned" ]; then echo "ok   ghcr.io/nvidia/openshell/$name:$ver@$got"; else bad "ghcr.io/nvidia/openshell/$name:$ver is $got, driver.go pins $pinned"; fi
  grep -q "\"ghcr.io/nvidia/openshell/$name:$ver@$pinned\"" internal/openshell/gateway/gateway-docker.toml ||
    bad "gateway-docker.toml does not pin ghcr.io/nvidia/openshell/$name:$ver@$pinned"
done

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for p in openshell openshell-gateway openshell-sandbox openshell-supervisor; do
  curl -fsSL "https://github.com/NVIDIA/OpenShell/releases/download/v$ver/$p-checksums-sha256.txt"
done | sed 's/ \*/  /' >"$tmp/published"
grep -o "'[0-9a-f]\{64\}  openshell[^']*\.tar\.gz'" internal/openshell/gateway/Dockerfile | tr -d "'" >"$tmp/pinned"
[ "$(wc -l <"$tmp/pinned")" -eq 8 ] || bad "the gateway Dockerfile pins $(wc -l <"$tmp/pinned") tarballs, not 8"
while read -r sum file; do
  if grep -qx "$sum  $file" "$tmp/published"; then echo "ok   $file $sum"; else bad "$file: pinned $sum, v$ver publishes $(grep " $file\$" "$tmp/published" | cut -d' ' -f1)"; fi
done <"$tmp/pinned"

if command -v gh >/dev/null; then
  sv=$(sed -n 's/^ARG SELKIES_VERSION=//p' sandboximage/Dockerfile)
  for a in amd64 arm64; do
    pinned=$(sed -n "s/^ *$sv-$a) want=\([0-9a-f]*\) ;;.*/\1/p" sandboximage/Dockerfile)
    got=$(gh api "repos/selkies-project/selkies/releases/tags/$sv" --jq ".assets[] | select(.name == \"selkies-$sv-ubuntu24.04-$a.deb\") | .digest")
    if [ "$got" = "sha256:$pinned" ]; then echo "ok   selkies-$sv-ubuntu24.04-$a.deb $pinned"; else bad "selkies-$sv-ubuntu24.04-$a.deb: pinned $pinned, the release has $got"; fi
  done
else
  echo "skip selkies (no gh)"
fi

now=$(digest "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/ubuntu:pull" "https://registry-1.docker.io/v2/library/ubuntu/manifests/24.04")
for f in sandboximage/Dockerfile internal/openshell/gateway/Dockerfile; do
  for pinned in $(sed -n 's/^FROM ubuntu:24\.04@\(sha256:[0-9a-f]*\).*/\1/p' "$f" | sort -u); do
    if [ "$pinned" = "$now" ]; then echo "ok   $f ubuntu:24.04@$pinned"; else echo "note $f pins ubuntu:24.04@$pinned; ubuntu:24.04 is now $now (Dependabot proposes the update)"; fi
  done
done
exit $fail
