# 0023. Serve the admin UI on its own tailnet node

- Status: Accepted
- Date: 2026-11-10

## Context

- The admin UI listens on `127.0.0.1` only. To use it from a phone, the owner installs Tailscale on the PC, runs `tailscale serve --bg http://127.0.0.1:17600` and starts Iwato with `-public-origin https://HOST.TAILNET.ts.net` ([ADR 0003](0003-tailscale-optional.md), `docs/user/publishing.md`). A double-click passes no flag, and `tailscale serve` is a step outside Iwato that the owner has to know about
- `tailscale serve` without Funnel lets every device of the tailnet reach the admin UI, the owner's tagged servers and devices shared from other tailnets included; only Iwato's sign-in stands in their way
- Iwato already runs tsnet nodes in its own process for the sandboxes' ports when the owner picks Tailscale in the settings (`internal/ingress/tailscale`): one tagged node per sandbox, made with the OAuth client the settings hold, certificates from the node, and callers identified with WhoIs and admitted only from the untagged, unshared devices of the owner logins (`ingress.ProviderAuth`)
- Alternatives:
  - Iwato runs `tailscale serve` on the PC's Tailscale client: needs the client on the PC, rewrites the owner's own Tailscale settings, and keeps the whole tailnet's reach
  - Keep the manual steps and only explain them better: the step stays outside Iwato

## Decision

- When the settings publish with Tailscale, Iwato also brings up one node for the admin UI, named after the nodes' prefix (`iwato` by default), with the same tag and OAuth client as the sandboxes' nodes and its own state folder
- The node serves the admin UI over HTTPS on 443 with its certificate. It admits only the devices `ProviderAuth` admits for the ports (untagged, unshared, of the owner logins); everyone else is refused before the admin UI. The admin UI's own sign-in, cookie and CSRF checks stay as they are
- Its address becomes the admin UI's public origin (as `-public-origin` is today: the Host check, pairing links, cookies, Web Push). `-public-origin` given on the command line keeps working and wins, for those who keep `tailscale serve`
- Choosing This PC only or Off takes the node down with the sandboxes' nodes

## Consequences

- Phone access needs only the settings' Tailscale setup (the OAuth client, the tag and the owner logins), no Tailscale client on the PC, no command and no flag
- The admin UI is reachable from fewer devices than through `tailscale serve`: the owner logins' own devices only
- The PC's browser reaches the node only when the PC itself is on the tailnet; otherwise the PC keeps `127.0.0.1` (the page is opened on the tailnet address only when it answers), and the PC and the phone use different origins
- Moving from `tailscale serve` changes the address: the phone signs in, installs the app and turns on notifications again on the new origin, and the owner can remove the old `tailscale serve`
- The owner logins must be set before a phone can connect; the settings say so while the list is empty
- The admin UI now has a listener outside loopback inside Iwato's process. A mistake in the node's admission check exposes the sign-in page to the tailnet (the sign-in still holds), so the check is shared with the ports' and tested on its own
