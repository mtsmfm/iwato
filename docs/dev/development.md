# Development

How to build, run and test Iwato. The code layout is in [architecture.md](architecture.md); the rules for changes (also for coding agents) are in [AGENTS.md](../../AGENTS.md); design decisions are in [docs/adr](../adr/README.md), and open work is in [docs/TODO.md](../TODO.md).

## Setup

After cloning:

1. `mise install` installs Go and Node (versions in `mise.toml`)
2. `python3 scripts/fetch-vendor.py` restores the fonts in `web/vendor` (not in the repository; checked against the hashes in `web/vendor/manifest.json`)
3. `go mod download`

### Developing inside an Iwato sandbox

The setup needs network rules (all GET only):

- mise: `dl.google.com` (`/go/go*.linux-amd64.tar.gz` and `.sha256`) and `nodejs.org` (`/dist/v*/**`)
- Go: `proxy.golang.org`, `sum.golang.org` and `storage.googleapis.com` (`/proxy-golang-org-prod/*`)
- the fonts, for python3: `github.com` (`/yuru7/moralerspace/releases/download/**`) and `release-assets.githubusercontent.com` (`/github-production-release-asset/754151133/*`)

There, `/home/dev` and `selkies-session` exist, so `TestAgentFilesPlacedWhenAbsent`, `TestAgentFilesSkipWhatDoesNotFit` and `TestDesktopStartWithoutTheDesktop` fail regardless of the change.

## Running

- `go run -tags uitest . serve -fake` runs the page against the fake backend the browser checks drive (`fake*.go`): no commands, no sandboxes, on any OS. It exists only in builds with the tag `uitest`; other builds (`scripts/build-exe.sh`, with or without `--debug`) have no `-fake`. Its terminal's `ccscroll` is a full-screen app that the wheel scrolls (to measure scrolling), `tui MS` redraws the whole screen every MS ms until a key (`tui MS LINES` prints LINES numbered lines instead), `propose HOST` adds an agent's proposal, and `-fake-latency 80ms` adds to the terminals' round trip. For the policy tab's speed: `-fake-policy FILE` (YAML with `network_policies`) makes dev's current rules, and `-fake-gateway-latency 30ms` adds to each gateway call; `BenchmarkPolicyEditCycle` (`-tags uitest`) times each edit route on a synthetic policy of a large sandbox's size (or on the file in `IWATO_BENCH_POLICY`), and `TestPolicyEditGatewayCalls` bounds the gateway calls each makes
- Everything else needs Windows with WSLc, or on macOS and Linux a Docker-compatible engine such as Colima ([ADR 0002](../adr/0002-macos-docker-compatible-runtime.md))

## Building the exe

`scripts/build-exe.sh` builds `dist/windows-{amd64,arm64}/iwato.exe`, `dist/darwin-arm64/iwato` and `dist/linux-{amd64,arm64}/iwato`. `web/` is embedded in the exe, so UI-only changes need a build too.

