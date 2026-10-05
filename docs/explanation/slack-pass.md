# How a Slack pass decides

The Slack controller makes each Slack workspace's channels match the policy's `slack` table: a channel is bound to
groups, its wanted members are those groups' holders, and every pass the controller invites the people who belong and,
where the channel is `strict`, removes the people who do not. Since v1.63 it is a loop inside the one `sluis serve`
process, like the [GitHub controller](github-controller.md), and has no listener of its own.

This page is why it behaves as it does: what a pass reads, what it decides and what it refuses. To connect a workspace
see [Connect a Slack workspace](../how-to/connect/slack-workspace.md); for the states, scopes, metrics and audit
actions see [Slack reference](../reference/slack.md).

There are three kinds of channel. A **policy channel** is bound in git, to
internal groups. A **console channel** is an ordinary channel kept as a record
on the console, fed by directory groups and individual addresses. A **Slack
Connect channel** is a channel shared between your own workspaces, also kept as
a record on the console. A channel is one kind and is managed one way. The
console has an area for all three, under SYSTEMS, Slack: see
[the Slack area](../reference/slack.md#what-the-slack-area-shows).


How a channel is bound (`from`, `mode`, `ignore`, `adopt`, `private`) is the
[policy's Slack section](../reference/policy-bindings.md#slack-channels). This page is
what the controller does with it, and how to run it.

## What it does, every pass

For every workspace the policy declares:

1. **Reads Slack whole.** Who the bot token is (checked against the team
   recorded when the workspace was first installed), every channel the bot can
   see, the members of the channels it manages, the account for each address
   the decision needs, and pending Slack Connect invitations. A read that is not whole, such as a missing scope,
   a rate limit that outlasts every retry or a page that fails, fails the
   workspace's pass and decides nothing: a partial read must never look like a
   workspace in which nobody has an account.
2. **Asks the console which domains each directory serves**, once per pass:
   a person is looked up in a workspace by their address in the domains of the
   workspace's **owning directory**, and nowhere else (see
   [Where a workspace's team, owner and domains come from](#where-a-workspaces-team-owner-and-domains-come-from)).
   A workspace with no owner holds every person, saying *no owning directory:
   set the owner on the console*; a directory that cannot be read, or is no
   longer connected, fails the workspace's pass and changes nothing.
3. **Asks the directory who holds each bound group**, once per pass for every
   workspace, and **who is in each directory group** the console channels and
   Slack Connect channels name (see [Console channels](../how-to/connect/slack-console-channels.md)). The console answers under the digest of the policy it computed
   with, and the controller acts only on answers under its own: during a rollout
   the two restart at different moments.
4. **Derives** the wanted state, and **asks the directory to vouch** for each
   address a removal or a leaver report rests on. One question per address per
   pass, however many channels or workspaces name the person. An address the
   directory could not answer for, or answered for under another policy, is not
   vouched for, and its removal waits (row state `retrying`).
5. **Decides**, then acts if the workspace is listed in `policy.controllers.slack.enabledWorkspaces`;
   otherwise it only reports.
6. **Publishes every workspace's report together** into the ConfigMap
   `<release>-slack-status`, one key per workspace.

### Channels: created if missing, otherwise adopted by name

A bound channel is an **idempotent upsert**. When no channel of the declared
name is visible to the bot it is created; when one is, it is **adopted**: a
public channel is joined, then managed; a private channel the bot is in is
managed. Each adoption is recorded once as `roster.slack_channel.adopted`.
`adopt: <id>` stays as an optional disambiguation for a renamed channel or two
candidates. The roster never converts a channel, never unarchives one and never
makes a second channel under another name; each of those is **held**, with the
reason on the channel:

| Found | Held as |
|---|---|
| the channel's visibility differs from the declared `private` | *the channel is public in Slack but the policy says private* (or the reverse): change one of them |
| an archived channel has the name | *archived: unarchive it in Slack or rename it* |
| Slack refuses to create it as `name_taken` and no such channel is visible | *a private channel named X exists that the bot cannot see; invite the bot to it* (or unarchive it, if that is what it is) |
| the name is a Slack Connect channel | managed as a shared channel, not bound here |
| `adopt: <id>` names a channel the bot cannot see | *adopt X: the bot cannot see that channel; if it is private, invite the bot first, otherwise check the id* |
| `adopt: <id>` names a Slack Connect channel | *it is managed as a shared channel, not bound here* |
| a private channel is visible but the bot is not in it | *the bot is not in this private channel: invite the bot first* |
| the policy channel and a console record define the same channel (the same name, or the same channel id adopted) | *defined in both git and the console: held and unchanged until one definition is removed*, in the controller's report of the policy channel AND of the record; nothing on it changes in Slack. The console's own row for the record reads *held*, with the same reason |

A `strict` adopted channel removes only after the usual vouching and breakers:
the first pass after adopting it is subject to the breaker like any other, so a
channel that holds many people the policy does not name is held for an
operator's confirmation, never emptied.

### Modes

- **`extend`** (the default) only adds. Nobody is ever removed from an
  `extend` channel.
- **`strict`** (private channels only) adds and removes. It never removes
  bots or apps, the bot itself, deactivated users, guests, people of another
  workspace, anybody on the channel's `ignore` list, anybody with no address on
  the account, or anybody the directory has not vouched for.


### Breakers

A removal set that is over half of one channel's members, or over half of the
workspace's managed members, removes nobody and is reported with a
**fingerprint** of exactly that set. An operator's confirmation of that
fingerprint lets that set, and no other, go ahead; it lapses after 24 hours.
**One confirmation covers every gate its fingerprint covers**: when one channel
trips both its own breaker and the workspace's on the same set, confirming it
once is enough. A set that changed has a different fingerprint and needs its own
confirmation.

### Leavers

A person gone from the directory (authoritatively: not found, or suspended) who
is still an active member of a managed channel is a **leaver**. The report lists
them with the channels they are in, and the audit trail records
`roster.slack_leaver.reported` once. The controller removes nobody on that
account alone: a strict channel's removal rule is the only thing that removes,
and a leaver in an `extend` channel or a public channel stays there until a
person deals with it.

### Dry run until `enabledWorkspaces`

A workspace is born disabled. Every pass derives it, publishes what WOULD
change, and calls Slack for nothing that changes it and records nothing in the
audit trail. Enabling one is a reviewed change to `policy.controllers.slack.enabledWorkspaces`; removing
it again stops the controller acting in it, and undoes nothing. The console's
own Slack calls (connect, disconnect, revoke) are not part of a pass and are not
gated by `enabledWorkspaces`. Archiving on delete follows the switch: it is offered, and
accepted, only for a workspace whose controller reports that it acts. Remove a
workspace from `enabledWorkspaces` before removing it from the policy: the controller
refuses to start naming a key the policy does not declare, and the chart refuses
to render it.

### What it never does

- **Create accounts.** A person with no Slack account is a held row until they
  have one.
- **Touch user groups.** Only channel membership.
- **Remove anybody from a public channel.** Slack lets only administrators do
  that; `strict` on a public channel is refused when the policy loads.
- **Change a channel's visibility**, invite guests, or act on a bot, an app or
  a deactivated account.
- **Act on a partial read or on another policy's answer.**

## Failure is per workspace

A workspace that is not connected, or is created and not yet installed (no bot
token), is reported `waiting` with no error over the last report that had rows:
it is a state a workspace passes through, not a fault, and the console shows a
neutral note. A workspace whose read of Slack failed, whose owning directory
cannot be read or is not connected, or whose bot token belongs to another team
than the one recorded, is reported `failed` with the reason over the last report
that had rows, so the page does not blank. Neither stops the other workspaces'
passes.

## Where a workspace's team, owner and domains come from

The policy names a workspace by its **key** and binds channels in it. A key is
lowercase letters, digits and `-`, at most 40, starting and ending with a
letter or digit. It does
not say which Slack team the key stands for, which directory owns it or which
domains its people use: sluis knows each of those already, and a
second copy in a file would only drift from the first. A policy that still
carries `team_id`, `domains` or `owner` is refused at load, with a message
saying so.

| Fact | Where it comes from | How to change it |
|---|---|---|
| **owner**: the connected directory the workspace belongs to | chosen when the workspace is connected, and recorded in its connection record | the installation-wide operator's **Change owner**, on the workspace's card on the Workspaces tab |
| **team**: the Slack workspace the key stands for | the team `oauth.v2.access` reports at the **first install**; every later install or reconnect must match it (a token for another team is revoked and refused) | disconnect and connect again |
| **domains** a person is looked up by | the domains the **owning directory serves**, read from the console every pass | change what the directory serves (the directory's own page) |

Who may connect a workspace nobody has connected yet, and who then owns it:

- the **installation-wide operator** chooses the owning directory from the
  connected directories, or *none*;
- an operator of **exactly one** connected directory owns what they connect:
  the form shows that directory and asks nothing;
- an operator of **several** connected directories chooses among theirs;
- anybody else, and anybody who names a directory they do not operate, is
  refused.

The owner is recorded with the connect audit record
(`roster.slack_workspace.connected` carries `owner`). Once recorded it is
changed only by the installation-wide operator, and the change is its own
audit record (`roster.slack_workspace.owner_changed`, with the previous and
new owner). The console names an owning directory by its workspace id and every
domain it is authoritative for, for example `C0example — acme.example,
globex.example`; served domains that are not authoritative are left out.

Connecting a workspace nobody has connected records the **connecting
operator's directory** as its owner: whoever connects it first owns it. That
is a rule about who operates the connection inside sluis; Slack itself
still requires an owner or administrator of the target workspace to approve
the App, so the console grants nothing in Slack. The installation-wide
operator can change the owner afterwards, and the change is audited.