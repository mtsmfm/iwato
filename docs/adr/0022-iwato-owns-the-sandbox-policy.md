# 0022. Iwato owns each sandbox's policy; providers bind credentials per endpoint

- Status: Proposed
- Date: 2026-10-30

## Context

- Iwato mirrors OpenShell's incremental policy merges and keeps provider rules in per-provider generated profiles ([ADR 0006](0006-providers-own-credentialed-hosts.md), [ADR 0014](0014-scopes-for-every-provider-type.md)). Policy and provider layers compete for the same hosts (endpoint ambiguities, credential conflicts), a scope change waits for OpenShell's next provider sync, and every edit asks the gateway for the effective policy again (`policy_reads.go`).
- OpenShell keeps every policy revision and every decided proposal in full; a proposal embeds two complete effective policies (`draft_policy_chunk`, `sandbox_policy` in its `objects` table). Large policies make proposals slow and once broke `policy.local`'s `/wait` (gRPC size limit).
- OpenShell (v0.1.6) supports endpointless provider profiles whose static credentials are bound by the policy: `credential_binding: {provider}` on a network endpoint, scoped to that endpoint's host, port and path. Overlapping endpoints may differ in binding only when their path specificity differs; the more specific endpoint routes the request (`openshell-policy` `ambiguity.rs`). OAuth-refreshed credentials bind the same way. `policy.local` proposals cannot carry a binding, and OpenShell's proposal prover does not see bound credentials, so its `credential_reach_expansion` check and `proposal_approval_mode=auto` are blind to them.
- Program matching (`binaries`) matches the connecting executable or any ancestor and every process runs as the same user, so any program that starts others (shells, interpreters, git, gh, agents) extends a rule to everything. It is not a boundary; host, L7 rules, bindings and the credential's own scope are. Gateway sandboxes cannot turn program matching off.
- Each policy revision carries immutable `provenance` (`UpdateConfigRequest.annotations`); provider profiles carry free `annotations`.
- Alternatives: keep layering provider profiles (the conflicts above stay); OpenShell's auto-approval for GET (passes any method to public hosts and cannot see bound credentials); OSV malicious-package deny rules (thousands of rules, late compared with package-manager cooldowns); layered policies rebased under approved changes (more than needed now).

## Decision

- Iwato is the source of truth for each sandbox's network policy. It keeps the policy as one OpenShell policy YAML in a per-sandbox git repository in Iwato's data folder, one commit per change with the reason as its message, and applies the whole policy to OpenShell with the commit in the revision's `provenance`. History, diffs and undo come from git. OpenShell's policy is read only to import it, to detect changes made outside Iwato (offered as import or overwrite), and for load status.
- New sandboxes start from Iwato's presets, one per trait, chosen independently at creation: packages (registries), tool installs (mise, runtimes, GitHub's releases) and agent CLIs (which require tool installs), on by default, and reading references (GET and HEAD), off. Starting an agent CLI with its provider attached works on the first try. Container registries are left to proposals: a sandbox has no container runtime.
- Agents keep proposing through `policy.local`; the owner approves in Iwato. Each approval becomes its own rule whose endpoint carries the approved path, never a merge into an existing rule. OpenShell's automatic approval stays off.
- Rules Iwato writes allow every program (`binaries: [{path: "/**"}]`); the proposing program is shown for information only.
- Built-in provider profiles are endpointless. A profile lists the hosts its credential is meant for in the annotation `iwato/credential-hosts` (`host:port`, comma separated). Attaching a provider adds its initial endpoints with `credential_binding` (GitHub: reads and clone; Claude Code, Grok Build, Codex: what their API needs; Atlassian: MCP read tools). When a proposal's host is in an attached provider's `iwato/credential-hosts`, the approval offers attaching that provider's credential to the new endpoint (off by default); the policy screen can attach or remove it later per endpoint. Profiles a user registers as YAML are used as written.
- The approval shows Iwato's own checks as warnings, never as blocks: the host's answer from a malware-filtering resolver (1.1.1.2), its registration date by RDAP (under 30 days), a short list of exfiltration services, uninspected (L4) rules, and broad write rules. OpenShell's L4 drafts open narrowed to GET and HEAD.
- The sandbox image sets package-manager defaults against malicious releases: a release-age cooldown, no install scripts, and lockfile installs.

## Consequences

- Supersedes [ADR 0006](0006-providers-own-credentialed-hosts.md) and [ADR 0014](0014-scopes-for-every-provider-type.md): provider scopes and generated per-provider profiles go away, and credential reach is decided per endpoint in the policy. [ADR 0008](0008-default-deny-network-with-policy-advisor.md) still holds; its merge handling is replaced by one rule per approval.
- Existing sandboxes are recreated, not migrated.
- OpenShell's database still grows with every revision and proposal; Iwato cannot delete them. Recreating a sandbox clears them.
- Iwato must detect equal-specificity overlaps with different bindings itself before applying, and it carries the check OpenShell's prover cannot do for bound credentials.
- The first request to a new host still fails until a rule is approved; the default presets keep that rare for common work.
- Program matching gives no protection in Iwato's rules; the file-hash pinning of rebuilt programs still applies. Turning matching off in sandboxes would need an OpenShell patch.
- Credentials the proxy cannot scope (GraphQL arguments, git push branches) are narrowed by the token itself; the provider screen says so.
