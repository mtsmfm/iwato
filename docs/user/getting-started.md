# Getting started

[日本語](ja/getting-started.md)

## Requirements

- Windows 11 with WSL 3.0.1 or later, which provides WSLc (`wslc.exe`). Update with `wsl --update` (`wsl --update --pre-release` if the stable release is older) and check with `wslc.exe --version`
- An x64 or Arm64 PC. Iwato needs nothing else: no Docker Desktop, no account
- Or (experimental) an Apple Silicon Mac or Linux with a Docker-compatible engine: see [macOS](#macos-experimental)

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

## Command line

```
iwato                 create iwato.json if missing, then serve
iwato serve [-config iwato.json] [-listen 127.0.0.1:PORT] [-public-origin https://HOST.TAILNET.ts.net] [-session-hours 8] [-verbose]
iwato version
iwato help
```

Stop Iwato with Ctrl+C. Stopping or updating Iwato does not stop the sandboxes: with the standard image their terminals keep running too, and Iwato reattaches to them when it starts again ([terminals](terminals.md)).

## macOS (experimental)

On macOS Iwato runs the sandboxes on a Docker-compatible engine with OpenShell's own Docker compute driver ([ADR 0002](../adr/0002-macos-docker-compatible-runtime.md)). Windows keeps WSLc. Linux has no download: build Iwato from source ([building](../dev/development.md#building-on-linux)) and use Docker Engine; the rest is as on macOS.

This path has not yet been tried on a real Mac: the Linux CI runs it on Docker (`TestDockerE2E` in `.github/workflows/e2e.yml`) and tests the macOS build on a Mac runner (`.github/workflows/macos.yml`). Please report what does not work.

### Requirements

- An Apple Silicon Mac (the build is `darwin-arm64`)
- A Docker-compatible engine with Docker Engine 28.0 or later (OpenShell's requirement). Colima (free, MIT) is recommended; Docker Desktop and OrbStack work too, but may be paid for commercial use

### Set up Colima

```sh
brew install colima docker
colima start --cpu 4 --memory 8
```

`colima start` makes the `colima` Docker context current, which Iwato uses (Iwato runs the `docker` CLI, so `DOCKER_HOST`, `DOCKER_CONTEXT` and `docker context use` choose the engine).

Sandboxes run in Colima's Linux VM. The CPUs and memory given to the VM stay with it while sandboxes are stopped; to change them, `colima stop`, then `colima start --cpu N --memory GiB`.

Docker Desktop: enable host networking (Settings → Resources → Network → Enable host networking; OpenShell's supervisor uses it) and keep Enhanced Container Isolation off.

### Run Iwato

Download `iwato-darwin-arm64` from the pre-release [edge](https://github.com/mtsmfm/iwato/releases/tag/edge) and save it as `iwato` in a folder of its own (the config folder, as on Windows). Run it from there:

```sh
chmod +x iwato
xattr -d com.apple.quarantine iwato
./iwato
```

`chmod +x` makes the download executable. The binary is not signed, so `xattr` removes the quarantine that makes Gatekeeper block it.

`./iwato` checks the engine first: whether `docker` is there and the engine runs, its version, the VM's architecture, CPUs and memory, and the Docker Desktop settings above. The results are in the settings' **Startup checks**; when the engine is not running, the page says how to start it and Iwato goes on once it runs. Then it is as on Windows (above): open the printed URL, create a sandbox, start it.

### Differences from Windows

- Only images that follow the sandbox convention ([ADR 0005](../adr/0005-sandbox-image-convention.md), such as the standard image) can keep their home: OpenShell's Docker driver refuses a volume on the working directory or its parents, so an image whose `WORKDIR` is the home does not start
- No GPU: there is no way to pass the Mac's GPU to a container
- Secrets (the ingress secret) are encrypted with a key that the login Keychain keeps (item `iwato` / `secret-file-key`). On Linux they are plain files (mode 0600)
- `iwato.json` has no `wslc`. `docker` sets the CLI's path (default `docker` on `PATH`); `dockerSocket` sets where the engine's API socket is inside the engine (default `/var/run/docker.sock`, which Colima, Docker Desktop and OrbStack use in their VM; with rootless Docker on Linux, the CLI's `unix://` endpoint)

## Next

- [Sandboxes and images](sandboxes.md)
- [Network policy and approvals](network-policy.md)
- [Providers (credentials)](providers.md)
- [All user docs](README.md)
