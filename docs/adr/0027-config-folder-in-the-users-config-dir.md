# 0027. The config folder is in the user's config directory

- Status: Accepted
- Date: 2026-11-18

## Context

- Without `-config`, `iwato.json` was in the current directory, so the config folder was the folder Iwato was started in. A double-click on Windows makes that the exe's folder, but on macOS and Linux starting `iwato` through `PATH` from another folder made a separate Iwato, and the exe had to sit in a folder of its own
- OpenShell (`openshell-core`'s `paths.rs`) keeps its files under the XDG directories on every OS, macOS included (`$XDG_CONFIG_HOME` or `~/.config`, `$XDG_STATE_HOME` or `~/.local/state`), and on Windows under `%APPDATA%` (config) and `%LOCALAPPDATA%` (state, data)
- A folder next to the exe (`iwato-data/`) was considered: easy to see, but it fails when the exe is somewhere not writable or shared (`/usr/local/bin`, Program Files, a package manager's directory) and for `go run`

## Decision

- Without `-config`, Iwato uses `iwato.json` in the folder it starts in when one is there (what existing setups have), else `iwato/iwato.json` in the user's config directory: `%LOCALAPPDATA%` on Windows, `$XDG_CONFIG_HOME` (when absolute) or `~/.config` on macOS and Linux
- Windows uses `%LOCALAPPDATA%`, not OpenShell's roaming `%APPDATA%`: `iwato.json` lists this PC's sandboxes, and the folder holds its certificates and logs
- The config folder stays one folder (configuration, logs, secrets), as before; it is not split into config and state
- Existing files are not moved: the gateway's certificates and the sandboxes' state are in that folder

## Consequences

- The exe can be anywhere and started from anywhere; one Iwato per user unless `-config` says otherwise
- The folder is out of sight: the settings' Startup checks show the path of `iwato.json`, and the log opens from the page and the tray
- Setups that kept `iwato.json` next to the exe keep working when started there (a double-click, or the autostart, which passes `-config`)
