# 0008. Default-deny network, widened by Policy Advisor proposals the owner approves

- Status: Accepted
- Date: 2026-10-07

## Context

Sandboxes run coding agents that act on their own and hold (placeholder) credentials. Open egress would let an agent, or code it runs, send data or credentials anywhere. A fixed allow list cannot anticipate what each task needs (package registries, APIs, model downloads). OpenShell enforces per-sandbox network policy (host, port, program, and for inspected traffic method and path) and has the Policy Advisor: with `agent_policy_proposals_enabled`, agents get the `policy.local` API and a guide to propose rules, and OpenShell also drafts proposals from blocked connections.

## Decision

- A new sandbox's policy allows no network except the presets chosen for it (`sandboxPolicy` in `openshell.go`, `presets/`)
- Iwato turns the Policy Advisor on for every sandbox (`enablePolicyAdvisor` in `openshell_proposals.go`). Agents propose the narrowest rule they need; the owner approves, narrows or rejects it in Iwato's policy tab, and gets a Web Push notification when not looking
- The sandbox's `/AGENTS.md` tells agents how a denial looks and to propose a rule right away, narrowly, without retrying in a loop or working around the policy

## Consequences

- Nothing leaves the sandbox without the owner's approval of a rule, at the cost of approval prompts while a sandbox is new. Presets (read-only package downloads, installers) reduce them
- Iwato's approval flow has to know OpenShell's rule semantics: endpoint ambiguity, merges into existing rules, and rules that would carry provider credentials uninspected (`endpoint_ambiguity.go`, `policy_prediction.go`, ADR 0006)
- The guide does not reach the workload with upstream drivers (NVIDIA/OpenShell#3698), so Iwato supplies it (the driver today, the image per ADR 0005)
