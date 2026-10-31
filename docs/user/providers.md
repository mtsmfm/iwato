# Providers (credentials)

[日本語](ja/providers.md)

A provider holds a credential (a token, an API key, an OAuth grant) in OpenShell's gateway. Programs in a sandbox with the provider attached see only a placeholder in an environment variable; OpenShell's proxy replaces it with the real value when a request takes it to a destination that carries the credential. Iwato never shows the values again.

## Adding and attaching

Open **Providers** in the sidebar, choose a type, give the provider a name (it cannot be changed later; for example `github-personal`) and enter the value. Each type shows which variables it sets and where the value is sent.

Attach providers in the create form, or later with the sandbox menu's Providers. Attaching or detaching on a running sandbox takes effect after a restart: the sandbox shows "Restart pending" until then, and "Waiting to apply" while the sandbox picks the change up. A provider attached while a process runs reaches only processes started afterwards. Attaching one of Iwato's types adds its rule to the sandbox's policy (below); when a rule already there disagrees with it, attaching names the rule and offers to fix it.

Iwato's provider types:

| Type | Variables | Notes |
|---|---|---|
| Claude Code | `CLAUDE_CODE_OAUTH_TOKEN` | sign in with a Claude account (below), sent to api.anthropic.com |
| Grok Build | `XAI_OAUTH_TOKEN` | sign in with an xAI account (below) |
| Codex | `CODEX_CHATGPT_ACCESS_TOKEN`, `CODEX_CHATGPT_ACCOUNT_ID` | sign in with a ChatGPT account (below), sent to chatgpt.com/backend-api/codex and /backend-api/wham |
| OpenAI API | `OPENAI_API_KEY` | api.openai.com/v1 |
| OpenRouter | `OPENROUTER_API_KEY` | openrouter.ai/api |
| Atlassian | `ATLASSIAN_ACCESS_TOKEN` | OAuth (below); `claude` sends it to mcp.atlassian.com/v2/mcp |
| GitHub | `GH_TOKEN`, `GITHUB_TOKEN` | used by `gh` and git over HTTPS. Reads, clone and fetch; writes and push need approved proposals |
| Hugging Face | `HF_TOKEN`, `HUGGING_FACE_HUB_TOKEN` | downloads only; uploads need approved proposals. The file CDN (`*.hf.co` and the like) gets no value: approve its proposal as read only |
| Civitai | `CIVITAI_API_TOKEN`, `CIVITAI_TOKEN` | read and download only; generation (uses Buzz) needs approved proposals. Where downloads are redirected gets no value: approve that proposal as read only |
| Comfy Cloud | `COMFY_CLOUD_API_KEY`, `COMFY_API_KEY` | also runs workflows (uses credits) |
| Vast.ai | `VAST_API_KEY` | read and search offers only; creating instances needs approved proposals |
| RunPod | `RUNPOD_API_KEY` | read only (api.runpod.io, rest.runpod.io, api.runpod.ai, hapi.runpod.net); creating and starting pods and serverless runs need approved proposals |
| Modal | `MODAL_TOKEN_ID`, `MODAL_TOKEN_SECRET` | the `modal` CLI and Python client (gRPC over HTTP/2 to api.modal.com and api.modal2.com); also runs and deploys apps (costs money). **Caution:** a sandbox with it can run code on Modal, which has open internet access, so the sandbox's network policy no longer limits where data can go, and that code can read the workspace's Modal Secrets |
| Cloudflare | `CLOUDFLARE_API_TOKEN` | read only (api.cloudflare.com/client/v4); deploys and DNS changes need approved proposals. Set `CLOUDFLARE_ACCOUNT_ID` yourself (it is not a secret) |

Only GitHub and Atlassian were tried on real hardware so far.

To see a type's OpenShell profile, use View the profile as YAML. A tag tells where it comes from (shipped with Iwato, custom, or read from the gateway). A profile imported with OpenShell's CLI is read from the gateway and written out in OpenShell's YAML format, so the original file's comments and layout are not kept.

Providers of a profile Iwato does not ship, such as those made with OpenShell's CLI, are listed under Other with their profile id. They can be detached and deleted as usual; their values cannot be changed in Iwato.

## Where credentials go

A credential goes only to the policy's destinations that carry it ([ADR 0022](../adr/0022-iwato-owns-the-sandbox-policy.md)). Iwato's types have none of their own: attaching a provider adds its rule to the sandbox's policy, shown with **Credential: NAME**, and detaching takes out every destination that carries it. The rule allows what the type needs: GitHub reads (API GETs, GraphQL queries), clone and fetch; Claude Code, Grok Build and OpenRouter their APIs; Atlassian the tools that only read; the others as in the table. The rule can be edited like any other.

