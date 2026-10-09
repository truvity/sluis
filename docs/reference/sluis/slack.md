# Slack reference

States, scopes, console screens, metrics and audit actions of the Slack controller.
Mechanics: [How a Slack pass decides](../../concepts/sluis/slack-pass.md). Setup: [Connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md).

## Held, retrying and reported rows

| State | Means |
|---|---|
| `held` | Work is waiting for a person. The reason names it: no Slack account yet, a deactivated or bot account, an account of another workspace, no address in the owning directory's served domains, no workspace owner, an invisible private channel of that name (invite the bot), an archived channel of that name (unarchive or rename it), or a visibility that disagrees with the policy |
| `retrying` | A removal the directory could not vouch for, or a change Slack refused with Slack's words; tried again next pass |
| `reported` | Said, never acted on: a guest, an account of another workspace, an account with no address |
| `ignored` | On the channel's `ignore` list |

The console shows a row held only for a missing Slack account as *waiting for them*; every other hold is *needs you*.
`roster.slack_action.held` is audited once, when the row becomes held.

## Bot scopes

`connection.BotScopes` is the one list.

| Scope | For |
|---|---|
| `users:read` | `users.info` |
| `users:read.email` | `users.lookupByEmail`, and the address in `users.info` |
| `channels:read` | `conversations.list`, `.info` and `.members` of public channels |
| `groups:read` | the same, for private channels the bot is in |
| `channels:manage` | `conversations.create`, `.invite`, `.kick` and `.archive` in public channels |
| `groups:write` | `conversations.create`, `.invite`, `.kick` and `.archive` in private channels |
| `channels:join` | `conversations.join`, which adopts a public channel |
| `conversations.connect:write` | `conversations.inviteShared`, `.acceptSharedInvite` |
| `conversations.connect:manage` | `conversations.listConnectInvites` |

## What the Slack area shows

SYSTEMS, Slack is one entry with five tabs.

| Tab | Address | Shows |
|---|---|---|
| Workspaces | `#/slack` | Per workspace: connection (*not connected*, *created, not installed*, *installed*, *scopes missing*), team, owning directory, **acts** or **dry run**, last pass, managed channel count, breaker banner with **Confirm**, leavers |
| Channels | `#/slack/channels` | Every managed channel of every workspace, with *New channel* for operators. Filters: workspace, kind (policy, console, Slack Connect), state (ok, pending, waiting, held, invalid, not reported), name or Slack-id search. Filters live in the address: `?workspace=&kind=&state=&q=` |
| Slack Connect | `#/slack/connect` | [Slack Connect channels](../../guides/sluis/connect/slack-connect-channels.md) |
| Discovered | `#/slack/discovered` | [Slack Connect channels](../../guides/sluis/connect/slack-connect-channels.md) |
| Apps | `#/slack/apps` | [Slack Apps](../../guides/sluis/connect/slack-apps-catalogue.md) |

*Pending* is a channel the controller is about to create, adopt or accept.

A channel page, `#/slack/channels/<workspace>/<name>`, shows:

| Part | Shows |
|---|---|
| Feed | Internal groups, or directory groups and addresses |
| Channel | Mode, privacy, and whether it is created, adopted or held |
| People | Each person as in step, **will invite**, **will remove**, **held** with the reason, **retrying** or **reported** |
| Slack Connect | Each side of the channel |
| Safety | The removal breaker with **Confirm**, and the audit history |

A person's page lists each channel's state and reason.

## Confirming a breaker from the console

A pass that would remove over half of a workspace's or channel's members shows a banner and **Confirm** (operators only).
Confirming writes `_confirm.<workspace>.json` into the records ConfigMap. A channel writes `_confirm.<workspace>.<channel>.json`. It records `roster.slack_removals.confirmed`.
It lapses after 24 hours. The console refuses a fingerprint that is not the latest report's.

## API

| Service | Calls |
|---|---|
| `SlackService` | `GetSlackStatus`, `BeginSlackWorkspaceConnect`, `RequestSlackPass`, `ChangeSlackWorkspaceOwner`, `DisconnectSlackWorkspace`, `ConfirmSlackRemovals` |
| `SlackChannelService` | `ListSlackChannels`, `CreateSlackChannel`, `UpdateSlackChannel`, `DeleteSlackChannel` |
| `SlackSharedChannelService` | `ListSlackSharedChannels`, `CreateSlackSharedChannel`, `UpdateSlackSharedChannel`, `DeleteSlackSharedChannel` |
| `SlackAppService` | `ListSlackApps`, `CreateSlackApp`, `InstallSlackApp` |
| `AccessService` | `ListServedDomains`, `ResolveDirectoryGroups` |

See the [contracts](contracts.md).

## Metrics

Over OTLP:

| Metric | Labels |
|---|---|
| `slack_roster.passes` | `workspace`, `outcome` |
| `slack_roster.changes` | `workspace`, `action`, `outcome` |
| `slack_roster.rows`, `slack_roster.channels` | `workspace`, `state` |
| `slack_roster.user_cache` | `workspace`, `result` |
| `slack_roster.breaker_trips`, `slack_roster.leavers`, `slack_roster.shared_invalid` | `workspace` |

The metric prefix is a legacy identifier, renamed in v1.75–v1.76.

## Audit

Every action is in [audit actions](audit-actions.md).

| Recorded by | Actions |
|---|---|
| controller | `roster.slack_channel.created`, `.adopted`; `roster.slack_member.invited`, `.removed`; `roster.slack_shared.invited`, `.accepted`; `roster.slack_action.held`; `roster.slack_leaver.reported` |
| console | `roster.slack_workspace.connected`, `.owner_changed`, `.connect_refused`, `.disconnected`; `roster.slack_app.created`, `.installed`, `.install_refused`; `roster.slack_console_channel.created`, `.updated`, `.deleted`; `roster.slack_shared_channel.created`, `.updated`, `.deleted`; `roster.slack_channel.archived`; `roster.slack_removals.confirmed` |
