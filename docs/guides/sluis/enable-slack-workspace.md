# Enable a Slack workspace

Switch the Slack controller from dry run to acting in one workspace.

## Before you start

- Declare the workspace under `slack.workspaces.<key>` ([bindings](../../reference/sluis/policy-bindings.md#slack-channels)). Connect and install it from the console ([connect a Slack workspace](connect/slack-workspace.md)).

- Put the release's ServiceAccount in `all:access-roster:viewer` and set `config.controllers.slack`.

- Let the controller reach `slack.com:443`. The chart does not open that egress.

## 1. Read the dry run

Leave the key out of `policy.controllers.slack.enabledWorkspaces` and let a pass run. Allow up to two minutes: the controller checks credentials every 30 seconds and the kubelet needs about a minute to project a Secret. *Refresh* asks for a pass now, once per minute per workspace. Saving a console channel or Slack Connect record starts no pass.

The card says *Installed - waiting for the first pass* until a report newer than the connection exists. The pass is a dry run (`tick.outcome` is `dry-run`) and its rows are what enabling would do. Read the held and retrying rows, the leavers and *will remove*.

## 2. Enable

Add the key to `policy.controllers.slack.enabledWorkspaces` and roll out. The card says it **acts**, and `roster.slack_member.invited` or `.removed` records appear ([audit actions](../../reference/sluis/audit-actions.md)). Watch the tick series ([telemetry](../../reference/sluis/telemetry.md)).

## 3. Handle what you see

- A removal over half of a channel's managed members, or of the workspace's, is held with a fingerprint. Confirm that fingerprint within 24 hours ([breakers](../../reference/sluis/slack.md#confirming-a-breaker-from-the-console)).

- A person gone from the directory but still in a channel shows under *leavers*. Removals need the directory to vouch.

- A held row is not an error ([held rows](../../reference/sluis/slack.md#held-retrying-and-reported-rows)).

- A `failed` workspace says why: a bot token of another team, an unreadable or disconnected owning directory, an incomplete Slack read, or a console on another policy. The last good rows stay under it.

- With no owning directory, every person is held *no owning directory: set the owner on the console*.

- A refused Slack callback is audited as `roster.slack_workspace.connect_refused` or `roster.slack_app.install_refused`, and the token is revoked.

## Roll back

Remove the key from `enabledWorkspaces` and roll out. The workspace returns to dry run. Invitations, removals and created channels stay. This is the emergency stop.
