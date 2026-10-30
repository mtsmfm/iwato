<!-- From NVIDIA OpenShell v0.1.6 (crates/openshell-supervisor-process/src/skills/policy_advisor.md), Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES, Apache-2.0 (https://www.apache.org/licenses/LICENSE-2.0). Modified by Iwato: this comment and the section "Iwato additions" at the end were added. -->
# OpenShell Policy Advisor

Use this when OpenShell blocks a network request and the response or logs say
`policy_denied`.

## Goal

Draft the smallest policy proposal that allows the user's current task without
giving the sandbox broad new network access. The developer approves or rejects
the proposal; do not try to bypass policy.

## Local API

The sandbox-local policy API is reachable at `http://policy.local`:

- `GET /v1/policy/current` — current effective policy as YAML.
- `GET /v1/denials?last=10` — most recent network/L7 denials seen by this
  sandbox (newest first), returned as raw shorthand log lines. Each line
  carries the timestamp, class, severity, action, host/port, binary, policy
  name, and (for denied events) a short reason. Read the lines directly; you
  do not need to parse them into structured fields.
- `POST /v1/proposals` — submit a proposal. The 202 response carries
  `accepted_chunk_ids` (one ID per operation the gateway accepted) and
  `rejection_reasons` (one entry per operation the gateway refused at
  submit-time). The two arrays together account for every operation you
  sent.
- `GET /v1/proposals/{chunk_id}` — immediate state of one proposal.
  Returns `status` (`pending` / `approved` / `rejected`),
  `rejection_reason` (the reviewer's free-form note, only set on reject),
  and `validation_result` (the gateway prover's verdict on this chunk;
  may be empty).
- `GET /v1/proposals/{chunk_id}/wait?timeout=<seconds>` — block on this
  proposal until the developer decides or the timeout expires. Default
  60s, clamped [1, 300]. On timeout you get `status: "pending"` plus
  `timed_out: true`. On approval the response also carries
  `policy_reloaded: true|false` indicating whether the local sandbox has
  already loaded a policy containing the approved rule. Use this endpoint
  instead of polling `/v1/proposals/{chunk_id}`.

The proposal body takes an `intent_summary` and one or more `addRule`
operations. Each `addRule` carries a complete narrow `NetworkPolicyRule`.

## Workflow

1. Read the denial response body. Use `layer`, `method`, `path`, `host`,
   `port`, `binary`, `rule_missing`, and `detail` as evidence.
2. Fetch the current policy from `/v1/policy/current`.
3. Fetch recent denials from `/v1/denials` if the response body is incomplete.
4. Prefer L7 REST rules for REST APIs. **Proposals against hosts where no
   credential is in scope auto-approve** (see Auto-approval below). Any
   credentialed reach or capability change goes to human review — that is
   the design. L7 is still the agent-speed path because the prover can
   precisely describe the change (which method was added on which path).
   When a client can use the explicit proxy but its wire protocol is opaque
   to L7 inspection (`ssh`, `nc`, `git-remote-http`), omit `protocol` and
   retain default TLS handling. Never propose `protocol: tcp` or `tls: skip`:
   those choices bypass application-authority inspection and require an
   administrator to add the rule explicitly.
5. Draft the narrowest rule: exact host, exact port, exact binary when known,
   exact method, and the smallest safe path.
6. Submit the proposal, save `accepted_chunk_ids` from the response, and
   tell the developer what you proposed. If the response also carries
   `rejection_reasons`, the gateway refused those operations at
   submit-time before any human review; fix them and resubmit before
   waiting on the rest.
7. For each accepted chunk_id, call
   `GET /v1/proposals/{chunk_id}/wait?timeout=300` and act on the result:
   - `status: "approved"` with `policy_reloaded: true` — retry the
     original denied action. The merged policy is already loaded; the
     request should succeed. If it still fails with `policy_denied`,
     re-read the denial — your rule may not match. If it fails for any
     other reason, surface to the user.
   - `status: "approved"` with `policy_reloaded: false` — approval
     landed but the local sandbox hasn't observed the reload within the
     `/wait` window. Re-issue the same `/wait` call once with
     `timeout=30`. If the second response is still
     `policy_reloaded: false`, surface to the user rather than retrying
     blind; do not loop tightly.
   - `status: "rejected"` — read `rejection_reason` and
     `validation_result`. `rejection_reason` is what the reviewer typed;
     `validation_result` is the prover's verdict, which often explains
     a reject driven by automated checks. If either has content, address
     the specific feedback and submit a revised proposal. If both are
     empty, draft something materially different or ask the user.
   - `status: "pending"` with `timed_out: true` — call `/wait` again.
   - Any non-2xx response — surface to the user; do not retry the denied
     action without approval.

## Proposal shape

A complete narrow REST-inspected rule looks like this:

```json
{
  "intent_summary": "Allow gh to update repository contents in NVIDIA/OpenShell only.",
  "operations": [
    {
      "addRule": {
        "ruleName": "github_api_repo_contents_write",
        "rule": {
          "name": "github_api_repo_contents_write",
          "endpoints": [
            {
              "host": "api.github.com",
              "port": 443,
              "protocol": "rest",
              "enforcement": "enforce",
              "rules": [
                {
                  "allow": {
                    "method": "PUT",
                    "path": "/repos/NVIDIA/OpenShell/contents/**"
                  }
                }
              ]
            }
          ],
          "binaries": [
            {
              "path": "/usr/bin/gh"
            }
          ]
        }
      }
    }
  ]
}
```

## Auto-approval

Auto-approval is opt-in via the `proposal_approval_mode` setting,
managed through the standard settings model. Reviewers set it at the
gateway scope (fleet-wide) with `openshell settings set --global
proposal_approval_mode auto` or at the sandbox scope with `openshell
settings set <name> proposal_approval_mode auto`. The CLI's `openshell
sandbox create --approval-mode auto` is a shorthand that writes the
sandbox-scoped setting at create time. Gateway scope wins when both are
set; the default (no setting) is `"manual"`.

When auto-approval is enabled and the prover finds nothing new, the
gateway approves the chunk with actor `system:auto` and the
`CONFIG:APPROVED` audit event carries `auto=true`, `source=<mode>`,
`prover_delta=empty`, and `resolved_from=<gateway|sandbox>`. The
agent's `/wait` returns approved in ~1 second. When the prover does
find something — or the setting is `"manual"`/unset — the chunk lands
in `pending` for human review.

The prover answers four formal questions about each proposed change.
Each "yes" answer is its own categorical finding — there is no
severity grade. Any finding blocks auto-approval.

- **`link_local_reach`** — the proposal grants reach to a link-local IP
  range (`169.254.0.0/16`, `fe80::/10`) or a known metadata hostname
  such as `metadata.google.internal`. Cloud metadata endpoints like
  `169.254.169.254` live here. **Never** propose access to these —
  these endpoints serve credentials regardless of what the sandbox
  itself holds.
- **`l7_bypass_credentialed`** — the proposal lets a binary using a
  wire protocol the L7 proxy cannot inspect (`/usr/bin/git`,
  `/usr/lib/git-core/git-remote-http`, `/usr/bin/ssh`, `/usr/bin/nc`)
  reach a host where a sandbox credential is in scope. Wire protocols
  opaque to L7 are unbounded by L7 scoping; the reviewer must decide
  whether to trust the binary with the credential.
- **`credential_reach_expansion`** — the proposal grants a binary
  credentialed reach to a (host, port) it could not reach before. New
  authenticated reach is a stated intent change — the reviewer
  confirms whether the binary should be able to authenticate to the
  host at all.
- **`capability_expansion`** — the proposal adds a new HTTP method on
  a (binary, host, port) that already had credentialed reach. The
  reviewer sees exactly which method was added and decides if it's
  part of the agent's task. Mutating methods (PUT, POST, PATCH,
  DELETE) are typical sources of this finding.

What auto-approves (under `auto` mode):

- Proposals where the prover finds zero of the four categories — for
  example, L7 rules against hosts with no credential in scope
  (public-content fetches from CDNs, schema URLs, public API
  discovery).

If your proposal escalates and you'd like it to auto-approve, look
first at whether the host actually needs a credentialed binary. A
public-content GET often doesn't, and switching to a different host
(or removing the credential dependency) makes the finding go away.
Credentialed mutations are *supposed* to escalate — propose the
narrow rule and wait for review.

## Refining an earlier auto-suggested rule

When the sandbox observes a denial it cannot scope to L7 — e.g., a binary
trying to connect to a host the proxy hasn't seen at the application layer
— it auto-drafts a broad L4 proposal so the operator has something concrete
to look at. These mechanistic drafts are visible to you alongside any other
pending proposals.

If you see a pending mechanistic L4 draft you can do better than, just
submit a refined L7 proposal for the same `(host, port, binary)`. The
gateway will automatically reject the mechanistic draft with reason
"superseded by chunk X" — no extra cleanup or `supersedes_chunk_id` needed.
The new submission wins by structural overlap.

## Norms

- Do not propose wildcard hosts such as `**` or `*.com`.
- Do not propose `access: full` to fix a single denied REST request.
- Do not propose access to link-local addresses (`169.254.0.0/16`,
  `fe80::/10`) or known metadata hostnames such as
  `metadata.google.internal`. Cloud-metadata endpoints there can hand out
  the host's credentials.
- Do not propose `protocol: tcp` or `tls: skip`. If the task requires native
  TCP or a raw TLS tunnel, ask an administrator to add that access explicitly.
- Do not include query strings, tokens, credentials, or secret values in
  paths.
- Explain uncertainty in `intent_summary` instead of widening the rule.
- If pushing with `git` fails, that is a separate L4 or protocol-specific
  path from GitHub REST API access. Propose it separately.

## Iwato additions

Not part of OpenShell's guide. The owner reviews every proposal in Iwato and
wants to be asked as seldom as is safe, without a wider reach than the task
needs.

- **Versions are `*`.** Write a version number in a path (a URL path, a file
  name, a binary path) as `*`, so the rule keeps working after an update and
  the next version does not need another approval:
  `/go/go*.linux-amd64.tar.gz`, `/home/dev/.local/share/mise/installs/go/*/bin/go`,
  `/home/dev/.local/share/claude/versions/*`. `*` matches one path component;
  `**` matches several. Do not use `**` for a version.
- **Hosts where anyone can publish files** (`github.com`, `api.github.com`,
  `raw.githubusercontent.com`, `gist.github.com`, `gitlab.com`, `huggingface.co`,
  `storage.googleapis.com`, object-storage buckets, package registries that
  take uploads) say nothing about who wrote the content. Scope the path to
  the owner the task is about (organization, account or bucket id) and, when
  the task is about one repository, to that repository, and use `*` inside
  that scope for what changes from one request to the next (refs, files,
  releases, versions, issue numbers), so that the same repository does not
  need a new approval for each of them:
  - git over HTTPS: `/ORG/REPO/info/refs`, `/ORG/REPO/git-upload-pack`,
    `/ORG/REPO/git-receive-pack`. Write the path as the remote's URL has it
    (without `.git` when the URL has none). Take `ORG/REPO` from the remote or
    the task; do not guess it.
  - GitHub REST: `/repos/ORG/REPO/**` for reads, the exact path for writes.
  - raw content and archives: `/ORG/REPO/**`.
  - Use `ORG/*` (every repository of one owner) only when that owner is the
    user's own or the task names it. Never wildcard the owner itself
    (`/*/*/...`, `/**`), and never allow such a host by its name alone: any
    account on it could be the other end.
- **Read wide, write narrow.** Inside that scope, `GET`, `HEAD` and `OPTIONS`
  may use `**`. `PUT`, `POST`, `PATCH` and `DELETE` name the exact path, with `*`
  only for an id or a number (`/repos/ORG/REPO/pulls/*/merge`).
- **A binary is the executable that really runs.** The proxy matches the
  resolved path of the process (`readlink -f`), which can differ from the name
  you typed: `git-remote-https` runs as `/usr/lib/git-core/git-remote-http`.
  Read the denial's `binary` and propose that path, with `*` for any version
  in it.
- **A changed executable stays denied.** OpenShell pins the hash of each
  executable in a connection (the process and every parent) on first use. A
  denial that says "binary integrity check failed" or "executable changed"
  means a file at a pinned path was rebuilt or replaced; no rule allows it
  until the sandbox restarts. Do not retry it or propose a broader rule. For a
  program you rebuild, follow "Programs you rebuild" in `/AGENTS.md` (build
  each version to a new path and propose `/home/dev/bin/app-*`).

## Local logs (read-only)

Two local files complement the API and are useful when debugging policy
behavior:

- `/var/log/openshell.YYYY-MM-DD.log` — shorthand log of sandbox activity.
  This is what `/v1/denials` reads from.
- `/var/log/openshell-ocsf.YYYY-MM-DD.jsonl` — full OCSF JSON events, only
  written when the `ocsf_json_enabled` setting is on. Not used by
  `/v1/denials`; useful for SIEM ingestion.
