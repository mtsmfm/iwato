# 0028. This PC's addresses stay next to Tailscale, until the owner moves this PC to the tailnet's

- Status: Accepted
- Date: 2026-11-23

## Context

- Port publishing has one provider at a time (ADR 0003): this PC only (`*.localhost`), Tailscale, or off. Choosing Tailscale stopped `http://PORT.SANDBOX.localhost:PORT/`, so the PC running Iwato could open the sandboxes' ports only through the tailnet, which needs the Tailscale app on that PC too. Iwato itself joins the tailnet without it (tsnet), so a first-time user did not expect to need it
- The page on this PC (`http://127.0.0.1:PORT/`) and the page's tailnet node (ADR 0023) are separate origins: sign-ins, the installed app and notifications are each per origin
- `*.localhost` has no sign-in: any browser or program on this PC can open the ports. The tailnet's addresses admit only the owners' devices
- Alternatives:
  - Keep one provider and document that this PC needs Tailscale: the onboarding problem stays
  - A separate "this PC too" switch: two settings for one choice, and its default would have to be on to avoid the problem

## Decision

- With Tailscale, this PC's `*.localhost` publishing runs next to it by default. The Ports tab shows both addresses
- The owner can move this PC to the tailnet's address too (`tailnetOnly`, in the settings and at the end of the Tailscale guide): then `*.localhost` stops, and a browser opening the page on this PC's address is redirected to the page's node. Only navigations are redirected; the API, WebSockets and WebDAV keep answering on this PC's address, as Iwato itself (the second start's hand-over, the tray) and Windows' drives use them. While the node is down the page stays on this PC's address
- Turning it on is refused unless this PC's own network (not Iwato's tsnet nodes) reaches the page's node over HTTPS, which needs the Tailscale app on this PC. Iwato checks it from the server: the page's CSP lets it reach no other origin, and the browser and Iwato run on the same PC

## Consequences

- Tailscale works on this PC without the Tailscale app; the app becomes optional, for one address on every device
- While both run, this PC keeps the unauthenticated `*.localhost`, as before Tailscale was chosen; the setting says so
- No way back is offered if this PC later loses the tailnet while `tailnetOnly` is on, beyond the redirect stopping while the page's node is down; the setting is changed from the tailnet's page
