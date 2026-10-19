# Architecture Decision Records (ADR)

Each major design decision is recorded in its own file, so that later we can trace why things are the way they are.

- File names are `NNNN-kebab-title.md` (e.g. `0002-macos-docker-compatible-runtime.md`). Numbers start at 0001, go up in order, and are never reused
- An accepted ADR is not rewritten (except to fix typos or references). To change a decision, write a new ADR that names the ADR it supersedes, and set the old ADR's status to "Superseded by NNNN" (the status line is the only part that may change)
- While an ADR is Proposed it may be edited. Once accepted in a PR, set its status to "Accepted"
- Do not write detailed implementation steps or lists of open work (those belong in the PR and `docs/TODO.md`). Write what was decided, why, and the consequences

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context

What the problem is and what constrains it (point to code facts by file name). Also the alternatives considered and why they were not chosen.

## Decision

What we will do.

## Consequences

The good, the bad (costs, risks, new work needed), and open points.
```
