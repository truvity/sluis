# How does a Slack pass decide?

A pass invites the people who belong in a channel and, in a `strict` channel, removes the people who do not. For channel kinds and where a workspace's owner, team and domains come from, see [the Slack controller](slack-reconciler.md).

To connect a workspace, see [connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md). For states and scopes, see the [Slack reference](../../reference/sluis/slack.md). Bindings are in the [policy](../../reference/sluis/policy-bindings.md#slack-channels).

## What a pass does

For every workspace the policy declares, a pass runs these steps:

1. **Read Slack whole.** Check the bot token against the team recorded at first install. Read every visible channel, the members of managed channels, the account for each needed address, and pending Slack Connect invitations.

2. **Ask which domains each directory serves.** A person is looked up only by an address in the owning directory's domains.

3. **Ask who holds each bound group** and each directory group a console or Slack Connect channel names. The controller acts only on answers under its own policy digest.

4. **Derive** the wanted state, and ask the directory to vouch for each address a removal or leaver report rests on. A removal on an address not vouched for waits (row state `retrying`).

5. **Decide**, then act if the workspace is in `policy.controllers.slack.enabledWorkspaces`.

6. **Publish** all reports together into the ConfigMap `<release>-slack-status`, one key per workspace.

A read that is not whole fails the workspace's pass and decides nothing. Examples are a missing `users:read.email` scope, an unrelieved rate limit and a failed page. A workspace with no owner holds every person: *no owning directory: set the owner on the console*.

## Channels are created or adopted by name

A bound channel is an idempotent upsert. If the bot sees no channel of the declared name, the controller creates it. If it sees one, the controller adopts it: it joins a public channel and manages it, or manages a private channel the bot is in.

Each adoption is recorded once as `roster.slack_channel.adopted`. Use `adopt: <id>` for a renamed channel or two candidates.

The controller never converts or unarchives a channel, and never makes a second one under another name. It holds each case with the reason on the channel:

| Found | Held as |
|---|---|
| the channel's visibility differs from the declared `private` | *the channel is public in Slack but the policy says private*, or the reverse: change one of them |
| an archived channel has the name | *archived: unarchive it in Slack or rename it* |
| Slack refuses creation as `name_taken` and no such channel is visible | *a private channel named X exists that the bot cannot see; invite the bot to it* |
| the name is a Slack Connect channel | managed as a shared channel, not bound here |
| `adopt: <id>` names a channel the bot cannot see | *adopt X: the bot cannot see that channel; if it is private, invite the bot first, otherwise check the id* |
| `adopt: <id>` names a Slack Connect channel | *it is managed as a shared channel, not bound here* |
| a private channel is visible but the bot is not in it | *the bot is not in this private channel: invite the bot first* |

## Modes

`extend` is the default and only adds.

`strict` applies to private channels and also removes. It skips bots, apps, deactivated users, guests and people of another workspace. It also skips the bot itself, anybody on the `ignore` list or with no address on the account, and anybody the directory has not vouched for. The policy load refuses `strict` on a public channel.

A newly adopted `strict` channel that holds many unnamed people waits at the breakers and is never emptied.

## Breakers

A removal set over half of one channel's members, or over half of the workspace's managed members, removes nobody. The report carries a **fingerprint** of that set. An operator's confirmation of the fingerprint lets that set go ahead, and no other, for 24 hours.

One confirmation covers every gate the fingerprint covers. A changed set needs its own.

## Leavers

A **leaver** is an active member of a managed channel whom the directory authoritatively no longer has, as not found or suspended. The report lists leavers with their channels, and the audit trail records `roster.slack_leaver.reported`.

Only a strict channel's removal rule removes. A leaver in an `extend` or public channel stays until a person deals with it.

## Dry run until enabled

A workspace starts disabled. Its passes publish what would change, make no changing Slack call and record nothing in the audit trail.

To enable a workspace, add it to `policy.controllers.slack.enabledWorkspaces` in a reviewed change. Removing it stops the controller acting and undoes nothing. Remove a workspace from `enabledWorkspaces` before you remove it from the policy, or the controller will not start and the chart will not render.

Archiving on delete is offered only for a workspace whose controller acts.

## What it never does

The controller creates no Slack account, touches no user group, and invites or removes no guest. It removes nobody from a public channel and never changes a channel's visibility. It never acts on a bot, an app, a deactivated account, a partial read or another policy's answer. A person with no Slack account is a held row.

## Failure is per workspace

A workspace that is not connected or not yet installed reports `waiting` with no error. A failed Slack read, an unreadable owning directory or a bot token from another team reports `failed` with the reason. Neither stops another workspace.

## Decided in

- [ADR 0017](../../decisions/0017-the-slack-reconciler-membership-only.md): membership only
- [ADR 0018](../../decisions/0018-do-not-configure-what-the-product-knows.md): owner and domains
- [ADR 0022](../../decisions/0022-the-console-archives-only-ordinary-channels-only-when-asked.md): archiving
