# Publishing a sandbox's ports (ingress)

Lets the owner's browser open ports a sandbox listens on. Agents do not request them and there is no per-port approval. ADR 0003 makes Tailscale optional. There are two ways to publish (providers):

- **This PC only (`local`, the default)**: `http://<port>.<sandbox>.localhost:<Iwato's port>/` (e.g. `http://5173.dev.localhost:17600/`). No account needed. Opens only in this PC's browsers. HTTP and WebSocket only
- **Tailscale (`tailscale`, optional)**: `https://iwato-<sandbox>.<tailnet>.ts.net:<port>/`. Opens from a phone too, and TCP passes through as is. Needs a Tailscale account and an OAuth client ("Tailscale (optional)" below)

Chosen in the settings (Settings) (`off`, publishing nothing, can also be chosen). User-facing steps are in `../user/publishing.md`.

## This PC only (local)

Received on the same listener as the admin UI (`127.0.0.1:<Iwato's port>`) and split by Host (`internal/ingress/local`). It does not use a separate port, to keep the URL form (ADR 0003) and not add a listener.

- Splitting (`local.Split`): for each connection, the first request's headers are read ahead (5 seconds from the first byte, up to 32 KiB; the first byte is awaited for 60 seconds because browsers open connections ahead of time (preconnect)). Only when the Host is `<port>.<sandbox>.localhost[:<Iwato's port>]`, the sandbox is in the configuration, and the connection is a direct one to Iwato (from 127.0.0.1, without `X-Forwarded-*`, `Forwarded`, `Tailscale-User-Login` or `Tailscale-Headers-Info`; anything through `tailscale serve` is not let through) does it go to the ingress core. Everything else goes to the admin UI as before and gets the admin UI's Host check (403 for anything other than `127.0.0.1:PORT` and the `-public-origin` host). So the admin UI and its API are never served on a published host (`/api/...` also reaches the sandbox)
- Strict parsing: the port is decimal 1–65535 (no leading 0); the sandbox name follows Iwato's name rule (`^[a-z][a-z0-9-]{0,31}$`) and is a DNS label (does not end with a hyphen, no `--` at the 3rd and 4th characters; reserved for `xn--` and the like). Sandboxes whose names do not fit have no URL, and the Ports tab says so. Case-insensitive. A trailing `.`, extra labels or another port go to the admin UI (403)
- Later requests on the same connection get 421 unless their Host is the same (port and sandbox). Sending a published host in the middle of an admin UI connection gets the admin UI's 403
- No authentication (ADR 0003). Anyone using a browser on this PC can open it, the same as running a dev server locally. Each request is only checked to be a direct connection. Browsers do not send the admin UI's cookie (host-only on `127.0.0.1`, `SameSite=Strict`), but cookies with a matching name are removed before passing to the sandbox, just in case
- Origin / Referer are rewritten to `http://localhost:PORT` only when they equal the published origin (`http://5173.dev.localhost:17600`); other sites' values pass through unchanged (so the dev server can reject them itself)
- Remaining risks (accepted): state-changing requests from other sites to dev servers or debuggers that do not check Origin (responses cannot be read because of CORS; Chrome asks for permission via Local Network Access), and other OS users on the same PC. Both are the same as running locally without Iwato
- Do not relay Iwato's port as raw TCP (`tailscale serve --tcp` / `--tls-terminated-tcp`, `netsh interface portproxy`, etc.). Relayed connections come from loopback without proxy headers, so they look direct, and tailnet or LAN devices reach the published ports without authentication (the admin UI is still protected by its login). For a phone, put HTTPS `tailscale serve` (which adds the headers) in front, as in `../user/publishing.md`
- TCP ports (mode `tcp`) cannot be split by Host and do not pass. Opening one gives a 501 page, and the Ports tab says it is a TCP setting. Use Tailscale when TCP is needed
- A port disabled in the configuration gives 404; a port nothing listens on gives a 502 page (the connection is not cut, unlike Tailscale). The error pages' "open Iwato" link is `http://127.0.0.1:PORT/`

### Browsers and name resolution