Writes come from proposals. Approving one under a destination that carries a credential carries it too by default; for a host the credential is meant for, the approve dialog offers it, off by default. A provider is global, but its rules are per sandbox.

What the proxy cannot narrow, narrow in the credential itself:

- GitHub: GraphQL arguments and the branches a push writes are not matched. Use a [fine-grained token](https://docs.github.com/en/authentication/keeping-your-account-secure/managing-your-personal-access-tokens) limited to the repositories and permissions you need.
- Atlassian: a tool's arguments (which site or project) are not matched: narrow the OAuth scopes. Register the MCP with the command the providers view shows: it ends the URL with `?tools=all`, so every tool is called by its own name. Without it, Atlassian reaches most tools through `execute` tools, which name the tool in their arguments and so are not read tools; an MCP registered without it can be removed with `claude mcp remove atlassian` and added again.

Providers made before this (each with a profile of its own) keep working as they were made and are marked so; add them again to have their rules in the policy.

## Custom providers

Add a custom one as YAML adds a provider type of your own as an OpenShell provider profile. Iwato lints it and runs its own checks before saving, and stores it as `profiles/ID.yaml` in the config folder (read when Iwato starts, so you can edit it with another editor). Ids starting with `iwato-` are reserved for Iwato's own.

## OAuth providers

For Atlassian, the Claude account sign-in and the xAI subscription, Iwato runs the sign-in, hands the refresh token to the gateway, which refreshes it from then on, and forgets it ([ADR 0009](../adr/0009-refresh-tokens-stay-in-the-gateway.md)). Iwato's config folder never holds a refresh token.

### Atlassian (Jira, Confluence)

Adding an Atlassian provider asks for scopes (by default Jira and Confluence read, write and search, the user's details, and `offline_access` for refreshing) and opens Atlassian's authorization page. On another device such as a phone, after allowing, copy the address of the page that failed to open and paste it into Iwato. In the sandbox, register the MCP server for `claude` with the command the providers view shows. The providers view shows the refresh state; Refresh now refreshes at once. When the grant needs authorizing again, use Reconnect (the same for the Claude sign-in and the xAI subscription); attached sandboxes need a restart after reconnecting, since the placeholder changes.

### Claude account sign-in for Claude Code

Iwato signs in with Claude Code's own public client id (not one Anthropic issued to Iwato), as `claude auth login` does. On the PC, Sign in with Claude comes back to Iwato by itself; on a phone, use **Sign in on a phone (paste a code)** and paste the code Claude's page shows (`code#…`). The sandbox's `claude` reads `CLAUDE_CODE_OAUTH_TOKEN`; Iwato also sets the token's scopes (`CLAUDE_CODE_OAUTH_SCOPES`, in `~/.config/iwato/env`) so that `/usage` works. Open a new terminal after attaching the provider to a running sandbox. claude.ai connectors stay off: the provider's rule does not allow their host. Check with `claude -p 'hello'`.

Anthropic's terms govern subscription (Free, Pro, Max) OAuth tokens: see [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance). Here only Claude Code in the sandbox uses the token, but Iwato signs in with Claude Code's client id. If the gateway's first refresh fails, Iwato says so.

Iwato no longer offers the "Claude Code (setup token)" type. A provider made with it keeps working and shows under "Other", where it can still be detached and deleted.

### ChatGPT sign-in for Codex

Iwato signs in to auth.openai.com with Codex's device code login (the same steps on a PC and a phone), using Codex's own public client id. Turn on device code authorization for Codex in ChatGPT's settings (Security) first. In the sandbox, the terminals write `~/.codex/auth.json` with the provider's placeholders (only when there is none, or it is Iwato's), so `codex` starts signed in; the gateway refreshes the token. Do not print that file: a placeholder in an agent's conversation stops it. If you ran `codex login` before, run `codex logout` and open a new terminal. Check with `codex exec 'hello'`. Codex's apps (connectors), plugins and telemetry are not in the provider's rule: approve or reject their proposals.

### xAI subscription for Grok Build

Iwato signs in to auth.x.ai with a device code (the same steps on a PC and a phone), using Grok Build's own public client id (not one xAI issued to Iwato). In the sandbox, Iwato sets `GROK_AUTH_PROVIDER_COMMAND` for terminals (in `~/.config/iwato/env`, read by the standard image's shell) so that `grok` uses the token. If you ran `grok login` before, run `grok logout`. Check with `grok -p 'hello'`.
