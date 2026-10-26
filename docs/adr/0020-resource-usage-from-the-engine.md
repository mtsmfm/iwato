# 0020. Resource usage read through the engine

- Status: Accepted
- Date: 2026-10-26

## Context

The owner wants each sandbox's CPU, memory, home size and GPU memory in the page. Inside the sandbox the workload user cannot read them: OpenShell's Landlock denies `/sys/fs/cgroup`, `/proc/meminfo` shows the whole VM, and `df` of the home shows the whole disk. WSLc has no `stats` command (`fakewslc` follows its argument definitions); Docker has `docker stats`, but one way for both engines keeps one code path. ADR 0016 rules out helper binaries in the image.

Alternatives: an exec through OpenShell as the sandbox user (inside Landlock, so no cgroup); `docker stats` on Docker only; a daemon in the image (ADR 0016).

## Decision

- Iwato reads them with the engine's `container exec` into the workload container (found by the drivers' `openshell.ai/*` labels), as nobody (65534) with a fixed `PATH` (`env -i`; outside OpenShell's Landlock, so never root and never the image's `PATH`, which may name folders the sandbox can write): `cat` of the cgroup v2 files, `/proc/meminfo` and `/proc/stat` for the PC's totals, `nvidia-smi` for sandboxes with a GPU. The home's size is `du -x -d 3` run as the home's owner at the lowest CPU and I/O priority, at most every 10 minutes per running sandbox and one at a time; its last result is kept in `resources.json` beside the configuration
- Only while a page asks (`GET /api/resources`, every 4 seconds while it shows; the shown sandbox is read at most every 4 seconds, the others every 14), and only for sandboxes the list saw running. With the memory notification on, every 30 seconds while pushes can go out
- CPU is the cgroup's usage between two reads, as a share of its `cpu.max` (else the PC's cores); memory is `memory.current` less the inactive file cache (as `docker stats`), against `memory.max` (else the PC's memory). The GPU's numbers are the whole GPU's: WSL gives no per-process memory

## Consequences

- No change to the image or to OpenShell; standard tools only (`sh`, `cat`, `grep`, `du`, `sort`, `nvidia-smi`, `timeout`, `nice`, `ionice` when present). Images without them show nothing or partial numbers
- Each read is an engine CLI call plus an exec: about 15 a minute with a page open and one sandbox running, plus 4 a minute per other running sandbox
- The agent can see these short-lived processes in `ps` while they run
- History is kept by the page only (10 minutes); closing it loses it
