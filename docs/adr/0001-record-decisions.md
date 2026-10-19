# 0001. Record design decisions as ADRs

- Status: Accepted
- Date: 2026-10-17

## Context

The reasons behind Iwato's design have so far been scattered across the architecture section of `AGENTS.md` (「構成」), code comments, the research notes in `docs/openshell/`, and PR descriptions. Ahead of the release we are making major policy decisions, such as supporting macOS and making Tailscale optional, so we want the decisions and their reasons traceable in one place.

## Decision

Major design decisions are recorded as ADRs in `docs/adr/`. The format and process are in `docs/adr/README.md`. The list of work until the release lives in `docs/TODO.md` and is referenced from the ADRs.

## Consequences

- The reasons for a decision can be read without digging through PR history. Since accepted ADRs are not rewritten, the history remains even if a decision later changes
- Writing them takes effort. Only policy decisions become ADRs; implementation details stay in code comments and `AGENTS.md` as before
- Existing decisions (the in-house WSLc driver, making `iwato-term` the first process, keeping the refresh token in the gateway, default deny + Policy Advisor, always making the home a volume) will also be turned into ADRs over time (`docs/TODO.md`)
