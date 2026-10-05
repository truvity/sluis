# Back up and restore what the console holds

## Purpose

Keep a copy of what an operator connected through the console and cannot be minted again, and put it back after a loss.

## Preconditions

- `kubectl` access to the service's namespace, and a secret manager that travels with your backups.
- The names below are the Kubernetes (`legacy` State) layout. On a State adapter see
  [on a State adapter](#on-a-state-adapter).

## Before you start

- **What lands in the store is the credential.** Name a store the installation already trusts with material of that
  weight. Nothing in the service depends on the copy.
- **Restoring from a backup is a rotation.** Writing the good value back is not enough: the service reads each credential
  once, so it needs the restart in [rotate keys and credentials](rotate-keys-and-credentials.md).
- **Do not leave a pulling `ExternalSecret` in place** after a restore. The service is the writer; a pull would let a
  stale copy overwrite a freshly connected workspace.
- **Before 1.7 a namespace also holds one `<release>-credential-<tenant>` Secret per workspace.** Start-up copied each in
  by name and left the old object for a rollback; reconnecting or disconnecting that workspace removes it.

## Steps

### 1. Know what there is

Five Secrets in the service's namespace, each under a name known in advance:

| Secret | Holds |
|---|---|
| `<release>-workspace-credentials` | every console-connected workspace's credential, one key per workspace, with a copy of its record |
| `<release>-github-apps` | every connected organisation's App key and the link App's client, each with a copy of its record |
| `<release>-github-links` | people's GitHub links, tokens included |
| `<release>-github-runner-apps` | every runner App, its record beside its keys |
| `<release>-github-catalogue-apps` | every catalogue App, its record beside its keys |

Slack keeps its state in two Secrets and a ConfigMap, none of which anything upstream can re-deliver:

| Object | Holds |
|---|---|
| `Secret <release>-slack-credentials` | each connected workspace's client id and secret, and its bot token once installed |
| `ConfigMap <release>-slack-workspaces` | each workspace's record (`<workspace>.json`), the Slack Connect channels defined on the console (`_shared.<name>.json`), the console channels (`_channel.<workspace>.<name>.json`), and transient confirmations (`_confirm.*`) and pass markers (`_pass.*`) |
| `Secret <release>-slack-records` | a mirror of the records above (`<workspace>.json`, `_shared.*`, `_channel.*`), kept in the same code path that writes the ConfigMap, because a `PushSecret` reads Secrets only |

`<release>-slack-status` is derived and is not copied.

### 2. Back up

**Run** copy the objects into your secret manager. The chart renders the copy for the ones nothing upstream can
re-deliver: `directory.push` writes the whole of `<release>-workspace-credentials` and `githubApps.push` the whole of
`<release>-github-apps`, each an External Secrets `PushSecret` of the entire Secret under one remote key, off until
written ([values](../reference/chart-values.md#values)). `slackState.push` copies the two Slack Secrets, each whole
under its own remote key (`remoteKey`, `recordsRemoteKey`), with `deletionPolicy: None`. The other three GitHub Secrets
are a `PushSecret` of your own.
**Expect** a remote key per Secret in the store.
**Verify** read one back from the store and compare its keys with `kubectl get secret -o json | jq '.data | keys'`.
**Rollback**: delete the `PushSecret`; the remote keys stay until you delete them.

### On a State adapter

With `ports.adapter` other than `legacy` there are no such Secrets: the credentials are in the Secrets port, and the
service copies the five bundles into OpenBao itself, entry for entry, with `exports` of `source: bundle`
([exports](../reference/exports.md)). The copy is made at start, within seconds of a
change and every hour. A failed one is the alert `AccessRosterExportFailing`
([telemetry](../operations/telemetry.md#accessrosterexportfailing)). To restore from one, read the key (`bao kv get`,
[0013](../decisions/0013-openbao-access-through-the-bao-cli.md)), write each entry of its JSON object back as a key of
the Secret of that name, and proceed as step 3. The service does not read an export back: the Secrets port is the source
of truth, and a lost Secrets store is what the copy is for.

### 3. Restore the five Secrets

**Run** put the five Secrets back into the namespace, with the labels they carried, before the service starts or before
restarting it. Then restart.
**Expect** at start the service rebuilds every workspace ConfigMap and every GitHub record that is missing beside a
credential, then reopens the workspaces.
**Verify** the log names each workspace id; a restored workspace shows as never probed until its first probe. A link
token that rotated since the copy means that person links again; a declared Secret is re-delivered by whatever declared
it.
**Rollback**: delete the restored Secrets, and the service comes up empty again.

### 4. Restore the Slack state

**Run**

1. Put both Slack Secrets back before the service starts, with the labels they carried
   (`app.kubernetes.io/managed-by=directory-roster`, `app.kubernetes.io/part-of=<release>`, and
   `access-roster.truvity.github.io/kind` of `slack-workspaces` for the credentials and `slack-records` for the
   records). Use an `ExternalSecret` that pulls each remote key into the Secret of that name (`dataFrom: extract`),
   applied once and deleted after, or read each remote key and write its entries as the Secret's keys.
2. Start or restart the service.

**Expect** if the records ConfigMap is missing or holds no record and `slack-records` has some, start repopulates the
ConfigMap from it and logs the restored keys. A ConfigMap that has records is never added to, because a record missing
from it may have been removed on purpose; the mirror is brought up to date with it instead. The Slack controller needs no
restart: it notices the restored credentials and records within 30 seconds (the kubelet may take about a minute to
project them) and passes straight away.
**Verify** each workspace shows as never probed until that pass, then healthy. Confirmations and pass markers are not
restored: re-confirm any pending removal set.
**Rollback**: none, because the step only fills what is missing. Both copies are needed: the records say which
workspaces are connected, the credentials let the controller act.

### 5. Without a copy

**Run** a lost workspace credential: press **Connect** again as the same admin role account (the tenant id matches and
the domains return authoritative after the first snapshot). A lost GitHub App: *Disconnect* then *Create* on the App's
page, on the GitHub page's Apps tab. Slack: **Connect** each workspace again (a new throwaway configuration token, a new
App, an owner installs it).
**Expect** the team to match the one recorded at the first install, which is lost with the records: the first install
after a total loss records the new team. Console channel and Slack Connect records and each workspace's owner are lost
and must be re-entered; channels in Slack are untouched.
**Verify** the page shows the connection healthy.
**Rollback**: *Disconnect*.

## Afterwards

- Restart after any restore and confirm from the log that the credentials were read.
- Practise the restore in a test namespace once; an untested backup is a guess.
- Tell the people whose GitHub link tokens rotated since the copy to link again.
