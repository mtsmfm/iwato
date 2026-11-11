# 0026. Read the subscriptions' usage inside a sandbox

- Status: Accepted
- Date: 2026-11-11

## Context

- The owner wants Claude's and Codex's subscription limits (the 5-hour and weekly windows) in view, as Omarchy's status bar widgets show them
- Both services answer with them for the sign-in token: Claude at `api.anthropic.com/api/oauth/usage` (Claude Code's `/usage`), Codex at `chatgpt.com/backend-api/wham/usage`. Neither is documented; community tools read them
- The tokens are the providers' credentials, which the gateway holds and refreshes ([ADR 0009](0009-refresh-tokens-stay-in-the-gateway.md)). A sandbox the provider is attached to has a placeholder; its proxy puts the credential in for the provider's endpoints, which already cover both paths
- Alternatives: Iwato reads the access token from the gateway (whether its API returns it is unverified, and Iwato would hold a token); the daemon in the sandbox reads it (needs a new daemon, so a recreate); the agents' own responses carry rate-limit headers (nothing passes them to Iwato)

## Decision

- Iwato reads each service with one exec of `curl` in a running sandbox the provider is attached to, the token being the placeholder; every 5 minutes while a page is in use and every 30 otherwise
- The answers become windows (kind, percent used, reset time), kept with when and where they were read; a failed read keeps the last windows and says why
- The sidebar shows them under the totals, and on Windows the tray's tooltip; Grok is left out until its usage can be read

## Consequences

- Iwato still holds no token; the request goes out under the sandbox's policy like an agent's
- Without a running sandbox that has the provider, the reading stays as it was (shown as older); a provider attached while the sandbox ran reaches it only after a restart
- The endpoints may change without notice; a changed answer shows as "could not read" until Iwato follows it
