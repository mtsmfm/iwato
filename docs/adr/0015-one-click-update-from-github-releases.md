# 0015. One-click update from GitHub Releases

- Status: Proposed
- Date: 2026-10-20

## Context

Iwato is one exe (or binary) downloaded from GitHub Releases: the pre-release `edge`, replaced on every push to main, and version releases. Updating meant downloading it again, stopping Iwato and starting the new one by hand. The binaries are not signed (no Authenticode or Apple notarization), so the exe cannot rely on an OS check of who built a download.

Each exe pins its gateway and sandbox images by digest (`buildinfo.go`). A new gateway image recreates the gateway container (`internal/openshell/gateway`), and OpenShell then restarts the running sandboxes: their processes and terminals end, their volumes stay. A new Iwato alone keeps them: the daemon in the sandbox holds the terminals (ADR 0011).

## Decision

- Every published build attaches `iwato-build.json` (`scripts/build-meta.sh`): version, commit, channel, the image digests, and the SHA-256 of each binary. Published exes know their channel and repository (`buildChannel`, `buildRepo`); other builds never check
- The exe fetches its channel's file over HTTPS from github.com (edge: the release `edge`, an update when the commit differs; release: the latest release, an update when the version is newer) at start and every 6 hours
- On the owner's click it downloads its own binary from the same release, refuses it unless the SHA-256 matches and it runs and reports the expected build, swaps it in next to the old one and restarts on the same port, keeping the logins
- Before that, the page says whether the gateway's image changes and lists the running sandboxes that OpenShell will restart. The gateway is kept when only the tag differs and the digest is the same

## Consequences

- Updating is one click, and a build whose gateway is unchanged does not disturb the sandboxes
- The trust is HTTPS to GitHub and the repository's release assets: anyone who can publish a release can ship an update, as they can ship the download today. Signing would need keys and a different release flow
- The edge release is replaced asset by asset: the metadata goes last, and a mismatch in between refuses the update until the next check
- The exe must be writable where it is; otherwise the update fails before changing anything. A failed restart puts the old exe back. Windows and macOS restarts (console, Gatekeeper) are checked on real machines only
