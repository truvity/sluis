# Policy: GitHub teams, people, Slack channels and the access document

The tables that bind internal groups to things outside the token. Part of [the policy](policy.md). To use them:
[bind GitHub teams](../../guides/sluis/bind-github-teams.md), [bind Slack channels in git](../../guides/sluis/bind-slack-channels-in-git.md).

## GitHub teams

```yaml
github:
  globex:                                   # the organisation's login
    members: [all:globex:employee]          # in the organisation, with or without a team
    teams:
      team-platform:                        # the team's SLUG, not its display name
        members: [all:platform:engineer]
        maintainers: [all:platform:lead]
      team-security:
        members: [all:security:analyst]
  acme:
    teams:
      team-platform:
        members: [all:platform:engineer]
    ignore:                                 # left alone here, whatever the bindings say
      - admin@partner.example               # an address in a bound group nobody can take out
      - temp-owner                          # a GitHub login: a temporary owner, a break-glass seat
```

A team is a consumer of an internal group, as a client's `requires` is: the holders of these groups are the people the
team should contain. Which accounts hold a group is answered once, in `groups`.

| Key | Meaning |
|---|---|
| `members` (organisation) | people who belong in the organisation **without** a team. Being in a bound team implies membership, so this is not a list of everybody |
| `teams.<slug>.members` | internal groups whose holders are team members |
| `teams.<slug>.maintainers` | internal groups whose holders are team maintainers. A holder of a maintainer group is a maintainer even when a member group also names them |
| `ignore` | addresses or GitHub logins never managed. An ignored address is never invited and not reported as waiting to link; an ignored login is never added, removed or changed, linked or not, owner or not. Two files ignoring accounts in one organisation ignore both |

Nothing here grants anything, and none of it appears in a token: the GitHub controller reads the table and makes each
organisation match. The same team slug in two organisations is two different teams. There is no `owner` key: which
directory owns an organisation is recorded when it is connected ([policy-ownership.md](policy-ownership.md)).

| Refused | Because |
|---|---|
| a group nothing declares | the binding would name something with no meaning |
| a team with neither `members` nor `maintainers` | *remove everyone* is not expressed by leaving a list out |
| an organisation binding no group and no team | *stop managing this organisation* is expressed by removing it |
| the same team declared twice across merged files | the second would silently replace the first |
| the same organisation's `members` declared twice | the same reason |
| `github.<org>.owner` | removed in v1.41.x; the message says the owner is chosen on the console |

The console's Rules page lists a binding beside every other rule, linked to its internal group.

## People

```yaml
people:
  jdoe: [j.doe@acme.example, john@globex.example]   # one person, two companies' addresses
```

`people` says which addresses are the same person. The key is a name you choose, matching lowercase letters, digits,
`.`, `_` and `-`, starting with a letter or digit, at most 63 characters. It is generic: any reconciler that looks a
person up by their address in one domain uses it. It **only links addresses**: it never says who holds a group, and a
person listed here gains nothing until the directory puts one of their addresses in a group.

Refused: a person with no address, an address that is not one, the same address under two people or twice under one, and
a key that is not a plain name. Addresses are compared lowercased. The same person in two merged files is a clash.

## Slack channels

```yaml
slack:
  workspaces:
    acme:                                   # a key WE choose
      channels:                             # by channel NAME, as Slack spells it
        eng-private: {private: true, mode: strict, ignore: [boss@acme.example, U0123ABCD], from: [acme:eng:member]}
        ops: {from: [acme:sre:member], adopt: C0123ABCD}   # adopt an existing channel by id
    globex: {}                              # declared; its channels are bound elsewhere
```

The Slack controller reads this table. It is a loop inside the one `sluis serve` process (v1.63 on) and changes only the
workspaces listed in `controllers.slack.enabledWorkspaces` of the policy document; every other declared workspace is a
dry run. What it does with the keys, and how to run it: [Connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md)
and [the Slack reconciler](../../concepts/sluis/slack-reconciler.md).

