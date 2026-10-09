# sluis chart

Deploys the whole of sluis: the directory reader, the policy,
the OpenID provider, the login page, the console and the audit trail in
one process, and, when `config.controllers.github` and `config.controllers.slack` name them, the
GitHub and Slack controllers, each in a loop of its own in that same process. The chart runs one
image, `ghcr.io/truvity/sluis/sluis`, as ONE Deployment: `sluis serve`. A controller has no
listener of its own, and is a dry run for every organisation or
workspace until it is listed in `policy.controllers.github.enabledOrgs` or `policy.controllers.slack.enabledWorkspaces`.
The process is configured by one service document, the `config` value, rendered as it
stands and validated against the schema the binary uses (`schemas/config/sluis.schema.json`); a secret is named in it
and reaches a pod as a file the chart projects from `secrets` (or, for
`secrets.source: env`, through `secretEnv`). See [docs/reference/sluis/configuration.md](../../docs/reference/sluis/configuration.md).
Published to `ghcr.io/truvity/charts/sluis` on every
`v*` tag of the repository; the tag is the chart's version.

What the chart includes, what it expects and every value are documented in
[docs/reference/sluis/configuration.md](../../docs/reference/sluis/configuration.md). A release of the chart's earlier name
moves with two values that keep every object's name:
[the migration](../../docs/guides/sluis/migrate/migrate-from-the-access-issuer-chart.md).
`values.schema.json` is strict at the top level: an unknown key fails the render.

Two ways to give the chart its configuration:

- **Documents mode (preferred).** `documents.service` (the service document, `sluis.yaml` v3) and `documents.policy`
  (the policy document, `policy.yaml` v2), rendered by `sluisctl render`. The chart keeps only the deployment-level
  values (image, resources, replicas, route, alerts, mounts).
- **Values mode (deprecated, removed after one minor).** `config`, `policy`, `exchange.clusters`, `exchange.aws`,
  `githubApps.catalogue` and `slackApps` render the two documents. NOTES.txt says so while it is used.

**Rendered documents are trusted, so they must come from `sluisctl render`.** With `documents.service` and
`documents.policy` the chart puts the two documents into their ConfigMaps unchanged. Helm cannot run sluis's loader, so the
chart does not re-validate them: it holds them to what it mounts and refuses an `http://` OpenBao address and an adapter
setting that names a credential (the two things a hand edit would put in that a reviewer misses), and the service's own
loader checks the rest at start, after the rollout. An estate therefore renders with `sluisctl render` (never by hand),
commits the output, and runs `sluisctl render --check` in CI so that a committed document is always what the installation
renders to ([ADR 0038](../../docs/decisions/0038-estates-render-through-sluis.md)).

**Input secrets in documents mode.** What is projected follows the document's *input* source,
`secrets.source` (with `secrets.root: /var/run/sluis/secrets`), never the credentials adapter (`adapters.secrets`, which
may be `openbao` and holds what sluis writes). With `secrets.source: file` the chart projects each entry of the chart
value `secrets` as the file `<root>/<name>` (mode 0440, read-only): the confidential clients' `clients/<id>/secret`,
`providers/google/default/client-id` and `client-secret`, `issuer/state-secret`. A policy client's `secret` is that
name, so the Kubernetes Secret behind it is declared here, and the chart refuses a client whose name is not declared:

```yaml
secrets:
  - {name: clients/argocd/secret, secretName: sluis-client-argocd, key: client-secret}
  - {name: providers/google/default/client-secret, secretName: sluis-google, key: client-secret}
  - {name: issuer/state-secret, secretName: sluis-inputs, key: state-secret}
```

The chart refuses a name that the documents use (a policy client's `secret`, the Google OAuth client of
`oauthClient.provider`, and every `...Secret` field of the service document, such as `stateSecret` or `passwordSecret`)
and `secrets` does not declare, and a name declared twice. The pod runs as 65532:65532 with `fsGroup: 65532` whenever secret
files are projected, so they stay readable at mode 0440; `fsGroup` also makes the pod's other projected volumes
(ServiceAccount tokens, mode 0640) group-readable.

With `secrets.source: ssm` or `openbao` the inputs are read from the store by name and `secrets` projects nothing.

Three things it will not do for you. It does not create the signing key
— cert-manager issues one, or external-secrets delivers one, because a
service that mints its own credential is an exception to how every other
credential here is provisioned. It does not put an authenticating proxy
in front of the issuer: this *is* the thing that authenticates, and a
proxy would have nowhere to send anyone. And it does not make a second
copy of what the console adds unless you ask it to: the Secrets that hold it are
named in
[docs/reference/sluis/configuration.md](../../docs/guides/sluis/operate/back-up-and-restore.md)
(the Slack ones, `<release>-slack-credentials`, `<release>-slack-records` and
`<release>-slack-catalogue-apps`, are named in the values), and
`directory.push`, `githubApps.push` and `slackState.push` render an External
Secrets `PushSecret` for the ones nothing upstream can re-deliver. Copying the
rest is the deployment's job.

