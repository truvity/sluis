# Policy: the service's own two groups, and who owns what

Which groups sluis reads for itself, how a workspace id in the scope position narrows them, and how a GitHub
organisation or a Slack workspace gets an owner. Part of [the policy](policy.md).

## The service's own two groups

`all:access-roster:operator` and `all:access-roster:viewer` are the only names the service reads out of the policy for
itself (the names keep the product's former name, [ADR 0035](../decisions/0035-renamed-to-sluis.md)). It holds no role
vocabulary of its own: an identity is an operator because the policy puts it in the operators group, as for any other
relying party.

A **workspace id** in the scope position makes the role held over that one tenant:

```yaml
groups:
  all:access-roster:operator:                     # the whole installation
    members: [platform-admins@a.example]
  C0northern:access-roster:operator:              # one directory only
    members: [it-admins@north.example]
  C0northern:access-roster:viewer:
    matchers: [{ email_domain: north.example }]
```

A scope is a naming convention over the ordinary table, not a column in it. Only the service's two roles read a
workspace out of the scope position; `north.example:k8s:admin` is an ordinary group and grants nothing over a workspace.

| | Installation-wide role | Scoped role |
|---|---|---|
| connect a new directory, upload a key | yes | **no**: the workspace does not exist yet |
| edit the policy or the OAuth client | **nobody**: the policy is a file in git and the client is a Secret | |
| reconnect, probe, refresh, choose domains or groups, disconnect | every workspace | the named one |
| list directories, groups and people | every workspace | only the named ones |

A scope never widens the installation-wide role and never narrows it. **Recovery is not scoped**: it exists for the day
the directory or the policy is what is broken, and a recovery scoped to one workspace could not repair the workspace
whose absence caused it. Never set a [`groups_delimiter`](policy-clients.md#groups_delimiter) on the service's own
client: it parses these names by splitting on `:`.

## Who owns a GitHub organisation

The same two groups can operate one company's GitHub organisations and not another's. Which directory owns an
organisation is **not in the policy**: it is recorded in the organisation's connection when it is connected. The policy
names an organisation by its login and binds its teams, and carries no `owner`.

**Who may connect an organisation nobody has connected yet, and who then owns it** (the same rule for a Slack
workspace):

| The caller | Owner of what it connects |
|---|---|
| the installation-wide operator (`all:access-roster:operator`) | **chosen** on the connect form: any connected directory, or none |
| an operator of exactly one connected directory (`<id>:access-roster:operator`) | that directory, without being asked |
| an operator of several connected directories | **chosen** among them, and must |
| an operator of a directory that is not connected, a viewer, anyone else | refused |

The owner is recorded with the connection and its audit record (`roster.github_org.connected` carries `owner`). Changing
it is the installation-wide operator's alone (`ChangeGitHubOrganisationOwner`, the *Change owner* button), recorded as
`roster.github_org.owner_changed`.

| An organisation... | may be operated by |
|---|---|
| connected with an owner | the `<owner>:access-roster:operator` of that workspace, **or** the `all:access-roster:operator` |
| connected with none, or before owners were recorded | the `all:access-roster:operator` alone |
| not connected yet | connecting it: the installation-wide operator, or an operator of a directory, as above |

"Operated" is every action on it: connect, reconnect, disconnect, confirm its removals, its runner Apps, the Apps the
catalogue declares for it and their recent tokens, and finishing a connect when GitHub sends the browser back (the role
is checked again there; the owner a flow will record travels in the signed state). Adopting links (`ImportGitHubLinks`)
is people's rather than an organisation's, so an operator of any directory may do it, but it adopts an account only on
the evidence of an organisation that caller may operate.

What is *seen* follows the recorded owner: a viewer scoped to a workspace sees the organisations that workspace owns,
their Apps and reports, and nothing of another company's. The link App and the links are every organisation's, so they
are the installation-wide viewer's alone. A scoped role over a workspace that owns no organisation and could connect
none is refused the GitHub pages. A policy that still carries `github.<org>.owner` is **refused at load**; delete the
key. To connect one: [how-to/connect/github-organisation.md](../how-to/connect/github-organisation.md).

## Who owns a Slack workspace

A Slack workspace is known to the policy by its key and the channels bound in it. Three facts are not in it, because
sluis knows each at run time:

| Fact | Where it comes from |
|---|---|
| the workspace's **owner**, the directory it belongs to | chosen when the workspace is connected, by the rule above, and recorded in its connection |
| the Slack **team** | recorded from `oauth.v2.access` at the first install; every later install or reconnect must belong to the same team (anything else is revoked and refused), and the controller acts with a bot token only while `auth.test` agrees |
| the **domains** a person is looked up by | the domains the owning directory serves, read from the console every pass; never copied |

Changing the owner is the installation-wide operator's alone (`ChangeSlackWorkspaceOwner`), recorded as
`roster.slack_workspace.owner_changed`. A workspace with **no owner** has no domains to look anyone up by, so every
person in it is held with *no owning directory: set the owner on the console*, and nothing is invited. If the owning
directory cannot be read or is no longer connected, the workspace's pass fails and changes nothing.

| A Slack workspace... | may be operated by |
|---|---|
| connected with an owner | the `<owner>:access-roster:operator` of that directory workspace, **or** the `all:access-roster:operator` |
| connected with none | the `all:access-roster:operator` alone |
| not connected yet | connecting it: the installation-wide operator, or an operator of a directory |

"Operated" is every action on the workspace and on its [catalogue Apps](../how-to/connect/slack-apps-catalogue.md):
connecting, creating, installing and reinstalling, and finishing an install when Slack sends the browser back. A
catalogue App is created in a workspace that is already connected: the page refuses with *connect the workspace first*
until the workspace has recorded its team. What is seen follows the recorded owner, as for GitHub. A policy that still
carries `slack.workspaces.<key>.team_id`, `.domains` or `.owner` is **refused at load**, with a message saying where the
value now comes from.
