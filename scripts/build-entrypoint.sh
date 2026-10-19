#!/bin/sh
# Builds Iwato's daemon (cmd/iwato-entrypoint) for the sandbox image's
# architectures into sandboximage/_entrypoint/, where its Dockerfile takes
# it: the image carries the daemon of the same commit as Iwato
# (docs/adr/0005-sandbox-image-convention.md). The image's workflows run it
# before building; the directory is not in the repository.
set -eu
cd "$(dirname "$0")/.."
mkdir -p sandboximage/_entrypoint
for a in ${ARCHES:-amd64 arm64}; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -buildvcs=false -trimpath -ldflags="-s -w" -o "sandboximage/_entrypoint/iwato-entrypoint-$a" ./cmd/iwato-entrypoint
done
