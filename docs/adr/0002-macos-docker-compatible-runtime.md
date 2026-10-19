# 0002. Run on macOS with a Docker-compatible engine and OpenShell's Docker driver

- Status: Accepted
- Date: 2026-10-17

## Context

Iwato currently runs only on Windows (WSLc).

- On anything other than Windows, `main.go` refuses everything except `serve -demo` (`Iwato runs on Windows (WSLc); serve -demo is available elsewhere`)
- What is tied to Windows:
  - `engine.go`: calls `wslc.exe` (the volumes and images Iwato manages itself). Building and pulling the standard image in `images.go` also go through it
  - `internal/openshell/wslc`: OpenShell's WSLc compute driver (a port of OpenShell's Docker driver). Iwato.exe listens on Windows loopback, and the gateway container relays a Unix socket with socat
  - `internal/openshell/gateway`: creates and starts the gateway as a WSLc container (uses `wslc.Runner`)
  - `internal/protect`: secrets (such as the ingress secret) use DPAPI on Windows; elsewhere they are unencrypted 0600 files
  - `checks.go` (the startup checks shown in the browser): the first check is WSLc
- Ways to run containers on macOS: a Docker-compatible engine (Docker Desktop, OrbStack, Colima, etc.), Apple's `container`, and OpenShell's VM driver (`openshell-driver-vm`, libkrun; it runs on Apple Silicon macOS too, but upstream marks it Experimental)
- The in-house driver adds behavior the Docker driver lacks: it places `iwato-term`, places the Policy Advisor guide (NVIDIA/OpenShell#3698), allows a writable volume to be the working directory itself, and sets `/dev/shm` to 1 GB. Maintaining OpenShell drivers in Iwato is a cost to keep as small as possible

## Decision

- On macOS (and, as a bonus, native Linux), sandboxes run on a Docker-compatible engine, and the compute driver is upstream OpenShell's Docker driver, used as is
- The behavior the in-house driver added is covered not by a driver but by the image convention (ADR 0005) and driver configuration:
  - The image carries `iwato-term` and the Policy Advisor guide
  - The home volume is kept within upstream's rule (a volume cannot cover WORKDIR or its parents) by placing the working directory (WORKDIR) outside the home
  - `/dev/shm` via a tmpfs in `driver_config` (`size_bytes`)
- We do not write a driver for Apple's `container`. Once OpenShell's VM driver is no longer Experimental, a separate ADR will consider it as a way to avoid installing a Docker-compatible engine
- Windows always uses WSLc (we do not require Docker Desktop, so as not to bind users to Docker Desktop's license (commercial use in large organizations is paid). WSLc also ships with Windows, so nothing else needs installing. WSLc also groups containers into one VM per session (containerd and dockerd inside), but it shuts the VM down 30 seconds after no containers are running and no operations are in progress, and while running it drops the page cache to return memory (microsoft/WSL `src/windows/wslcsession/WSLCIdleState.h`, `WSLCSession.cpp`, `src/linux/init/WSLCInit.cpp`). While Iwato runs, the gateway container keeps running so the VM does not shut down, but the page-cache dropping still works. Running Selkies on both, memory appeared not to be returned on Docker, and it was on WSLc. Whether Docker Desktop on WSL returns memory depends on WSL's `autoMemoryReclaim` setting (unverified). For disk, the session's storage is a dynamic (not sparse) VHDX, and ext4 is mounted with `discard` (`wsl::core::filesystem::CreateVhd(..., false, false)`). Whether space freed by prune is returned to Windows as a smaller VHDX file cannot be determined from the source; like Docker Desktop, it is expected not to shrink. To be verified on real hardware). Upstream has no WSLc driver, so the in-house driver (`internal/openshell/wslc`) stays. However, behavior that the convention (ADR 0005) now covers (placing `iwato-term` and the guide, the working-directory volume) is removed, keeping it a port of upstream's Docker driver and keeping the difference from upstream small

Reason: leave driver maintenance to OpenShell, and have Iwato add what it needs within the forms upstream accepts (the image and `driver_config`).

## Consequences

- Split the Engine (volumes, images) and gateway startup so they can be swapped per runtime (today both `Engine` and `gateway.Ensure` assume wslc). On macOS and Linux the gateway uses upstream's Docker driver with the engine's socket
- Meet the requirements of upstream's Docker driver: Docker Desktop must have host networking enabled, and Enhanced Container Isolation cannot be used (`docs/how-it-works/sandboxes/runtimes.mdx`). Volumes Iwato creates get the `openshell.ai/sandbox-attachable` and `openshell.ai/sandbox-attachable-workspace` labels, and the gateway enables `allow_driver_config`
- Images that do not follow the convention (no `iwato-term`, WORKDIR is the home) work with the previous terminal method, and files use OpenShell's SFTP (rooted at WORKDIR)
- For the same reason, macOS does not assume Docker Desktop either. Guidance and checks default to a free engine (Colima, MIT); Docker Desktop and OrbStack (both may be paid for commercial use) work but are not recommended. Podman also has an upstream Podman driver (`openshell-driver-podman`), to be considered later as another option
- Docker-compatible engines on macOS also run containers in a single Linux VM, so CPU and memory assigned to the VM are not returned when sandboxes stop (Colima fixes the VM size at startup). The startup checks shown in the browser (`checks_docker.go`) show the VM's allocation; whether to stop the VM when all sandboxes are stopped is to be considered later
- Users must install a Docker-compatible engine themselves. The startup checks (`checks_docker.go`) check that the engine exists, is running, its settings (host networking etc.), version and architecture, and explains how to install it if missing
- On macOS, secrets go in the Keychain (a darwin version of `internal/protect`). Linux stays plaintext 0600 as today (Secret Service later)
- Distribution: a darwin arm64 build and a Homebrew tap, unsigned (no paid Developer ID signing and notarization; Homebrew avoids most of the Gatekeeper friction, and the docs explain removing the quarantine attribute otherwise). Images need arm64 versions (`release.yml` already builds amd64/arm64)
- Add macOS-runner builds and tests to CI (e2e tests that run Docker run on Linux)
- Native Linux also works (a by-product)
- No GPU on macOS. Docker-compatible engines run containers inside a Linux VM, and there is no way to pass Apple's GPU (Metal) to a container (OpenShell's Docker driver uses CDI for GPUs and the VM driver uses VFIO, both Linux-oriented). "Use GPU" is not offered on macOS, and the desktop runs with CPU rendering. libkrun's virtio-gpu (Venus, Vulkan) has been tried with Podman and krunkit, but OpenShell does not support it
