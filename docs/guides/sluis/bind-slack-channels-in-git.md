# Bind a Slack channel in git

Keep a channel the infrastructure owns, such as an alert channel, filled with the holders of internal groups.

## Before you start

- Connect the workspace and install its app ([connect a Slack workspace](connect/slack-workspace.md)). The workspace needs an owning directory, because people are found by its domains.

- Declare the groups in `groups` ([bindings](../../reference/sluis/policy-bindings.md#slack-channels)).

- A channel defined in both git and the console is held on both sides until one definition goes. To move it to the console, remove it from the policy first.

- Policy channels take internal groups only, through `from`. A `members:` key is refused.

## 1. Declare the channel

```yaml
slack:
  workspaces:
    acme:
      channels:
        alerts: {from: [acme:sre:member]}
        eng-private: {private: true, mode: strict, ignore: [boss@acme.example], from: [acme:eng:member]}
```

`mode: strict` needs `private: true`, because only an administrator can remove somebody from a public channel. The default `extend` only adds. An existing channel is adopted by name: a public one is joined, a private one the bot is in is managed. Visibility never changes and an archived channel is never unarchived.

```sh
sluisctl policy render policy/ -o /tmp/policy.yaml
```

A refusal names the key: an undeclared group, a malformed name, `ignore` without `strict`.

## 2. Roll out as a dry run

Roll out with the workspace absent from `controllers.slack.enabledWorkspaces`. To run one pass by hand:

```sh
sluis tick slack acme --config <file>
```

The pass reports `dry-run` rows: who would be invited, who removed, and what is held with a reason. Read the held rows and every `strict` removal. A private channel the bot cannot see is held with *invite the bot to it*.

## 3. Enable

Add the workspace key to `enabledWorkspaces` and roll out ([enable a Slack workspace](enable-slack-workspace.md)). Invitations appear in Slack, and the audit trail shows `roster.slack_*` events and `roster.slack_channel.adopted` once per adopted channel.

`strict` removes only after the directory vouches. The first pass after adopting a channel meets the breaker. More than half of the channel's members, or of the workspace's managed members, waits for an operator to confirm that set.

Verify: the channel's members are the holders of its `from` groups. Tell the owners that removing somebody from a `strict` channel by hand is undone at the next pass unless they are in `ignore`.

## Roll back

Remove the key from `enabledWorkspaces`. Nothing is undone, and this is the emergency stop. To drop a channel, remove it from the policy and roll out.

## Decided in

[ADR 0019](../../decisions/0019-two-kinds-of-slack-channel-never-mixed.md), [ADR 0020](../../decisions/0020-hold-on-double-definition-instead-of-taking-over.md).
