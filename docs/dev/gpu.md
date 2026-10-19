# GPU and desktop

## GPU (WSLc driver)

OpenShell's API carries a GPU request (`SandboxSpec.resources.gpu`, optionally with a count), and the Docker driver implements it. Iwato's WSLc driver implements it too. The WSLc facts below come from reading microsoft/WSL (master 96a7faa and the 3.0.1 tag) and OpenShell v0.1.2 on 2026-10-08.

### What WSLc (WSL 3.0.1) does

- `wslc container create/run --gpus all`. The only value is `all` (`src/windows/wslc/arguments/ArgumentValidation.cpp:377`). There is no way to pick a GPU or a count (a TODO at `wslc.idl:335`), and no `--device` or arbitrary CDI
- Internally it becomes Docker's `DeviceRequests={cdi, "microsoft.com/wslc=gpu"}` (`src/windows/wslcsession/WSLCContainer.cpp:2569-2577`). The VM's init writes the CDI spec to `/etc/cdi/microsoft.com-wslc.json` (`src/linux/init/WSLCInit.cpp:73-110`)
- The container gets:
  - `/dev/dxg` (rwm). Its mode is 0666 in the kernel, so non-root users can open it
  - `/usr/lib/wsl/lib` and `/usr/lib/wsl/drivers`, mounted read-only
  - a createContainer hook `wsl-gpu-hook` that writes `/etc/ld.so.conf.d/ld.wsl.conf` in the rootfs, runs the image's `/sbin/ldconfig`, and bind-mounts the lib's executables (`nvidia-smi` etc.) into `/usr/bin` (`WSLCInit.cpp:129-176`)
  - no `LD_LIBRARY_PATH` (ld.so.cache resolves the libraries)
- The default session always has the GPU enabled (`WSLCSessionManager.cpp:130`). The VM's GPU is for compute (no display)
- Images without `/sbin/ldconfig` (distroless etc.) should fail to start, as the hook fails (`util.cpp:733`)
- NVIDIA CUDA gets the same pieces as CUDA on WSL (`/dev/dxg`, `libcuda.so`, the DriverStore). WSL's own tests stop at dxg read/write and ldconfig
- AMD and Intel get no `/dev/dri`, so only D3D12 / DirectML (`libd3d12`, `libdxcore`) or Mesa's d3d12 work

### What the driver does

- Validation (`driver.go`): no count or 1 is accepted, 2 or more is `FailedPrecondition`, like the Docker driver on WSL (one selectable device "all"; OpenShell `crates/openshell-core/src/gpu.rs:518`). `cdi_devices` stays refused (`mounts.go`)
- Create: a GPU request adds `--gpus all`
- Capabilities: `DefaultSelectionSupported: true, CountSelectionSupported: true` (the Docker driver with CDI reports both, `crates/openshell-driver-docker/src/lib.rs:1003-1005`). The gateway only relays them for display
- Resource claim: `"openshell.gpu":"true"` in the bootstrap's and descriptor's `resource_claims` (`internal/openshell/wslc/boundary.go`; Docker: `crates/openshell-driver-docker/src/isolation.rs:96-97`). With the claim and an existing `/dev/dxg`, the supervisor adds `/usr/lib/wsl` (read) and `/dev/dxg` and `/proc` (read-write) to Landlock (`crates/openshell-sandbox/src/boundary_server.rs:86-115, 2489-2494`). Without the claim the workload cannot open `/dev/dxg`
- Stop and start: WSLc's inspect shows nothing about GPUs, so creation records a label `iwato.gpu=true`. `rotate` (`session.go`) rewrites only the session fields of the bootstrap and descriptor JSON saved at creation, so `resource_claims` survive (tested). The label is not read today
- Isolation: seccomp allows by default and does not block ioctl (`seccomp.rs`). Landlock is ABI V3, so the device ioctl restriction (IOCTL_DEV, V5) does not apply. Only opening the path matters, which the claim solves
- `/dev/shm`: every sandbox gets `--shm-size 1g` (see the desktop below)
- UI: **Use the GPU** in the create form, and a GPU mark on the card. On a Docker engine the form offers it, and the create API accepts it (else `gpu_unavailable`), only when OpenShell's Docker driver would: `docker info` has `CDISpecDirs` and a CDI device `nvidia.com/gpu=…` in `DiscoveredDevices` (`Engine.gpuAvailable`, kept for a minute; the list sends `gpu: false` otherwise)

### Tests

