# 0024. Iwato only pulls its images

- Status: Accepted
- Date: 2026-11-10

## Context

- [ADR 0004](0004-standard-image-contents.md) made images prebuilt in ghcr.io and kept local builds "only on request (an option)". In practice three paths still built: development exes built the gateway image, `--local-images` built both, and `go run`, the tests and the Docker e2e built both from the sources embedded in the exe (`images.go`, `internal/openshell/gateway`)
- Those builds need `docker buildx`, download Ubuntu packages and the OpenShell release on every fresh machine, and are uncached in CI: the Docker e2e timed out twice while archive.ubuntu.com was slow
- `image.yml` already publishes both images for every push to main (`edge`, `src-<hash>`) and for every pull request from a branch of this repository (`pr-<N>`), with a BuildKit cache. The edge and pull request exes pin those by digest ([ADR 0019](0019-try-pull-request-builds.md))
- Alternatives: keep `--local-images` for changes not pushed yet (keeps the embedded build contexts, the buildx check and both build paths for a rare use); cache the e2e's local build (keeps the build in Iwato)

## Decision

- Iwato never builds its images. Every exe, `go run` and the tests pull `iwato-gateway` and `iwato-sandbox` from the registry: releases by version and digest, edge and pull request exes by their tag and digest, anything else `edge`
- The exe no longer embeds the build contexts (`sandboximage/`, the gateway's Dockerfile and files); `image.yml` and `release.yml` are the only builders
- A change to an image is tried through a pull request: its images are published under `pr-<N>`, used by its own build or by `scripts/build-exe.sh --images ghcr.io/mtsmfm --image-tag pr-N`
- The Docker e2e pulls the images published for its commit's sources (`src-<hash>`), pinned by digest

## Consequences

- No `docker buildx` requirement and no startup check for it; the first start only pulls
- A change to `sandboximage/` or the gateway that is not pushed cannot be run by Iwato; it has to go through a pull request (or a hand-made build and tag outside Iwato)
- A branch pushed without a pull request whose image sources differ from main's has no images; its Docker e2e fails with that reason
- The exe is smaller (no embedded image sources)
- The tests read `sandboximage/` from the repository
