# Slack reference

States, scopes, the Slack area of the console, metrics and audit actions of the Slack controller. For the reasoning see [How a Slack pass decides](../explanation/slack-pass.md); to connect a workspace see [Connect a Slack workspace](../how-to/connect/slack-workspace.md).

## Held, retrying and reported rows

| State | Means |
|---|---|
| `held` | something is to be done and is not being done until a person acts. The reason says what: no Slack account yet, the account is deactivated or a bot's, it belongs to another workspace, no address of the person is in the owning directory's served domains, the workspace has no owner, a private channel of that name exists that the bot cannot see (invite the bot to it), an archived channel has that name (unarchive it or rename it), the visibility disagrees with the policy |
| `retrying` | a removal the directory could not vouch for this pass, or a change Slack refused (with Slack's words); tried again next pass |
| `reported` | said, never acted on: a guest, an account of another workspace, an account with no address |
| `ignored` | on the channel's `ignore` list |

A row held only because the person has no Slack account yet is shown on the
console as *waiting for them*: only the person can move it forward. Every other
hold is *needs you*.

A hold is recorded in the audit trail once, when it becomes held, as
`roster.slack_action.held`; not every pass, and not again after a restart.

## Bot scopes

The bot scopes are one list, `connection.BotScopes`, each for a method the
roster calls:

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

SYSTEMS, Slack is one entry with five tabs: Workspaces, Channels, Slack Connect,
Discovered and Apps.

**Workspaces** (`#/slack`) shows, per workspace, the connection (*not
connected*, *created, not installed*, *installed*, *scopes missing*), the team
once recorded, the owning directory, whether the controller **acts** or is in a
**dry run**, when its last pass was, how many channels it manages (a link to the
Channels tab), a breaker banner with **Confirm**, and the leavers.

**Channels** (`#/slack/channels`) lists every managed channel of every
workspace in one list (policy channels, console channels and Slack Connect
channels), with *New channel* for operators. It narrows by workspace (any side
of a channel), kind (policy, console, Slack Connect), state (ok, pending,
waiting, held, invalid, not reported; *pending* is a channel the controller is
about to create, adopt or accept) and a name or Slack-id search, reads "N of M
shown" when a filter hides some, and keeps every selection in the address
(`#/slack/channels?workspace=&kind=&state=&q=`).

**Slack Connect** (`#/slack/connect`) and **Discovered** (`#/slack/discovered`)
are described on [Slack Connect channels](../how-to/connect/slack-connect-channels.md), and
**Apps** (`#/slack/apps`) on [Slack Apps](../how-to/connect/slack-apps-catalogue.md). The old
addresses `#/slack-apps` and `#/slack-connect` still open the matching tab.

Every managed channel has a page of its own,
`#/slack/channels/<workspace>/<name>` (or by Slack id): what feeds it (internal
groups for a policy channel; directory groups and individual addresses for a
console or Slack Connect channel), its mode, each person's state and why, each
side of a Slack Connect channel, the removal breaker with its **Confirm**, and
its history from the audit trail. The pages of a directory group, an internal
group and a person link back: a directory group lists the Slack channels it
feeds and how its people stand there; an internal group lists the policy
channels that name it; a person's page has a Slack section with each channel's
state and reason, and marks the channels that list them individually. Nothing on
these pages is a credential.

Per channel the page says its mode, privacy, whether it will be created or
adopted or is held, and each person as in step, **will invite**, **will
remove**, **held** (with the reason), **retrying** or **reported**.

## Confirming a breaker from the console

A workspace or channel whose pass would remove more than half of its members
shows a red banner with the set's fingerprint behind a **Confirm** button
(operators only). Confirming writes `_confirm.<workspace>.json`, or
`_confirm.<workspace>.<channel>.json` for a channel's own breaker, into the
records ConfigMap, records `roster.slack_removals.confirmed`, and lapses after
24 hours. The console refuses a fingerprint that is not the latest report's for
that gate: a set that changed since the page was loaded needs looking at again.

The API behind the area is `SlackService` (`GetSlackStatus`,
`BeginSlackWorkspaceConnect`, `RequestSlackPass`, `ChangeSlackWorkspaceOwner`,
`DisconnectSlackWorkspace`, `ConfirmSlackRemovals`), `SlackChannelService` for
console channels (`ListSlackChannels`, `CreateSlackChannel`,
`UpdateSlackChannel`, `DeleteSlackChannel`), `SlackSharedChannelService` for
Slack Connect records (`ListSlackSharedChannels`, `CreateSlackSharedChannel`,
`UpdateSlackSharedChannel`, `DeleteSlackSharedChannel`) and `SlackAppService`
for catalogue Apps (`ListSlackApps`, `CreateSlackApp`, `InstallSlackApp`). The
directory side is `AccessService.ListServedDomains` and
`ResolveDirectoryGroups`; every call is in the
[contracts](contracts.md). The catalogue's
[Slack Apps](../how-to/connect/slack-apps-catalogue.md) are separate: they create Apps for other
purposes; this page's App is the roster's own.

## Metrics

Pushed over OTLP like the GitHub controller's: `slack_roster.passes` (by
workspace and outcome), `slack_roster.changes` (by action and whether Slack
accepted it), `slack_roster.breaker_trips`, `slack_roster.rows` and
`slack_roster.channels` (by state), `slack_roster.leavers` and
`slack_roster.shared_invalid`.

## Audit

Every action is in the audit catalogue (`internal/audit/catalogue/roster.yaml`,
version 1.7.0). The controller records for itself; the console records what it
does:

| Recorded by | Actions |
|---|---|
| the controller | `roster.slack_channel.created`, `.adopted`; `roster.slack_member.invited`, `.removed`; `roster.slack_shared.invited`, `.accepted`; `roster.slack_action.held`; `roster.slack_leaver.reported` |
| the console | `roster.slack_workspace.connected`, `.owner_changed`, `.connect_refused`, `.disconnected`; `roster.slack_app.created`, `.installed`, `.install_refused`; `roster.slack_console_channel.created`, `.updated`, `.deleted`; `roster.slack_shared_channel.created`, `.updated`, `.deleted`; `roster.slack_channel.archived`; `roster.slack_removals.confirmed` |