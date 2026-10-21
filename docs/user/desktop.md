# Desktop and GPU

[日本語](ja/desktop.md)

The standard image has a desktop for GUI apps: Xfce, Firefox and Selkies (a remote desktop in the browser), with Mesa's d3d12 driver. It is not tied to any agent and runs entirely as the sandbox user `dev`, without root. Background and what is not verified yet: [docs/dev/gpu.md](../dev/gpu.md).

## Start and open it

- Start: run `iwato-desktop` in the sandbox's terminal (it starts in the background; the log is `~/.cache/iwato-desktop.log`). When it is already running, it says so. `iwato-desktop status` shows whether it runs, `iwato-desktop stop` stops it, `iwato-desktop run` runs it in the foreground
- Open: in the Ports tab, open the link for port **6080** ([publishing](publishing.md)). The desktop has no login of its own: it listens on loopback only, and the publishing decides who can open it (with this PC only, browsers on this PC; with Tailscale, the devices of the people in **Owner logins**)
- It does not start by itself when the sandbox starts. Stopping the sandbox kills the desktop abruptly; the next `iwato-desktop` cleans up what that left behind (stale locks and sockets)
- Inside the desktop, links open in its Firefox (`BROWSER=firefox`). `iwato-desktop-doctor` prints what a link click would use
- `IWATO_DESKTOP_PORT` changes the port, `IWATO_DESKTOP_ARGS` passes more Selkies options (for example `--encoder=jpeg`)

## GPU

The GPU is optional. Tick Use the GPU when creating the sandbox (or later when recreating it) to render and encode on the GPU; without it the desktop renders on the CPU. WSLc passes the whole GPU (`--gpus all`): there is no choice of model or count. The card shows a GPU mark.

- NVIDIA gets CUDA as on WSL. AMD and Intel get D3D12 / DirectML and Mesa's d3d12 (no `/dev/dri`)
- Every sandbox gets a 1 GB `/dev/shm` (Firefox crashes with Docker's default 64 MB)
- On a Docker engine, Use the GPU appears only when Docker has an NVIDIA GPU through CDI (the NVIDIA Container Toolkit's CDI spec): not on macOS ([ADR 0002](../adr/0002-macos-docker-compatible-runtime.md))