- Unit tests (`internal/openshell/wslc/driver_test.go`): count (none and 1 accepted, 2 or more refused), Capabilities, the claim in the bootstrap and descriptor, the claim after a stop → start rotate. `server_ext_test.go` covers `gpu` in the create API
- `fakewslc`: `--gpus` accepts only `all` (else `Invalid gpus value: '…'. Only 'all' is supported.`, WSL's wording); `FAKE_WSLC_GPU=0` fails with `GPU support not enabled on the session`; the container record keeps the GPU
- The fake runs as a host process, so there is no `/dev/dxg` and the supervisor adds nothing to Landlock. The e2e can only cover the flag, the claim, the label and the claim after rotate, and refusing a count of 2 or more

### Checking on real hardware (Windows + GPU)

Not yet recorded as checked: opening `/dev/dxg` (including the Landlock grant), the ldconfig hook with the image, `nvidia-smi`, CUDA, AMD / Intel, and how the UI shows a failed `--gpus all` on a PC or session without a GPU. In a sandbox created with **Use the GPU**:

- `ls -l /dev/dxg /usr/lib/wsl/lib`
- `ldconfig -p | grep -E 'cuda|d3d12|dxcore'`
- `nvidia-smi`
- a CUDA sample, e.g. `python3 -c "import torch; print(torch.cuda.is_available())"`

## Desktop (Selkies)

The standard image (`sandboximage/Dockerfile`) contains a desktop, run with the `iwato-desktop` command (`start`, `stop`, `status`, `run`). There is no separate desktop image. It has been run on real hardware. Tests cover the script's shell syntax, the GPU / no-GPU branches and link creation, start / stop / status (`desktop_test.go`, `TestDesktopScript*` in `openshell_test.go`, with a fake `selkies-session` and temporary directories), and string checks of the Dockerfile.

The desktop layer is part of the standard image, so if it fails to build, the whole standard image fails.

### Build, and why it runs without root

linuxserver/webtop is not used. OpenShell runs the workload as the image's user `dev`, with all capabilities dropped and no sudo, so webtop's root-based s6 `/init` and its runtime writes to `/etc` and ldconfig do not work.

- The standard image's `Dockerfile` installs everything at build time as root (it ends with `USER dev`); at run time everything runs as `dev`. Heavy layers come before frequently changing ones (`COPY etc/` etc.) for caching
  - Xfce (`xfce4`, `xfce4-terminal`), Xvfb, PulseAudio, D-Bus, Japanese and emoji fonts
  - Firefox from Mozilla's apt repository (Ubuntu 24.04's firefox is a snap shell; the key fingerprint is checked)
  - Mesa d3d12 (`libgl1-mesa-dri mesa-va-drivers mesa-utils vainfo`)
  - Selkies' official .deb (`selkies-2.0.0-ubuntu24.04-<arch>.deb`: its own Python environment in `/opt/selkies`, `selkies` and `selkies-session` in `/usr/bin`; apt pulls the dependencies). An empty `SELKIES_DEB_SHA256` builds without verification and logs the hash (`WARNING: selkies .deb … SHA-256 is …`); pin it through the build argument
