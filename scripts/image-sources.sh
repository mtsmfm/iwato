#!/bin/sh
# Prints a short hash of the sources of one of Iwato's images at HEAD (NAME is
# gateway or sandbox; together they are image.yml's path filter). image.yml
# tags what it publishes src-HASH and skips an image whose tag exists, and
# build.yml's edge exes and pr-build.yml's pin the images of their own commit
# by that tag. Only what goes into an image counts: a CI-only change does not
# change its digest, which would recreate the owners' gateways.
set -eu
cd "$(dirname "$0")/.."
case ${1:-} in
  gateway) set -- internal/openshell/gateway internal/openshell/wslc/driver.go ;;
  sandbox) set -- sandboximage cmd/iwato-entrypoint internal/termd internal/rootsftp go.mod go.sum scripts/build-entrypoint.sh ;;
  *) echo "usage: $0 gateway|sandbox" >&2; exit 2 ;;
esac
git ls-tree HEAD -- "$@" | sha256sum | cut -c1-16
