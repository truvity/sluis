# Back up and restore what the console holds

Keep a copy of what an operator connected through the console and cannot be minted again, and put it back after a loss.

## Before you start

- You need `kubectl` on the namespace and a secret manager. The names below are the `legacy` State layout: see [on a State adapter](#on-a-state-adapter).

- A restore is a rotation: restart afterwards ([rotate keys and credentials](rotate-keys-and-credentials.md)).

- Delete any pulling `ExternalSecret` after a restore: a stale copy would overwrite a new connection.

## 1. Know what there is

Five Secrets in the service's namespace:

| Secret | Holds |
|---|---|
| `<release>-workspace-credentials` | each console-connected workspace's credential, one key per workspace, with a copy of its record |
| `<release>-github-apps` | each connected organisation's App key and the link App's client, each with a copy of its record |
| `<release>-github-links` | people's GitHub links, tokens included |
| `<release>-github-runner-apps` | each runner App, its record beside its keys |
| `<release>-github-catalogue-apps` | each catalogue App, its record beside its keys |

Slack state:

| Object | Holds |
|---|---|
| `Secret <release>-slack-credentials` | each workspace's client id and secret, and its bot token once installed |
| `ConfigMap <release>-slack-workspaces` | workspace records (`<workspace>.json`), console Slack Connect channels (`_shared.<name>.json`), console channels (`_channel.<workspace>.<name>.json`), pending confirmations (`_confirm.*`) and pass markers (`_pass.*`) |
| `Secret <release>-slack-records` | a mirror of the records above, because a `PushSecret` reads Secrets only |

`<release>-slack-status` is derived and is not copied.

## 2. Back up

The chart renders a `PushSecret`, off until set, for the Secrets nothing upstream can re-deliver:

- `directory.push` and `githubApps.push` copy `<release>-workspace-credentials` and `<release>-github-apps` whole ([values](../../../reference/sluis/chart-values.md#values)).

- `slackState.push` copies the two Slack Secrets under `remoteKey` and `recordsRemoteKey`.

- The other three GitHub Secrets need a `PushSecret` of your own.

To verify, compare a remote key's keys with `kubectl get secret -o json | jq '.data | keys'`. To roll back, delete the `PushSecret`.

## On a State adapter

With `ports.adapter` other than `legacy` the credentials are in the Secrets port, not in these Secrets. Backup and restore cover the whole installation ([ADR 0041](../../../decisions/0041-the-secret-contract.md)); the `source: bundle` exports are retired ([exports](../../../reference/sluis/exports.md)).

## 3. Restore the five Secrets

Put the Secrets back with their labels and restart. The service rebuilds missing workspace ConfigMaps and GitHub records.

## 4. Restore the Slack state

1. Put both Slack Secrets back with the labels they carried: `app.kubernetes.io/managed-by=directory-roster`, `app.kubernetes.io/part-of=<release>`, and `access-roster.truvity.github.io/kind` of `slack-workspaces` or `slack-records`. They are legacy identifiers, renamed in v1.75–v1.76.

2. Fill each from its remote key with a one-off `ExternalSecret` using `dataFrom: extract`.

3. Restart the service. It refills an empty records ConfigMap from `slack-records` and never adds to one with records. Confirmations are not restored.

## 5. Without a copy

Reconnect instead. **Connect** the workspace as the same admin role account, *Disconnect* and *Create* the GitHub App, and **Connect** each Slack workspace with a new configuration token. Re-enter console channel records, Slack Connect records and workspace owners.

## Verify

A restored workspace shows as never probed until its first probe. A person whose link token rotated links again.
