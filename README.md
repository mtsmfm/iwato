# Iwato

[日本語](README.ja.md)

<img src="web/icon.svg" width="96" alt="">

> [!WARNING]
> Iwato is under active development. Expect breaking changes (settings, sandboxes, images and the API may change without a migration path) until the first stable release.

**Iwato** (岩戸) runs AI coding agents (Claude Code, Codex, Grok Build, …) in [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) sandboxes on your Windows PC, from a single exe, and lets you use them from a browser, including your phone.

- **Sandboxes on WSLc**: Iwato runs OpenShell's gateway in WSLc, with its own WSLc compute driver. Nothing else to install: no Docker Desktop, no account
- **Network by approval**: a sandbox's network is denied by default. Agents propose the rules they need through OpenShell's Policy Advisor, and you approve or reject them in the page, with push notifications
- **Credentials stay outside**: tokens live in OpenShell providers; the sandbox sees only placeholders, and each provider has a scope (for example which GitHub repositories may be written)
- **Terminals that keep running**: Iwato keeps the screens, and a daemon in the sandbox keeps the processes, so terminals survive closing the browser and restarting Iwato
- **Your home persists**: `/home/dev` is a volume that survives recreating the sandbox
- **Files, editor, ports, desktop**: the home as a network drive, an editor tab, dev servers at `http://<port>.<sandbox>.localhost:17600/`, and a desktop in the browser

## Requirements

- Windows 11 with WSL 3.0.1 or later (`wslc.exe --version`)
- Or (experimental) an Apple Silicon Mac, or Linux, with a Docker-compatible engine: [Colima](https://github.com/abiosoft/colima) is recommended, Docker Desktop and OrbStack work too

## Install and first run

Download `iwato-windows-amd64.exe` (or `-arm64`) from [Releases](https://github.com/mtsmfm/iwato/releases) (until the first release, from the pre-release [edge](https://github.com/mtsmfm/iwato/releases/tag/edge), rebuilt from `main` on every change), save it as `iwato.exe` in a folder of its own, and run it there:

```powershell
.\iwato.exe
```

Open the printed `http://127.0.0.1:17600/#token=...` (once, within 10 minutes). The first start pulls the images and starts the gateway, which takes a few minutes; if WSLc or the engine cannot be used, the page says why and how to fix it. Then create a sandbox, start it and open its terminal.

On macOS (experimental, not yet tried on a real Mac):

```sh
brew install colima docker docker-buildx
colima start --cpu 4 --memory 8
./iwato
```

Details: [getting started](docs/user/getting-started.md) (macOS: [its section](docs/user/getting-started.md#macos-experimental)).

## Documentation

- User guide: [docs/user](docs/user/README.md) — sandboxes, network policy, providers, terminals, files and editor, publishing ports, desktop and GPU, settings, troubleshooting
- Development: [docs/dev/development.md](docs/dev/development.md), [architecture](docs/dev/architecture.md), [design decisions (ADRs)](docs/adr/README.md)

## License

See [LICENSE](LICENSE) and [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