- `iwato-desktop run` (`/usr/local/bin`) prepares the GPU and runs `selkies-session --session xfce --port=6080 --enable-basic-auth=false --enable-https=false`. `selkies-session` (`src/selkies/session.py` in Selkies' repository) starts Xvfb (`-displayfd` picks a free display, with a cookie), PulseAudio (`--daemonize=no` if none runs), the desktop through `dbus-run-session` (the xfce xsession) and Selkies, as children of the calling user's process. No s6, root or sudo
- Port 6080 (http), listening on loopback only (no `--public`). No login and no TLS: Iwato's **Ports** tab exposes it (HTTPS ends there) and the tailnet's ACL decides who reaches it. `IWATO_DESKTOP_PORT` and `IWATO_DESKTOP_ARGS` change it
- Start: `iwato-desktop` (= `iwato-desktop start`) in a terminal. It runs in the background (`setsid nohup`), logging to `~/.cache/iwato-desktop.log`. It does **not** start automatically. Without a GPU it renders on the CPU

### GPU environment

- `GALLIUM_DRIVER=d3d12` and `LIBVA_DRIVER_NAME=d3d12` are **not** image ENV: whether they are harmless without `/dev/dxg` is unknown (Mesa does not promise to fall back to llvmpipe when a named d3d12 driver cannot create a screen). `desktop/gpu-env.sh` sets them only when `/dev/dxg` exists
- Delivery: an image's ENV stays in the main process but is lost in exec (`docs/dev/openshell.md`). Terminal shells get it from `/etc/profile.d/iwato-gpu.sh` (login shells) and one line in `/etc/bash.bashrc` (interactive shells); `iwato-desktop` reads that file. Non-interactive execs such as `bash -c` do not get it
- NVIDIA: at every start, `iwato-desktop` links `/usr/lib/wsl/drivers/nv_dispi.inf_amd64_*/libcuda.so.1.1` (the last glob match) as `libcuda.so.1` and `libcuda.so`, and `libnvidia-encode.so.1` if present, into `$HOME/.local/lib/wsl-nvidia`, and prepends that to `LD_LIBRARY_PATH`. Reason: `/usr/lib/wsl/lib/libcuda.so.1` lacks the CUDA-EGL interop symbols (`cuGraphicsEGLRegisterImage`), so Selkies (pixelflux) refuses NVENC and falls back to x264. The store's hash changes with driver updates, hence every start. Without a GPU (no `/dev/dxg`), or with a non-NVIDIA GPU (no store), this is skipped

### /dev/shm

WSLc's default, like Docker's, is 64 MB, which crashes Chromium and Firefox (Selkies' docs recommend `--shm-size=2g`). Any sandbox can run the desktop, so the driver gives **every** sandbox `--shm-size 1g` (`memoryDeviceFlags`, `TestMemoryDeviceFlags`); `--gpus all` is added only for a GPU request. `--shm-size` only caps the `/dev/shm` tmpfs (`size=`); it allocates nothing, and only written pages use memory (Docker's meaning; counted within `--memory` if set). WSLc accepts it at container create (`container create` in WSL's `ArgumentDefinitions.h`; the fake wslc takes it as a flag with a value). `iwato-desktop run` warns below 512 MB.

### Commands

- `iwato-desktop` / `iwato-desktop start`: starts in the background (`setsid nohup … run`; stdout and stderr to `~/.cache/iwato-desktop.log`, rotated to `.1` past 1 MB; stdin `/dev/null`). Waits up to 5 seconds for the pidfile (`~/.cache/iwato-desktop.pid`), then prints the pid and that port 6080 is published from the **Ports** tab. Already running: "already running", exit 0. An image without the desktop (no `selkies-session`): exit 1
- `iwato-desktop stop`: TERM to the pid and its descendants (`run`'s TERM handler stops the session), up to 10 seconds, then KILL; removes the pidfile. Not running: "not running", exit 0
- `iwato-desktop status`: running: pid and the log path, exit 0; else exit 1
- `iwato-desktop run`: runs in the foreground (what `start` runs), including the cleanup below and the no-GPU fallback
- Idempotence: whether `/proc/<pid>/cmdline` of the pidfile's pid contains `iwato-desktop` (so a pid reused after stop → start is told apart). Check and pidfile write are not locked (only two simultaneous starts can overlap)

### What a sandbox stop does

1. Iwato's stop → OpenShell `StopSandbox` → the WSLc driver's `StopSandbox`: the supervisor container first gets `wslc container stop --time 5` (SIGTERM, 5 seconds) and is removed, then the workload container gets `container stop --time 2`. The workload container is **not** removed
2. On SIGTERM the supervisor SIGKILLs the workload's processes (and as PID 1 may not exit by itself; see `docs/dev/openshell.md`). Nothing passes TERM to the desktop: Xvfb, PulseAudio, D-Bus, Selkies and Firefox are **killed abruptly**. `iwato-desktop run`'s TERM/INT cleanup only works for `iwato-desktop stop` or `kill <pid>`, never on a sandbox stop
3. The driver puts no tmpfs on the workload container (only the supervisor's `/tmp`, `/run`, `/var/log`). The same container starts again, so `/tmp` (including `/tmp/.X11-unix` and `/tmp/.X*-lock`) and `/sandbox` should survive stop → start (the container's writable layer, as with Docker's `stop` → `start`). Volumes survive. `/dev/shm` is new at every start. All processes are gone and pids start small again
4. So after a restart there are the killed desktop's stale locks and sockets and a pidfile pointing at a dead pid. The next `iwato-desktop` (`run`) cleans them up

### Cleanup at the next start (beginning of `iwato-desktop run`)

Only when the owning process is **gone** (live ones are never touched; the process's `/proc/<pid>/comm` name is checked too):

- X: `/tmp/.X<n>-lock` (contains a pid; no process named `X*`) and its `/tmp/.X11-unix/X<n>`; `X<n>` sockets without a lock
- PulseAudio: `pid`, `native`, `dbus-socket` in `$XDG_RUNTIME_DIR/pulse`, `~/.config/pulse/*-runtime`, `/tmp/pulse-*` when their `pid` is dead
- D-Bus: files in `~/.dbus/session-bus/*` whose `DBUS_SESSION_BUS_PID` is dead (`dbus-run-session`'s private bus should be an abstract socket and leave no file)
- Selkies: its own files and sockets in `/tmp/selkies-*`, `/tmp/.selkies-*`, `/tmp/pixelflux-*` (names guessed; it listens on TCP 6080 and needs no file)
- Firefox: the profile's `lock` (when the pid in its `<ip>:+<pid>` target is dead) and `.parentlock`; a lone `.parentlock` when no firefox runs (`~/.mozilla/firefox`, `~/.config/mozilla/firefox`)
- Then the GPU preparation (relinking `~/.local/lib/wsl-nvidia`) as always, or a CPU desktop without a GPU

`run` keeps `selkies-session` as a child (no exec). On TERM/INT it sends TERM to the child and its descendants (`pgrep -P`), waits 5 seconds, KILLs the rest, removes the pidfile and exits 143. A normal exit removes the pidfile too.

### Xvfb fails with "Failed to compile keymap"

Symptom (WSLc, non-root `dev`): `XKB: Failed to compile keymap` → `Failed to activate virtual core keyboard` → `[selkies-session] Xvfb did not come up`. `_XSERVTransmkdir: Owner of /tmp/.X11-unix should be set to root` is an **unrelated warning** (Xvfb created `/tmp/.X11-unix` as `dev`).

Cause (reproduced in an Iwato sandbox under the same OpenShell seccomp filter: `NoNewPrivs: 1`, `Seccomp: 2`, no capabilities):

- To build a keymap, Xvfb starts xkbcomp with `Popen` (`os/utils.c` in xorg-server 21.1.12). Before exec the child calls `setgid(getgid())` and `setuid(getuid())` and `_exit(127)`s if either fails
- OpenShell's seccomp makes `setuid`/`setgid` fail with EPERM **even when the id does not change** (Python's `os.setuid(os.getuid())` gets `Operation not permitted`). The child dies before exec, xkbcomp never runs, and the server says it cannot compile the keymap
- Not the cause: `/var/lib/xkb` is read-only, but the server writes to `/tmp/` when `access(XKM_OUTPUT_DIR, W_OK|X_OK)` fails; the owner of `/tmp/.X11-unix` is only a warning; `-xkbdir` makes no difference. Xvfb has no verbosity option (`-verbose`/`-logverbose` are Xorg's), and ptrace is blocked by seccomp

Fix: `sandboximage/desktop/iwato-noid.c`, a small LD_PRELOAD library `libiwato-noid.so` whose `setuid`/`setgid` return 0 when the id is already the caller's and otherwise call the real ones. The Dockerfile builds it with the image's toolchain (`/usr/local/lib/iwato/libiwato-noid.so`), and `iwato-desktop run` preloads it for the whole session (selkies-session starts Xvfb, so the environment is the reliable way). A missing file only warns. `TestDesktopScriptPreloadsTheSetuidShim` checks the preload and the warning. If Xvfb still fails, look at `iwato-desktop-doctor` (in the image: `id`, directories, whether `setuid` works, `LD_PRELOAD`, a 5-second Xvfb trial, the log's tail).

### Links: "Failed to execute default Web Browser"

- The message is `xfce4-mime-helper`'s (xfce4-settings, behind `exo-open --launch WebBrowser`)
- Xfce's default is `WebBrowser=debian-sensible-browser` (`/etc/xdg/xfce4/helpers.rc`, xfce4-helpers). `sensible-browser` runs `$BROWSER` first (done unless it exits 126/127), then looks for `/usr/bin/x-www-browser`
- The terminal shell exports `BROWSER=/usr/local/bin/iwato-browser` (`etc/shell/envs`), which writes OSC 6464 to the controlling terminal `/dev/tty`. The desktop session has none, so it exits 1, and `iwato-desktop`, started from a terminal, passed that `BROWSER` on
- Fix: `iwato-desktop run` exports `BROWSER=firefox` and `IWATO_DESKTOP_SESSION=1`; `etc/shell/envs` sets `BROWSER=firefox` when `IWATO_DESKTOP_SESSION` is set (the desktop's xfce4-terminal). Iwato's web terminal keeps `iwato-browser` (new tab). The Dockerfile points `x-www-browser` and `gnome-www-browser` at `/usr/bin/firefox`, sets `WebBrowser=firefox` in `/etc/xdg/xfce4/helpers.rc`, and maps http, https, `text/html` and `application/xhtml+xml` to Firefox's desktop file in `/etc/xdg/mimeapps.list` (the build stops if no `/usr/share/applications/firefox*.desktop` exists). A user's `~/.config/xfce4/helpers.rc` or `~/.config/mimeapps.list` wins
- Firefox under OpenShell: the policy's writable paths include `/dev/shm` (`sandboxPolicy` in `openshell.go`; applies to sandboxes created since). Firefox's own sandbox (user namespaces, seccomp, chroot) cannot be built inside Landlock + seccomp, so `iwato-desktop run` sets `MOZ_DISABLE_CONTENT_SANDBOX`, `MOZ_DISABLE_GMP_SANDBOX`, `MOZ_DISABLE_RDD_SANDBOX`, `MOZ_DISABLE_SOCKET_PROCESS_SANDBOX` and `MOZ_DISABLE_UTILITY_SANDBOX` to 1. The isolation boundary is OpenShell's sandbox
- `iwato-desktop-doctor` has a "browser" section (`BROWSER`, `helpers.rc`, `mimeapps`, `xdg-mime query default`, `xfce4-mime-helper --query WebBrowser`, `firefox --version`)

### Open points

Not yet recorded as checked on real hardware:

- Whether `/tmp` and `/sandbox` really survive stop → start on WSLc (if not, the cleanup just finds nothing), and what `wslc container stop` sends to the workload's processes and when it kills them
- Some cleaned-up names are guesses (PulseAudio's `dbus-socket`, Selkies' `selkies-*`/`pixelflux-*`). Look with `ls -la /tmp ~/.config/pulse $XDG_RUNTIME_DIR` while the desktop runs
- Whether Firefox starts cleanly after being killed (session restore dialog etc.)
- Whether reading `/proc` (other processes' `comm` and `cmdline`), `setsid` and `pgrep` work under the supervisor's Landlock. If not, live processes look dead (removing locks is mostly harmless, as Xvfb picks another display), but if `desktop_running` cannot see the desktop, a second start fails binding Selkies' 6080
- With only `/dev/dxg` and no `/dev/dri`, whether Selkies can use NVENC (its encoder selection is written around DRI render nodes, `DRI_NODE`); otherwise it runs x264 on the CPU
- GL through Mesa d3d12 (`glxinfo -B` showing `D3D12 (<GPU>)`), VA-API, and whether `libd3d12`/`libdxcore` are found through the ldconfig hook. Xvfb does not use `-glamor` (no `/dev/dri`), so GLX is Xvfb's software; whether apps' GL runs on the GPU and how fast it is through Xvfb

### Checking on real hardware (Windows + NVIDIA)

1. Build or pull the standard image. Errors starting with `desktop:` are the desktop layer (Mozilla's key, fetching or installing Selkies' .deb)
2. Create a sandbox with **Use the GPU** (standard image). Look at any policy proposals
3. In a terminal: `df -h /dev/shm` (1.0G), `ls -l /dev/dxg`, `echo $GALLIUM_DRIVER` (`d3d12`), `ls -l ~/.local/lib/wsl-nvidia` (after iwato-desktop)
4. Run `iwato-desktop`, check `iwato-desktop status` and `~/.cache/iwato-desktop.log`. Publish 6080 in the **Ports** tab and open it in a browser. Check that `iwato-desktop stop` stops it
5. In the desktop's terminal: the renderer in `glxinfo -B`, Graphics in Firefox's `about:support`, NVENC or x264 in Selkies' stats, and whether `grep -h libcuda /proc/$(pgrep -f 'selkies' | head -1)/maps` points at `/usr/lib/wsl/drivers/…/libcuda.so.1.1`
6. In a sandbox without a GPU: `iwato-desktop` comes up with a CPU desktop, and `df -h /dev/shm` is 1.0G
7. Links: the panel's "Web Browser", a link in xfce4-terminal, `xdg-open https://example.com`. If they fail, see the browser section of `iwato-desktop-doctor`
8. Stop and start the sandbox, run `iwato-desktop` again: no `Xvfb did not come up`, and stale locks are cleaned up
