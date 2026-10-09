# Kubernetes objects the service owns

The objects `sluis serve` writes in its own namespace with `store: kubernetes` (the legacy adapter), everything an
operator adds in the console. With `ports.adapter: dynamodb` the same records live in the State and the credentials in
the Secrets port, and these objects are not written ([store](../../concepts/sluis/store.md)).

`<release>` is the chart's full name, so two installations in one namespace do not write over each other, and `<tenant>`
is a readable part of the tenant id followed by a short hash of it: a tenant id belongs to the backend, not to
Kubernetes, so the hash carries the uniqueness the readable part may have lost. Why a record and its credential are two
objects: [Configuration: the model](../../concepts/sluis/configuration.md#a-record-and-its-credential-are-two-objects).

| Object | Holds | Written by |
|---|---|---|
| `ConfigMap <release>-workspace-<tenant>` | backend, domains, served domains, admin, connected by/at, last health, credential type | the service |
| `Secret <release>-workspace-credentials` | one entry per console-connected workspace, key `<tenant>.json`: the credential (refresh token, or service-account key) and a copy of the workspace's record without its health | the service (Connect, UploadKey), created empty at start. Releases before 1.7 kept a `Secret <release>-credential-<tenant>` each; start-up moves them in |
| `Secret <release>-slack-records` | a MIRROR of the Slack records ConfigMap `<release>-slack-workspaces`: exactly its `<workspace>.json` records, `_shared.<name>.json` Slack Connect definitions and `_channel.<workspace>.<name>.json` console channel records, never `_confirm.*` or `_pass.*`. It exists because a `PushSecret` reads Secrets only; nothing reads it but the chart's recovery copy and the service's own start | the service, in the same code path that writes the ConfigMap, and reconciled at start. If the ConfigMap holds no record and this Secret does, start repopulates the ConfigMap from it |
| `ConfigMap <release>-slack-workspaces` | one record per connected Slack workspace (`<workspace>.json`), the Slack Connect records (`_shared.<name>.json`), the console channel records (`_channel.<workspace>.<name>.json`), and the transient operator markers: confirmations (`_confirm.*`), pass requests (`_pass.*`) and consumed install states | the service (Connect, the Slack Connect and channel editors, Refresh, Confirm), created empty at start |
| `Secret <release>-slack-credentials` | one entry per connected workspace, `<workspace-id>.json`: the App's client id and secret and, once installed, the bot token | the service (Connect, the install callback), created empty at start; read by the Slack controller in the same process |
| `Secret <release>-oauth-client` | OAuth client id and secret | declared via `oauthClient.secret.name` and read-only. The console used to be able to write one; it cannot since the console became read-only, because a credential a console can change is one somebody can change from a browser |
| `Secret <release>-session-key` | signs the session cookie and the consent-flow state | the service, generated on first start; rotate by deleting |
| the signing key | a PEM private key, mounted as a file | **not the issuer** — cert-manager issues one, or external-secrets delivers one. The issuer reads it from the file, never through the API; its key id is the key's own RFC 7638 thumbprint, so nothing has to carry one beside it |
| `ConfigMap <release>-policy` | the policy document: the declared policy, the exchange's clusters and AWS accounts (**no secret in any row**), the GitHub and Slack App catalogues, what the controllers may change and the exports. One file, `policy.yaml`, read once at start | the chart |
| `PushSecret <release>-github-app-<id>` | the instruction to copy one catalogue App's `app_id`, `installation_id` and `private_key` to the store and path its entry names — that App's three property keys and nothing else. **What lands there is the App's key**, a second durable copy, rotated as one | the chart, for each `githubApps.catalogue` entry carrying `push`; External Secrets does the copying |
| `PushSecret <release>-workspace-copy` | the instruction to copy the whole of `Secret <release>-workspace-credentials`, every key as one JSON object, to the store and path `directory.push` names. A recovery copy: restoring is an operator writing it back, deliberately. `deletionPolicy: None`, so the copy outlives the Secret it is for | the chart, when `directory.push` is written; External Secrets does the copying |
| `PushSecret <release>-github-apps-copy` | the same for `Secret <release>-github-apps` — the link App and one App per bound organisation — to where `githubApps.push` names | the chart, when `githubApps.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-credentials-copy` | the instruction to copy the whole of `Secret <release>-slack-credentials` to the store and path `slackState.push` names. `deletionPolicy: None` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-records-copy` | the same for `Secret <release>-slack-records`, to `slackState.push.recordsRemoteKey` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `ConfigMap <release>-slack-status` | the Slack controller's last report, one document per workspace | created empty by the service at start; its data replaced by the controller, which is granted this one name (get, update, patch) |
| `Secret <release>-slack-catalogue-apps` | every catalogue Slack App: `<id>.client_id`, `<id>.client_secret` and, once installed, `<id>.slack_bot_token`, beside `<id>.record.json` | the service (a catalogue App's Create and Install), created empty at start |
| `PushSecret <release>-slack-app-<id>` | the instruction to copy one catalogue Slack App's bot token (property `bot_token`, nothing else) to the store and path its entry names. What lands there is the token | the chart, for each `slackApps` entry carrying `push` |
| `ConfigMap <release>-github-status` | the GitHub controller's last report, one document per organisation | created empty by the service at start; its data replaced by the controller, which is granted this one name |
| `ConfigMap <release>-github-orgs` | one record per connected GitHub organisation: App id and slug, installation, connected by and at | the service (Connect a GitHub organisation), created empty at start |
| `Secret <release>-github-apps` | one credential per connected organisation: the App's id, installation and private key, and a copy of the organisation's record; the link App's likewise | the service (Connect), created empty at start so the controller's volume always has a Secret behind it; read by the service only to uninstall on Disconnect |
| `Secret <release>-github-links` | one link per GitHub account (`<id>.json`): its login, the addresses it proves, its state, the person's token pair | the service, which writes a link; the controller, which rewrites it as it checks — the one Secret its Role may update, by name |
| `Secret <release>-github-runner-apps` | every runner App. An installed App is `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key` — the names gha-runner-scale-set's `githubConfigSecret` reads — beside `<tier>.<org>.record.json`. An App created and not yet installed has its record and `<tier>.<org>.pending_private_key` only, so a copy never hands runners an App they cannot register with | the service (a runner App's Create and Install), created empty at start; read by the service only to find the installation and to uninstall on Disconnect. A deployment copies the three keys to its runners, for example with an External Secrets `PushSecret` |
| `Secret <release>-github-catalogue-apps` | every catalogue App, by its catalogue id. An installed App is `<id>.github_app_id`, `<id>.github_app_installation_id` and `<id>.github_app_private_key` beside `<id>.record.json` (`version, id, org, app_id, app_slug, installation_id, html_url, connected_at, connected_by`). An App created and not yet installed has its record and `<id>.pending_private_key` only | the service (a catalogue App's Create and Install), created empty at start; read by the service to ask GitHub, as the App, what the App and its installation hold, and to uninstall on Disconnect. A deployment copies it for backup, for example with an External Secrets `PushSecret`; one App's three property keys are projected to a store by `githubApps.catalogue[].push` |

## Labels

Every service-written object carries `app.kubernetes.io/managed-by=directory-roster`, `app.kubernetes.io/part-of=<release>`
and `access-roster.truvity.github.io/kind`, whose value is one of:

| Kind | On |
|---|---|
| `workspace`, `workspace-credentials`, `settings` | the workspace records, the credentials Secret, the console's settings |
| `github-status`, `github-orgs`, `github-links`, `github-runner-apps`, `github-catalogue-apps` | the GitHub objects (`github-orgs` is both the records ConfigMap and the Apps Secret) |
| `slack-workspaces`, `slack-records`, `slack-status`, `slack-catalogue-apps` | the Slack objects (`slack-workspaces` is both the records ConfigMap and the credentials Secret) |
| `credential` | a per-workspace Secret that a release before 1.7 wrote |

The workspace id as the backend spells it is the annotation `access-roster.truvity.github.io/workspace-id`. The service
moves every object of its release from the keys an older release wrote to the keys above when it starts, before it reads
any of them, so an upgrade, or a restore of objects an older release wrote, needs no step of its own (a rollback past it
does: see the CHANGELOG). Export everything with

```sh
kubectl -n <namespace> get secret,configmap -l app.kubernetes.io/managed-by=directory-roster -o yaml
```

The Secrets to copy for a backup, and how to put them back: [Back up and restore](../../guides/sluis/operate/back-up-and-restore.md).

`store: memory` writes none of it: a restart is a fresh installation. It is the binary's default, because a local run and
the demonstration need no cluster; the chart's `config` defaults to `kubernetes`. A service started on the memory store
says so at WARN on its first line, naming what a restart would lose.
