# 0019. Trying pull request builds from the settings

- Status: Accepted
- Date: 2026-10-25

## Context

Large changes need a check on the owner's real machine before they are merged, often from a phone over Tailscale. The exes come from GitHub Releases and update themselves with SHA-256 checks (ADR 0015), but only along their own channel (edge or release). A workflow artifact's exe needs a manual download and, unlike the published ones, builds its gateway image and pulls the sandbox image's moving `edge` tag.

Alternatives: one moving channel for all pull requests (no way to pick one); remembering the trial in the settings (the exe already knows what it was built as); deleting the images' `pr-<N>` tags on close (in ghcr.io a tag goes only with its version, which `src-<hash>` and maybe `edge` share).

## Decision

- Each push to a pull request from a branch of this repository publishes the pre-release `pr-<N>` (`.github/workflows/pr-build.yml`): the exes of its head, pinned by digest to the images of their own sources (`image.yml` publishes `src-<hash>` and `pr-<N>` for these pull requests, never `edge` or `sha-`), and `iwato-build.json` with channel `pr`, the number and the title. Closing or merging deletes the release and its tag; the images' `pr-<N>` tags stay. Forks and Dependabot get nothing
- These exes are built with the channel `pr-<N>`: they check only their own release and offer its newer pushes with one click like any update, never edge on their own. When the release is gone the settings say so
- The settings of edge and `pr-<N>` exes list the `pr-*` pre-releases (GitHub's releases API) with the number, title, head commit and build time, and install one, or go back to edge, through the same download, hash check, `version` check and rollback, whether or not it is newer. The confirmation shows the full head commit and warns that the build is unmerged and that a broken build may need the PC to recover. Release exes do not switch

## Consequences

- A pull request can be tried and left with two clicks, remotely
- Anyone who can push a branch to the repository can publish a build the owner may install; the owner chooses it and sees its commit. This is the trust ADR 0015 already has in the repository's releases
- `image.yml` runs on every pull request push (cheap when the `src-` images exist), and the images' `pr-<N>` tags accumulate in ghcr.io until removed by hand
- Listing uses GitHub's API without a token (60 requests an hour per address), only when the owner opens the list
