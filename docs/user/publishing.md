# Publishing ports and phone access

[日本語](ja/publishing.md)

A server running in a sandbox (Vite, Rails, a Jupyter notebook, the desktop) opens from the card's Ports tab. It lists the ports a process in the sandbox listens on (on 0.0.0.0, 127.0.0.1, :: or ::1) with their links, updated every 5 seconds. No approval per port is needed, and agents cannot publish anything themselves.

Choose how ports are published in **Settings**: **This PC only** (the default), **Tailscale (phones too)**, or **Off**.

## This PC only (default)

Each port opens at `http://<port>.<sandbox>.localhost:<Iwato's port>/`, for example `http://5173.dev.localhost:17600/`. No account and no setup.

- Open it in a browser on this PC (Chrome, Edge, Firefox). These browsers resolve `*.localhost` to this PC themselves and treat it as a secure context (Service Workers and the clipboard work)
- There is no authentication: anyone using a browser on this PC can open it, the same as a dev server you run locally. Iwato's own page stays at `http://127.0.0.1:…` and never appears on these addresses
- HTTP and WebSocket only. TCP (psql and the like) does not pass; use Tailscale for that
- Iwato rewrites the `Host` header to `localhost:PORT` (so Vite's host check and HMR work as on localhost)
- Programs other than browsers may not resolve `*.localhost` (Windows' resolver, PowerShell, Python). With curl: `curl --resolve 5173.dev.localhost:17600:127.0.0.1 http://5173.dev.localhost:17600/`
- A sandbox whose name is not a valid DNS label (for example ending in a hyphen, or with `--` as its 3rd and 4th characters) has no links
- A port nobody listens on shows an error page (502); a stopped sandbox shows 503

Remaining risks, accepted as the same as running the server without Iwato: pages from other sites can send requests to a dev server or debugger that does not check `Origin` (they cannot read the answers; Chrome asks for permission), and other OS users on the same PC can open the ports. Do not relay Iwato's port as raw TCP (`tailscale serve --tcp`, `netsh interface portproxy`): relayed connections look local and would reach published ports without authentication.

## Tailscale (optional)

With Tailscale, each sandbox becomes a node on your tailnet, and each port opens at `https://iwato-<sandbox>.<tailnet>.ts.net:<port>/` from your phone and your other devices. Any TCP passes too (`psql`, `ncat`). It needs a Tailscale account and an OAuth client.

One-time setup:

1. In Tailscale's admin console, under DNS, turn on MagicDNS and HTTPS Certificates
2. In the tailnet policy (Access controls), add the tag (the settings have a copy button):

   ```json
   "tagOwners": {
     "tag:iwato": ["autogroup:admin"]
   }
   ```

   Only if you removed the default grant that lets everyone reach everything, also add `{"src": ["autogroup:member"], "dst": ["tag:iwato"], "ip": ["*"]}`
3. Settings → OAuth clients → Generate OAuth client, with only Keys → Auth Keys (write), and the tag `tag:iwato`
4. In Iwato's settings, choose Tailscale (phones too), paste the client secret (`tskey-client-…`) and save. Enter your Tailscale login under Owner logins (for example `alice@github`; filled in when you use Iwato through `tailscale serve`) and save. The status becomes "publishing"

Behaviour:

- Only the untagged devices of the people in **Owner logins** can connect. Tagged nodes (other sandboxes, servers) and devices shared from other tailnets are refused
- Plain `http://` works too (traffic inside the tailnet is already encrypted); the links use `https://`
- A stopped sandbox's node stays and its ports show 503. Deleting the sandbox logs its node out
- The secret is not kept in `iwato.json`: it is stored next to it in `ingress-secret.bin`, encrypted with Windows DPAPI (on macOS with a key the Keychain keeps; on Linux unencrypted, mode 0600). The environment variable `IWATO_TS_AUTHKEY`, when set, is used instead. Node states are under `ingress\tailscale\<sandbox>\` in the config folder
- A reusable auth key (`tskey-auth-…`) also works in place of an OAuth client

### Per-port settings

There is no screen for them yet; set them in `iwato.json` per sandbox:

```json
"sandboxes": [{"name": "dev", "ingress": {"ports": {"5432": {"mode": "tcp"}, "3001": {"host": "preserve"}, "9229": {"disabled": true}}}}]
```

- `"disabled": true` closes a port (404). `"disabled": true` on a sandbox's `ingress` closes the whole sandbox
- `"mode": "tcp"` passes raw TCP (Tailscale only; this PC only shows that it cannot)
- `"host"`: `localhost` (the default: `Host: localhost:PORT`, and the published `Origin` rewritten to `http://localhost:PORT`), `forwarded` (`Host: localhost:PORT` plus `X-Forwarded-Host`, for Rails' `config.hosts`) or `preserve` (the published host as is)

## Use Iwato from your phone

Iwato's own page listens on `127.0.0.1` only. To use it from a phone, put `tailscale serve` in front of it on Windows and tell Iwato the HTTPS address:

```powershell
tailscale serve --bg http://127.0.0.1:17600
.\iwato.exe serve -public-origin https://HOSTNAME.TAILNET.ts.net
```

Then use Sign in on another device and scan its QR code with the phone (on the tailnet). Push notifications and the WebDAV drive with app passwords also need this address. This is independent of how ports are published: you can publish ports to this PC only and still use Iwato from the phone.

More detail on how publishing works: [docs/dev/ingress.md](../dev/ingress.md).
