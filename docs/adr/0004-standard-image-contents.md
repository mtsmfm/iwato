# 0004. What goes into the standard image

- Status: Accepted
- Date: 2026-10-17

## Context

- The user inside the sandbox (`dev`) is not root and has no sudo (OpenShell drops all privileges). OS packages cannot be installed later, so anything not in the image is unavailable
- The current standard image (`sandboximage/Dockerfile`) has Ubuntu 24.04 and common tools (git, gh, tmux, curl, ripgrep, fzf, zoxide, eza, bat, jq, python3, etc.), mise and starship (`install-tools.sh`), and the desktop (Xfce, Firefox, Selkies, Mesa). There is no compiler (what was installed for the desktop's shim is removed in the same layer)
- mise installs language runtimes and tools well with user privileges, but anything built from source (Ruby, mise's default Python build, etc.) needs a C toolchain and development headers. The current image lacks them, so those installs fail
- A release exe pulls the images from ghcr.io (`.github/workflows/release.yml` publishes amd64/arm64 on tags, `scripts/build-exe.sh --images`). Any other exe (development builds) builds them locally from the source embedded in the exe; the desktop layer alone is about 1 GB and takes a long time

## Decision

- The standard image includes what a user without root cannot easily install with mise:
  - OS packages and a build toolchain (build-essential, pkg-config, cmake, etc.)
  - Development headers mise needs to build runtimes (for Ruby and Python: libssl-dev, zlib1g-dev, libyaml-dev, libffi-dev, libreadline-dev, libsqlite3-dev, libbz2-dev, liblzma-dev, etc.)
  - Candidate common libraries and CLIs: sqlite3, postgresql-client, a MySQL client, unzip/zip/xz-utils, rsync, openssh-client, imagemagick, ffmpeg, locales, and the system libraries Playwright/Chromium need to run
- Language runtimes and tools that mise installs well (node, python, go, ruby, java, various CLIs) are not included. Users install them with mise (the python3 that is already part of the OS stays)
- The desktop (Xfce, Firefox, Selkies) stays in the default image. We do not split into a light image and a desktop image for now
- Images are pulled prebuilt from ghcr.io. Every merge to main publishes an `edge` tag, and development exes pull that too. Local builds happen only on request (an option)
- The exact package list is decided in the implementing PR (the above are candidates)

## Consequences

- Installing Ruby, Python, etc. with mise no longer gets stuck on build failures. Playwright's browsers can also be installed into the user's home and run
- The image gets bigger (an estimated few hundred MB for the toolchain, headers, ffmpeg and imagemagick). The first pull takes longer
- Development exes also start faster (no build). In exchange, `edge` is the latest main, so testing local changes requires asking for a build. A workflow that builds `edge` is needed (today's `release.yml` handles tags only)
- The desktop has been run on real hardware
- macOS (ADR 0002) uses the arm64 image. We need to check that all packages (Selkies' .deb, Firefox, etc.) are available on arm64
