# 0005. Sandbox convention: only the home persists, and the working directory is outside it

- Status: Accepted
- Date: 2026-10-17

## Context

- Everything under the home should persist (agent programs and settings, dotfiles, work). On the other hand, some tools (Claude Code, etc.) warn when you work directly in the home, so work can happen in a subdirectory such as `~/work`
- OpenShell's assumptions (`docs/how-it-works/sandboxes/runtimes.mdx`, `crates/openshell-driver-docker/README.md`):
  - On Docker and Podman, the image's `WORKDIR` is the working directory (`/sandbox` if unset). It lives in the container's writable layer: it survives stop and start, but is lost when the sandbox is deleted
  - Anything that should outlive the sandbox goes in a user-created named volume mounted under the working directory (e.g. `/sandbox/work`). A volume cannot cover WORKDIR itself or its parents (children are fine)
  - So if WORKDIR is `~/work`, the home `/home/dev` becomes a parent of WORKDIR and cannot be a volume
- OpenShell's SFTP is confined to WORKDIR (`RESOLVE_IN_ROOT` in `crates/openshell-sandbox/src/sftp.rs`). Iwato's 「ファイル」 (Files), editor and image paste use this SFTP today
- The current standard image has `WORKDIR /home/dev` with the home as a volume. Only the in-house WSLc driver allows this (ADR 0002)

## Decision

The convention for Iwato's sandboxes and the standard image (and any image that wants all of Iwato's features) is:

- `WORKDIR` is outside the home (`/sandbox`). It is not used as OpenShell's working directory
- The home `/home/dev` is always a volume (as today). Since it is not a parent of WORKDIR, it can persist within upstream's rules
- **Only what is under the home persists**. Choosing "paths to persist" at creation goes away, and whether the home is a volume is not a choice either. Anything outside the home (`/sandbox`, `/usr/local`, etc.) is lost when the sandbox is deleted or recreated
- Work happens in `~/work`. Iwato's terminals start there
- The image carries Iwato's in-sandbox daemon, which serves SFTP in addition to terminals. Iwato starts it as the sandbox user with a TTY-less exec and talks over stdin/stdout (the same as today's terminal attach). It is rooted at the home. 「ファイル」 (Files), the editor and image paste use it
- The image also carries the Policy Advisor guide (`/etc/openshell/skills`) and `/AGENTS.md`
- Since the daemon is no longer only for terminals, today's `iwato-term` is renamed `iwato-entrypoint`: it is the sandbox's first process, and it serves terminals and SFTP

## Consequences

- The home survives sandbox recreation in full, without touching OpenShell's drivers (ADR 0002)
- Images that do not follow the convention still work: without the daemon, terminals use the previous method and files use OpenShell's SFTP (rooted at WORKDIR)
- The image ships the daemon at the same version as Iwato. Updating Iwato can leave the image's daemon older. On connect, the daemon and Iwato negotiate version and capabilities: features the old daemon supports keep working, and new ones show "available after recreating". Compatibility is broken only deliberately. OpenShell pins the hashes of the connecting process's and its parents' executables, so the daemon is not replaced while the sandbox runs (as today)
- OpenShell features rooted at WORKDIR, such as `openshell sandbox upload` and `download`, point somewhere other than Iwato's work area (`~/work`)
- If we propose upstream that writable volumes may be WORKDIR or its parents, `WORKDIR ~/work` becomes possible. We will propose it if needed
- Existing sandboxes (`WORKDIR /home/dev`) get the new convention when recreated. The same home volume is attached
- The creation screen, the settings (`Sandbox.Persist`) and `IWATO_PERSIST` become simpler. Persistence is explained in one sentence, "only what is under the home persists", so agents and users are not confused
- Volumes that existing sandboxes persisted for paths outside the home: Iwato does nothing. Recreating with the new convention does not attach them; the old volumes just remain listed in the Volumes screen, and moving the data is up to the user
- Tools that keep data outside the home (databases using `/var/lib`, etc.) are configured to point under the home. The image's `/AGENTS.md` says so
- Images whose WORKDIR is inside the home (such as the current standard image) are refused by upstream's Docker driver, because the home cannot be a volume. On Windows, the in-house driver keeps running them as today
