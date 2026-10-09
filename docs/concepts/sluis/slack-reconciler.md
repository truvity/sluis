# What does the Slack controller manage?

The Slack controller is a loop in the sluis process (`controllers.slack`) with no listener, on the same [rails](github-controller.md#reconciler-rails) as the GitHub controller. It makes each Slack workspace's channels contain the people who hold the groups bound to them. [How a Slack pass decides](slack-pass.md) covers the pass itself.

To connect a workspace, see [connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md), [Slack Connect channels](../../guides/sluis/connect/slack-connect-channels.md) and [Slack Apps](../../guides/sluis/connect/slack-apps-catalogue.md).

## Three kinds of channel

A **policy channel** is bound in git to internal groups. A **console channel** is an ordinary channel kept as a console record. Directory groups of the workspace's owning directory and individual addresses feed it. A **Slack Connect channel** is shared between your own workspaces and also kept as a console record. The console shows all three under SYSTEMS, Slack ([the Slack area](../../reference/sluis/slack.md#what-the-slack-area-shows)).

One channel is managed one way. The console refuses a record for a channel the policy binds, by name or by adopted id. A channel defined both ways is held on both sides as *defined in both git and the console*. Nothing on it changes until you remove one definition.

To move a channel to the console, remove it from the policy and Manage it from Discovered. Nested groups resolve with cycles ended, depth and size bounded, and a read cut short refuses the channel for the pass.

## Where a workspace's team, owner and domains come from

The policy names a workspace by its **key**: lowercase letters, digits and `-`, at most 40, starting and ending with a letter or digit. It declares nothing else about the workspace. A policy that carries `team_id`, `domains` or `owner` is refused at load.

| Fact | Where it comes from | How to change it |
|---|---|---|
| **owner**: the connected directory the workspace belongs to | chosen at connect, recorded in the connection record | the installation-wide operator's **Change owner** on the workspace's card |
| **team**: the Slack workspace the key stands for | the team `oauth.v2.access` reports at the first install; later installs must match, and a token for another team is revoked and refused | disconnect and connect again |
| **domains** a person is looked up by | the domains the owning directory serves, read from the console every pass | change what the directory serves |

Who connects a workspace nobody has connected owns it:

- The installation-wide operator chooses the owning directory from the connected directories, or *none*.

- An operator of one connected directory owns what they connect, and the form asks nothing.

- An operator of several connected directories chooses among theirs.

- Anybody else, and anybody who names a directory they do not operate, is refused.

The connect audit record carries `owner` (`roster.slack_workspace.connected`). Only the installation-wide operator changes it afterwards, as its own record (`roster.slack_workspace.owner_changed`, with previous and new owner). Slack still requires an owner or administrator of the target workspace to approve the App.

## Slack Connect channels

A Slack Connect record holds a name, a host, the workspaces it is shared `with`, sources, and visibility as one bool or per side. Sources are directory groups of any connected directory and individual addresses. The record keeps the channel's Slack id when sluis found the channel rather than created it.

The host creates the channel and invites each guest workspace's bot. A guest accepts the pending invitation from the host's team only. Each side then invites its own people. A person joins from the host when they have a host-domain address. Otherwise they join from the first `with` workspace, in order, where they have one. Otherwise the host holds them. A side waiting for the other is `waiting`, not held.

For a channel a record manages, a bot that does not list a side is asked once per pass, by id with `conversations.info`. It probes the workspaces Slack names as guests, or the workspaces the record names as sides when Slack names none. A channel no record manages is never probed.

## What is written

`status.Workspace` is one versioned JSON document per workspace. It holds, per channel and per person, `ok`, `will-invite`, `will-remove`, `held` with the reason, `retrying`, `reported` or `ignored`. It also holds the two breakers, the leavers and the last pass's time and outcome.

A workspace's connection is two objects. The console shows a record. Only the controller reads the credential: client id and secret, and the bot token. The token is empty between creating the app and installing it. The client secret is kept because a scope-upgrade reinstall needs it. The object names are in [Kubernetes objects](../../reference/sluis/kubernetes-objects.md).

## Decided in

- [ADR 0018](../../decisions/0018-do-not-configure-what-the-product-knows.md): do not configure what the product knows
- [ADR 0019](../../decisions/0019-two-kinds-of-slack-channel-never-mixed.md): two kinds of channel
- [ADR 0021](../../decisions/0021-slack-connect-channels-are-console-records.md): Slack Connect records
- [ADR 0023](../../decisions/0023-guest-side-probe-only-for-managed-slack-connect-channels.md): guest-side probe
