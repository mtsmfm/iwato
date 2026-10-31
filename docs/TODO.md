# TODO

Open work. Decisions live in `docs/adr/`; this file lists only the work. Remove items when done.

## macOS (ADR 0002)

- [ ] Release asset and a Homebrew tap (unsigned; `docs/user/getting-started.md` explains removing the quarantine attribute)
- [ ] Try it on a real Mac with Colima, Docker Desktop (host networking on) and OrbStack: the gateway's port reaching the Mac, `/var/run/docker.sock` in each VM, the Keychain prompt-free, sandboxes, terminals, files, ingress
- [ ] Consider stopping the engine's VM (or telling the owner) when all sandboxes are stopped; the startup checks show the VM's CPUs and memory
- [ ] Podman (upstream's `openshell-driver-podman`) as another engine

## Windows resources (ADR 0002)

- [ ] Verify on real hardware: whether the WSLc session's VHDX shrinks after deleting images and volumes and pruning; whether memory returns to Windows when all sandboxes are stopped
- [ ] If the VHDX does not shrink, consider documenting how to shrink it (WSLc session settings, or Optimize-VHD etc.)

## Image convention (ADR 0005)

- [ ] Verify on real hardware: an existing sandbox (old image) keeps starting and attaching after the update, then recreate it; `wslc image inspect` shows the `iwato.entrypoint` label (Config.Labels) so new sandboxes run the daemon

## Publishing (ADR 0003)

- [ ] Check `*.localhost` on Windows hardware: Firefox, Edge, `curl.exe` and PowerShell's name resolution (`docs/dev/ingress.md`)

## Presets

- [ ] Import user presets from a URL
- [ ] Still unverified against real traffic: pip/uv, `gem install`, `uv python install` through `releases.astral.sh` (where it redirects). yarn's `registry.yarnpkg.com` is in no preset

## Providers

- [ ] Claude Code sign-in (`iwato-claude-subscription`): on real hardware, check that Anthropic takes the sign-in without `org:create_api_key` and the gateway's refresh (a form body; Claude Code sends JSON), and the access token's lifetime; review the type against Anthropic's legal and compliance page on subscription OAuth tokens

## Images (ADR 0004)

- [ ] Verify the arm64 image (for macOS)

## Distribution

- [ ] Distribute the Windows exe unsigned through winget and/or scoop (no code-signing certificate)

## On hold

- [ ] Fix zoxide changing `$?` in the prompt
- [ ] Upstream OpenShell issue: reloading the policy drops all live connections
