# 0003. Make Tailscale optional; by default use Iwato from the PC without an account

- Status: Accepted
- Date: 2026-10-17

## Context

- The admin UI listens on `127.0.0.1:17600` and can be used directly from the PC's browser. To use it from a phone, today you put `tailscale serve` in front of it on Windows and start Iwato with `-public-origin https://HOSTNAME.TAILNET.ts.net` (README)
- Port publishing (`docs/dev/ingress.md`) is Tailscale only. `internal/ingress` has five layers: Route / Backend dialer / Auth / Provider / Protocol, and only the Provider (`ingress.Provider`) is swappable. The only Provider today is `internal/ingress/tailscale` (one tsnet node per sandbox), and the Auth is `ProviderAuth` (checks via Tailscale WhoIs that the request comes from the owner's device; it requires the Provider to return `HasIdentity`). An `iwato` Auth that checks Iwato's login cookie exists only as a shape (`Authorizer`)
- So port publishing needs a Tailscale account and an OAuth secret, and even people who only want to try Iwato are asked to sign up
- Plain HTTP on a LAN address from a phone does not work: Web Push and PWAs (Service Worker) need trusted HTTPS (a secure context)
- Alternatives:
  - Cloudflare Tunnel: also needs an account, and the account-less Quick Tunnel gets a random URL each time (PWA and notification registrations do not survive). Traffic goes through an outside service
  - Installing a self-signed CA on the phone: hard for users, and needs per-OS steps and an explanation of the risks

## Decision

- By default, neither Tailscale nor any account sign-up is needed. The admin UI is used from the PC's browser at `127.0.0.1`
- Add a new account-less Provider for port publishing (called "local" here) and make it the default. URLs have the form `http://<port>.<sandbox>.localhost:<Iwato's listen port>/`. Browsers resolve `*.localhost` to loopback and treat it as a secure context (Service Worker and clipboard work). It can be opened only from that PC
- Phone access (`tailscale serve` + `-public-origin` for the admin UI) and the current per-sandbox tsnet publishing (`internal/ingress/tailscale`) remain only for those who opt into Tailscale

## Consequences

- A first-time user just starts the exe and can try everything up to port publishing
- Phone access still needs Tailscale. The first-run screen lets the user choose "this PC only" or "from my phone too", and shows the Tailscale steps only for the latter. Tailscale in the settings screen becomes opt-in
- Restructure the docs: Tailscale becomes an optional chapter (README, `docs/dev/ingress.md`)
- To decide for the local Provider:
  - Listening: if it shares the admin UI's port and routes by Host, split before the current Host check (`server.go`; anything other than `s.host` and `publicHost` gets 403). A separate port changes the URL form
  - Authentication: the local Provider adds no authentication. Anyone using a browser on this PC can open it, the same as running a dev server locally. Pages from other sites can also send requests to localhost (they cannot read responses because of CORS; Chrome asks for permission via Local Network Access), but the protection is the same as for a local dev server:
    - Route by Host and reject any Host other than `<port>.<sandbox>.localhost[:port]` (so DNS rebinding, where an attacker's domain points at 127.0.0.1, does not get through)
    - Rewrite `Origin` and `Referer` to `localhost:PORT` only when they equal the published origin (as the current ingress does). Pass other sites' `Origin` through unchanged so the dev server can reject it itself
    - The remaining risks (state-changing requests to dev servers or debuggers that do not check `Origin`; other OS users on the same PC) are accepted as the same as running without Iwato locally. Document them
  - The admin UI keeps its authentication (login token and cookie) as before and stays on `127.0.0.1` (not `localhost`). Published ports run HTML and JS written by the agent in the owner's browser, so without authentication on the admin UI, such a page could use Iwato's API (approving proposals, attaching and detaching providers, terminals) to get out of the sandbox. The cookie stays host-only and `SameSite=Strict`, and the admin UI's API is not served on published ports' Hosts. Connections through `tailscale serve` and other processes on the same PC also come from local, so only the login can tell whether it is the owner
  - Origins: each `<port>.<sandbox>.localhost` is a separate origin, so localStorage and Service Workers are separated between sandboxes and between ports, and the admin UI's (`127.0.0.1`) cookie (host-only, `SameSite=Strict`) is not sent. However, verify whether different `*.localhost` hosts count as the same site (whether SameSite cookies are sent between sandboxes)
  - Browser support: check for browsers or OS resolvers that do not resolve `*.localhost` to loopback themselves
