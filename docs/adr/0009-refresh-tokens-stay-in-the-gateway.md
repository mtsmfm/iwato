# 0009. OAuth refresh tokens are kept by the gateway, not by Iwato

- Status: Accepted
- Date: 2026-10-08

## Context

Some providers authenticate with OAuth: the Atlassian MCP (`atlassian*.go`) and later the xAI subscription for Grok Build (`xai.go`). Access tokens expire within about an hour and need a refresh token to renew. Someone has to hold the refresh token and refresh on time:

- Iwato could keep it in its config folder and push new access tokens to the gateway. Then Iwato's folder holds a long-lived secret, and refreshing depends on Iwato's own timers
- OpenShell's gateway can refresh provider credentials itself (`oauth2_refresh_token` in a provider profile, `ConfigureRefresh`), storing them encrypted on its state volume, while sandboxes only see a placeholder that its proxy replaces in flight

## Decision

Iwato.exe runs the authorization (OAuth with PKCE and a 127.0.0.1 redirect or a pasted URL for Atlassian, a device code login for xAI), creates the provider, hands the refresh token to the gateway (`oauth_grant.go`) and forgets it. The gateway refreshes, stores rotated refresh tokens, and sandboxes get only the placeholder.

## Consequences

- Iwato's config folder holds no refresh tokens, and only one party rotates them (two holders of a rotating refresh token would invalidate each other)
- Placeholders do not change on refresh, so running agents keep working. Reconnecting creates a new grant and a new placeholder, so attached sandboxes need a restart
- Iwato is limited to what OpenShell's refresh sends (`grant_type`, `client_id`, `refresh_token`: no client secret, scope or `resource`). A provider that needs more cannot be refreshed until OpenShell supports it; Iwato checks the first refresh right after connecting and shows the failure (`docs/dev/providers.md`)
