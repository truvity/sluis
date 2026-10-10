# Policy: GitHub teams, people, Slack channels and the access document

The tables that bind internal groups to things outside the token, part of [the policy](policy.md). How: [bind GitHub teams](../../guides/sluis/bind-github-teams.md), [bind Slack channels in git](../../guides/sluis/bind-slack-channels-in-git.md).

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
    ignore:
      - admin@partner.example               # an address in a bound group nobody can take out
      - temp-owner                          # a GitHub login: temporary owner, break-glass seat
```

| Key | Meaning |
|---|---|
| `members` (organisation) | people in the organisation without a team; membership of a bound team is implied |
| `teams.<slug>.members` | internal groups whose holders are team members |
| `teams.<slug>.maintainers` | internal groups whose holders are maintainers; wins over a member group |
| `ignore` | addresses or logins never managed: never invited, added, removed or changed; files merge by union |

| Rule | Value |
|---|---|
| Effect | grants nothing; appears in no token; the GitHub controller makes each organisation match |
| Same slug in two organisations | two teams |
| Organisation owner | not in the policy; recorded at connect ([ownership](policy-ownership.md)) |
| Console | the Rules page lists each binding beside other rules |

| Refused | Note |
|---|---|
| a group nothing declares | |
| a team with neither `members` nor `maintainers` | |
| an organisation binding no group and no team | remove it to stop managing it |
| a team declared twice across merged files | |
| an organisation's `members` declared twice | |
| `github.<org>.owner` | the message says the owner is chosen on the console |

## People

```yaml
people:
  jdoe: [j.doe@acme.example, john@globex.example]   # one person, two companies' addresses
```

| Rule | Value |
|---|---|
| Purpose | says which addresses are one person; grants nothing |
| Key | lowercase letters, digits, `.`, `_`, `-`; starts with letter or digit; at most 63 characters |
| Comparison | addresses lowercased |
| Refused | person with no address; invalid address; address under two people or twice under one; invalid key |
| Merge | the same person in two files is a clash |

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

The Slack controller runs inside `sluis serve`. It changes only `controllers.slack.enabledWorkspaces`; other workspaces are dry runs. See [Connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md) and [the Slack reconciler](../../concepts/sluis/slack-reconciler.md).

| Key | Meaning |
|---|---|
| workspace key | lowercase letters, digits, `-`; starts and ends alphanumeric; at most 40 characters. Team, owner and domains: [ownership](policy-ownership.md#who-owns-a-slack-workspace) |
| channel name | as Slack spells it: lowercase letters, digits, `-`, `_`; at most 80 |
| `from` | required; internal groups whose holders the channel contains; Slack user groups unsupported |
| `private` | the channel is private; never converts visibility |
| `mode` | `extend` (default) only adds; `strict` adds and removes, needs `private: true` |
| `ignore` | addresses or Slack user ids (`U0123ABCD`) that `strict` never removes; needs `mode: strict` |
| `adopt` | channel id `^[CG][A-Z0-9]{8,}$` for a renamed channel or two candidates |

| Rule | Value |
|---|---|
| Fed by | internal groups only; `members` and `sources` belong to console channel records; `members` here is refused as unknown |
| Slack Connect channels | not in the policy |
| Git and console | console refuses a record for a channel bound here (same name or `adopt` id) |
| Defined in both | held on both sides, *defined in both git and the console*, until one is removed; no take-over |
| Creation | created if missing, else adopted by name; public channel joined, private channel the bot is in managed; adoption audited as `roster.slack_channel.adopted` |
| Never done (held with reason) | convert visibility; unarchive; create a second channel when an invisible private channel has the name |
| `strict` removes | only after the directory vouches |
| `strict` never removes | bots, apps, the controller's bot, deactivated users, guests (reported), `ignore` entries |
| Breaker | stops a run removing over half a channel's members or the workspace's managed members, unless an operator confirms that set |
| No Slack account, or no address in workspace domains | held with that reason, not an error |
| Merge | workspace merges field by field; a channel comes from one file |
| Lint | bound groups count as consumed |

| Refused | Note |
|---|---|
| malformed workspace key or channel name | |
| `team_id`, `domains` or `owner` on a workspace; `owner` on a GitHub organisation | the message says where each comes from |
| `mode` not `extend` or `strict`; `strict` on a non-private channel | |
| `ignore` without `strict`; entry neither address nor user id; duplicate entry | |
| a channel with no `from` | |
| a group nothing declares | |
| `adopt` off-pattern, or one id adopted twice in a workspace | |

## The access document

A file with an `access` key spells the same tables as camelCase lists. `policy.ParseAccess` (called by `LoadDeclared`) and `sluisctl policy render` turn it into a layer. It adds no concept. Chart `documents.policy` takes a rendered document.

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
    all:sluis:operator:
      matchers: [{service_account: {namespace: sluis, name: sluis-recovery}}]
  clients:
    probe: {kind: exchange, requires: [env:ssh:admin]}
```

| Rule | Value |
|---|---|
| Matcher order | `emails`, `github`, `service_accounts`, `aws`, then the overlay's |
| Group only in overlay | created with only those matchers |
| Client in overlay and access | refused |
| Layer | a group, client or person also declared in another file is a clash; the merged policy is validated once |
| Group `github` fields | `repository`, `owner`, `visibility`, `ref`, `ref_type`, `event_name`, `workflow_ref`, `job_workflow_ref` |
| Group `service_accounts`, `aws` | only their own fields; unknown key refused |
| Client deployment keys | `secretKey`, `hostname`, `prefix`, `mount`, `cluster`, `proxy`, `deliver`; ignored by the policy |
