# OpenShell behaviour Iwato relies on

Upstream OpenShell behaviour (v0.1.4; some findings date from v0.1.2) that Iwato depends on or works around, and upstream bugs that are still open. Feature-specific findings live with the feature: SFTP and the policy's `read_only` in `files.md`, credentials and uninspected rules in `providers.md`, GPUs in `gpu.md`. `docs/dev/upstream/` holds the patch and the scripts referred to here. Re-check these when upgrading OpenShell.

## HOME and the image's ENV

Checked with the official Docker driver on 2026-10-07 (v0.1.2; `VERSION=X.Y.Z bash docs/dev/upstream/env-check-docker.sh` re-checks it):

- HOME is the working directory (`/sandbox`), whatever the image's `ENV HOME` says. Only `--env HOME=...` at creation takes effect. Iwato therefore sets `HOME=/home/dev` in the creation environment (`sandboxEnv`)
- The image's ENV stays in the main process but is gone in an exec. Anything a terminal needs from the image goes through shell startup files instead (`/etc/profile.d`, `/etc/bash.bashrc`; e.g. the GPU environment in `gpu.md` and `~/.config/iwato/env` in `providers.md`)
- An exec's `--workdir` changes only PWD

## The Policy Advisor guide does not reach the workload (NVIDIA/OpenShell#3698, open)

The supervisor means to install the guide (`/etc/openshell/skills`, and `/AGENTS.md` when the image has none) but writes it into its own container. Checked on 2026-10-07 with the Docker driver (WSL, Docker 29.2.1): none of the three files arrive, neither for a sandbox created after enabling `agent_policy_proposals_enabled` nor for one where it is enabled while running. v0.1.4 behaves the same. The image carries the guide instead (ADR 0005); the WSLc driver copied it into the workloads it created before that (`internal/openshell/wslc/README.md`).

To re-check on a new version: `VERSION=X.Y.Z bash docs/dev/upstream/skill-check-docker.sh` on any Linux with Docker (WSL with Docker Desktop works). Once upstream fixes it, images that follow the convention could drop their copy.

## openshell-sandbox does not exit on SIGTERM as PID 1 (NVIDIA/OpenShell#4635, open)

`openshell-sandbox` (0.1.2 through v0.1.4 and main as of 2026-10-13), running as the container's PID 1, answers SIGTERM by logging `Sandbox boundary received termination signal` and SIGKILLing the workload, but then does not exit. The stop timeout runs out and the container is killed (exit 137). This is OpenShell's behaviour, not WSLc's: the official Docker driver hangs the same way when the workload container is stopped directly (`docker stop -t 10`: 7 of 8 runs hit the timeout on 2026-10-07; `bash docs/dev/upstream/sigterm-repro-docker.sh [RUNS]` reproduces it). When it is not PID 1 it stops within 0.1 s with exit 0.

Cause (found 2026-10-07 with an instrumented v0.1.2 sandbox): a deadlock. The supervisor waits for the main process with a `Wait` request; the sandbox serves it on a blocking task (`serve_one` → `dispatch` → `ManagedProcess::wait`) that waits on a std Condvar, which an async task on the runtime (`spawned.wait()` in `ManagedProcess::spawn`) notifies. On SIGTERM the sandbox kills the main process and drops the runtime at once; the drop first cancels the async tasks (so the notification never comes) and then waits for the blocking pool (`BlockingPool::shutdown`). The stacks of a hung sandbox match this. Why only as PID 1 is a guess: the exit wait then sees every process in the namespace, so the notification misses the drop. v0.1.4 has the same shape (`BoundaryRuntime::shutdown` only calls `deactivate`).

Proposed fix: `docs/dev/upstream/openshell-sandbox-shutdown-wait.patch` (still applies to upstream main): in `BoundaryRuntime::shutdown`, record "boundary terminated" as the exit unless one is known and wake the Condvar. On WSLc the patched sandbox stopped 0.1 s after SIGTERM (60 s timeout before), and the e2e passes with it.

How Iwato avoids it, as the Docker driver does: the WSLc driver stops the supervisor first (`stop --time 5`, then remove), so the supervisor ends the workload through the sandbox and no `Wait` is left when the workload container gets its SIGTERM (`stop --time 2`). Measured on WSLc: 0.61 s, exit 1 (`received SIGTERM before supervisor confirmation`), three out of three; the official `openshell sandbox stop` takes about 0.5 s with the same exit. The short 2 s grace (`stopTimeoutSec` in `driver.go`) stays for when the workload container is stopped directly (e.g. the host shuts down).

## Listening sockets outlive their process (NVIDIA/OpenShell#4331, fixed in v0.1.4)

Up to v0.1.2 the network broker (the supervisor, PID 1) kept its own duplicate of every socket it created for a sandbox process while Created / Bound / Listening (`crates/openshell-isolation-interface/src/linux/socket_registry.rs`). After the listening process exited, the socket stayed in LISTEN in `/proc/net/tcp`, held only by PID 1, and still accepted connections until OpenShell's sweep ran. The sweep runs when a sandbox process's bind fails with EADDRINUSE (`bind_socket` → `collect_closed_socket_entries` in `crates/openshell-sandbox/src/network_broker.rs`) and treats a socket as live when some sandbox process (other than the broker) has its inode in `/proc/*/fd`.

v0.1.4 releases the broker's duplicates after listen (NVIDIA/OpenShell#4444). Sandboxes created before v0.1.4 keep the supervisor image and `openshell-sandbox` they were created with (both drivers pin them at creation) until recreated, so Iwato's ingress still triggers the sweep before reading `/proc/net/tcp`: it binds the supervisor's DNS relay 127.0.0.53:53, which always fails with EADDRINUSE (`ingress.ListenScript`, [ingress.md](ingress.md)). On v0.1.4 that is one harmless failed bind. Reading `/proc/*/fd` from Iwato's exec does not work instead: an OpenShell exec cannot read `/proc/PID/fd` of processes started in other sessions (such as the main terminal), so every port looked ownerless.
