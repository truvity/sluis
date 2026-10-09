# Enable a Slack workspace

## Purpose

Switch the Slack controller from dry run to acting in one workspace, and know how to stop it.

## Preconditions

- The policy declares the workspace under `slack.workspaces.<key>` ([policy's Slack section](../../reference/sluis/policy-bindings.md#slack-channels)).
- The release's ServiceAccount, which the controller runs as, is in `all:access-roster:viewer`, and
  `config.controllers.slack` is present.
- The workspace is connected and installed from the console ([connect a Slack workspace](connect/slack-workspace.md)).
- The console rolled out before the controller (it needs `ListServedDomains`, 1.42.0). Since v1.63 they are one process.
- The controller can reach `slack.com:443`; the chart does not open that egress.

## Before you start

- **A workspace not listed in `policy.controllers.slack.enabledWorkspaces` only reports.** Its pass is a dry run
  (`tick.outcome` is `dry-run`), and its rows are exactly what enabling would do.
- **A removal over half of a channel's managed members, or of the workspace's, is held** with a fingerprint of that set.
  Confirm names that fingerprint; it lapses after 24 hours, and a different set needs confirming again
  ([breakers](../../reference/sluis/slack.md#confirming-a-breaker-from-the-console)). The first pass after adopting a `strict`
  channel is subject to it like any other.
- **A person gone from the directory who is still in a channel shows under *leavers*** and is removed by nobody on that
  account alone; removals need the directory to vouch.
- **A held row is not an error.** Each carries its reason (no Slack account yet, a channel whose visibility differs, an
  archived channel, a private channel the bot cannot see, and so on): [held rows](../../reference/sluis/slack.md#held-retrying-and-reported-rows).
- **A workspace reported `failed` says why:** a bot token of another team than the one recorded at the first install, an
  owning directory that cannot be read or is no longer connected (set another owner on the console), a Slack read that
  was not whole, or a console answering under another policy (tried again within seconds; it clears when the rollout
  ends). The rows of the last good report are kept under it. A workspace with no owning directory is not a failure: every
  person is held *no owning directory: set the owner on the console*.
- **A callback Slack sends that is refused** (wrong team, a missing cookie) is audited as
  `roster.slack_workspace.connect_refused` or `roster.slack_app.install_refused`, and the token is revoked.

## Steps

### 1. Read the dry run

**Run** connect and install the workspace, list nothing in `policy.controllers.slack.enabledWorkspaces`, and let a pass
run. The controller looks at the credentials every 30 seconds and the kubelet may take about a minute to project a
changed Secret, so allow up to two minutes. **Refresh** on an installed workspace asks for a pass now (one request per
minute per workspace). Saving a console channel or Slack Connect record does not start a pass; it waits for the interval
or for Refresh.
**Expect** the card says *Installed - waiting for the first pass* until a report newer than the connection exists, then
shows the report.
**Verify** read the held and retrying rows and the leavers; nothing surprising is in *will remove*.
**Rollback**: none, because a dry run changes nothing.

### 2. Enable

**Run** add the workspace's key to `policy.controllers.slack.enabledWorkspaces` and roll out.
**Expect** the next pass acts. Its changes appear in the audit trail as `roster.slack_*`
([audit actions](../../reference/sluis/audit-actions.md)).
**Verify** the workspace card says it **acts**; `roster.slack_member.invited` or `.removed` records appear.
**Rollback**: step 3.

### 3. Stop

**Run** remove the key from `enabledWorkspaces` and roll out.
**Expect** the workspace returns to dry run. Nothing is undone: invitations, removals and created channels stay. This is
also the emergency stop.
**Verify** the card says **dry run**.
**Rollback**: add the key again.

## Afterwards

- Watch the tick series for the workspace ([telemetry](../../reference/sluis/telemetry.md)) and the first breaker, if any.
- Tell the workspace's owner that membership now follows the policy.
