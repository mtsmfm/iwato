# Network policy and approvals

[日本語](ja/network-policy.md)

A sandbox's network is **denied by default** ([ADR 0008](../adr/0008-default-deny-network-with-policy-advisor.md)). A new sandbox can reach only what its presets allow. Everything else is refused until you approve a rule.

## How rules get proposed

Iwato turns on OpenShell's Policy Advisor for every sandbox. When an agent needs a connection, it proposes the narrowest rule it needs (the sandbox's `/AGENTS.md` and the Policy Advisor guide tell it how). OpenShell also drafts proposals from blocked connections. Proposals wait in the sandbox's **Policy** tab, with a badge on the tab and the count of pending approvals in the page title. When you are not using Iwato, a push notification tells you (proposals within one minute are grouped into one; [notifications](terminals.md#notifications)).

## The policy tab

The switch at the top of the tab, which stays in place while you scroll, chooses between the lists (**List**) and the policy as YAML (**YAML**); the browser remembers the choice. Iwato keeps each sandbox's policy, with its history ([ADR 0022](../adr/0022-iwato-owns-the-sandbox-policy.md)); the proposals are OpenShell's. The lists:

- **Waiting for approval**: proposals waiting for you, the agent's first, with one opened beside them (below them on a phone; see [Approving](#approving)). **Reject all** rejects every one after a question
- **Approved list**: the sandbox's effective policy. Rules from approved proposals can be undone (**Undo**). Rules of providers whose profile has destinations of its own (imported with OpenShell's CLI) are read-only. A rule whose destinations carry a provider's credential is marked **Credential: NAME**. For inspected endpoints, the requests the gateway passed in audit mode and the requests it blocked are listed (from the last 2,000 lines of the sandbox's log), because a blocked method or path does not become a proposal
- **Rejected proposals**

The tab also has: **Add a rule** (for every program, like approvals), adding a host or a method and path to a rule (**Add a destination**, **Add methods and paths**), removing parts of a rule, attaching or removing a provider's token on an endpoint whose host the provider's credential is meant for (**Attach / Remove NAME's token**), **History** (each change with its reason and diff; **Revert** undoes one as a new change, except the first), and **Presets**.

When the policy was changed outside Iwato (with the `openshell` CLI, for example), a banner says so: **Import** keeps that change in Iwato's policy, **Overwrite with Iwato's** puts Iwato's back. Changes in the policy tab wait until you choose; changes made elsewhere (attaching a provider, a recreate) import it first.

Decisions made elsewhere (for example with the `openshell` CLI) show up within a minute.

### Editing the policy as YAML

YAML in the policy tab shows the sandbox's rules (`network_policies`, in the format of presets) to edit as a whole, filling the tab. Edits you have not applied stay when you switch to List and back (YAML is marked Unsaved); while YAML shows, List shows how many proposals wait. Review changes checks the YAML and lists the rules it adds, removes and changes; Apply then makes the policy exactly as written. Settings fixed when the sandbox was created (`filesystem_policy`, `landlock`, `process`) are shown folded below it, read-only: OpenShell does not change them on a running sandbox. Provider rules are not in the YAML.

Saving is refused when:

- the YAML cannot be read, or a rule added or changed is incomplete (every rule needs `endpoints` and `binaries`)
- endpoints would conflict with other rules or the providers' rules (OpenShell refuses those)
- the policy changed after you loaded it (an approval, for example; the editor says so as soon as it happens). Reload to edit the current policy. Without edits, the YAML is read again by itself

If a change made directly in OpenShell (its CLI) lands while saving, it may be replaced; the tab says so.

A rule bound to a provider's credential cannot change its programs.

Approved proposals stay approved, and presets stay in the list; taking a preset out later removes only the rules that are still as it made them.

## Approving

The opened proposal shows the agent's reason (none for OpenShell's own drafts), the destination, whether OpenShell inspects the requests, and the program that proposed it, for information only: approved rules allow every program ([ADR 0022](../adr/0022-iwato-owns-the-sandbox-policy.md)).

