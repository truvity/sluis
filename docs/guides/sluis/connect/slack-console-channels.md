# Manage Slack channels on the console

Keep an ordinary Slack channel as a console record fed by directory groups and addresses ([How a Slack pass decides](../../../concepts/sluis/slack-pass.md)).

## Before you start

- A channel is policy-bound or console-managed, never both. If both exist, both are held.

- Shared channels are [Slack Connect channels](slack-connect-channels.md).

- Groups and people come from the workspace's owning directory only.

| Kind | Declared | Fed by |
|---|---|---|
| Policy channel | `slack.workspaces[k].channels` in git | Internal groups |
| Console channel | Record `_channel.<workspace>.<name>.json` | Directory groups by address, and individual addresses |

## 1. Create a record

On the **Discovered** tab (`#/slack/discovered`) press **Manage** on a channel. Choose the mode, the `ignore` list and the sources:

- **Directory groups** are addresses such as `team@example.com`. Nested groups expand to a bounded depth.

- **Individual addresses** (`members`) must be active, known users. Set at least one of groups and members.

The people wanted are the union of both. A strict channel removes someone only when the directory confirms they are in none of its groups; otherwise they wait (`retrying`).

**Discovered** lists up to 500 unmanaged channels per workspace, without `#general` and archived ones. Filters are in [Slack Connect channels](slack-connect-channels.md#3-adopt-a-channel-that-is-already-shared).

## 2. Edit

An edit changes `mode`, `ignore`, the groups and the addresses. To change the workspace, name, channel id or visibility, create a new record and delete the old. A save takes effect within about two minutes. `strict` needs a private channel, and `ignore` needs `strict`.

## 3. Move a channel from git

1. Remove its `channels` entry from the policy.

2. After the next pass, **Manage** it from **Discovered**: keep the mode and `ignore`, and pick the groups.

A strict channel never removes past the breaker.

## 4. Delete or archive

Deleting a record leaves the channel in Slack. For a workspace in `policy.controllers.slack.enabledWorkspaces` the dialog offers *Also archive #name in Slack*, off by default. Otherwise archive by hand.

The console refuses, and keeps the record, when the report does not say the workspace acts, the channel id is unknown, or `conversations.info` says it is shared, invisible or unanswered (`FailedPrecondition` or `Unavailable`). Otherwise it deletes the record and calls `conversations.archive`, audited as `roster.slack_channel.archived`. If that fails, archive by hand.

## Verify

The page says "fed by 2 directory groups and 3 individual addresses". Owning-directory operators and the installation-wide operator may create, edit and delete. Audit actions are `roster.slack_console_channel.created`, `.updated` and `.deleted`.

Decided in [ADR 0019](../../../decisions/0019-two-kinds-of-slack-channel-never-mixed.md), [ADR 0020](../../../decisions/0020-hold-on-double-definition-instead-of-taking-over.md), [ADR 0022](../../../decisions/0022-the-console-archives-only-ordinary-channels-only-when-asked.md).
