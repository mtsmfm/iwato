# 0011. iwato-term is the sandbox's first process and keeps the terminals

- Status: Superseded by 0012
- Date: 2026-10-17

## Context

The main terminal used to be the sandbox's first process with a TTY, and extra terminals were TTY execs, each held by Iwato's connection through the gateway. Restarting or updating Iwato ended them, and the main terminal and the extra ones behaved differently.

## Decision

A daemon in the sandbox, `iwato-term` (`cmd/iwato-term`, `internal/termd`), owns the PTY and process of every terminal, main included, and keeps each terminal's last 1 MiB of output. New sandboxes start `iwato-term serve` as OpenShell's first process (no TTY). Iwato reaches it with a TTY-less exec of `iwato-term attach` and speaks framed messages over stdin/stdout (`internal/openshell/client/held.go`); after a reconnect the output is replayed and the screen repainted. Only the same UID in the sandbox may connect (a private socket directory and a peer UID check).

## Consequences

- Terminals outlive Iwato; Iwato reattaches to them when it starts. Sandboxes made before the daemon keep the old method
- OpenShell pins the hashes of a connecting process's and its parents' executables, so the daemon cannot be replaced while the sandbox runs; a newer one arrives with a restart (Iwato shows 「更新」 when the daemon is older)
- `PR_SET_DUMPABLE` is not set: OpenShell could then no longer follow process ancestry in `/proc` and would deny the network of every process under the daemon. The same-UID trust relies on OpenShell's seccomp blocking ptrace and memory reads
- The WSLc driver places the binary today. ADR 0005 moves it into the image and renames it `iwato-entrypoint`, as it will also serve SFTP