- **What it allows**: one checkbox per method and path; unchecked ones are left out of the approval. An uninspected proposal (L4; most of OpenShell's drafts) lists the methods on every path with `GET` and `HEAD` checked, and is approved as an inspected rule with the checked methods. Its last row lets everything through uninspected instead (checked when the port is not a web one)
- **To check**: warnings, which never block the approval. Red: the host is a malware or phishing domain by Cloudflare's filtering resolver (1.1.1.2), or a service made to receive or publish arbitrary data (webhook.site, request catchers, tunnels such as ngrok, out-of-band test hosts, pastebins, file drops). Amber: the domain was registered less than 30 days ago (by RDAP), the rule is uninspected, a write on every path or on a wildcard host, internal addresses (`allowed_ips`), settings that widen an endpoint, and destinations that reach the loopback of the machine Iwato runs on. Otherwise the facts found. Iwato looks hosts up from the machine it runs on, with short timeouts, and remembers the answers for a few hours; a lookup that fails shows as unknown
- **Attach NAME's token**: shown when the approved endpoint takes requests from one that carries a provider's credential (on, for example a `git push` under GitHub's `github.com`) or when its host is one an attached provider's credential is meant for (off). See [providers](providers.md#where-credentials-go)
- **Policy diff (YAML)**: the rule the approval adds, as it will be written
- **Reject** (with an optional reason, which the agent reads) or **Approve**

A rule on Iwato's own port is refused, from the approval, a rule added by hand and the YAML editor alike. When OpenShell would refuse the rule (endpoints on the same host, port and path whose settings differ), the opened proposal says why and Approve waits. The approval is for the version you saw: when the proposal changed in the meantime, Iwato refuses it and shows the new version.

A proposal with more than Iwato can show (more than 128 rules, for example) or with settings it does not show (GraphQL, MCP, request signing) can be approved only narrowed: reject it, or write the rule in YAML.

A rejected proposal can be approved later from the rejected list, as proposed only (OpenShell edits pending proposals only).

## Presets

A preset is a named set of rules. Choose presets in the create form (Details shows each one's hosts, methods and paths), or put them in and take them out later in the policy tab (Presets: Apply, Remove). Taking a preset out removes only the rules that are still as the preset made them. Presets are kept when a sandbox is recreated. The first commit of the policy history names the presets the sandbox started from. A preset that no longer exists (removed from Iwato, or your file deleted) is marked "preset not found": its rules stay in the policy as ordinary rules, and Remove only takes it off the list. A preset another one in the sandbox requires cannot be taken out first.

Iwato's presets:

| Preset | What it allows |
|---|---|
| Packages (on by default) | read-only (GET, HEAD, OPTIONS, and the POSTs npm's audit needs) from any program: npm, PyPI, RubyGems, crates.io, Go modules. No publishing or uploads |
| Tool installs (on by default) | read-only from any program: mise, the Node.js, Go and Rust toolchains, uv's Python builds, GitHub (github.com, api.github.com, release and archive downloads, raw files) |
| Agent CLIs (on by default; brings Tool installs) | installing and updating Claude Code, Grok Build and Codex |
| Reading references | read-only (GET, HEAD) from any program: Wikipedia (English, Japanese, images), arXiv (and export.arxiv.org), MDN, docs.python.org, go.dev and pkg.go.dev, doc.rust-lang.org and docs.rs, nodejs.org, docs.ruby-lang.org and ruby-doc.org, GitHub Docs, man7.org, rfc-editor.org, `*.readthedocs.io`, Stack Overflow, Hacker News |

Presets carry no credentials: the agents' APIs come with their providers. Preset rules allow every program ([ADR 0022](../adr/0022-iwato-owns-the-sandbox-policy.md)). Tool installs reads github.com and api.github.com without a credential; with the GitHub provider attached, they are read with its token.

### Your own presets

Add, edit and delete your own presets under Presets in the policy tab (New preset, Edit YAML, Delete). They are stored as `presets/*.yaml` in the config folder, and are read again whenever the list is shown, so you can also edit them with another editor; a file that cannot be read is listed with the reason. Your presets get the id `user:ID`, so they never shadow Iwato's.

The format is the `network_policies` of OpenShell's policy YAML, plus `id`, `label`, `note` and optional `params`, `default` (`true`: checked in the create form) and `requires` (preset ids that come with it):

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

When you save, Iwato checks that the preset does not conflict with the providers' rules. A preset that allows every host cannot be made: OpenShell refuses `*` and `**` as a host, and a rule with `allowed_ips` but no host would conflict with other rules and providers on the same port.