- CI (`.github/workflows/build.yml`) builds these on every push; on `main` it also replaces the assets of the pre-release `edge` (the tag `edge` follows `main`). Those exes are built with `--images ghcr.io/mtsmfm --image-tag edge --image-digests ...`: they pull both images of their own commit by digest, which `.github/workflows/image.yml` publishes on pushes to main (tagged `edge`, `sha-<commit>` and `src-<hash>` of `scripts/image-sources.sh NAME`, which the edge job waits for; an image whose `src-` tag exists is not rebuilt, only retagged); PRs from forks that touch the images only build the ones whose sources changed
- PRs from branches of this repository: `.github/workflows/pr-build.yml` publishes the pre-release `pr-<N>` on every push (`--channel pr-<N>`, pinned to the `src-` images of the PR's head, which `image.yml` publishes for them with the tag `pr-<N>`) and deletes it with its tag when the PR is closed; the settings install it ([ADR 0019](../adr/0019-try-pull-request-builds.md))
- Default (a development exe): the exe pulls the standard image `ghcr.io/mtsmfm/iwato-sandbox:edge` when it has none, pulls a newer one only on request, and builds the gateway image itself, which needs `docker buildx` (as does `--local-images`)
- `--local-images`: builds both images from the sources embedded in the exe (to try a change to `sandboximage/` before it reaches main)
- `--images REGISTRY`: pulls both images tagged with the exe's version (the release) or `--image-tag TAG`; with `--image-digests iwato-gateway=sha256:...,iwato-sandbox=sha256:...` as `tag@digest`, so a moved tag is not used and there is no update. Without digests, the `edge` tag is pulled by the tag alone: it is meant to move
- `--channel edge|release|pr-N --repo OWNER/REPO` (with `--image-digests`): a published exe, which offers updates from that repository's releases (`update.go`). CI also writes `dist/iwato-build.json` (`scripts/build-meta.sh`), the hashes and digests the update checks
- `--debug`: keeps the debug option (the page's `?font=fallback`). Use it only while debugging. No build of this script has the fake backend (`-tags uitest`)

Release: pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`, which publishes the images (amd64 and arm64) and the exes.

### Building on Linux

Optional (the edge pre-release has Linux binaries): after the [setup](#setup), `go build -ldflags '-X main.imageRegistry=ghcr.io/mtsmfm -X main.imageTag=edge' -o iwato .` builds one like a default build (a plain `go build .` carries no `iwato-entrypoint`, so it cannot build the sandbox image).

## Bumping pinned versions

Everything the images and the gateway download is pinned; `scripts/check-pins.sh` (also the `pins` job of `build.yml`) checks the pins against their sources. Needs `curl` and `jq`, and `gh` for Selkies; inside an Iwato sandbox, GET rules for `ghcr.io` (`/token`, `/v2/nvidia/openshell/*/manifests/*`), `auth.docker.io` (`/token`), `registry-1.docker.io` (`/v2/library/ubuntu/manifests/*`), `github.com` (`/NVIDIA/OpenShell/releases/download/**`) and `release-assets.githubusercontent.com` (OpenShell's release assets).

- OpenShell (`OpenShellVersion` in `internal/openshell/wslc/driver.go`):
  1. the release tarballs: `ARG OPENSHELL_TAG` in `internal/openshell/gateway/Dockerfile` and its `case` entry, the SHA-256 of the 4 tarballs for x86_64 and aarch64, copied from the release's `*-checksums-sha256.txt` (or `gh api repos/NVIDIA/OpenShell/releases/tags/vX.Y.Z --jq '.assets[] | [.name, .digest] | @tsv'`). A tag with no entry fails the build; `dev` is checked only against the checksums published with it
  2. the runtime images: `supervisorImageDigest` and `sandboxImageDigest` in `driver.go`, and the same refs in `gateway-docker.toml` (the Docker driver's). The digest is the tag's index (multi-architecture), the `Docker-Content-Digest` of `HEAD /v2/nvidia/openshell/NAME/manifests/X.Y.Z` on ghcr.io, as `scripts/check-pins.sh` prints on a mismatch (or `docker buildx imagetools inspect ghcr.io/nvidia/openshell/NAME:X.Y.Z`)
  3. the Go SDK at the release's commit (`go get github.com/NVIDIA/OpenShell/sdk/go@COMMIT`), and the compute driver protos (`internal/openshell/wslc/README.md`)
  4. the supervisor patch: rebase `h2-l7.patch` onto the release's commit (the commit hash in the Dockerfile's `supervisor-h2-l7` stage), run OpenShell's `cargo test -p openshell-supervisor-network`, and regenerate it with `git format-patch` ([openshell.md](openshell.md#the-patched-supervisor-http2-for-opted-in-endpoints-nvidiaopenshell2426))
  5. `go test ./internal/openshell/...` (`TestDockerfilePinsOpenShell`, `TestImageTag`) and `scripts/check-pins.sh`
- `ubuntu:24.04` (`FROM` in `sandboximage/Dockerfile` and the gateway's): pinned by the digest of the tag's index. Dependabot (`.github/dependabot.yml`) proposes the new digest weekly; by hand, `scripts/check-pins.sh` prints the current one
- Selkies (`sandboximage/Dockerfile`): `SELKIES_VERSION` and the `case` entry per architecture, from the release's asset digests (`gh api repos/selkies-project/selkies/releases/tags/VERSION --jq '.assets[] | [.name, .digest] | @tsv'`). A version or architecture with no hash, or a mismatch, fails the build; `--build-arg SELKIES_DEB_SHA256=` overrides the hash for one build
- CodeMirror and marked (`web/vendor/codemirror.js`, `markdown.js`): change `scripts/codemirror/package.json`, update the lock file (`npm install --package-lock-only` there), run `scripts/build-codemirror.sh` and put the printed hashes in `web/vendor/manifest.json`. The `codemirror` job of `build.yml` rebuilds them from the lock file (with its integrity hashes) and fails unless they are the committed files with the manifest's hashes
- Iwato's own release images need nothing: `release.yml` passes the digests it pushed to the exes

## Tests

Run locally only what you touched and what is related; CI runs the rest.

- CI on every push: `.github/workflows/build.yml` (`python3 scripts/fetch-vendor.py`, `go test ./...`, `go vet -tags uitest ./...` and `go test -tags uitest ./...` (the fake backend and the tests that use it), the darwin `go vet ./...`, `scripts/build-exe.sh`, which also builds the macOS and Linux binaries; rebuilding `web/vendor/codemirror.js` and `scripts/check-pins.sh`, [above](#bumping-pinned-versions)) and, when Go code, `internal/`, the images, the profiles or the presets change (its `paths`; not `web/` or `docs/`), `.github/workflows/e2e.yml` (`MODE=tls internal/openshell/wslc/e2e.sh`, on a runner set up like a disposable Linux dev container (see the steps in `e2e.yml`), with the work directory's logs kept as an artifact on failure; and `TestDockerE2E` on the runner's Docker). `.github/workflows/macos.yml` runs the tests on a Mac only when darwin-related files change. CI does not run `node scripts/ui-behavior-tests.cjs` or the browser checks
- Go: the packages you touched and related ones (`go test ./<pkg>`, `-run`). The tests that use the fake backend are in `*_fake_test.go` and need `-tags uitest` (`go test -tags uitest .` runs all of the root package's)
- UI: `node scripts/ui-behavior-tests.cjs`, and the browser checks for the screens you touched, at PC width and with `--phone`
- e2e: `MODE=tls internal/openshell/wslc/e2e.sh` runs the WSLc driver with a fake `wslc` (`fakewslc`) against the official OpenShell gateway and sandbox. Run it only in a disposable Linux machine or container with passwordless sudo (it writes system paths such as `/sandbox`, `/home/dev` and `/etc/openshell`; the list is at the top of the script), and when you touch the driver, the exchanges with OpenShell or the images. It takes about 2 minutes (several with an empty Go build cache), and only one run fits on a machine at a time (ports, `/sandbox` and certificates are shared, so runs wait on a lock): give background runs a long timeout. It prints `TIME` lines per step and removes its work directory at the end (`KEEP=1` keeps it on failure). `ONLY=<name>` runs one test (for example `ONLY=ingress`)
- Unit tests only where the logic is complex and a test is really worth it; check that a new test does not repeat what existing ones cover

### Browser checks

They build Iwato with `-tags uitest`, run `serve -fake` (the fake backend, made for these checks) and drive it with Playwright (Chromium headless shell) installed in `~/.cache/iwato-browser` (`npm i playwright` there, then install the headless shell). Each saves screenshots next to the binary it built. The OS libraries Chromium needs (Ubuntu 24.04 package names):

    sudo apt-get install -y libglib2.0-0t64 libnspr4 libnss3 libatk1.0-0t64 libatk-bridge2.0-0t64 libdbus-1-3 libx11-6 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxrandr2 libgbm1 libxcb1 libxkbcommon0 libasound2t64 libatspi2.0-0t64 fonts-noto-cjk

Run one with (`<repo>` is the checkout):

    cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/CHECK.cjs [--phone]

| Check | Covers |
|---|---|
| `term-check.cjs` | the terminal: typing in the fake backend, full-width characters, row height, history pages, a socket that dies unnoticed reconnecting on return, screenshots |
| `focus-check.cjs` | the terminal's keyboard focus: kept during list updates and output, keys still arrive after selecting text, returns after closing menus, panels and confirmations, a reconnect does not steal it from an input. Records focus moves (focusout's relatedTarget and the stack) and prints them on failure. No `--phone` |
| `keyboard-rows-check.cjs` | the on-screen keyboard does not change the terminal's rows (`holdRows` in `web/app.js`). Run it after changing anything around the keyboard or the page layout. No `--phone` |
| `policy-check.cjs` | the policy tab: the approve dialog, adding rules, removing parts, the history (the fake backend runs the same code on a fake gateway, `fake_policy.go`) |
| `resources-check.cjs` | the resources: the header's row on one line (short values on a phone), the Resources tab, the GPU note, the sidebar's line and totals, the settings' levels and hiding, a stopped sandbox's home, the volumes' sizes, the requests in an idle minute |
| `volumes-check.cjs` | the volumes view (per sandbox, deleting leftovers, the create form's note) |
| `providers-check.cjs` | providers: list, add (also without a name), change values, delete, Atlassian connect and reconnect (the fake's authorization page, pasting on a phone), the create form's choice, attach and detach with the restart choice (a restart goes through stopping and starting; a failed one says why), "waiting for restart", the profile's YAML. Real-hardware steps: [providers.md](providers.md) |
| `files-check.cjs` | the files tab: URLs, creating and revoking app passwords and secret URLs, reading and writing over WebDAV, the stopped state |
| `editor-check.cjs` | the editor: folder choice and memory, Ctrl+P, the tree (left on a PC, a drawer on a phone; expansion memory, open-file marks), editing, unsaved marks, Ctrl+S, conflicts with changes elsewhere (overwrite, reload), refusing binaries, image previews (PNG, SVG, WebP, refusing HTML named .png, raw headers), not taking the terminal's Ctrl+P, the stopped state |
| `split-check.cjs` | the editor and the terminal side by side: the toggle only on wide cards (none with `--phone`), both panes and both tabs current, the terminal's columns following the divider with few resize messages, each pane's keys (Ctrl+P), the policy tab replacing the split and the split coming back, the memory over a reload, the fallback to tabs in a narrow window |
| `ingress-check.cjs` | publishing (opening the Ports tab's `*.localhost` links, which must not show the admin page; the settings' Tailscale, secret and copied steps; the tab's nodes and port links; stopped), the startup checks and the standard image in the settings and the recreate dialog's pull option, switching the language, **Delete everything** (the dialog, the sign-out, signing in with the console's new URL), and finally the banner without an engine (`serve -fake -fake-engine-down`). Tailscale itself is checked on real hardware ([ingress.md](ingress.md)) |

Checks that look for strings already moved to the dictionary pin the context's locale (`locale: 'ja-JP'`); otherwise Playwright's default `en-US` shows English.

### Other checks

- The terminal model: `internal/termsync`'s `TestRandomOutput` applies frames the way the browser does and compares with the server's state after random output, resizes and screen switches. Go tests read a screen with `internal/termsync/termsynctest`
- Terminal recordings: `go run ./cmd/term-replay [-at MS] [-hist] FILE` replays a `termTrace` recording through `internal/screen` ([terminals](../user/terminals.md#terminal-recordings))
- Feature notes with real-hardware steps: [files.md](files.md), [gpu.md](gpu.md), [ingress.md](ingress.md), [providers.md](providers.md), [openshell.md](openshell.md)

## Ideas not started

1. Run e2e in several checkouts in parallel (separate ports, work directories and certificates, no lock)
2. Replace the browser checks' fixed waits (`waitForTimeout`) with waiting for states (faster, less flaky)
3. Split `web/styles.css` per feature and collect the element id list automatically (fewer merge conflicts)
