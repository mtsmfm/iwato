# Network policy and approvals

[日本語](ja/network-policy.md)

A sandbox's network is **denied by default** ([ADR 0008](../adr/0008-default-deny-network-with-policy-advisor.md)). A new sandbox can reach only what its presets allow. Everything else is refused until you approve a rule.

## How rules get proposed

Iwato turns on OpenShell's Policy Advisor for every sandbox. When an agent needs a connection, it proposes the narrowest rule it needs (the sandbox's `/AGENTS.md` and the Policy Advisor guide tell it how). OpenShell also drafts proposals from blocked connections. Proposals wait in the sandbox's **Policy** tab, with a badge on the tab and the count of pending approvals in the page title. When you are not using Iwato, a push notification tells you (proposals within one minute are grouped into one; [notifications](terminals.md#notifications)).

## The policy tab

The switch at the top of the tab, which stays in place while you scroll, chooses between the lists (**List**) and the policy as YAML (**YAML**); the browser remembers the choice. The lists show OpenShell's data only (Iwato stores none of it):

- **Waiting for approval**: proposals waiting for you. **Approve** or **Reject**
- **Allowed**: the sandbox's effective policy. Rules from approved proposals can be undone (**Undo**). Provider rules are read-only. Rules on the hosts a provider's credentials reach are marked as that provider's ("Provider X's destination"). For inspected endpoints, the requests the gateway passed in audit mode and the requests it blocked are listed (from the last 2,000 lines of the sandbox's log), because a blocked method or path does not become a proposal
- **Rejected**

The tab also has: **Add a rule**, adding a host or a method and path to a rule (**Add a destination**, **Add methods and paths**), removing parts of a rule, **History** (provider rules are not in it), and **Presets**.

Decisions made elsewhere (for example with the `openshell` CLI) show up within a minute.

### Editing the policy as YAML

YAML in the policy tab shows the sandbox's rules (`network_policies`, in the format of presets) to edit as a whole, filling the tab. Edits you have not applied stay when you switch to List and back (YAML is marked Unsaved); while YAML shows, List shows how many proposals wait. Review changes checks the YAML and lists the rules it adds, removes and changes; Apply then makes the policy exactly as written. Settings fixed when the sandbox was created (`filesystem_policy`, `landlock`, `process`) are shown folded below it, read-only: OpenShell does not change them on a running sandbox. Provider rules are not in the YAML.

Saving is refused when:

- the YAML cannot be read, or a rule added or changed is incomplete (every rule needs `endpoints` and `binaries`)
- endpoints would conflict with other rules or the providers' rules (OpenShell refuses those)
- the policy changed after you loaded it (an approval, for example; the editor says so as soon as it happens). Reload to edit the current policy. Without edits, the YAML is read again by itself

If a change made directly in OpenShell (its CLI) lands while saving, it may be replaced; the tab says so.

A rule added or changed that reaches a host where a provider's credentials go (any provider type, attached or not) is asked first, naming the providers: allowed, the credential goes with that traffic too. For GitHub, the provider's scope can allow it more narrowly. Adding a rule or methods and paths by hand, including from a provider rule's blocked requests, is asked the same way. A rule bound to a provider's credential cannot change its programs.

Approved proposals stay approved, and presets stay in the list; taking a preset out later removes only the rules that are still as it made them.

## Approving

The approve dialog shows the destination and the agent's reason first, then lets you narrow the proposal before approving:

- **Read only** or **Allow writes**: read only allows `GET`, `HEAD` and `OPTIONS` on the paths listed (none listed: every path). Allow writes sets the method per path, for example `POST` on one path (`*` is one segment, `**` is everything below); the writes it allows are listed above the paths. The dialog starts with what the proposal needs
- **Details**: the program (exactly that path, the same path with versions as `*` so an update keeps working (the default when the path has a version), names starting the same, everything under a folder, or any program; a rule also covers the processes the program starts), approving as proposed, and **Audit**: let every request through and record it, then later pick the recorded requests to allow and switch to enforcing (**Allow only the chosen ones and switch to enforce**). Use it when you do not know the paths yet

An approval applies the whole rule, so the dialog shows all of it:

