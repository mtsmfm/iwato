# Providers (credentials)

[日本語](ja/providers.md)

A provider holds a credential (a token, an API key, an OAuth grant) in OpenShell's gateway. Programs in a sandbox with the provider attached see only a placeholder in an environment variable; OpenShell's proxy replaces it with the real value when a request takes it to the provider's hosts. Iwato never shows the values again.

## Adding and attaching

Open **Providers** in the sidebar, choose a type, give the provider a name (it cannot be changed later; for example `github-personal`) and enter the value. Each type shows which variables it sets and where the value is sent.

Attach providers in the create form, or later with the sandbox menu's Providers. Attaching or detaching on a running sandbox takes effect after a restart: the sandbox shows "Restart pending" until then, and "Waiting to apply" while the sandbox picks the change up. A provider attached while a process runs reaches only processes started afterwards. When a rule of the sandbox's policy would let the provider's credential reach more than the provider allows (for example full access to `api.github.com` with GitHub's read-only scope), attaching names the rule and offers to remove it from the rule or to attach anyway.

Iwato's provider types:

| Type | Variables | Notes |
|---|---|---|
| Claude Code (setup token) | `CLAUDE_CODE_OAUTH_TOKEN` | a token from `claude setup-token`, sent to api.anthropic.com |
| Claude Code (sign in with Claude) | `CLAUDE_CODE_OAUTH_TOKEN` | sign in with a Claude account (below), sent to api.anthropic.com. The setup-token type above is recommended |
| Grok Build (xAI subscription) | `XAI_OAUTH_TOKEN` | sign in with an xAI account (below) |
| OpenRouter | `OPENROUTER_API_KEY` | openrouter.ai/api |
| Atlassian (MCP) | `ATLASSIAN_ACCESS_TOKEN` | OAuth (below); only `claude` may send it, to mcp.atlassian.com/v2/mcp |
| GitHub | `GH_TOKEN`, `GITHUB_TOKEN` | used by `gh` and git over HTTPS. Has repository scopes (below) |
| Hugging Face | `HF_TOKEN`, `HUGGING_FACE_HUB_TOKEN` | downloads only; uploads need approved proposals. The file CDN (`*.hf.co` and the like) gets no value: approve its proposal as read only |
| Civitai | `CIVITAI_API_TOKEN`, `CIVITAI_TOKEN` | read and download only; generation (uses Buzz) needs approved proposals. Where downloads are redirected gets no value: approve that proposal as read only |
| Comfy Cloud | `COMFY_CLOUD_API_KEY`, `COMFY_API_KEY` | also runs workflows (uses credits) |
| Vast.ai | `VAST_API_KEY` | read and search offers only; creating instances needs approved proposals |
| RunPod | `RUNPOD_API_KEY` | read only (api.runpod.io, rest.runpod.io, api.runpod.ai, hapi.runpod.net); creating and starting pods and serverless runs need approved proposals |

Only GitHub and Atlassian (MCP) were tried on real hardware so far. The profiles name the agents' executables where mise and the agents' own updaters put them (for example Claude Code under `/home/dev/.local/share/claude/versions/*` and mise's `/home/dev/.local/share/mise/installs/claude/*/claude`).

To see a type's OpenShell profile, use View the profile as YAML. A tag tells where it comes from (shipped with Iwato, made from a provider's scope, custom, or read from the gateway). A profile imported with OpenShell's CLI is read from the gateway and written out in OpenShell's YAML format, so the original file's comments and layout are not kept.

Providers of a profile Iwato does not ship, such as those made with OpenShell's CLI, are listed under Other with their profile id. They can be detached and deleted as usual; their values cannot be changed in Iwato.

## Scopes

Everything on the hosts a provider's credentials reach belongs to the provider ([ADR 0006](../adr/0006-providers-own-credentialed-hosts.md)): the provider decides what is allowed there, and the policy covers hosts without credentials.

The GitHub provider has a scope, set with Change scope:

- **Read only** (the default): API GETs, git clone and fetch, GraphQL queries
- **Write to the listed repositories** (`OWNER/REPO`): git push, REST API writes and release uploads for those repositories
- **Write everywhere**: wherever the token reaches

With either of the first two, **GraphQL mutations** you name (for example `createPullRequest`) are allowed too. GraphQL arguments cannot be matched, so a named mutation works for everything the token can reach.

