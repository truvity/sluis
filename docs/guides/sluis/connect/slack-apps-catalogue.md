# A catalogue of Slack Apps

Declare Slack Apps in values and create them from the console, as in the [GitHub App catalogue](github-apps-catalogue.md).

## Before you start

- Connect the `workspace` first on the Workspaces tab, or Create and Install fail with *connect the workspace first*.

- Set `config.store: kubernetes`.

- The service refuses to start on a bad entry: unknown key, duplicate id, bad scope, or an undeclared `workspace`.

## 1. Declare an App

```yaml
slackApps:
  - id: sync                     # [a-z0-9-], at most 32, unique; never changes
    workspace: acme              # a key of the policy's slack.workspaces
    name: acme-sync              # optional; default <workspace>-<id>, at most 35
    description: Keeps channels in step with groups   # optional; at most 140
    botScopes:                   # the bot token's scopes; at least one
      - channels:read
      - users:read
      - users:read.email
    push:                        # optional; off unless written out
      secretStore: {name: example-store, kind: SecretStore}
      remoteKey: platform/slack-apps/sync
```

The chart renders this into `ConfigMap <release>-slack-apps-catalogue`, read once at start. Scopes use Slack's names, such as `conversations.connect:write`.

## 2. Get a configuration token

On [api.slack.com/apps](https://api.slack.com/apps), signed in to the owning workspace, choose **Generate Token** under **Your App Configuration Tokens**. Copy the **Access Token** (`xoxe.xoxp-`). It expires in 12 hours, is used once and is never stored or logged.

## 3. Create and install

On the **Apps** tab (`#/slack/apps`):

1. **Create.** Paste the token. The manifest has the bot user, the `botScopes` and one redirect URL, with no events or interactivity. The App reads *created, not installed*.
2. **Install.** An owner approves on Slack, and the console exchanges the code for the bot token. A token for another team is revoked and the install refused. The App reads *installed*.

When you add a scope the row says **scopes missing**. **Reinstall** needs a fresh token to update the manifest, then an owner approves.

The owning directory's operators, or the installation-wide operator, may do this ([ownership](../../../reference/sluis/policy-ownership.md#who-owns-a-slack-workspace)).

## 4. Find the credentials

The Secret `<release>-slack-catalogue-apps` is created empty at start.

| Key | Holds | Exists |
|---|---|---|
| `<id>.record.json` | Ids, scopes, who and when. Never a credential | From Create |
| `<id>.client_id` | The OAuth client id | From Create |
| `<id>.client_secret` | The OAuth client secret | From Create |
| `<id>.slack_bot_token` | The bot token | From Install only |

Slack has no token exchange, so a consumer reads a copy. With a `ports.adapter` other than `legacy` and layout v4, read `external/slack/<id>` ([secrets](../../../reference/sluis/secrets.md#the-external-documents)). `push` is deprecated and applies only to `legacy`:

```yaml
push:
  secretStore: {name: example-store, kind: ClusterSecretStore}   # kind: SecretStore (default) | ClusterSecretStore
  remoteKey: platform/slack-apps/sync
  refreshInterval: 1h          # optional, default 1h
  deletionPolicy: None         # optional: None (default) | Delete
```

The consumer reads `bot_token` at `remoteKey`; only that key is copied. Rotate by reinstalling. The chart refuses two entries on one `remoteKey` and `push` without `config.store: kubernetes`.

## Verify

The App reads *installed*. Audit records `roster.slack_app.created`, `.installed` and `.install_refused` carry no token.

| You see | Fix |
|---|---|
| `invalid_auth` on create | The configuration token expired, was revoked or is for another workspace. Generate a fresh one |
| `invalid_manifest` on create | Slack rejected the declaration, usually a name or scope. The detail says where |
| *the policy's slack.workspaces does not name workspace* | The policy changed under a running service. Restore the key |
| *installed into workspace T... and the policy names T...* | The owner approved another workspace. Nothing was kept. Remove the App there and install from the right one |
| *declares scopes the App was created without* | Paste a token and reinstall |
| *This install did not start in this browser* | Start Install again within ten minutes in the same browser |

## Roll back

There is no **Disconnect**. Delete the App at `https://api.slack.com/apps/<app id>` and remove its keys from the Secret by hand. A removed entry stays listed as *no longer declared*.

Decided in: [ADR 0025](../../../decisions/0025-slack-apps-catalogue-keeps-credentials-mints-none.md).
