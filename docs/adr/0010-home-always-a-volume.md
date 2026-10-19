# 0010. The home is always a volume

- Status: Accepted
- Date: 2026-10-09

## Context

Sandboxes have no root and no sudo, so agents and tools are installed into the home: agents with their own installers (Claude Code in `~/.local/share/claude`, `~/.local/bin`), runtimes with mise (`~/.local/share/mise`), with their settings and logins next to them (`~/.claude`). Without a volume these live in the container's writable layer and are lost whenever the sandbox is recreated, for example to take a new image. Before this, only the paths the owner chose at creation were volumes. Also, OpenShell sets HOME to the working directory unless HOME is given at creation (`docs/dev/openshell.md`).

## Decision

Every sandbox gets a volume on `/home/dev`, besides any other persisted paths, and Iwato sets `HOME=/home/dev` at creation and in every exec (`sandboxEnv`). The image's user home is `/home/dev`.

## Consequences

- Agents, their logins and the owner's work survive recreating a sandbox. Provider profiles can name executables under `/home/dev` (`profiles/`), since their paths are stable
- The volume hides the image's own home, so shell defaults come from `/etc/bash.bashrc` and similar system files. The driver sets the volume's owner to the sandbox user at every start (`volumeowner.go`)
- ADR 0005 builds on this: only the home persists, and the working directory moves out of it