The exports are retired ([0041](../../docs/decisions/0041-the-secret-contract.md)): a consumer
reads the typed document at `external/<kind>/<id>` itself (see
[secrets](../../docs/reference/sluis/secrets.md#the-external-documents)). `exports.openbao.caBundle` and
`exports.openbao.token.audience` stay: they mount the CA and project the token the `openbao`
secrets adapter's login presents.

`telemetry.otlp.endpoint` sets the OpenTelemetry SDK environment on every pod:
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL` (`protocol`,
`http/protobuf` by default), an `OTEL_SERVICE_NAME` (one name: the controllers report under the process's) and every
`extraEnv` entry
(other `OTEL_*` variables only). Empty, nothing is rendered and nothing is
exported. See [docs/reference/sluis/telemetry.md](../../docs/reference/sluis/telemetry.md#wiring-it-with-the-chart).

Without `audit.s3.bucket` the audit trail stays in one replica's memory,
which is not a record; the service says so at start. A bucket needs an
identity to write with, and the chart carries no credential of its own:
set `serviceAccount.annotations` and let the cluster's pod-identity
webhook inject one, rather than handing this service a long-lived key.

`examples/github-apps.yaml` ships beside the values: a default set of
GitHub Apps an estate can copy — dependency updates split public from
private, a bot that approves pull requests and cuts tags, and one App for
the program that manages the organisation — with what each is for and why
they are separate identities. It is values to read and copy, not a
default: creating an App is an owner of the organisation confirming a
manifest
([guide](../../docs/guides/sluis/connect/github-apps-catalogue.md#1-declare-an-app)).

`slackApps` declares Slack Apps the way `githubApps.catalogue` declares GitHub
Apps: an operator creates each from
the console with a throwaway app configuration token (used once, never
stored), an owner of the workspace installs it, and the bot token is kept in
`<release>-slack-catalogue-apps`. An entry may `push` that one key to a
secret store
([guide](../../docs/guides/sluis/connect/slack-apps-catalogue.md)).

`config.controllers.slack` runs the Slack controller in the process (`consoleURL`, `interval`; the
workspaces it changes are `policy.controllers.slack.enabledWorkspaces`): it needs `exchange.clusters`
to name this cluster and `console.mount` to be set, and egress to `slack.com:443` from the fleet's own
policy
([guide](../../docs/guides/sluis/connect/slack-workspace.md#2-run-the-controller)).
`slackState.push` is a recovery copy of the Slack state: two `PushSecret`s, one
for `<release>-slack-credentials` at `remoteKey` and one for the mirror
`<release>-slack-records` at `recordsRemoteKey` (the two keys must differ), with
`deletionPolicy` fixed at `None`; it needs `config.store: kubernetes`
([runbook](../../docs/guides/sluis/operate/back-up-and-restore.md#1-know-what-there-is)).

The pod rolls so that a failed start leaves the old pod running: the default `RollingUpdate`
keeps an old pod until a new one is Ready, and Ready (a readiness probe on `/readyz`,
`config.probes.address`, default `:7070`) means the whole process, the controllers included,
finished starting. A controller in the process runs in every replica, so `replicaCount` above 1
needs the tick leases in a State the replicas share: the chart refuses it unless
`config.ports.adapter` is `dynamodb`
([when a second replica is safe](../../docs/concepts/sluis/high-availability.md#the-controllers-in-the-one-process)).
The controllers read the console as this pod's own ServiceAccount, so the policy's exchange must
admit that account (the policy's `viewer` group), and the audit installation knows one workload.

```sh
helm install sluis oci://ghcr.io/truvity/charts/sluis \
  --namespace sluis --create-namespace \
  --set config.issuerURL=https://issuer.example
```
## Where to go next

- Every value: [chart values](../../docs/reference/sluis/chart-values.md); every configuration key:
  [configuration](../../docs/reference/sluis/configuration.md).
- A worked install on Kubernetes with AWS storage: [Kubernetes on AWS](../../docs/get-started/sluis/kubernetes-aws.md);
  OpenBao as the secrets store: [the OpenBao secrets adapter](../../docs/reference/sluis/openbao-secrets-adapter.md).
- Upgrading across versions: the [upgrade pages](../../docs/guides/sluis/upgrade/v1.63.md), linked from the CHANGELOG.