- Chrome, Edge and Firefox resolve `*.localhost` to loopback (::1 and 127.0.0.1) themselves and treat it as a secure context (Service Worker, clipboard). Iwato listens on 127.0.0.1 only, so ::1 is refused and the browser then connects to 127.0.0.1
- Checked with Chromium (Playwright's headless shell): resolution, `isSecureContext`, and the SameSite behaviour below. Firefox, Edge and Safari are unchecked
- Clients that use the OS resolver may not resolve `*.localhost` (Windows' resolver, PowerShell's `Invoke-WebRequest`, Python, etc.). curl 7.85 and later resolves it itself. When it does not resolve: `curl --resolve 5173.dev.localhost:17600:127.0.0.1 http://5173.dev.localhost:17600/` or `curl -H "Host: 5173.dev.localhost:17600" http://127.0.0.1:17600/`

### Origin and site

- Each `<port>.<sandbox>.localhost` is its own origin (localStorage, Service Workers and cookies are separate)
- Site (the SameSite unit), checked with Chromium: `localhost` is not on the Public Suffix List, so the default rule `*` makes it a public suffix and the site is `<sandbox>.localhost`. Ports of the same sandbox (`3000.dev.localhost` and `5173.dev.localhost`) are same-site (`SameSite=Strict` cookies are sent); different sandboxes, and `127.0.0.1` (the admin UI), are cross-site (`Sec-Fetch-Site: cross-site`, no cookies). Ports of one sandbox share one trust boundary, so this is accepted. Firefox should follow the same rule but is unchecked
- The admin UI stays on `127.0.0.1` (an IP) and is cross-site to every `*.localhost`. The admin UI's API refuses cross-site requests (`Sec-Fetch-Site`) and other Origins

## Structure

`internal/ingress` has five layers. Only the provider is swapped; the rest is shared.

| Layer | Implementation | Role |
|---|---|---|
| ① Route | `ingress.Routes` (`portOptions` in `ingress_config.go`) | (sandbox, port) → overrides (disabled, mode, Host). Ports are not enumerated |
| ② Backend dialer | `ingress.Backend` + `client.Forwarder` (`internal/openshell/client/forward.go`) | ForwardTcp directly over gRPC (an SSH session token shared by 3 connections, revoked after 30 seconds unused. The gateway returns a connection slot only after its relay is cleaned up, so a closed connection still counts for 1 second, and when the token's limit (`SSH session connection limit reached`) refuses, it retries once with a new token. It returns only after the gateway's response headers (the slot was taken). When the sandbox's limit of 20 refuses: 503). At most 14 at once per sandbox (of OpenShell's limit of 20, 6 are left for terminals and files). When short, idle HTTP keep-alive connections are closed to free slots (also every 200 ms while waiting); after 10 seconds HTTP gets 503 (Retry-After) and TCP is cut. 503 while stopped. When 127.0.0.1 is refused it retries on ::1 (for Node's localhost; ::1 first from then on) |
| ③ Auth | `ingress.ProviderAuth` (tailscale), `local`'s auth (swapped per connection by `Conn.Auth`) | tailscale: WhoIs; only untagged devices of the owners' logins. Tagged nodes (other sandboxes, etc.) and devices shared from other tailnets are refused. local: no authentication, only direct connections from this PC. `iwato` / `both` later (only the `Authorizer` shape exists) |
| ④ Provider | `internal/ingress/local` (default) | Splits the admin UI's listener by Host and passes only HTTP (`Conn.HTTPOnly`; refused ports get a page instead of a cut) to the core. "This PC only" above |
| ④ Provider | `internal/ingress/tailscale` (optional) | A tsnet node per sandbox, accepting with `RegisterFallbackTCPHandler`. Ports the core does not pass ("Ports that pass" below) are reset (they look closed). When the first byte is 0x16, TLS is terminated with tsnet's certificate (h2 / http/1.1). Plain HTTP passes as is (below) |
| ⑤ Protocol | `ingress.Server` (`core.go`, `proxy.go`) | The first bytes tell whether it is HTTP (if nothing comes in 300 ms it is TCP: servers that speak first, such as MySQL). HTTP goes through a ReverseProxy with a per-sandbox `http.Transport` (up to 12 keep-alive); WebSocket relays the Upgrade as is (when the client closes, the sandbox side closes at once and frees the slot, even if the relay does not pass on the half-close). Anything else is raw TCP (with half-close) |

Host rewriting (overridable per port in `iwato.json`):

- `localhost` (default): Host becomes `localhost:PORT`, and Origin / Referer equal to the published origin become `http://localhost:PORT`. Vite's allowedHosts and its HMR WebSocket's Origin check pass as is
- `forwarded`: Host is `localhost:PORT`, Origin unchanged, and `X-Forwarded-Host` carries the published host (for Rails' config.hosts)
- `preserve`: the published host as is

In every mode `X-Forwarded-Proto` (https/http) and `X-Forwarded-For` (the tailnet IP) are added, a response `Location` on localhost / 127.0.0.1 is rewritten to the published URL, and `Domain=localhost` is removed from `Set-Cookie`.

Why plain HTTP is not redirected to https: inside the tailnet traffic is already encrypted by WireGuard, and the same URL then works with `ws://`, curl and clients that do not follow redirects (the same behaviour as localhost:PORT). The UI's links are always `https://` FQDNs. Apps that need a secure context (Service Worker, clipboard, etc.) are opened with the https link.

## Tailscale (optional)

Chosen to open from a phone too, or to pass TCP as is. The rest of this section is about this provider.

### Configuration (iwato.json)

`provider` is `local` (when absent; an iwato.json without ingress settings, and an older iwato.json saved with Tailscale turned off, also become local), `tailscale` or `off`. The UI does not write local.

```json
"ingress": {
  "provider": "tailscale",
  "tailscale": {"tag": "tag:iwato", "hostnamePrefix": "iwato-", "owners": ["alice@github"]}
},
"sandboxes": [{"name": "dev", "ingress": {"ports": {"5432": {"mode": "tcp"}, "3001": {"host": "preserve"}, "9229": {"disabled": true}}}}]
```

`tag` and `hostnamePrefix` are not written when they are the defaults. `auth` is `provider` only (`iwato` / `both` are refused as "not available yet"). A sandbox's `"disabled": true` closes that sandbox's node (its state is kept; with local it does not publish that sandbox). Per-port overrides (`ports`) work the same with local (only mode `tcp` cannot be opened with local). There is no UI for per-port overrides yet.

### Secret

An OAuth client secret (`tskey-client-…`) or a reusable auth key (`tskey-auth-…`) is pasted in the settings (Settings). It is not put in `iwato.json` but saved in `ingress-secret.bin` in the same folder:

- Windows: encrypted with DPAPI (CurrentUser, with entropy) (`internal/protect`)
- macOS: encrypted with AES-256-GCM, with a key the login Keychain keeps for Iwato (`internal/protect`)
- Linux (dev containers, tests): **not encrypted**, plaintext in a 0600 file
- The environment variable `IWATO_TS_AUTHKEY` is used when set (cannot be changed from the UI)

The UI shows only "set (tskey-client-… last 4 characters)". Without a secret publishing stops (state "not set"). With OAuth, Iwato appends `?ephemeral=false&preauthorized=true` and gives it to tsnet, which issues a one-time key per node (tsnet's default is ephemeral). Nodes that have already joined (a state folder exists) get no key (tsnet would issue a key on every start otherwise).

Node state lives in `<config folder>/ingress/tailscale/<sandbox>/` (0700). It holds the node's private key.

### Node lifetime

| Event | Node |
|---|---|
| Start | Joins (the first time with the key, later as the same node from the state folder) |
| Stop | Stays. Opening gives the "stopped" 503 page; TCP is cut |
| Iwato exits or restarts | Close (no Logout). On start, nodes of running or joined sandboxes come back |
| Delete | Logout (tagged nodes are expected to disappear from the tailnet) and the state folder is deleted |
| Deleted while Iwato was down | The next Sync logs out and deletes what has a state folder but is not in the configuration |

Sync runs when Iwato starts, after a sandbox starts, stops or is deleted, each time the UI fetches the list (only when the state changed), and every 15 seconds. tsnet's log goes to Iwato's debug log (`iwato.log`, the UI's log (「ログ」)), and sending logs to Tailscale is turned off (the equivalent of `TS_NO_LOGS_NO_SUPPORT` and `logtail.Disable`). Nodes do not work on a tailnet that requires log sending.

Dependency: `tailscale.com v1.102.5` (the latest v1.102 patch; works with the Go 1.26 in `mise.toml`. v1.104 needs Go 1.27). The Windows exe grows from 20 MB to 55 MB (after strip).

### Tunnel MTU

tsnet nodes start with `TS_DEBUG_MTU=1200` (`internal/ingress/tailscale/tsnet.go`; Tailscale's default is 1280). On paths such as home routers hairpinning traffic addressed to themselves, packets of the default size were dropped and TLS handshakes or downloads hung or slowed. When `TS_DEBUG_MTU` is set in the environment, that value is used (for trying other values). The network stack (netstack) reads the value each time it builds a packet. Unverified on real hardware: the effect on a connection that was slow.

### Ports that pass

local uses the same decision (ports that do not pass get a 502 page instead of a cut).

Connections from the tailnet pass only to ports of a running sandbox where a running process listens on 0.0.0.0, 127.0.0.1, :: or ::1. Ports nothing listens on, and ports where only the listening socket is left, are reset by the node (the tsnet handler returns `(nil, true)`; a port check from the tailnet sees them closed). Ports disabled in the configuration too.

- Reading: one exec of `sh -c` (`ingress.ListenScript`) first triggers OpenShell's sweep (below), then reads `/proc/net/tcp` and `/proc/net/tcp6` and counts the LISTEN (0A) lines (`ingress.ParseProcNetTCP`). The DNS relay on 127.0.0.53 and sockets bound to other specific addresses (the relay only connects to 127.0.0.1 and ::1) are not counted
- Why listening sockets are left: OpenShell's network broker (the supervisor, PID 1) keeps its own duplicate of each socket it created for a sandbox process (NVIDIA/OpenShell#4331, `docs/dev/openshell.md`). After the process ends, the socket stays in LISTEN and still accepts connections until OpenShell's sweep runs. Test servers' ports (httptest etc.) looked open forever, and the node kept relaying to them
- Triggering the sweep: each time a sandbox process's bind fails with EADDRINUSE, the broker closes the duplicates no process holds (the supervisor reads every process's `/proc/*/fd` itself). So `ListenScript` first has python3 bind the supervisor's DNS relay 127.0.0.53:53 (always in use, so it fails, and never collides with the user's servers). Checking owners from the exec side (reading `socket:[INODE]` in `/proc/*/fd`) did not work on real hardware: Iwato's exec cannot read the fds of processes started in other sessions, such as the main terminal, so every port looked ownerless
- The decision is made per new connection (`ingress.Server.Admit` → `ingressManager.forwarded`). A reading is reused for 3 seconds per sandbox, with only one exec running at a time. The decision runs in the tsnet handler (called while holding the node's lock), so it does not wait for the exec: when the reading is older than 3 seconds it answers from the previous one and reads again in the background; when nothing has been read yet it waits 1.5 seconds and lets the connection through if the reading is not ready. The Ports tab uses the same reading (and waits for the re-read). A server starting or stopping shows a few seconds late
- A stopped sandbox (as Iwato last saw it) accepts every port and shows the "stopped" 503 page as before. When the ports cannot be read (the exec fails), connections are accepted too and get 502 / a cut as before. When the image has no python3, the sweep is not triggered and every LISTEN socket counts, leftovers included

## The ports tab (「ポート」)

Each sandbox's ports tab shows the ports that pass (the reading above) as links. local: `http://<port>.<sandbox>.localhost:<Iwato's port>/` (opened in this PC's browser; ports set to TCP have no link and show why). Tailscale: the node's state and `https://<node>:<port>/`. While the tab is open it reads again every 5 seconds. Ports not in the list do not pass (local gives a 502 page).

## What was checked

- Go tests (`go test ./...`): `internal/ingress` (reading `/proc/net/tcp`, Host rewriting, Location, Set-Cookie, reuse of sandbox-side connections, HTTP/2 over TLS, WebSocket Upgrade and Origin, freeing the slot of a closed WebSocket, raw TCP and half-close, TCP where the server speaks first, 503 when stopped, 502 with no listener, 503 at the limit, refusing non-owners / tags / shared devices, disabled ports, retrying on ::1, the limit of 14), `internal/ingress/tailscale` (with a fake node: start, stop, restart, disable, delete, removed from the configuration, the OAuth key's form, no key and retries after failures, TLS termination and identity, resetting ports the core refuses), `internal/openshell/client` (with a fake gateway: sharing and revoking the token, retrying at the token's limit and the sandbox's limit, telling refused from stopped, half-close, read deadline), the main package (configuration checks, storing the secret, the Sync flow and API, the decision on ports matching the Ports tab's list)
- `TestOpenShellIngressE2E` in `MODE=tls internal/openshell/wslc/e2e.sh` (alone: `MODE=tls ONLY=ingress …`): with the official gateway (v0.1.6) and a sandbox, through ForwardTcp and the ingress core (no Tailscale; a local listener stands in for the provider): HTTP on the sandbox's 127.0.0.1 (Host rewritten to `localhost:PORT`), WebSocket (Origin rewriting), 502 for a port with no listener, TCP where the server speaks first, 30 concurrent HTTP requests (waiting at the limit of 14), and 503 after a stop. It also checks that the test servers appear in the port list (the exec's `ingress.ListenScript`) (the sweep closes only the broker's duplicates, so servers outside the sandbox stay). That the sweep removes leftover sockets is checked on real hardware (step 6 below) and by the subtests below
  - Most servers are started by the test process on the sandbox's loopback (the same as the host with the fake wslc). In addition, two python servers started by another exec inside the sandbox are tried (subtests `held` and `detached`): they appear in the list (`ListeningPorts`, another exec), return 200 through the core, and after they end disappear from the list within seconds (the sweep closes OpenShell's duplicate, NVIDIA/OpenShell#4331) and give 502. `held` is a child of an exec that keeps running; `detached` runs in its own session and outlives its exec (the same shape as a server started from a terminal)
  - OpenShell's network broker (a thread in openshell-sandbox, as the workload's user, with no capabilities and no_new_privs) reads the process's memory when it checks bind / connect via seccomp notifications (`process_vm_readv`, else `/proc/PID/mem`). With Yama's `kernel.yama.ptrace_scope` at 1 (the default on Ubuntu and WSL) it can read only its descendants. On WSLc the sandbox is PID 1, so a process orphaned when its exec ends is reparented to the sandbox and stays a descendant. With the fake wslc it is not PID 1, so the process is reparented outside the sandbox, cannot be read, and its bind fails with EACCES (the sandbox log says `network notification denied ... process_vm_readv failed: EPERM`). CI (`e2e.yml`) sets `ptrace_scope` to 0, runs `detached` too, and makes every skip a failure with `IWATO_E2E_REQUIRE_INSIDE=1`. Locally (a dev container cannot change it) `detached` is skipped with the reason only when its bind fails with EACCES; any other bind failure always fails. File capabilities (`setcap`) do not help: the sandbox refuses to start with any capability, and they have no effect under no_new_privs
  - The relay to a server listening on `::1` only is unchecked, as a dev container may have no IPv6 loopback (the test prints "not verified"). The core's retry on ::1 is covered by unit tests
- local: `ingress_local_test.go` (with a real listener and the admin UI: a published host reaches the sandbox with Host `localhost:PORT`; only same-origin Origin / Referer are rewritten; the admin UI's cookie is removed; `/api/...` also reaches the sandbox; the admin UI only on `127.0.0.1`; another host on the same connection gets 421 / 403; unknown sandboxes, other ports, 0, 65536, leading 0, extra labels, a trailing `.`, and names that are not DNS labels get the admin UI's 403; proxy headers get 403; TCP gets 501; no listener gets 502; WebSocket and Origin), `internal/ingress/local` (requests after a preconnect, the read-ahead bytes reaching the admin UI, reading the Host)
- UI: `node scripts/ui-behavior-tests.cjs`, `scripts/ingress-check.cjs [--phone]` (the fake backend: opens the local link in Chromium; the ingress page shows and neither the admin UI nor its API does; the cookie is only on `127.0.0.1`; 403 for an unknown sandbox; secure context. Tailscale uses a fake provider)

The Tailscale parts (joining with tsnet, certificates, WhoIs, Logout) cannot run here (no tailnet). They are checked with the steps below.

## Checking on real Windows

### This PC only (local)

1. Start without changing settings, run `python3 -m http.server 8000` in sandbox `dev` → the Ports tab shows `http://8000.dev.localhost:17600/` (within 5 seconds) → clicking it opens the listing (Chrome and Edge, Firefox if possible)
2. Open Vite (the same as step 5 of the Tailscale checks) at `http://5173.dev.localhost:17600/`; HMR works, and "Blocked request. This host is not allowed" does not appear
3. `http://8000.dev.localhost:17600/api/sandboxes` is the sandbox's response (404 etc.), not Iwato's JSON. The admin UI at `http://127.0.0.1:17600/` keeps working
4. In PowerShell, check whether `curl.exe http://8000.dev.localhost:17600/` (add `--resolve 8000.dev.localhost:17600:127.0.0.1` if it does not resolve) and `Invoke-WebRequest` resolve
5. From a phone (tailnet), sending to the `-public-origin` `https://…ts.net/` with a changed Host does not reach the published ports (`tailscale serve` adds `X-Forwarded-For` etc.): the admin UI's 403

### Tailscale

#### Setup (once)

1. In Tailscale's admin console → DNS, enable MagicDNS and HTTPS Certificates (already done if you use tailscale serve)
2. Add the tag to the tailnet policy in Access controls (the settings (Settings) have a copy button):

   ```json
   "tagOwners": {
     "tag:iwato": ["autogroup:admin"]
   }
   ```

   Only if you removed the default "everyone → everything" grant, also add `{"src": ["autogroup:member"], "dst": ["tag:iwato"], "ip": ["*"]}`
3. Settings → OAuth clients → Generate OAuth client: scope Keys → Auth Keys, Write only; tag `tag:iwato`
4. In Iwato's settings (Settings): choose **Tailscale (phones too)** → paste the secret and save → "set (tskey-client-…xxxx)". Put your login (e.g. `alice@github`; filled in automatically when opened through tailscale serve) in **Owner logins** → save → the state is "Publishing with Tailscale"
5. `ingress-secret.bin` exists in the folder of `iwato.json` and its content is not plaintext (DPAPI). `iwato.json` has no secret

#### Checks

1. Start sandbox `dev` → in the settings' node list `dev` is connected (「接続中」), and Tailscale's admin console shows `iwato-dev` (tag:iwato, no key expiry)
2. Run `python3 -m http.server 8000` in the sandbox → the Ports tab shows `:8000` and `https://iwato-dev.<tailnet>.ts.net:8000/` (within 5 seconds)
3. Open the link on a phone (in the tailnet, an owners user) → the directory listing. The certificate is valid (no warning)
4. Plain: `http://iwato-dev.<tailnet>.ts.net:8000/` also opens (no redirect)
5. HMR: run Vite in the sandbox (`npm create vite@latest app -- --template vanilla && cd app && npm i && npx vite`; its default localhost may be ::1) → open `https://iwato-dev…:5173/`; editing `main.js` applies without a reload (the WebSocket connects to `wss://iwato-dev…:5173`). It opens without `--host` (the retry on ::1; if not, the "::1 only" relay does not pass). Vite's "Blocked request. This host is not allowed" does not appear (Host rewriting)
6. TCP: in the sandbox `python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",9000));s.listen();c,_=s.accept();c.sendall(b"hi\n")'` → from the PC `ncat iwato-dev.<tailnet>.ts.net 9000` (or PowerShell's `Test-NetConnection -Port 9000`) gets `hi`. After python ends (it ends after one connection), the same command is refused at once (`Test-NetConnection` shows TcpTestSucceeded False), because the sweep triggered on each reading removes the leftover LISTEN
7. Refusal: devices of users not in owners, and tagged nodes (other servers), are cut at once. Iwato's log (「ログ」) shows `refused … not one of the owner's devices`
8. Stop: stop the sandbox → the same URL shows the "stopped" 503 page. The node stays connected (「接続中」). Starting brings the same URL back
9. Restart: close Iwato and open it again → the node name does not change (no `-1`). No duplicates in the admin console
10. Delete: delete the sandbox → within a minute `iwato-dev` disappears from Tailscale's admin console (Logout), and the `ingress/tailscale/dev` folder is gone too. If not, the log (「ログ」) has `logout:`; delete it by hand in the admin console (whether Logout removes tagged nodes is unverified)
11. Create again with the same name and start → joins again as `iwato-dev` (no `-1`)
12. Delete the secret → "not set", and the nodes close (they stay offline in the admin console)
13. Concurrent connections: while using a terminal and files (WebDAV), reloading the page many times does not cut the terminal (the limit of 14 leaves 6)

Also check the exe's size (about 55 MB), memory per node, and that no Windows firewall prompt appears (tsnet should use outbound UDP only).

## Not implemented / later

- auth `iwato` / `both` (tickets and per-host cookies), domain / lan providers
- A UI for per-port overrides (they work in the configuration file)
- Checking the policy for ways from the sandbox into the tailnet (refuse 100.64.0.0/10, fd7a:115c:a1e0::/48 and *.ts.net in `DecideProposal` / `EditPolicy`). OpenShell's default SSRF refusal blocks them, but an approval can open them
- Using local and Tailscale at the same time (only one today)
- QR codes, showing process names
- Re-encrypting to HTTPS servers (self-signed) inside the sandbox
