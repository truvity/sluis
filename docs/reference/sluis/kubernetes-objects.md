# Kubernetes objects the service owns

The objects `sluis serve` writes in its namespace with `store: kubernetes`. With `ports.adapter: dynamodb` the records live in the State and these objects are not written ([store](../../concepts/sluis/store.md)).

`<release>` is the chart's full name. `<tenant>` is a readable part of the tenant id plus a short hash of it. Why a record and its credential are two objects: [Configuration: the model](../../concepts/sluis/configuration.md#a-record-and-its-credential-are-two-objects).

| Object | Holds | Written by |
|---|---|---|
| `ConfigMap <release>-workspace-<tenant>` | backend, domains, served domains, admin, connected by/at, last health, credential type | the service |
| `Secret <release>-workspace-credentials` | one entry per console-connected workspace, key `<tenant>.json`: the credential (refresh token, or service-account key) and a copy of the workspace's record without its health | the service (Connect, UploadKey), created empty at start. |
| `Secret <release>-slack-records` | mirror of `<release>-slack-workspaces`: its `<workspace>.json`, `_shared.<name>.json` and `_channel.<workspace>.<name>.json` records, never `_confirm.*` or `_pass.*`. A `PushSecret` reads Secrets only; the recovery copy and service start read this one | the service, with the ConfigMap; reconciled at start, which repopulates an empty ConfigMap from it |
| `ConfigMap <release>-slack-workspaces` | one record per connected Slack workspace (`<workspace>.json`), the Slack Connect records (`_shared.<name>.json`), the console channel records (`_channel.<workspace>.<name>.json`), and the transient operator markers: confirmations (`_confirm.*`), pass requests (`_pass.*`) and consumed install states | the service (Connect, the Slack Connect and channel editors, Refresh, Confirm), created empty at start |
| `Secret <release>-slack-credentials` | one entry per connected workspace, `<workspace-id>.json`: the App's client id and secret and, once installed, the bot token | the service (Connect, the install callback), created empty at start; read by the Slack controller in the same process |
| `Secret <release>-oauth-client` | OAuth client id and secret | declared via `oauthClient.secret.name`; read-only, the console cannot write it |
| `Secret <release>-session-key` | signs the session cookie and the consent-flow state | the service, generated on first start; rotate by deleting |
| the signing key | a PEM private key, mounted as a file | not the issuer: cert-manager issues it or external-secrets delivers it. The issuer reads the file; the key id is the key's RFC 7638 thumbprint |
| `ConfigMap <release>-policy` | the policy document: `policy.yaml`, read once at start: the policy, exchange clusters and AWS accounts, App catalogues, controller enablement and exports. No secret in any row | the chart |
| `PushSecret <release>-github-app-<id>` | copies one catalogue App's `app_id`, `installation_id` and `private_key` to the store and path its entry names. The copy is the App's key | the chart, for each `githubApps.catalogue` entry carrying `push`; External Secrets does the copying |
| `PushSecret <release>-workspace-copy` | copies all of `Secret <release>-workspace-credentials` as one JSON object to the store and path `directory.push` names. Recovery copy; an operator writes it back. `deletionPolicy: None`: the copy outlives the Secret | the chart, when `directory.push` is written; External Secrets does the copying |
| `PushSecret <release>-github-apps-copy` | the same for `Secret <release>-github-apps` (the link App and one App per bound organisation), to `githubApps.push` | the chart, when `githubApps.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-credentials-copy` | copies all of `Secret <release>-slack-credentials` to `slackState.push`. `deletionPolicy: None` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-records-copy` | the same for `Secret <release>-slack-records`, to `slackState.push.recordsRemoteKey` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `ConfigMap <release>-slack-status` | the Slack controller's last report, one document per workspace | created empty by the service at start; its data replaced by the controller, which is granted this one name (get, update, patch) |
| `Secret <release>-slack-catalogue-apps` | every catalogue Slack App: `<id>.client_id`, `<id>.client_secret` and, once installed, `<id>.slack_bot_token`, beside `<id>.record.json` | the service (a catalogue App's Create and Install), created empty at start |
| `PushSecret <release>-slack-app-<id>` | copies one catalogue Slack App's bot token (property `bot_token`) to the store and path its entry names | the chart, for each `slackApps` entry carrying `push` |
| `ConfigMap <release>-github-status` | the GitHub controller's last report, one document per organisation | created empty by the service at start; its data replaced by the controller, which is granted this one name |
| `ConfigMap <release>-github-orgs` | one record per connected GitHub organisation: App id and slug, installation, connected by and at | the service (Connect a GitHub organisation), created empty at start |
| `Secret <release>-github-apps` | one credential per connected organisation: the App's id, installation and private key, and a copy of the organisation's record; the link App's likewise | the service (Connect), created empty at start so the controller's volume always has a Secret behind it; read by the service only to uninstall on Disconnect |
| `Secret <release>-github-links` | one link per GitHub account (`<id>.json`): its login, the addresses it proves, its state, the person's token pair | the service writes a link; the controller rewrites it as it checks. The one Secret its Role may update |
| `Secret <release>-github-runner-apps` | every runner App. An installed App is `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key` (the names gha-runner-scale-set's `githubConfigSecret` reads) beside `<tier>.<org>.record.json`. An App not yet installed has its record and `<tier>.<org>.pending_private_key` only | the service (a runner App's Create and Install), created empty at start; read by the service only to find the installation and to uninstall on Disconnect. |
| `Secret <release>-github-catalogue-apps` | every catalogue App, by its catalogue id. An installed App is `<id>.github_app_id`, `<id>.github_app_installation_id` and `<id>.github_app_private_key` beside `<id>.record.json` (`version, id, org, app_id, app_slug, installation_id, html_url, connected_at, connected_by`). An App not yet installed has its record and `<id>.pending_private_key` only | the service (a catalogue App's Create and Install), created empty at start; read by the service to query GitHub as the App and to uninstall on Disconnect. `githubApps.catalogue[].push` projects one App's three property keys to a store |

## Labels

Every service-written object carries the labels `app.kubernetes.io/managed-by=directory-roster` (a legacy identifier, kept), `app.kubernetes.io/part-of=<release>` and `access-roster.truvity.github.io/kind`:

| Kind | On |
|---|---|
| `workspace`, `workspace-credentials`, `settings` | the workspace records, the credentials Secret, the console's settings |
| `github-status`, `github-orgs`, `github-links`, `github-runner-apps`, `github-catalogue-apps` | the GitHub objects (`github-orgs` is both the records ConfigMap and the Apps Secret) |
| `slack-workspaces`, `slack-records`, `slack-status`, `slack-catalogue-apps` | the Slack objects (`slack-workspaces` is both the records ConfigMap and the credentials Secret) |
| `credential` | a per-workspace Secret that a release before 1.7 wrote |

The annotation `access-roster.truvity.github.io/workspace-id` holds the workspace id as the backend spells it. At start the service moves objects written by an older release to the keys above. Export everything with

```sh
kubectl -n <namespace> get secret,configmap -l app.kubernetes.io/managed-by=directory-roster -o yaml
```

Back up and restore: [Back up and restore](../../guides/sluis/operate/back-up-and-restore.md).

`store: memory` writes none of it, and a restart is a fresh installation. It is the binary's default; the chart's `config` defaults to `kubernetes`. A service on it warns at start, naming what a restart loses.

## Controller access

The release's ServiceAccount has `get`, `update` and `patch` on ConfigMaps `<release>-github-status` and `<release>-slack-status`, and `get` and `update` on Secret `<release>-github-links`. Keys and records are volumes, so it can read no other Secret or ConfigMap. The policy's `exchange` must admit the ServiceAccount, and the audit installation's `workloadIdentity` must name it.