- **Caution** (above the choices): a proposal that names more than one program, or every program (`/**`); settings that widen an endpoint (`tls: skip`, `allow_encoded_slash`, `websocket_credential_rewrite`, `request_body_credential_rewrite`, `allow_uninspected_credentials`); destinations that reach the loopback of the machine Iwato runs on (`host.docker.internal`, `host.wslc.internal`, `host.containers.internal`, `host-gateway` and their addresses), where Iwato itself and the published pages listen. The approval allows only the program in Details, never the proposal's other programs
- **The proposal** (below the summary): every program and endpoint with all its settings, rules and deny rules

A proposal with more than Iwato can show (more than 8 programs or 20 endpoints, for example) or with settings it does not show (GraphQL, MCP, request signing) cannot be approved: reject it, or write the rule in YAML. A rule on Iwato's own port is refused, from the dialog, a rule added by hand and the YAML editor alike. The YAML editor's confirmation names rules that reach the host's loopback.

The approval is for the version you saw: when the proposal changed in the meantime (OpenShell refines proposals as more requests are blocked), Iwato refuses it, reloads the list and asks again.

Choices OpenShell would refuse cannot be picked (the dialog says so in one line, and why under Details): OpenShell rejects rules on the same host, port and path whose inspection settings differ (endpoint ambiguity), and Iwato checks this before sending. When the gateway refuses a change anyway, Iwato explains why.

A proposal for a host where an attached provider's credentials go is handled by the provider ([ADR 0006](../adr/0006-providers-own-credentialed-hosts.md)): for a provider with a scope (GitHub), approving widens the provider's scope instead of adding a policy rule (for example a `git push` to `OWNER/REPO` adds that repository, and an Atlassian MCP tool call adds that tool), and the proposal is then rejected with the reason "retry" once the sandbox has the new scope. Other proposals on such hosts are approved as rules, with a warning that the token rides along. See [providers](providers.md#scopes).

## Presets

A preset is a named set of rules. Choose presets in the create form, or put them in and take them out later in the policy tab (Presets: Apply, Remove). Taking a preset out removes only the rules that are still as the preset made them. Presets are kept when a sandbox is recreated. A preset that no longer exists (removed from Iwato, or your file deleted) is marked "preset not found": its rules stay in the policy as ordinary rules, and Remove only takes it off the list.

Iwato's presets:

| Preset | What it allows |
|---|---|
| Install Claude Code with mise | mise's downloads (GitHub, mise-versions.jdx.dev) and Claude Code's updates and settings (downloads.claude.ai, platform.claude.com, raw.githubusercontent.com) |
| Install Grok Build with mise | mise's version check and download, and grok's own updates (x.ai, storage.googleapis.com) |
| Package downloads | read-only (GET, HEAD, OPTIONS, and the POSTs that only read: npm's audit, the registries' tokens) from any program: npm, PyPI (and uv's Python builds), crates.io, the Rust toolchains (rustup), the Go proxy, RubyGems, mise, the Node.js and Go downloads, GitHub release and archive downloads and raw files, Docker Hub and ghcr.io images. No publishing or uploads |

Presets never include hosts where a provider's credentials go (github.com, api.github.com, the agents' APIs): those belong to the providers. The only exception is the GitHub rules of the mise preset for Claude Code (mise downloads from GitHub); the list marks them.

### Your own presets

Add, edit and delete your own presets under Presets in the policy tab (New preset, Edit YAML, Delete). They are stored as `presets\*.yaml` in the config folder, and are read again whenever the list is shown, so you can also edit them with another editor; a file that cannot be read is listed with the reason. Your presets get the id `user:ID`, so they never shadow Iwato's.

The format is the `network_policies` of OpenShell's policy YAML, plus `id`, `label`, `note` and optional `params`:

```yaml
id: my-preset
label: My preset
note: What it is for
params:                  # optional, used as ${repo}
  - name: repo
    label: Repository
    placeholder: OWNER/REPO
    pattern: '[A-Za-z0-9-]+/[A-Za-z0-9._-]+'
network_policies:
  allow_example_com_443:
    endpoints:
      - { host: example.com, port: 443, protocol: rest, enforcement: enforce, access: read-only }
    binaries: [/usr/bin/curl]
```

When you save, Iwato checks that the preset contains no provider hosts and does not conflict with the providers' rules. A preset that allows every host cannot be made: OpenShell refuses `*` and `**` as a host, and a rule with `allowed_ips` but no host would conflict with other rules and providers on the same port.