For details, edit the generated profile directly with Edit as YAML; the scope then shows as custom, and Back to the simple settings returns to the simple settings. Changing a scope needs no restart: running sandboxes show "Waiting to apply" until they apply it.

Iwato's scope is a second line of defence. Narrow the token itself too: use a [fine-grained token](https://docs.github.com/en/authentication/keeping-your-account-secure/managing-your-personal-access-tokens) limited to the repositories and permissions you need.

The other provider types have a scope too, with two choices: **Read only** (the default: the type's profile as shipped) and **Allow writes** (every request to the type's own destinations, such as uploads to Hugging Face or creating a pod on RunPod; it never adds a destination). A provider is shared by every sandbox it is attached to, so its scope applies to all of them; Change scope lists them.

The Atlassian (MCP) provider's choices are about MCP tools instead: **Read tools, and the tools listed** (the default: only the tools that read, plus the write tools you name, for example `createJiraIssue`) and **Every tool**. A tool's arguments cannot be matched, so a named tool works on every site and project the authorization reaches; narrow the OAuth scopes too. Register the MCP with the command the providers view shows: it ends the URL with `?tools=all`, so every tool is called by its own name. Without it, Atlassian reaches most tools through `execute` tools, which name the tool in their arguments and so count as writes; an MCP registered without it can be removed with `claude mcp remove atlassian` and added again.

When an agent proposes a rule on a provider's host, approving it widens the provider's scope ([approving](network-policy.md#approving), [ADR 0014](../adr/0014-scopes-for-every-provider-type.md)).

## Custom providers

Add a custom one as YAML adds a provider type of your own as an OpenShell provider profile. Iwato lints it and runs its own checks before saving, and stores it as `profiles/ID.yaml` in the config folder (read when Iwato starts, so you can edit it with another editor). Ids starting with `iwato-` are reserved for Iwato's own.

## OAuth providers

For Atlassian, the Claude account sign-in and the xAI subscription, Iwato runs the sign-in, hands the refresh token to the gateway, which refreshes it from then on, and forgets it ([ADR 0009](../adr/0009-refresh-tokens-stay-in-the-gateway.md)). Iwato's config folder never holds a refresh token.

### Atlassian (Jira, Confluence)

Adding an Atlassian (MCP) provider asks for scopes (by default Jira and Confluence read, write and search, the user's details, and `offline_access` for refreshing) and opens Atlassian's authorization page. On another device such as a phone, after allowing, copy the address of the page that failed to open and paste it into Iwato. In the sandbox, register the MCP server for `claude` with the command the providers view shows. The providers view shows the refresh state; Refresh now refreshes at once. When the grant needs authorizing again, use Reconnect (the same for the Claude sign-in and the xAI subscription); attached sandboxes need a restart after reconnecting, since the placeholder changes.

### Claude account sign-in for Claude Code

Iwato signs in with Claude Code's own public client id (not one Anthropic issued to Iwato), as `claude auth login` does. On the PC, Sign in with Claude comes back to Iwato by itself; on a phone, use **Sign in on a phone (paste a code)** and paste the code Claude's page shows (`code#…`). The sandbox's `claude` reads `CLAUDE_CODE_OAUTH_TOKEN`; Iwato also sets the token's scopes (`CLAUDE_CODE_OAUTH_SCOPES`, in `~/.config/iwato/env`) so that `/usage` works. Open a new terminal after attaching the provider to a running sandbox. claude.ai connectors stay off: the provider does not allow their host. Check with `claude -p 'hello'`.

`claude setup-token` is what Anthropic documents for non-interactive use, so the "Claude Code (setup token)" type is recommended. Anthropic's terms govern subscription (Free, Pro, Max) OAuth tokens: see [Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance). Here only Claude Code in the sandbox uses the token, but Iwato signs in with Claude Code's client id. If the gateway's first refresh fails, Iwato says so; use the setup-token type then.

### xAI subscription for Grok Build

Iwato signs in to auth.x.ai with a device code (the same steps on a PC and a phone), using Grok Build's own public client id (not one xAI issued to Iwato). In the sandbox, Iwato sets `GROK_AUTH_PROVIDER_COMMAND` for terminals (in `~/.config/iwato/env`, read by the standard image's shell) so that `grok` uses the token. If you ran `grok login` before, run `grok logout`. Check with `grok -p 'hello'`.