| Key | Meaning |
|---|---|
| workspace key | ours: lowercase letters, digits and `-`, starting and ending with a letter or digit, at most 40 characters. Its Slack team, owning directory and domains are not declared here ([policy-ownership.md](policy-ownership.md#who-owns-a-slack-workspace)) |
| channel name | as Slack spells it: lowercase letters, digits, `-`, `_`, at most 80 |
| `from` | **internal groups** whose holders the channel should contain. Required. Slack user groups are out of scope |
| `private` | the channel is private. Declaring it never converts a channel's visibility |
| `mode` | `extend` (default): only add; `strict`: add and remove. `strict` needs `private: true` |
| `ignore` | people a `strict` channel never removes: addresses, or Slack user ids such as `U0123ABCD`. Refused without `mode: strict` |
| `adopt` | an optional channel id (`^[CG][A-Z0-9]{8,}$`) to disambiguate, for a renamed channel or two candidates |

A policy channel is fed by internal groups only. Individual addresses (`members`) and directory groups (`sources`) are
fields of a console channel record, not of this file; a `members` key here is refused as unknown. Shared (Slack
Connect) channels are not in the policy.

**The two kinds never mix.** The console refuses to create or edit a record for a channel this section binds (the same
name or the same `adopt` id). If both definitions exist anyway, the channel is held on both sides, *defined in both git
and the console*, and nothing on it changes until one is removed. There is no "take over from git".

**Creation and adoption.** A channel is created if missing, otherwise adopted by name: a public channel is joined, a
private one the bot is in is managed, and the adoption is recorded once as `roster.slack_channel.adopted`. Never done,
each held with its reason: converting visibility, unarchiving an archived channel of that name, and creating a second
channel when a private channel the bot cannot see holds the name.

**Removal.** `strict` removes only after the directory vouches for the answer, never removes bots or apps, the
controller's own bot, deactivated users, guests (reported, never touched) or anybody on `ignore`, and a breaker stops a
run that would remove more than half of a channel's members or of the workspace's managed members unless an operator
confirms exactly that set. A person with no Slack account yet, or no address in the workspace's domains, is held with
that reason, never an error.

| Refused | Because |
|---|---|
| a malformed workspace key or channel name | Slack or the audit record would refuse it later |
| `team_id`, `domains` or `owner` on a workspace, or `owner` on a GitHub organisation | removed in v1.41.x in favour of what sluis records at run time; the message says where each comes from |
| `mode` other than `extend` or `strict`; `strict` on a channel that is not `private` | Slack would refuse every removal from a public channel |
| `ignore` without `mode: strict`, an entry that is neither address nor Slack user id, or one listed twice | an extend channel removes nobody |
| a channel with no `from` | *empty this channel* is not expressed by leaving a list out |
| a group nothing declares | the binding would name something with no meaning |
| `adopt` not matching the pattern, or one id adopted twice in a workspace | two bindings would fight over one channel |

Across merged files a workspace merges field by field; a channel comes from one file. Bound groups count as consumed
by the unused-group lint.

## The access document

A file with an `access` key is an *access document*: the same tables, spelled as lists and camelCase, which the loader
turns into the layer it would have read had it been written by hand (`policy.ParseAccess`, called by `LoadDeclared`;
`sluisctl policy render` merges it with the other layers). The chart's values mode (deprecated) rendered `access` and
`overlay` from values; with `documents.policy` the chart takes a rendered document instead.

```yaml
version: 1
access:
  lifetimes: {default: 4h}
  groups:
    - name: env:ssh:admin
      members: [ops@example.com]
      emails: [ada@example.com]        # one `email` matcher each
      github: [{owner: example-org, visibility: private}]
      service_accounts: [{cluster: alpha, namespace: widgets, name: e2e}]
      aws: [{account: "111122223333", role: probe, path: /}]
  people: [{name: ada, addresses: [ada@example.com]}]
  slack: [{workspace: acme, channels: [{name: ops, mode: strict, from: [env:ssh:admin]}]}]
  github: [{org: example-org, teams: [{slug: platform, members: [example-org:platform:member]}]}]
  vocabulary: {scopes: [{name: alpha}], things: [{name: ssh, scopes: [alpha], roles: [{name: admin}]}]}
  clients: [{name: console, kind: confidential, secret: console-client, requires: [env:ssh:admin]}]
  clientDocuments: {origins: [assistant.example.com], requires: [env:ssh:admin]}
  resources: [{id: "https://mcp.example.com", requires: [env:ssh:admin]}]
overlay:
  groups:
    all:access-roster:operator:
      matchers: [{service_account: {namespace: sluis, name: sluis-recovery}}]
  clients:
    probe: {kind: exchange, requires: [env:ssh:admin]}
```

It adds no concept. A group's matchers are written in this order: `emails`, `github`, `service_accounts`, `aws`, then the
overlay's. A group the overlay names and the access part does not is created with only those matchers. A client the
overlay declares that the access part also declares is refused. The access document is one layer like any other: a
group, client or person declared in it and in another file is a clash, and the merged policy is validated once.

A group's `github` entry carries only the eight fields CI-job rows use (`repository`, `owner`, `visibility`, `ref`,
`ref_type`, `event_name`, `workflow_ref`, `job_workflow_ref`); `service_accounts` and `aws` only theirs; an unknown key is
refused. A client row also accepts the keys its own deployment reads (`secretKey`, `hostname`, `prefix`, `mount`,
`cluster`, `proxy`, `deliver`), which take no part in the policy.
