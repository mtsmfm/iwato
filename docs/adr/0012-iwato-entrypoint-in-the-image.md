# 0012. iwato-entrypoint in the image is the sandbox's first process, with versions and capabilities

- Status: Accepted
- Date: 2026-10-17

Supersedes 0011.

## Context

ADR 0011 made a daemon in the sandbox, `iwato-term`, own every terminal as OpenShell's first process; the WSLc driver placed its binary at `/usr/local/lib/iwato/iwato-term` on creation and on every start, so a restart brought the daemon of the running Iwato. ADR 0005 moves the daemon into the image (the image convention), renames it `iwato-entrypoint` and has it also serve SFTP rooted at the home. Upstream's Docker driver (ADR 0002) places nothing, and an image keeps its daemon until the sandbox is recreated, so Iwato and the daemon may now differ in version for as long as a sandbox lives.

## Decision

- The daemon is `iwato-entrypoint` (`cmd/iwato-entrypoint`, `internal/termd`, `internal/rootsftp`), carried by the image at `/usr/local/lib/iwato/iwato-entrypoint` and named by the image label `iwato.entrypoint`. It is built from the same commit as Iwato. Sandboxes of a labelled image start `iwato-entrypoint serve` as OpenShell's first process (no TTY); the terminal protocol, the replay and the same-UID trust are ADR 0011's
- `iwato-entrypoint sftp -root /home/dev`, in a TTY-less exec as the sandbox user, serves SFTP confined to the home; Iwato's files, editor and image paste use it
- Versions: the daemon tells its version, capabilities (`terminals`, `sftp`) and path in every list and hello. Iwato uses what the daemon has, falls back for the rest (OpenShell's SFTP) and says the rest is available after recreating. Messages and fields only grow; compatibility is broken only by raising `termd.MinVersion`
- Sandboxes made before (first process `/usr/local/lib/iwato/iwato-term`) keep working: the WSLc driver places the current binary at that path on each of their starts, until they are recreated. The socket directory keeps its old name, so both binaries reach the same daemon

## Consequences

- A newer daemon reaches a sandbox only by recreating it (except the legacy sandboxes above, which still get it on a restart); Iwato shows 「更新」 only for those
- The image build needs the daemon's binaries (`scripts/build-entrypoint.sh` in the image workflows; a local build uses the one the exe embeds)
- Images without the label run as before the daemon: the main terminal is the first process, extra terminals end with Iwato, and files use OpenShell's SFTP
