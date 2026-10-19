#!/bin/sh
# Prints a short hash of the sources of Iwato's two images at HEAD (the paths
# of image.yml's filter). image.yml tags what it publishes src-HASH, and
# build.yml's edge exes pin the images of their own commit by that tag.
set -eu
cd "$(dirname "$0")/.."
git ls-tree HEAD -- sandboximage cmd/iwato-entrypoint internal/termd internal/rootsftp \
  internal/openshell/gateway internal/openshell/wslc/driver.go go.mod go.sum \
  scripts/build-entrypoint.sh scripts/image-sources.sh .github/workflows/image.yml |
  sha256sum | cut -c1-16
