# Coding agents in a sandbox

[日本語](ja/agents.md)

The standard image contains no agent CLI and no agent settings. Install each agent inside the sandbox with mise, as the sandbox user. Everything lands in the home, so it survives recreating the sandbox ([sandboxes](sandboxes.md#only-the-home-persists)).

## Network for installing

A new sandbox allows no network. To avoid approving each download, pick a preset at creation, or later in the policy tab ([presets](network-policy.md#presets)):

- Install Claude Code with mise
- Install Grok Build with mise

They add narrow rules (program, method, path) for the download hosts, which carry no credentials, including the agents' own updates. The agents' API hosts (`api.anthropic.com`, `api.x.ai`, …) are not in the presets: attach the agent's provider, or approve the agent's proposals ([providers](providers.md)). Anything else an agent needs, it proposes ([proposals](network-policy.md#how-rules-get-proposed)).

## Claude Code

```sh
mise use -g claude
```

It installs into `~/.local/share/mise/installs/claude/<version>`; Claude Code's own updates go into `~/.local/share/claude/versions/<version>`. For the API (`api.anthropic.com:443`), attach a provider of type Claude Code (setup token) (a token from `claude setup-token`) or approve the proposal.

## Grok Build

```sh
mise use -g grok
```

It installs into `~/.local/share/mise/installs/grok/<version>`; grok's own updates go into `~/.grok/downloads`. Use the provider Grok Build (xAI subscription) (sign in with a personal xAI account such as SuperGrok). See [providers](providers.md#xai-subscription-for-grok-build).

## Codex and other agents

Iwato has no provider for Codex yet: approve its proposals. Other agents work the same way.

## The sandbox's instructions for agents (`/AGENTS.md`)

The standard image has `/AGENTS.md`, which explains the environment to agents: how a blocked connection looks and how to propose a rule, what persists, that credentials are placeholders, git over HTTPS, the desktop. Neither Claude Code nor Codex reads `/AGENTS.md` by itself, so on every start of a sandbox Iwato places, **only when absent**:

- Claude Code: `~/.claude/CLAUDE.md`, one line `@/AGENTS.md` that includes it
- Codex: `~/.codex/AGENTS.md`, a symbolic link to `/AGENTS.md`

An existing file (whatever its content, even a broken symbolic link) is left alone. Iwato does nothing when the home is missing, not owned by the sandbox user or a symbolic link, when `~/.claude` or `~/.codex` is a symbolic link or a file, or when the image has no `/AGENTS.md`. To opt out, create your own `~/.claude/CLAUDE.md` or `~/.codex/AGENTS.md` (an empty one is enough; the home persists). To keep the instructions in your own file, add the line `@/AGENTS.md`. For other agents, point their global instructions file at `/AGENTS.md`.
