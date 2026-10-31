# Providers: how they work and how to check them on real hardware (Windows WSLc)

How to check that Iwato's providers (OpenShell providers) work in a real sandbox. The local e2e (`MODE=tls internal/openshell/wslc/e2e.sh`) uses a fake wslc and covers importing profiles, creating, attaching and detaching, the sandbox's environment variables being placeholders, and where the gateway keeps its key. Substitution in flight (OpenShell's proxy) can only be tried on real hardware.

Run `providers-check.sh` inside the sandbox (it never prints tokens). To get it there, paste it into a terminal or put it under the home.

## Setup

1. Start the new exe. The gateway image changes, so the gateway is recreated, and the log shows `imported the provider profile iwato-github` and `iwato-claude-subscription`, `iwato-grok-subscription`, `iwato-codex-subscription`, `iwato-atlassian`, `iwato-openai`, `iwato-openrouter`, `iwato-huggingface`, `iwato-civitai`, `iwato-comfy-cloud`, `iwato-vastai`, `iwato-runpod`, `iwato-modal`, `iwato-cloudflare` (not on later starts)
2. Check that the key is on the volume (so values stay readable after the gateway is recreated):

       wslc container exec iwato-personal-gateway ls -l /var/lib/openshell/credentials

   `key-encryption-key.bin` (600, root) must exist. `personal` is the configured namespace
3. Get the current standard image (gh and the credential helper in /etc/gitconfig). Existing sandboxes keep the old image until recreated

## Profile YAML (profiles/)

Iwato's profiles are `profiles/ID.yaml` (`iwato-github`, `iwato-claude-subscription`, `iwato-grok-subscription`, `iwato-atlassian`, `iwato-openrouter`, `iwato-huggingface`, `iwato-civitai`, `iwato-comfy-cloud`, `iwato-vastai`, `iwato-runpod`, `iwato-modal`, `iwato-cloudflare`), written in OpenShell's format (OpenShell's `providers/*.yaml`, `docs/how-it-works/providers/profiles.mdx`). They are embedded in the exe (go:embed), and `providers_yaml.go` turns them into protos with the same rules as OpenShell's CLI (enum names follow `crates/openshell-providers/src/profiles.rs` and `crates/openshell-policy/src/l7_validate.rs`). OpenShell silently ignores unknown keys; Iwato rejects them (to catch typos). What only the UI uses (display name, category heading, description, value field names, usage hints, experimental, OAuth, hint commands) is not in the YAML but in `providers_profiles.go`.

Checks:

- `go test -tags uitest -run 'Profile|YAML' .`: the files load, unknown keys are rejected, and the protos built from YAML equal the ones formerly written in Go (same hash, so the gateway's profiles are not updated)
- OpenShell's lint (it calls the gateway's `LintProviderProfiles`, so it needs a gateway): `MODE=tls internal/openshell/wslc/e2e.sh` runs `openshell provider profile lint -f` on `profiles/*.yaml`. By hand, while the e2e runs (or against Iwato's gateway): `~/.cache/openshell-e2e/0.1.6/openshell provider profile lint -f profiles/iwato-github.yaml`

The providers panel's **View the profile as YAML** (for each provider and for the type chosen in the add form) shows the bundled file for Iwato's profiles (**Shipped with Iwato**), and for profiles imported with OpenShell's CLI the gateway's copy written out in OpenShell's format (**From the gateway**; without comments and formatting, `resource_version`, Iwato's hash annotation, `source` or `scope`) (`GET /api/provider-profiles/ID`).

## Uninspected rules and providers (allow_uninspected_credentials)

OpenShell adds a provider's value only to inspected traffic (L7, `protocol: rest` etc.). When the sandbox policy has a rule that allows an attached provider's endpoint (overlapping host and port, and the provider's path within the rule's path) without inspection (L4, or `tls: skip`), the gateway rejects the effective policy (`credentialed endpoint 'api.anthropic.com:443' in rule 'allow_api_anthropic_com_443' uses L4-only ...`, `find_uninspected_credentialed_endpoint` in `crates/openshell-server/src/grpc/policy.rs`). OpenShell refuses such rules when they are added or a proposal is approved, but attaching only looks at the creation-time policy, so a rule approved later lets the attach through, and the sandbox's provisioning fails after 300 seconds.

Iwato therefore:

- reads the latest policy before attaching and, on a conflicting rule, does not attach and names the rule. **Remove this destination from the rule, and attach** removes that endpoint from the rule and then attaches. The rule's programs (e.g. curl) can no longer reach that endpoint; the provider's rules (`_provider_…`) allow only the provider's programs, inspected
- while attached, refuses approving a proposal as proposed and hand-added rules that allow the endpoint uninspected. Narrowing them with **Read only** or **Allow writes** passes
- never uses `allow_uninspected_credentials` (it would send values uninspected)

On real hardware: in a sandbox without providers, approve curl → api.anthropic.com:443 as proposed (or add an uninspected rule) → attaching the claude provider is refused with the rule's name → **Remove this destination from the rule, and attach** → it attaches and **Waiting to apply** goes away → the sandbox still starts after a restart.

### After switching to an Iwato with changed profiles

At every start Iwato compares the profile content hash (`iwato/profile-hash`) and updates the gateway's profile when it differs (log: `updated the provider profile …`). Providers and sandbox policies are not rewritten. Running sandboxes take the new provider rules (`_provider_…`, including the program list) at the next config sync (about 10 seconds; OpenShell's profiles.mdx: "Running sandboxes observe the updated provider-derived policy on their next config sync"). Placeholders do not change, so no restart is needed; a denied request goes through when retried.

## (a) gh api user

1. In the menu's **Providers**, add type GitHub, name `github-personal`, with a GitHub token (a fine-grained PAT etc.)
2. Choose `github-personal` under **GitHub** when creating a sandbox (or attach it from an existing sandbox's ⋯ → **Providers** and **Restart now**)
   - Attaching to a running sandbox shows **Restart now** / **Later** at once and marks the row **Waiting to apply** until the sandbox picks it up (about 10 seconds). If it cannot (withheld / failed), the reason is shown; restart
3. In a terminal, `sh providers-check.sh github`. Success: `GH_TOKEN` and `GITHUB_TOKEN` are placeholders and `login: <you>` is printed

On failure, look at denied requests in the **Policy** tab. `credential_endpoint_mismatch` means the destination is outside the profile; `binary ... not allowed` means gh's path differs (is `readlink -f $(command -v gh)` `/usr/bin/gh`?).

## (b) git clone / push (private repository)

1. In (a)'s sandbox, `sh providers-check.sh git OWNER/PRIVATE-REPO`. A successful clone means git sent the placeholder from the credential helper as Basic auth and OpenShell substituted it
2. The GitHub provider's initial rule (ADR 0022) does not allow push (clone and fetch only). Commit in the clone and `git push`: it stops with 403 and a policy proposal appears (agents such as Claude Code propose on their own). Approving it adds a rule for the push's paths that carries the token by default (the approve dialog's credential choice); the push then goes through.
3. Wait until **Waiting to apply** clears (about 10 seconds) and retry `git push`. The token arrives without reattaching or restarting

## (c) claude (iwato-claude-subscription, experimental)

0. The standard image has no claude. Install it in the sandbox with mise (`mise use -g claude@latest`, preset `packages`) (the home `/home/dev` is always a volume, so no reinstall later). The profile's programs cover both (below)
1. Add a provider of type **Claude Code (sign in with Claude)** as in (l)
2. Attach it to the sandbox and restart (or open a new terminal)
3. `sh providers-check.sh claude`. `PROVIDER-OK` means Claude Code runs on the placeholder and OpenShell substitutes `Authorization: Bearer` to `api.anthropic.com`
4. Check interactive `claude` too: `/status` shows `CLAUDE_CODE_OAUTH_TOKEN` as the auth

To check, and on failure:

- In the Claude Code 2.1.287 binary, `CLAUDE_CODE_OAUTH_TOKEN` is used as the Bearer as is, without checking the format (`sk-ant-oat01-`). A version that checks the format would not work
- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`, if set, take precedence
- The profile's endpoint is `api.anthropic.com` only. Telemetry and other destinations show up as proposals; approve or reject them
- The profile's programs are claude's real executables: `/home/dev/.local/share/claude/versions/*` (the installer and auto-update) and `/home/dev/.local/share/mise/installs/claude/*/claude` (mise). Even when installed with mise, the auto-updated versions/ copy may run (`~/.local/bin/claude` points into versions/). Check the running one with `readlink /proc/$(pgrep -n claude)/exe`. A claude elsewhere gets no credential (a proposal `…/claude → api.anthropic.com:443` appears)

## (d) Atlassian MCP (iwato-atlassian)

Iwato.exe gets the OAuth grant from Atlassian (dynamic client registration, PKCE, `resource=https://mcp.atlassian.com/v2/mcp`) and hands the refresh token to the OpenShell gateway. The gateway refreshes the access token at `https://auth.atlassian.com/oauth/token` (oauth2_refresh_token; at most hourly, 5 minutes before expiry), and the sandbox only gets the placeholder. Iwato does not keep the refresh token (ADR 0009). The local e2e uses a fake Atlassian and a copy of the profile with a loopback token_url, and covers creation, the refresh settings, two refreshes (rotation), reconnecting and the sandbox's placeholder. Real Atlassian can only be tried on real hardware.

### Open points on Atlassian's side

- Lifetimes of the access and refresh tokens, and whether the refresh token rotates: Iwato's log (menu **Log**) shows them on connect, never the values
  - `atlassian oauth: NAME: token response fields ..., lifetimes map[expires_in:N ...] s, refresh_token true, scope "..."`: the exchange's response (`refresh_token_expires_in` etc. would show here)
  - `the gateway refreshed it; access token lifetime ...`: the gateway's first refresh right after connecting (capped at the profile's max_lifetime of 1 hour)
- Whether refresh needs `resource`: OpenShell v0.1.6's refresh sends only `grant_type=refresh_token, client_id, refresh_token` (no client_secret, scope or resource). If the first refresh fails, the UI shows "Connected, but the gateway's first refresh failed (STATE)". `invalid_target` / `invalid_request` suggest resource is required (needs an OpenShell change; the current token works until it expires)
- Rotation: two successful **Refresh now** in a row mean the gateway stores and uses the new refresh token (`reauthorization_required` / `oauth_invalid_grant` on the second points at storing the rotation or resource)

### Steps

1. Start the new exe. The log shows `imported the provider profile iwato-atlassian`
2. (Optional) In Windows PowerShell, check the metadata Iwato reads at run time:

       curl.exe -s https://mcp.atlassian.com/.well-known/oauth-protected-resource/v2/mcp
       curl.exe -s https://auth.atlassian.com/.well-known/oauth-authorization-server/VCeDsk8ZHncYF1g234fKtc4lNipbBhu3

   If `token_endpoint` is not `https://auth.atlassian.com/oauth/token`, Iwato stops ("Atlassian's token endpoint differs from the profile") and the profile needs an update
3. In Atlassian admin, check that Rovo MCP's "domain control" allows clients returning to localhost (127.0.0.1) (skip if not an admin)
4. Menu **Providers** → type **Atlassian (MCP)**, name `atlassian-work` → **Connect to Atlassian** → check the scopes, **Ask for authorization** → **Allow in Atlassian**
   - Browser on the same PC: after logging in and allowing, Atlassian returns to `http://127.0.0.1:PORT/callback` with a page saying the authorization was received (「許可を受け取りました」). Iwato shows that it is connected and **The gateway refreshes it**, with the expiry and the next refresh
   - Phone (over the tailnet): the 127.0.0.1 page cannot open after allowing. Copy the URL from the address bar, paste it into Iwato's **Redirect URL** and **Send**
   - Without a return within 5 minutes the listener closes (start over)
5. `atlassian-work` shows **The gateway refreshes it**. Press **Refresh now** twice: both succeed and the expiry moves. Note `refreshed; access token lifetime` in the log
6. Attach `atlassian-work` from the sandbox's ⋯ → **Providers** and **Restart now**. A sandbox takes one Atlassian provider (a second **Attach** is disabled)
7. Register the MCP in a terminal, in single quotes (with double quotes the shell writes the placeholder into the settings, which breaks after reconnecting):

       claude mcp add --transport http atlassian 'https://mcp.atlassian.com/v2/mcp?tools=all' --header 'Authorization: Bearer ${ATLASSIAN_ACCESS_TOKEN}'

8. `sh providers-check.sh atlassian`. Success: `ATLASSIAN_ACCESS_TOKEN` is a placeholder and `ATLASSIAN-OK <name>` comes back. In interactive `claude`, `/mcp` shows atlassian connected and a Jira issue can be read
9. Narrowing by destination and program:
   - `curl -s -H "Authorization: Bearer $ATLASSIAN_ACCESS_TOKEN" https://mcp.atlassian.com/v2/mcp` is denied (the profile's only program is claude; reject the proposal)
   - sending the placeholder to another allowed destination gets 403 `credential_endpoint_mismatch` (substituted only on mcp.atlassian.com /v2/mcp)
10. The MCP keeps working in the same claude for over an hour (past the access token's expiry; the placeholder does not change on refresh)
11. Reconnect: revoke Iwato in Atlassian's profile (connected apps) → **Refresh now** (or the next automatic refresh) shows "Authorization needed again" → **Reconnect** to allow again. Sandboxes that have it become **Restart pending** (a new grant changes the placeholder); restart and repeat 8
12. Read tools: the provider's initial rule allows only the read tools. In `claude`, reading a Jira issue works and creating one is denied (the policy tab shows the denied `tools/call`). Approving the proposal (or adding `createJiraIssue` to the rule's `tools/call` names in the YAML editor) makes creating work without a restart. Also note which `MCP-Protocol-Version` the requests carry (the rule accepts every revision OpenShell knows) and whether `claude` complains about the refused `DELETE` when it exits

On failure:

- "Cannot register a client with Atlassian (dynamic client registration)": possibly refused by domain control. Client ID Metadata Documents need metadata published at an https URL, which Iwato does not have, so they are not used
- "The gateway's first refresh failed": see "whether refresh needs resource" above; note the state and failure code
- atlassian fails in `/mcp` with "Authorization header ... rejected": does `claude mcp get atlassian` still show `${ATLASSIAN_ACCESS_TOKEN}`? If it shows the placeholder string, it was registered with double quotes; `claude mcp remove atlassian` and redo 7. Also look at denied requests in the policy tab
- The running claude's executable: `readlink /proc/$(pgrep -n claude)/exe` is `/home/dev/.local/share/claude/versions/*` or `/home/dev/.local/share/mise/installs/claude/*/claude`

## Model hubs, generation, GPU clouds and hosting (common)

`iwato-openrouter`, `iwato-huggingface`, `iwato-civitai`, `iwato-comfy-cloud`, `iwato-vastai`, `iwato-runpod`, `iwato-modal` and `iwato-cloudflare` are not checked against real OpenShell (substitution). The comment at the top of each YAML gives the sources (official code and docs) and what could not be checked.

- **Substitution**: in OpenShell v0.1.4 a static value is an environment-variable placeholder (`openshell:` followed by `resolve:env:KEY`). It is replaced with the real value in HTTP headers (Bearer, Basic, any header), the path and the query (`?token=` etc.) of requests to the profile's endpoints (host, port, path). `auth_style` / `header_name` are display metadata and do not decide where substitution happens. The body (JSON etc.) only for endpoints with `request_body_credential_rewrite: true` (UTF-8 JSON, form or text, up to 256 KiB; larger bodies fail, so Comfy Cloud enables it only for `/api/prompt`)
- **Programs**: these tools run on python (`hf`, `vastai`, `comfy`, ComfyUI, SDKs), node or curl. OpenShell matches the executable (or an ancestor process), not the script, so allowing python lets any python program reach the endpoint with the value. It cannot be narrower, so the profiles list the standard image's `/usr/bin/python3*`, mise's `/home/dev/.local/share/mise/installs/python/*/bin/python3*`, uv's `/home/dev/.local/share/uv/python/*/bin/python3*` (venv pythons link to these), mise's node, curl and wget (not in the standard image). Python elsewhere (pyenv, conda, …) gets proposals
- **Download hosts (no value needed)**: Hugging Face's `cdn-lfs*.huggingface.co` and `*.hf.co` (Xet: `cas-bridge.xethub.hf.co` etc.), Civitai's signed-URL storage and Comfy Cloud's output storage carry their own authorization in signed URLs, and clients send no value there. They are not in the profiles: every endpoint of a profile with a value is a credential endpoint, so the placeholder would be substituted there too (the value could reach a CDN), and rules allowing those hosts uninspected (L4, as proposed) would be refused (above). Outside the profile they appear as ordinary proposals on the first download; check the program and host and approve with **Read only** (inspected, so a stray placeholder stops at `credential_endpoint_mismatch`)
- **Writes and billing**: Hugging Face uploads, Civitai generation (Buzz), creating or deleting Vast.ai instances and RunPod pods, and Cloudflare deploys are not allowed by the providers' initial rules. They show up as proposals; approving one with the credential attached allows it (ADR 0022). OpenRouter inference and running Comfy Cloud workflows are what those keys are for, so they are allowed

To check (each with `sh providers-check.sh NAME`, never printing tokens; add a provider of the type, attach it and restart first):

## (e) OpenRouter (iwato-openrouter)

1. Type **OpenRouter**, value: an OpenRouter API key (`sk-or-...`)
2. `sh providers-check.sh openrouter`. Success: `OPENROUTER_API_KEY` is a placeholder, `GET /api/v1/key` is 200 and a chat reply comes back. By hand:

       curl -s https://openrouter.ai/api/v1/models -H "Authorization: Bearer $OPENROUTER_API_KEY" | head -c 200

3. Python SDK: `OpenAI(base_url="https://openrouter.ai/api/v1", api_key=os.environ["OPENROUTER_API_KEY"])`. Other agents (OpenCode etc. with their own executables) get proposals, as their paths are not in the profile

## (f) Hugging Face (iwato-huggingface)

1. Type **Hugging Face**, value: a User Access Token (`hf_...`; read is enough)
2. `sh providers-check.sh huggingface`. Success: `HF_TOKEN` and `HUGGING_FACE_HUB_TOKEN` are placeholders, `name: <you>`, and `hf auth whoami` (older: `huggingface-cli whoami`) prints your name. Install `hf` with `uv tool install huggingface_hub` or `pip install --user huggingface_hub` (approve pypi.org etc. as proposals)
3. The last line's small download triggers proposals for the CDN (`cdn-lfs*.huggingface.co` or `*.hf.co`). Approve with **Read only**, retry, and expect `200 https://…`. Also try `hf download OWNER/REPO` on a gated or private model
4. git: `git clone https://huggingface.co/OWNER/REPO` (private: `https://USER:$HF_TOKEN@huggingface.co/...`; a Basic password placeholder is substituted too). git-lfs runs from git, so git's rules cover it
5. To check: the destinations of Xet transfers (`hf_xet`: `cas-server.xethub.hf.co`, `cas-bridge.xethub.hf.co` etc.) and `hf.co` short URLs. See denied requests in the policy tab. Uploads (`hf upload`) propose `POST /api/…/commit/…` etc.

## (g) Civitai (iwato-civitai)

1. Type **Civitai**, value: a Civitai API key (account settings)
2. `sh providers-check.sh civitai`. Success: `CIVITAI_API_TOKEN` and `CIVITAI_TOKEN` are placeholders, `/api/v1/me` returns your data and the `?token=` favourites list comes back (substituted both as Bearer and in the query)
3. Download: `curl -fLOJ "https://civitai.com/api/download/models/ID?token=$CIVITAI_TOKEN"` with a model that needs a login. The storage host (unknown) shows up as a proposal; approve with **Read only** and add the host name to this document and the profile's comment
4. Generation (`POST /v2/consumer/workflows` on orchestration.civitai.com) is proposed (approving allows writes for the provider). It spends Buzz, so try a cheap one

## (h) Vast.ai (iwato-vastai)

1. Type **Vast.ai**, value: a Vast.ai API key (for narrower permissions, one made with `vastai create api-key --permission_file`)
2. Install `vastai` (`uv tool install vastai` or `pip install --user vastai`). `vastai set api-key` is not needed (it reads VAST_API_KEY)
3. `sh providers-check.sh vastai`. Success: `VAST_API_KEY` is a placeholder, `vastai show user` returns the account and `vastai search offers` (PUT /api/v0/search/asks/) lists offers
4. `vastai create instance …` is proposed (it costs money). If approved, check through `vastai destroy instance ID` afterwards (DELETE, also proposed)
5. To check: whether `--explain` shows only console.vast.ai/api/v0. cloud.vast.ai and run.vast.ai (serverless, with the key in the JSON body) are not in the profile

## (i) Comfy Cloud (iwato-comfy-cloud)

1. Type **Comfy Cloud**, value: a platform.comfy.org API key (`comfyui-...`; the API needs a paid subscription)
2. `sh providers-check.sh comfy`. Success: `COMFY_CLOUD_API_KEY` and `COMFY_API_KEY` are placeholders, `/api/user` is 200 and `/api/queue` answers (substituted in `X-API-Key`)
3. Running a workflow: run Comfy's documented example (`POST /api/prompt`, `wss://cloud.comfy.org/ws?clientId=…&token=$COMFY_CLOUD_API_KEY`, fetching outputs from where `GET /api/view`'s 302 points) in python. To check:
   - whether the WebSocket's `?token=` is substituted (the upgrade request's query; OpenShell's docs say queries are)
   - Partner Nodes: whether `/api/prompt` with the placeholder in `extra_data.api_key_comfy_org` passes (body substitution is enabled for `/api/prompt` only; whether the more specific `/api/prompt` wins over `/api/**`)
   - the output storage (the 302's target, unknown) shows up as a proposal; approve with **Read only** and add the host name to this document and the profile's comment
   - image uploads (`POST /api/upload/image`, multipart) over 256 KiB pass (body substitution only on `/api/prompt`)
4. When ComfyUI runs in the sandbox and uses Partner Nodes, it sends `X-API-KEY` to api.comfy.org. ComfyUI's python must be one of the profile's programs (mise, uv or the standard python)

## (j) RunPod (iwato-runpod)

`providers-check.sh` has no RunPod case; check by hand.

1. Type **RunPod**, value: a RunPod API key. Install `runpodctl` with mise (`mise use -g github:runpod/runpodctl`) or the `runpod` Python SDK
2. `RUNPOD_API_KEY` is a placeholder, and listing the account's pods with `runpodctl` or the SDK (`runpod.get_pods()`) works (GraphQL queries and REST GETs)
3. Creating, starting or deleting a pod and serverless runs (`POST /v2/ID/run`) are proposed (they cost money). Approving widens the provider to **Allow writes**
4. To check: the destinations in the profile's comment (`api.runpod.io`, `rest.runpod.io`, `api.runpod.ai`, `hapi.runpod.net`); see denied requests in the policy tab

## (k) Grok Build with an xAI subscription (iwato-grok-subscription, experimental)

Grok Build with a personal xAI account (SuperGrok, X Premium, …). Iwato.exe logs in at `auth.x.ai` with the device code flow (RFC 8628: `POST /oauth2/device/code` → the user confirms the code on xAI's page and allows → poll `POST /oauth2/token`) and hands the refresh token to the OpenShell gateway. The gateway refreshes the access token at `https://auth.x.ai/oauth2/token` (oauth2_refresh_token; 5 minutes before expiry, at most hourly), and the sandbox only gets the `XAI_OAUTH_TOKEN` placeholder. Iwato does not keep the refresh token (ADR 0009). Based on xai-org/grok-build's source (`config.rs`, `device_code.rs`, `oidc/protocol.rs`, `external_auth.rs` in `crates/codegen/xai-grok-login`, and the user guide's `02-authentication.md`); not checked against real xAI. The local e2e (`TestOpenShellGrokSubscriptionE2E`) uses a fake auth.x.ai and a copy of the profile with a loopback token_url, and covers login, the refresh settings, two refreshes (rotation), reconnecting, `~/.config/iwato/env` being written at start, and attaching to a running sandbox, detaching and restarting, with a shell that read the image's `env.sh` printing the placeholder through `GROK_AUTH_PROVIDER_COMMAND` (gone after detaching and restarting). The e2e sandbox runs on the host, not the image, so the shell reads `env.sh` itself.

Note: the login uses Grok Build's own public client ID (`b1a00492-…`, no secret), grok's scopes and `referrer=grok-build`. It is not a client xAI issued to Iwato, so it stops working if xAI blocks or changes it.

### How grok gets the token

grok supports an external auth command, `GROK_AUTH_PROVIDER_COMMAND` (run with `sh -c`; it uses the JSON `{"access_token", "expires_in", "issuer"}` on stdout. With `issuer` `https://auth.x.ai` it treats it as an xAI session and sends `Authorization: Bearer` and `X-XAI-Token-Auth: xai-grok-cli` to `cli-chat-proxy.grok.com`; before expiry and after a 401 it reruns it with `GROK_AUTH_EXPIRED=1`). Iwato makes it a command that prints `XAI_OAUTH_TOKEN` (`grokAuthCommand`) and puts it into the terminal environment file `~/.config/iwato/env` (`term_env.go`):

- OpenShell (v0.1.4) fixes a sandbox's environment at creation (there is no update API, and provider values are only placeholders). So at every start Iwato writes this file as the sandbox user (only when the content differs; links are not followed). The content does not depend on attachment: a line that exports the command only when `XAI_OAUTH_TOKEN` is set and `GROK_AUTH_PROVIDER_COMMAND` is not yet. The supervisor puts `XAI_OAUTH_TOKEN` into the first process and every exec while the provider is attached, and drops it when detached
- The standard image's `/etc/iwato/env.sh` reads the file from login shells (`/etc/profile.d/iwato-env.sh`) and interactive shells (`/etc/bash.bashrc`) (only the user's own regular file, not a link). No agent names are in it (the image is agent-neutral). The user's `~/.profile` and `~/.bashrc` run later, so a `GROK_AUTH_PROVIDER_COMMAND` set there wins
- So attaching or detaching takes effect in the first terminal after a restart (like other providers' **Restart pending**), and in additional terminals opened after the supervisor picks it up. The file is needed once; the only gap is the first start of a new home volume (the first shell may start before Iwato's first write; one more restart fixes it). **Recreate** keeps the home volume, so the file is already there
- With your own image, have shell startup read `~/.config/iwato/env`

Precedence (Auth Precedence in grok's guide, `flow.rs`): a model's `api_key` > the session (`~/.grok/auth.json`) > `XAI_API_KEY`. With the command set, it is the first means when there is no session, and refreshes go through it. A previous `grok login` record in `~/.grok/auth.json` is used until it expires (then the command's result replaces it), so `grok logout` first. The command's result is also written to `~/.grok/auth.json`, but it holds the placeholder. Until a restart after detaching, running shells keep the command (after the restart there is no `XAI_OAUTH_TOKEN`, so it is not set; the file's line stays), the command fails and grok falls back to a browser login (which cannot refresh).

### Open points on xAI's side

- Whether the device code login works from Iwato: grok also sends `x-grok-client-version` and `x-grok-client-surface`; Iwato does not. A refusal shows `device code request: HTTP …`
- Refresh: the gateway sends only `grant_type=refresh_token, client_id, refresh_token` (as grok does for personal accounts). For team or organization logins (`principal_type` Team / Organization in the access token) grok also sends `principal_type` and `principal_id`, so Iwato refuses those. If the first refresh fails, the UI shows "The gateway's first refresh failed"
- Token lifetimes and rotation: Iwato's log on connect (`xai oauth: NAME: token response fields ..., lifetimes ...`, `the gateway refreshed it; access token lifetime ...`). Two **Refresh now** in a row succeed
- Whether grok works with a placeholder token (not a JWT): grok does not verify the token and only peeks at `principal_type` when it is a JWT. xAI sees the real token (in `/user` etc.) only after the proxy's substitution

### Steps

1. Start the new exe. The log shows `imported the provider profile iwato-grok-subscription`
2. Menu **Providers** → type **Grok Build (xAI subscription)** → **Connect to xAI**. A code and **Allow in xAI** appear. On a PC or a phone, log in on xAI's page, check the code matches and allow (within the time limit). Iwato shows "Connected as grok-subscription" and **The gateway refreshes it**, with the expiry and the next refresh
3. Press **Refresh now** twice in the list; both succeed
4. Attach it from the sandbox's ⋯ → **Providers** and **Restart now** (or choose it in the create form). Install grok with mise (`mise use -g grok`, preset `packages`)
5. In a terminal, `cat ~/.config/iwato/env` shows an `export GROK_AUTH_PROVIDER_COMMAND=…` line conditional on `XAI_OAUTH_TOKEN`, `echo "$GROK_AUTH_PROVIDER_COMMAND"` prints the command, and `sh -c "$GROK_AUTH_PROVIDER_COMMAND"` prints `{"access_token":"<placeholder>",…,"issuer":"https://auth.x.ai"}` (the placeholder is not a secret, but do not paste it into an agent's conversation)
6. `grok logout`, then `grok -p 'hello'`. A reply means grok sent the placeholder to `cli-chat-proxy.grok.com/v1` and OpenShell substituted it. Interactive `grok` starts right away, without a login screen
7. The same grok keeps working for over an hour (the placeholder does not change on refresh)
8. Reconnect: logging in again with **Reconnect** makes sandboxes that have it **Restart pending** (the placeholder changes). Restart and repeat 5 and 6
9. After detaching and restarting, neither `XAI_OAUTH_TOKEN` nor `GROK_AUTH_PROVIDER_COMMAND` is set (the conditional line in `~/.config/iwato/env` stays)

On failure:

- Look at denied requests in the policy tab. Paths other than `/v1` on `cli-chat-proxy.grok.com`, and other hosts (trace uploads etc.), show up as proposals; decide whether they are needed. `binary ... not allowed` is grok's executable path (the profile's `binaries`)
- "The gateway's first refresh failed": note the state and failure code (`oauth_invalid_grant`: rotation or client_id; `oauth_invalid_request`: extra parameters needed)

## (l) Claude Code with a Claude account sign-in (iwato-claude-subscription, experimental)

Iwato.exe signs in with Claude Code's public OAuth client (authorization code with PKCE: a loopback redirect on the PC, or Claude's page showing a code to paste from a phone) and hands the refresh token to the gateway, which refreshes the access token; the sandbox gets the `CLAUDE_CODE_OAUTH_TOKEN` placeholder (ADR 0009). The profile's comment gives the sources; it is not checked against real Anthropic. `TestClaudeFlow` covers the flow with a fake.

1. Menu **Providers** → type **Claude Code (sign in with Claude)** → **Sign in to Claude** → **Sign in with Claude** (on a phone: **Sign in on a phone (paste a code)** and paste the code Claude's page shows)
2. Press **Refresh now** twice; both succeed. If the first refresh fails, the UI says so (the gateway sends a form body; Claude Code sends JSON)
3. Attach it and restart, then `sh providers-check.sh claude` as in (c)

## (m) Cloudflare (iwato-cloudflare)

`providers-check.sh` has no Cloudflare case; check by hand.

1. Type **Cloudflare**, value: a Cloudflare API token (not the global API key). Install wrangler with mise (`mise use -g npm:wrangler`) and set `CLOUDFLARE_ACCOUNT_ID` in the terminal
2. `CLOUDFLARE_API_TOKEN` is a placeholder, and `wrangler whoami` and `wrangler deployments list` work (GETs). `wrangler d1 info DB` checks GraphQL queries (POST `/client/v4/graphql`)
3. `wrangler deploy` is proposed (it needs writes, including the assets upload with its own JWT). Approving widens the provider to **Allow writes**
4. To check: cloudflared (`mise use -g cloudflared`) after `cloudflared tunnel login` (its token is in cert.pem, not the variable): `cloudflared tunnel list` passes and `cloudflared tunnel create` is proposed. See denied requests in the policy tab

## (n) Codex with a ChatGPT sign-in (iwato-codex-subscription, experimental)

Iwato.exe signs in at `auth.openai.com` with Codex's device code login (Codex's public client `app_EMoamEEZ73f0CkXaXp7hrann`; not RFC 8628: `POST /api/accounts/deviceauth/usercode`, the owner enters the code at `/codex/device`, `POST /api/accounts/deviceauth/token` answers an authorization code and its PKCE verifier, exchanged at `/oauth/token`) and hands the refresh token to the gateway. The provider has two credentials: `CODEX_CHATGPT_ACCESS_TOKEN` (refreshed) and `CODEX_CHATGPT_ACCOUNT_ID` (the id token's `chatgpt_account_id`, Codex's `chatgpt-account-id` header). Codex reads neither from the environment: the terminals' `~/.config/iwato/env` writes `~/.codex/auth.json` with their placeholders (only when absent or Iwato's, marked by the refresh token `iwato-gateway-refreshes`), and points Codex's own refresh at a closed local port (`CODEX_REFRESH_TOKEN_URL_OVERRIDE`).

### Open points on OpenAI's side

- The device code login must be turned on for the account (ChatGPT's settings, Security); otherwise the start fails with `oauth_codex_device_disabled`
- Refresh: Codex sends JSON; the gateway sends a form (`grant_type`, `client_id`, `refresh_token`). The token endpoint takes a form for the code exchange; the refresh is not verified. Refresh tokens are single use
- Placeholders in `~/.codex/auth.json`: OpenShell resolves the revisioned placeholder of the shell that wrote it while the provider stays the same; a reconnect needs a restart and a new terminal

### Steps

1. Menu **Providers** → type **Codex (sign in with ChatGPT)** → **Sign in with ChatGPT**. Enter the code at OpenAI's page; Iwato shows "Connected as codex-subscription"
2. Press **Refresh now** twice; both succeed
3. Attach it and restart; install codex with mise (`mise use -g codex`, the Agent CLIs preset)
4. In a new terminal: `sh providers-check.sh codex` (both variables are placeholders, `auth.json` is Iwato's, `codex exec` replies)
5. Interactive `codex` starts without a login screen and answers; `/status` shows the ChatGPT account
6. Denied requests (apps `/backend-api/ps/mcp`, plugins, telemetry on `ab.chatgpt.com`) show in the policy tab; decide whether they are needed
