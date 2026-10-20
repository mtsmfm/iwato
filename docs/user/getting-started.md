# Getting started

[日本語](ja/getting-started.md)

## Requirements

- Windows 11 with WSL 3.0.1 or later, which provides WSLc (`wslc.exe`). Update with `wsl --update` (`wsl --update --pre-release` if the stable release is older) and check with `wslc.exe --version`
- An x64 or Arm64 PC. Iwato needs nothing else: no Docker Desktop, no account
- Or (experimental) an Apple Silicon Mac, or x64 or Arm64 Linux, with a Docker-compatible engine: see [macOS and Linux](#macos-and-linux-experimental)

## Install

Download `iwato-windows-amd64.exe` (or `iwato-windows-arm64.exe`) from [Releases](https://github.com/mtsmfm/iwato/releases) (until the first release, from the pre-release [edge](https://github.com/mtsmfm/iwato/releases/tag/edge), rebuilt from `main` on every change) and save it as `iwato.exe` in a folder of its own. To build it yourself instead, see [development](../dev/development.md).

The exe is not signed, so Windows SmartScreen may warn when you run a downloaded copy for the first time ("More info" → "Run anyway").

The folder you start Iwato in becomes its **config folder**: Iwato keeps `iwato.json`, its log, its secrets and your own presets and provider profiles there. Start it from the same folder every time (or pass `-config PATH\iwato.json`).

## First run

```powershell
.\iwato.exe
```

Iwato then:

1. Creates `iwato.json` if there is none (with no sandboxes)
2. Runs its startup checks (WSLc and the configuration). When WSLc cannot be used, the page says why and how to fix it, and Iwato goes on by itself once it can (**Check again** checks at once). The settings list all the checks
3. Pulls the sandbox and gateway images from ghcr.io (nothing is built). This takes a few minutes the first time; the output goes to `image-build.log`
4. Starts the OpenShell gateway in WSLc

The console prints a URL such as `http://127.0.0.1:17600/#token=...`. Open it in your browser. It works once, within 10 minutes; do not share it. When port 17600 is taken Iwato uses the next free one (up to 17669); `-listen 127.0.0.1:PORT` fixes the port.

The page opens while the images and the gateway are still being prepared and shows the progress. When preparation ends (or fails, with the reason), the console prints the sign-in URL again: the same one if it was not used yet, a new one if it expired, and none if it was already used.

Then:

1. Create a sandbox with **Create a sandbox** in the sidebar: a name, and an image (leave it empty for the standard image). See [sandboxes](sandboxes.md)
2. Start it with **Start** and open its terminal
3. Install an agent inside it: see [agents](agents.md)

## Signing in and other devices

- A browser that has signed in stays signed in for the session lifetime (8 hours by default; `-session-hours 1`–`720`), also across restarts of Iwato
- To sign in another browser or device, use Sign in on another device in the sidebar: it shows a one-time link and a QR code, valid for 10 minutes. On the other device, open the link or paste it into "Sign in"
- Using Iwato from a phone needs Tailscale: see [phone access](publishing.md#use-iwato-from-your-phone)
- Turn on push notifications with **Turn on notifications** in the sidebar; see [terminals](terminals.md#notifications)
- The page can be installed as an app (PWA) from the browser's menu

## The console and the log

The console shows only the startup lines, the sign-in URL, a one-line notice when a startup check fails (the page has the details), and warnings and errors that need you (a failed preparation, a stopped driver, port problems). Everything else (the gateway and driver output, image builds, requests) goes to `iwato.log` in the config folder (moved to `iwato.log.1` at 10 MB) and to **Log** in the sidebar. `.\iwato.exe serve -verbose` also prints it on the console.

## Updating Iwato

The exes and binaries from GitHub Releases check for a new build when they start and every 6 hours: the edge pre-release for a new commit, a release for a newer release. When there is one, **Update** shows in the sidebar and in **Settings**. Iwato then downloads its new program from the same release, checks it against the release's SHA-256, replaces itself and restarts on the same URL; the page reconnects by itself and you stay signed in.

Sandboxes and their terminals keep running, unless the update brings a new gateway: then OpenShell restarts the running sandboxes, which ends their processes and terminals (files are kept). The confirmation lists them first. Builds you made yourself do not check.

## Command line

```
iwato                 create iwato.json if missing, then serve
iwato serve [-config iwato.json] [-listen 127.0.0.1:PORT] [-public-origin https://HOST.TAILNET.ts.net] [-session-hours 8] [-verbose]
iwato version
iwato help
```

Stop Iwato with Ctrl+C. Stopping or updating Iwato does not stop the sandboxes: with the standard image their terminals keep running too, and Iwato reattaches to them when it starts again ([terminals](terminals.md)).

## macOS and Linux (experimental)

On macOS and Linux Iwato runs the sandboxes on a Docker-compatible engine with OpenShell's own Docker compute driver ([ADR 0002](../adr/0002-macos-docker-compatible-runtime.md)). Windows keeps WSLc.

This path has not yet been tried on a real Mac: the Linux CI runs it on Docker (`TestDockerE2E` in `.github/workflows/e2e.yml`) and tests the macOS build on a Mac runner (`.github/workflows/macos.yml`). Please report what does not work.

### Requirements

- macOS: an Apple Silicon Mac (the build is `darwin-arm64`)
- Linux: an x64 or Arm64 machine (`linux-amd64`, `linux-arm64`)
- A Docker-compatible engine with Docker Engine 28.0 or later (OpenShell's requirement). On macOS Colima (free, MIT) is recommended; Docker Desktop and OrbStack work too, but may be paid for commercial use. On Linux, [Docker Engine](https://docs.docker.com/engine/install/), rootful or rootless

Iwato runs the `docker` CLI, so `DOCKER_HOST`, `DOCKER_CONTEXT` and `docker context use` choose the engine.

### Set up Colima (macOS)

```sh
brew install colima docker
colima start --cpu 4 --memory 8
```

`colima start` makes the `colima` Docker context current, which Iwato uses.

Sandboxes run in Colima's Linux VM. The CPUs and memory given to the VM stay with it while sandboxes are stopped; to change them, `colima stop`, then `colima start --cpu N --memory GiB`.

Docker Desktop: enable host networking (Settings → Resources → Network → Enable host networking; OpenShell's supervisor uses it) and keep Enhanced Container Isolation off.

### Set up Docker Engine (Linux)

Install it, start it (`sudo systemctl enable --now docker`) and let your user run `docker` without sudo (`sudo usermod -aG docker $USER`, then log in again), or use rootless Docker.

### Run Iwato

Download `iwato-darwin-arm64`, `iwato-linux-amd64` or `iwato-linux-arm64` from the pre-release [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) and save it as `iwato` in a folder of its own (the config folder, as on Windows). Run it from there:

```sh
chmod +x iwato
xattr -d com.apple.quarantine iwato   # macOS only
./iwato
```

`chmod +x` makes the download executable. The binary is not signed, so on macOS `xattr` removes the quarantine that makes Gatekeeper block it.

`./iwato` checks the engine first: whether `docker` is there and the engine runs, its version, the engine's architecture, CPUs and memory, and the Docker Desktop settings above. The results are in the settings' **Startup checks**; when the engine is not running, the page says how to start it and Iwato goes on once it runs. Then it is as on Windows (above): open the printed URL, create a sandbox, start it. Iwato does not open the browser itself.

### Differences from Windows

- Only images that follow the sandbox convention ([ADR 0005](../adr/0005-sandbox-image-convention.md), such as the standard image) can keep their home: OpenShell's Docker driver refuses a volume on the working directory or its parents, so an image whose `WORKDIR` is the home does not start
- No GPU on macOS: there is no way to pass the Mac's GPU to a container
- Secrets (the ingress secret) are encrypted on macOS with a key that the login Keychain keeps (item `iwato` / `secret-file-key`). On Linux they are plain files (mode 0600) in the config folder
- `iwato.json` has no `wslc`. `docker` sets the CLI's path (default `docker` on `PATH`); `dockerSocket` sets where the engine's API socket is inside the engine (default `/var/run/docker.sock`, which rootful Docker Engine uses, as do Colima, Docker Desktop and OrbStack in their VM; with rootless Docker on Linux, the CLI's `unix://` endpoint)

## Next

- [Sandboxes and images](sandboxes.md)
- [Network policy and approvals](network-policy.md)
- [Providers (credentials)](providers.md)
- [All user docs](README.md)
