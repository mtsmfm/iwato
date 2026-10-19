# 0007. An in-house OpenShell compute driver for WSLc

- Status: Accepted
- Date: 2026-10-07

## Context

Iwato runs each sandbox as an NVIDIA OpenShell sandbox. OpenShell runs sandboxes through compute drivers (Docker, Podman, Kubernetes, a VM driver) and accepts external drivers over gRPC (the compute driver protos in `internal/openshell/proto/`). On Windows, Iwato wants containers without asking users to install anything:

- WSLc (`wslc.exe`) ships with Windows and runs containers in one VM per session. It shuts the VM down when idle and drops the page cache to return memory (details in ADR 0002)
- Docker Desktop and upstream's Docker driver would work, but bind users to Docker Desktop's license (paid for commercial use in large organizations) and an extra install
- Upstream has no WSLc driver

## Decision

Iwato ships its own compute driver, `internal/openshell/wslc` (`openshell-driver-wslc`), a port of upstream's Docker driver contract to `wslc.exe` (workload and supervisor containers, boundary files copied in as tar archives, `driver_config` mounts in the Docker driver's schema). Iwato.exe runs it on Windows loopback, and the gateway container reaches it through a socat relay of the driver's Unix socket (`internal/openshell/wslc/README.md`). It follows OpenShell's behaviour and changes it only where WSLc forces it or where it is clearly inconvenient (`AGENTS.md`).

## Consequences

- Windows needs nothing besides WSLc. Iwato depends on WSLc's CLI and its differences from Docker (no `--network host`, no capability drop at create, `--gpus all` only; see `docs/dev/gpu.md`)
- Iwato maintains a driver: every OpenShell upgrade must be followed (the driver targets v0.1.4), and upstream bugs show up in it (`docs/dev/openshell.md`). `fakewslc` and `e2e.sh` test it against the official gateway and sandbox without Windows
- The driver grew behaviour the Docker driver lacks (placing `iwato-term` and the Policy Advisor guide, a writable volume on the working directory, `/dev/shm` of 1 GB). ADR 0002 and ADR 0005 move these into the image convention and `driver_config`, so the driver stays a plain port and other platforms can use upstream's Docker driver
