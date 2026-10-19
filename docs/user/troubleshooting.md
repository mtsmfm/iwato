# Troubleshooting

[日本語](ja/troubleshooting.md)

Start with the settings' Startup checks (WSLc or the engine, and the configuration, each with how to fix it) and the log: **Log** in the sidebar, or `iwato.log` in the config folder. `serve -verbose` prints the whole log on the console.

## Iwato does not start

- **WSLc cannot run** (the page shows it and waits): install WSL 3.0.1 or later on Windows 11 and check that `wslc.exe --version` works. If `wslc.exe` is not on `PATH`, put its full path in `iwato.json` as `"wslc"`
- **The port is taken**: Iwato tries 17600 to 17669. Pick one with `-listen 127.0.0.1:PORT`
- **Preparing the images or the gateway failed**: the console and the log say why; image pulls and builds are in `image-build.log`. Check the network to ghcr.io and start Iwato again

## Signing in

- **The URL was already used or expired**: the start URL works once, within 10 minutes. From a signed-in browser, use Sign in on another device; otherwise restart Iwato for a new URL
- **Signed out after a while**: sessions last 8 hours by default (`-session-hours`, up to 720)

## Network inside the sandbox

- **A command fails with 403, `policy_denied`, a proxy error or a reset connection**: the policy refused it. Look at the **Policy** tab: approve the proposal, or add a rule. A refused method or path on an inspected endpoint does not become a proposal; the approved list shows such blocked requests under the rule
- **Approving fails with an ambiguity error**: OpenShell refuses rules on the same host, port and path with different inspection settings. The dialog says which rule conflicts and how to fix it
- **A rebuilt program is refused with "binary integrity"**: OpenShell pins each program's hash the first time it connects; a program changed at the same path is refused until the sandbox restarts. Build each version to a new path (the sandbox's `/AGENTS.md` explains this to agents)

## Providers

- **The variable is missing or still old**: providers attached or detached on a running sandbox apply after a restart (the sandbox shows **Restart pending**). Programs started before see no change
- **Atlassian or xAI says the authorization expired**: use Reconnect in the providers view, then restart the attached sandboxes

## Terminals and files

- **A terminal does not come back after updating Iwato**: sandboxes made from an image without Iwato's daemon end extra terminals when Iwato exits. Recreate the sandbox with the standard image ([sandboxes](sandboxes.md#recreate))
- **The files or editor show `/sandbox` instead of the home ("Opens the working directory (/sandbox).")**: the sandbox was made from an image without Iwato's daemon, or one whose daemon is older than Iwato; recreate it with the standard image
- **A broken terminal screen**: turn on terminal recordings and replay them ([terminals](terminals.md#terminal-recordings))
- **Windows cannot copy a large file to the drive**: Windows' WebDAV client stops at about 48 MB by default

## Published ports

- **A tool cannot resolve `5173.dev.localhost`**: browsers resolve `*.localhost` themselves, other programs may not; use `curl --resolve 5173.dev.localhost:17600:127.0.0.1 …`
- **502**: nothing listens on that port in the sandbox (or it listens only on a specific address other than loopback or all addresses)
- **503**: the sandbox is stopped, or too many connections are open at once
- **Vite says "Blocked request. This host is not allowed"**: should not happen with the default `host` setting; check the port's `host` in `iwato.json` ([per-port settings](publishing.md#per-port-settings))
- **Tailscale node missing or a duplicate (`-1`)**: see the log for `logout:` lines and remove stale nodes in Tailscale's admin console

## Start over

Delete everything in the settings deletes everything Iwato made and starts as on a first run ([settings](settings.md#delete-everything)).

## Reporting a problem

Open an issue at <https://github.com/mtsmfm/iwato/issues> with the settings' Startup checks, the output of `iwato version` and the relevant part of `iwato.log`. The log contains sandbox names, hosts and paths: read it before posting.
