# 0016. No helper commands in the image for agents; Iwato writes what they read

- Status: Proposed
- Date: 2026-10-20

## Context

Agents in a sandbox sometimes need facts only Iwato knows. The first case is the URLs of the ports they serve: the sandbox's name in Iwato, Iwato's port and the Tailscale node's name, which change with the settings and when the node joins. A helper command in the image (for example one that prints a port's URL) would have to be shipped, versioned and kept compatible with older images, and agents would still have to find it and think of running it. Agents already read `/AGENTS.md` at the start of every session (ADR 0005), and some tools read environment variables.

## Decision

- Iwato adds no new helper commands or binaries to the sandbox image for agents to run
- What agents need to know goes into what they already read: the generated `/AGENTS.md`, and environment variables where a tool reads them (`~/.config/iwato/env`)
- In the standard image `/AGENTS.md` links to `~/.config/iwato/AGENTS.md`. Iwato's daemon, `iwato-entrypoint`, writes it at start from the image's text (`/etc/iwato/AGENTS.md`), keeping the section Iwato wrote last; Iwato rewrites the file through an exec whenever that section changes. The first section is "Your URLs"

## Consequences

- An agent gives the owner a working URL without running anything. Agents read the file when a session starts, so a session that started before a change sees the old URLs; the section says so
- A new capability for agents first asks whether `/AGENTS.md` or the environment can carry it
- The helpers already in the image stay, as they are not looked up by agents: `iwato-browser` is `BROWSER` for programs that open links, and `iwato-desktop` and `iwato-desktop-doctor` are for starting and diagnosing the desktop
- Images made before keep a plain `/AGENTS.md` and get no URLs; recreating the sandbox with a newer image gives them. A home the daemon cannot write leaves `/AGENTS.md` dangling
