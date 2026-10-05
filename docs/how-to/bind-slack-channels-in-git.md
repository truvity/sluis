# Bind a Slack channel in git

## Purpose

Keep a channel the infrastructure owns (an alert channel, say) filled with the holders of internal groups, from the
policy.

## Preconditions

- The workspace is connected on the console and its app is installed ([Connect a Slack workspace](connect/slack-workspace.md)).
  The workspace has an owner, because people are found by the domains of the owning directory.
- The internal groups you bind are declared in `groups` ([policy reference](../reference/policy-bindings.md#slack-channels)).
- The channel is not already a console record. The two kinds never mix.

## Before you start

- **Policy channels are fed by internal groups only** (`from`). Addresses and directory groups belong to console channels;
  a `members:` key here is refused as unknown.
- **`mode: strict` needs `private: true`.** Slack lets only an administrator remove somebody from a public channel, so a
  strict public channel is refused at load. Start with the default `extend`, which only adds.
- **A channel that already exists is adopted by name**, not created again: a public one is joined, a private one the
  bot is in is managed. Visibility is never converted, an archived channel is never unarchived, and a private channel
  the bot cannot see holding the name is held with *invite the bot to it*.
- **A channel defined in both git and the console is held on both sides** until one definition is removed. There is no
  take-over; to move a channel to the console, remove it from the policy first.
- **A workspace is born disabled.** Binding channels changes nothing until its key is in
  `controllers.slack.enabledWorkspaces`; every other declared workspace is a dry run.
- **A policy change is a new instance.** Preview before every apply, and read the preview.
- **Strict removes only after the directory vouches**, and the first pass after adopting a channel is subject to the
  breaker (more than half the channel's members, or the workspace's managed members, stops the run until an operator
  confirms that exact set).

## Steps

### 1. Declare the channel

**Run**: add to a policy file:

```yaml
slack:
  workspaces:
    acme:
      channels:
        alerts: {from: [acme:sre:member]}
        eng-private: {private: true, mode: strict, ignore: [boss@acme.example], from: [acme:eng:member]}
```

**Expect**: `sluisctl policy render policy/ -o /tmp/policy.yaml` succeeds. A refusal names the key and why (a group
nothing declares, a malformed name, `ignore` without `strict`).

**Verify**: the console's Slack area shows the channel as defined in git, with what would happen.

**Rollback**: remove the entry.

### 2. Roll out in dry run

**Run**: roll the policy out with the workspace not yet in `controllers.slack.enabledWorkspaces`. Optionally run one
pass by hand: `sluis tick slack acme --config <file>`.

**Expect**: the pass reports `dry-run` with its rows: who would be invited, who removed, and what is held with a reason.

**Verify**: read the held rows (no Slack account, no address in the workspace's domains, no owning directory) and the
removals of every `strict` channel.

**Rollback**: remove the channel from the policy and roll out.

### 3. Enable the workspace

**Run**: add the workspace key to `controllers.slack.enabledWorkspaces` and roll out (the full procedure is
[Enabling a Slack workspace](enable-slack-workspace.md)).

**Expect**: invitations appear in Slack, and the audit trail shows `roster.slack_*` events, `roster.slack_channel.adopted`
once for an adopted channel.

**Verify**: the channel's members are the holders of its `from` groups.

**Rollback**: remove the key from `enabledWorkspaces`. Nothing is undone; that is also the emergency stop.

## Afterwards

Watch the next passes for held rows. Tell the channel's owners that membership is now managed in git, and that removing
somebody from a `strict` channel by hand is undone at the next pass unless they are in `ignore`.
