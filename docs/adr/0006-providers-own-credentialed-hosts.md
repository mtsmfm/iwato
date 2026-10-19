# 0006. Providers own the hosts their credentials reach; policies cover the rest

- Status: Accepted
- Date: 2026-10-17

## Context

- OpenShell separates two checks (`docs/how-it-works/providers/profiles.mdx` in OpenShell):
  - network policy authorizes traffic (host, method, path, protocol operation);
  - a credential binding authorizes using one provider instance's credentials at an endpoint. It comes from the provider profile's `endpoints` (host, port and canonical path, e.g. `/v1/**`). A request carries a credential only when both pass, and a binding never widens the policy.
- A provider profile also brings its own network rules (`_provider_*`) when attached.
- Iwato's `iwato-github` profile binds the token to all of `api.github.com`, `github.com` and `uploads.github.com`, with read-only rules. Writes come from the policy: approved proposals, or the "write to one GitHub repository" preset first written with the YAML presets (`presets/`). Because the binding covers the whole host, the token rides along with whatever the policy allows, so the policy effectively decides where the token can write. Policy and provider both shape the same hosts.
- What L7 rules can express (`docs/how-it-works/policies/network-rules.mdx`, `schema.mdx`):
  - REST: method and path, so GitHub writes can be scoped per repository (`/repos/OWNER/REPO/**`, `/OWNER/REPO.git/git-receive-pack`).
  - GraphQL: operation type, operation name and top-level fields (e.g. queries plus the `createPullRequest` mutation). Arguments and variables are not matched, so a mutation cannot be limited to a repository.
  - MCP: JSON-RPC method and tool name (e.g. allow `tools/call` for read tools only). Tool arguments are not matched.
- Wanted: saying which repositories are reachable on the GitHub provider itself, a simple editor for the common cases, YAML for the details, and custom providers and policies entered as YAML in the UI.

## Decision

- Everything on the hosts a provider's credentials reach belongs to the provider: where the credential binds and what is allowed there. Policies (presets, proposals, hand-added rules) cover hosts that carry no credential.
- A provider instance in Iwato is its credentials plus an access scope. Iwato renders the scope into an OpenShell profile for that instance (OpenShell profiles are per provider type and an instance selects one, so each scoped instance gets its own generated profile id derived from the built-in one). Binding paths follow the scope, so the token is used only where the scope says.
- The scope is expressed per protocol:
  - REST: methods and paths (per repository for GitHub);
  - GraphQL: operation types and allowed operation or field names;
  - MCP: allowed methods and tool names.
  Where arguments cannot be matched (GraphQL mutations, MCP tools), the UI says the permission applies to everything the credential can reach, and the docs recommend narrowing the credential itself (e.g. a fine-grained GitHub token limited to repositories). Iwato's scope is defence in depth over the credential's own scope.
- GitHub's simple scopes (the first built-in example):
  - read-only (REST GET, git clone/fetch, GraphQL queries) — the default, same as today;
  - write to listed repositories (git push and REST writes under each `OWNER/REPO`; no GraphQL mutations unless named);
  - named GraphQL mutations (e.g. `createPullRequest`), marked as not repository-scoped;
  - full write.
  Atlassian's MCP follows the same shape: read tools by default, write tools added by name.
- UI:
  - a simple editor on each provider for the common choices (read-only, which repositories, which named operations);
  - the full generated profile in a YAML editor for details. Once edited by hand, the simple editor shows that the YAML is custom and offers resetting to the simple scope;
  - custom providers (a whole profile) and custom presets (policy rules for uncredentialed hosts) are added and edited as YAML in the UI, with OpenShell's lint and Iwato's checks (ambiguity, credentials flowing uninspected) before saving. Files in the config folder (`presets/`, `profiles/`) remain the storage, so they can still be edited outside Iwato.
- A proposal that touches a provider's host is shown in the approval dialog as widening that provider's scope; approving it changes the provider's scope, not the sandbox policy.

## Consequences

- The "write to one GitHub repository" preset is removed from the built-in presets (`presets/`); repository writes become part of the GitHub provider's scope.
- Different scopes for different sandboxes need different provider instances (the token may be the same). Changing a scope re-renders that instance's profile; OpenShell applies profile changes to attached sandboxes on the next config sync.
- Rules for provider hosts no longer live in the policy, so the policy and the provider stop competing for the same host (fewer endpoint ambiguity conflicts). Existing sandboxes may already hold approved policy rules on provider hosts (e.g. an approved git push); they keep working, and the policy tab marks them as belonging to a provider's scope.
- GraphQL mutations and MCP tools cannot be scoped below the credential; this is stated in the UI rather than hidden.
- Generated profiles are Iwato's own ids (not OpenShell's built-in ones), imported like the built-in Iwato profiles today.
