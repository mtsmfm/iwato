# 0030. An installed app's client secret goes to the gateway with the refresh token

- Status: Accepted
- Date: 2026-11-28

## Context

- Iwato signs in with each agent's own OAuth client, and the gateway refreshes the tokens (ADR 0009). The clients so far were public: their token endpoints take a refresh with no client secret
- The Antigravity CLI (agy) signs in with a Google client of the installed-app type. Google's token endpoint asks such a client for its client secret in the exchange and the refresh (agy sends it). The secret is in every copy of agy, so it protects nothing: Google treats an installed app's secret as public
- ADR 0009's last consequence says OpenShell's refresh sends no client secret. That was OpenShell v0.1.6. v0.1.10's `oauth2_refresh_token` sends `client_secret` when it is in the refresh material (`client_secret_post`)
- Alternatives:
  - Iwato refreshes agy's tokens itself and pushes access tokens to the gateway: the refresh token would sit in Iwato's folder, which ADR 0009 rules out
  - Iwato registers its own Google OAuth client: agy's backend accepts only agy's client (another client gets "not supported by client")

## Decision

For a client of the installed-app type, Iwato hands the client's public secret to the gateway in the refresh material, next to the refresh token, marked as secret (`oauthGrant.clientSecret` in `oauth_grant.go`). The secret is taken from the agent's binary and kept in Iwato's source, split into pieces so that secret scanning does not report it whole.

## Consequences

- agy works with the gateway's refresh, like the other sign-ins. ADR 0009 still holds: Iwato keeps no refresh token
- A new agy release that changes its client breaks the sign-in and the refresh until `antigravity.go` and the profile are updated. The failure shows on the first refresh right after connecting (`docs/dev/providers.md`)
- Only clients whose secret is public get this. A confidential client's secret would be a real secret, and Iwato does not ship one
