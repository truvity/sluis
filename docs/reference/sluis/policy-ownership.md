# Policy: the service's own two groups, and who owns what

The groups sluis reads for itself, how a workspace id narrows them, and how a GitHub organisation or Slack workspace gets an owner, part of [the policy](policy.md).

## The service's own two groups

`all:access-roster:operator` and `all:access-roster:viewer` are the only group names the service reads for itself. They are legacy identifiers, renamed in v1.75–v1.76 but kept: [ADR 0035](../../decisions/0035-renamed-to-sluis.md). The service also reads `sluis` in place of the thing: `all:sluis:operator`, `all:sluis:viewer` and the scoped forms. Both confer the same roles, and `sluisctl render` carries either. A policy that names both holds the union. The old spelling is deprecated and goes in v1.76. An identity is an operator because the policy puts it in the operators group.

A workspace id in the scope position scopes the role to that tenant:

```yaml
groups:
  all:access-roster:operator:                     # the whole installation
    members: [platform-admins@a.example]
  C0northern:access-roster:operator:              # one directory only
    members: [it-admins@north.example]
  C0northern:access-roster:viewer:
    matchers: [{ email_domain: north.example }]
```

| Rule | Value |
|---|---|
| Scope | naming convention over the ordinary table; only the two roles read it |
| `north.example:k8s:admin` | an ordinary group; grants nothing over a workspace |
| A scope | never widens or narrows the installation-wide role |
| Recovery | not scoped |
| `groups_delimiter` | never set on the service's own client ([`groups_delimiter`](policy-clients.md#groups_delimiter)) |

| Action | Installation-wide role | Scoped role |
|---|---|---|
| connect a directory, upload a key | yes | no |
| edit the policy or the OAuth client | nobody (git file, Secret) | nobody |
| reconnect, probe, refresh, choose domains or groups, disconnect | every workspace | the named one |
| list directories, groups, people | every workspace | the named ones |

## Who owns a GitHub organisation

The directory that owns an organisation is recorded at connect time. The policy names an organisation by login, binds its teams and carries no `owner`. A policy with `github.<org>.owner` is refused at load: delete the key.

The same rule applies to a Slack workspace.

| Caller connecting an unconnected organisation | Owner recorded |
|---|---|
| installation-wide operator (`all:access-roster:operator`) | chosen on the connect form: any connected directory, or none |
| operator of one connected directory (`<id>:access-roster:operator`) | that directory, unasked |
| operator of several connected directories | chosen among them; required |
| operator of an unconnected directory, a viewer, anyone else | refused |

| Fact | Value |
|---|---|
| Audit | `roster.github_org.connected` carries `owner` |
| Change owner | installation-wide operator only (`ChangeGitHubOrganisationOwner`, *Change owner* button); audited as `roster.github_org.owner_changed` |

| An organisation... | may be operated by |
|---|---|
| connected with an owner | `<owner>:access-roster:operator` or `all:access-roster:operator` |
| connected with none, or from before owners were recorded | `all:access-roster:operator` alone |
| not connected | connecting: installation-wide operator, or an operator of a directory |

| Rule | Value |
|---|---|
| Operated means | connect, reconnect, disconnect, confirm removals, runner Apps, catalogue Apps and their recent tokens, finishing a connect on GitHub's return |
| Role check on return | repeated; the flow's owner travels in the signed state |
| `ImportGitHubLinks` | any directory operator; adopts an account only on evidence from an organisation that caller may operate |
| Seen | a scoped viewer sees organisations its workspace owns, their Apps and reports |
| Link App and links | belong to every organisation; installation-wide viewer only |
| Scoped role over a workspace owning none | refused the GitHub pages |

How: [connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md).

## Who owns a Slack workspace

The policy knows a workspace by its key and bound channels. Three facts come from run time.

| Fact | Source |
|---|---|
| owner directory | chosen at connect by the rule above; recorded in the connection |
| Slack team | `oauth.v2.access` at first install; later installs must match or are revoked and refused; the controller acts only while `auth.test` agrees |
| domains | those the owning directory serves, read every pass, never copied |

| Rule | Value |
|---|---|
| Change owner | installation-wide operator only (`ChangeSlackWorkspaceOwner`); audited as `roster.slack_workspace.owner_changed` |
| No owner | no domains; every person held with *no owning directory: set the owner on the console*; nobody invited |
| Owning directory unreadable or disconnected | the workspace pass fails and changes nothing |
| Operated means | connecting, creating, installing, reinstalling, finishing an install on Slack's return; also its [catalogue Apps](../../guides/sluis/connect/slack-apps-catalogue.md) |
| Catalogue App create | needs a connected workspace with a recorded team; else *connect the workspace first* |
| Seen | follows the recorded owner, as for GitHub |
| Refused at load | `slack.workspaces.<key>.team_id`, `.domains`, `.owner`; the message says where each comes from |

| A Slack workspace... | may be operated by |
|---|---|
| connected with an owner | `<owner>:access-roster:operator` or `all:access-roster:operator` |
| connected with none | `all:access-roster:operator` alone |
| not connected | connecting: installation-wide operator, or an operator of a directory |
