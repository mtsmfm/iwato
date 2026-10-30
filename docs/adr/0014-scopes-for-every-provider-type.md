# 0014. Every built-in provider type has a scope

- Status: Superseded by [0022](0022-iwato-owns-the-sandbox-policy.md)
- Date: 2026-10-18

## Context

- [ADR 0006](0006-providers-own-credentialed-hosts.md) gives a provider instance an access scope, rendered into a generated OpenShell profile of its own. Only GitHub (`iwato-github`) got one. The other built-in profiles (`profiles/*.yaml`: Claude Code, Grok Build, OpenRouter, Atlassian, Hugging Face, Civitai, Comfy Cloud, Vast.ai, RunPod) are shared by all their providers, so a write (an upload, creating a pod) could only be allowed as a policy rule on the provider's host, which ADR 0006 says belongs to the provider.
- OpenShell cannot change a provider's profile after creation, so a scope must be decided when the provider is created: existing providers of a shared profile cannot get one.
- Per-API simple scopes (like GitHub's repositories) would need knowledge of every API's paths. What every type has in common is its own endpoints and their read rules.

## Decision

- Every built-in provider type gets a scope, chosen per provider (providers are global; the scope applies to every sandbox the provider is attached to). New providers of any built-in type, including the OAuth ones (Atlassian, the xAI subscription), are created with their own generated profile `BASE-for-NAME`.
- Types other than GitHub have two modes:
  - read-only (the default): the built-in profile as it is;
  - writes allowed: every request on the profile's own endpoints (REST every method, GraphQL every operation, MCP every tool). It never adds a host, port, path or program.
  GitHub keeps its modes (read-only, repositories, named mutations, full write).
- The generated profile stays editable as YAML (custom, resettable), within the base's hosts and programs, as for GitHub.
- A proposal that is a write on such a provider's endpoints is offered in the approve dialog as switching the provider to writes allowed.

## Consequences

- Providers created before this keep their shared profile and work unchanged; the page marks them as made before scopes, to be added again to choose a scope.
- "Read-only" names the default, not a guarantee: some built-in profiles allow writes already (inference POSTs for Claude Code, OpenRouter and Grok Build; Atlassian's MCP is `access: full`).
- Writes allowed is all or nothing per provider; finer scopes (per path, per MCP tool) are done in the YAML editor.
