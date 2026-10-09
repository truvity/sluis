# Connect a Slack workspace

Connect a Slack workspace and run its controller. See [How a Slack pass decides](../../../concepts/sluis/slack-pass.md), the [Slack reference](../../../reference/sluis/slack.md) and the [policy's Slack section](../../../reference/sluis/policy-bindings.md#slack-channels). See also [console channels](slack-console-channels.md) and [Slack Connect channels](slack-connect-channels.md).

## Before you start

- Declare the workspace in the policy's `slack.workspaces`.

- Set `config.store: kubernetes`.

- Team, owner and domains are [recorded on connect](../../../concepts/sluis/slack-pass.md#where-a-workspaces-team-owner-and-domains-come-from).

## 1. Connect

1. On [api.slack.com/apps](https://api.slack.com/apps) choose **Generate Token** under **Your App Configuration Tokens**. The `xoxe.` token expires in 12 hours, is used once and is never stored or logged.
2. Press **Connect** on the workspace's card, choose the owning directory if offered, and paste the token. The console creates the App.
3. An owner approves on Slack. Slack returns to `/connect/slack/workspace/callback` and the console keeps the bot token in `Secret <release>-slack-credentials`.

Later installs are kept only for the first team. Another team's token is revoked (`auth.revoke`) and `roster.slack_workspace.connect_refused` is recorded.

## 2. Run the controller

```yaml
config:
  controllers:
    slack:
      consoleURL: http://sluis.access.svc:8080/console   # this release's own Service
      interval: 15m                                      # default
policy:
  controllers:
    slack:
      enabledWorkspaces: []   # nothing changes until a workspace is listed
exchange:
  clusters:
    - name: prod        # the service verifies the controller's token against this key set
      issuer: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE
      jwksUri: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE/keys
```

```yaml
groups:
  all:access-roster:viewer:
    matchers:
      - service_account: { cluster: prod, namespace: access, name: sluis }   # the controller runs as the release's ServiceAccount
```

The chart refuses an undeclared `enabledWorkspaces` entry. With `audit.*` set, map `<release>` to the audit source `roster`. Allow `slack.com:443` in egress for pods `app.kubernetes.io/name: sluis`. To act, follow [Enable a Slack workspace](../enable-slack-workspace.md).

## 3. Run a pass now

A changed credential or channel record starts a full pass within about two minutes. **Refresh** (operators only) writes the marker `_pass.<workspace>.json` into the records ConfigMap; a second request within 60 seconds is refused. The marker is not a record and is left out of the recovery copy.

## 4. Reconnect or disconnect

**Reconnect** rotates the token or grants new scopes; on **scopes missing** it asks for a fresh configuration token.

**Disconnect** revokes the bot token, deletes the credential, record and confirmation, and records `roster.slack_workspace.disconnected`. If Slack will not revoke, **Forget anyway** forgets it and the audit record says so.

Disconnect keeps `_channel.<workspace>.*` records and hosted Slack Connect records. Delete them first, or they apply to the next team connected under that key.

## What the controller reads

The controller calls the console API (`ListHolders`, `ResolveDirectoryGroups`, `Explain`, `ListServedDomains`) and reads mounted volumes:

| Object | Holds |
|---|---|
| Secret `<release>-slack-credentials` | `<workspace>.json`: app id, client id and secret, the bot token once installed |
| ConfigMap `<release>-slack-workspaces` | Workspace records, `_channel.*`, `_shared.*`, `_confirm.*` and `_pass.*` |

Both are optional. The mirror Secret `<release>-slack-records` feeds the `slackState.push` recovery copy ([back up](../operate/back-up-and-restore.md#1-know-what-there-is)).
