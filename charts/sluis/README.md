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
`secrets.source: env`, through `secretEnv`). See [docs/reference/configuration.md](../../docs/reference/configuration.md).
Published to `ghcr.io/truvity/charts/sluis` on every
`v*` tag of the repository; the tag is the chart's version.

What the chart includes, what it expects and every value are documented in
[docs/reference/access-issuer.md](../../docs/reference/access-issuer.md).
`Moving from the `access-issuer` chart is one release's change, with two values to keep every object's name:
[docs/reference/configuration.md](../../docs/reference/configuration.md#migrating-from-the-access-issuer-chart).
`values.schema.json` is strict at the top level: an unknown key fails the
render.

Three things it will not do for you. It does not create the signing key
— cert-manager issues one, or external-secrets delivers one, because a
service that mints its own credential is an exception to how every other
credential here is provisioned. It does not put an authenticating proxy
in front of the issuer: this *is* the thing that authenticates, and a
proxy would have nowhere to send anyone. And it does not make a second
copy of what the console adds unless you ask it to: the Secrets that hold it are
named in
[docs/reference/configuration.md](../../docs/reference/configuration.md#restoring-from-the-secrets-alone)
(the Slack ones, `<release>-slack-credentials`, `<release>-slack-records` and
`<release>-slack-catalogue-apps`, are named in the values), and
`directory.push`, `githubApps.push` and `slackState.push` render an External
Secrets `PushSecret` for the ones nothing upstream can re-deliver. Copying the
rest is the deployment's job.

`config.exports` copies the secrets the console keeps (a Slack App's bot token, the
runner and catalogue Apps, the seven recovery bundles) into OpenBao, written by the
service itself and never a dependency
([0034](../../docs/decisions/0034-exports-go-to-openbao-directly.md)); `config.ports.export`
says which OpenBao and how to log in, and `exports.openbao.caBundle` and
`exports.openbao.token.audience` mount the CA and project the token the login
presents. On a State adapter this replaces the `push` values, which stay for the
`legacy` storage and are deprecated. See
[docs/reference/configuration.md](../../docs/reference/configuration.md#exports-and-the-export-port).
`alerts.rules.exportFailing` and `exportStale` and a dashboard row cover it.

`telemetry.otlp.endpoint` sets the OpenTelemetry SDK environment on every pod:
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL` (`protocol`,
`http/protobuf` by default), an `OTEL_SERVICE_NAME` (`access-issuer`: the controllers report under the one
process's name) and every `extraEnv` entry
(other `OTEL_*` variables only). Empty, nothing is rendered and nothing is
exported. See [docs/operations/telemetry.md](../../docs/operations/telemetry.md#wiring-it-with-the-chart).

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
([guide](../../docs/connect/github-apps-catalogue.md#a-default-set)).

`slackApps` declares Slack Apps the way `githubApps.catalogue` declares GitHub
Apps: an operator creates each from
the console with a throwaway app configuration token (used once, never
stored), an owner of the workspace installs it, and the bot token is kept in
`<release>-slack-catalogue-apps`. An entry may `push` that one key to a
secret store
([guide](../../docs/connect/slack-apps-catalogue.md)).

`config.controllers.slack` runs the Slack controller in the process (`consoleURL`, `interval`; the
workspaces it changes are `policy.controllers.slack.enabledWorkspaces`): it needs `exchange.clusters`
to name this cluster and `console.mount` to be set, and egress to `slack.com:443` from the fleet's own
policy
([guide](../../docs/connect/slack-workspace.md#running-the-controller)).
`slackState.push` is a recovery copy of the Slack state: two `PushSecret`s, one
for `<release>-slack-credentials` at `remoteKey` and one for the mirror
`<release>-slack-records` at `recordsRemoteKey` (the two keys must differ), with
`deletionPolicy` fixed at `None`; it needs `config.store: kubernetes`
([runbook](../../docs/operations/runbook.md#slack-state)).

The pod rolls so that a failed start leaves the old pod running: the default `RollingUpdate`
keeps an old pod until a new one is Ready, and Ready (a readiness probe on `/readyz`,
`config.probes.address`, default `:7070`) means the whole process, the controllers included,
finished starting. A controller in the process runs in every replica, so `replicaCount` above 1
needs the tick leases in a State the replicas share: the chart refuses it unless
`config.ports.adapter` is `dynamodb`
([why](../../docs/operations/runbook.md#a-controller-release-that-crash-loops),
[when a second replica is safe](../../docs/operations/high-availability.md#the-controllers-how-they-roll-and-when-a-second-replica-is-safe)).
The controllers read the console as this pod's own ServiceAccount, so the policy's exchange must
admit that account (`all:access-roster:viewer`), and the audit installation knows one workload.

```sh
helm install sluis oci://ghcr.io/truvity/charts/sluis \
  --namespace sluis --create-namespace \
  --set config.issuerURL=https://issuer.example
```

## Kubernetes on EKS: AWS storage, KMS-wrapped signing, OpenBao secrets

The `k8s-aws` preset (the former `aws-eks`) is DynamoDB state, S3 blobs, KMS-wrapped signing and an
in-process ticker; the secrets are SSM, or OpenBao as here. The inputs (the signing state secret, a
client's secret) arrive as files, so `secrets.source` stays `file`; the openbao adapter holds what
sluis writes and the exports. OpenBao scopes by namespace, so the root is `sluis` in the
installation's own namespace (`kv/sluis/private/credentials/...`, `kv/sluis/export/...`).
With KMS signing the chart renders no Certificate and mounts no signing Secret; the chart's default
`config.signingKey.file` is dropped with a `null`. The pod's AWS role comes from EKS Pod Identity
(nothing to render) or, with `serviceAccount.awsIdentity: irsa`, from the annotation of `awsRoleArn`.

```yaml
config:
  issuerURL: https://access.example
  preset: k8s-aws
  signingKey:
    file: null                      # no key file: KMS holds the keys
    kmsWrapped:
      keyId: alias/sluis-signing
      region: eu-west-1
      stateSecret: issuer/state-secret      # a name in `secrets` below
      rotateEvery: 24h              # the rotation alert fires at 26h
  ports:
    dynamodb: {table: sluis, region: eu-west-1}
    blob: {adapter: s3, s3: {bucket: sluis-blobs, region: eu-west-1}}
  adapters:
    secrets:
      adapter: openbao
      settings:
        address: https://openbao.example
        caFile: /var/run/access-issuer/openbao-ca/ca.pem     # exports.openbao.caBundle
        namespace: kernel
        mount: kv
        root: sluis
        auth:
          method: jwt
          mount: jwt-kernel
          role: sluis
          tokenFile: /var/run/openbao/token                  # exports.openbao.token.audience
  audit: {writer: https://audit.example:8443}
secrets:
  - {name: issuer/state-secret, secretName: sluis-inputs, key: state-secret}
serviceAccount:
  awsIdentity: pod-identity          # or irsa, with awsRoleArn
exports:
  openbao:
    caBundle: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
    token: {audience: openbao-kernel}   # a ServiceAccount token projected for the jwt login
```

The OpenBao policy, the value layout and the per-export `namespace` are in
[configuration](../../docs/reference/configuration.md#the-openbao-secrets-adapter); to keep tokens
issued by the old file keys valid across the cutover, see `signingKey.verifyOnly` and its
[cutover note](../../docs/reference/configuration.md#cutting-over-to-kms-wrapped-signing-without-signing-everyone-out).

## Moving from v1.62 (three Deployments) to one

v1.63 runs the GitHub and Slack controllers inside `sluis serve`
([decision 0037](../../docs/decisions/0037-one-process-everywhere.md)), so the chart renders one
Deployment, `<release>`, and the `controllerGithub` and `controllerSlack` values are gone. On
`helm upgrade`:

1. Move each controller's values to `config.controllers.github` / `config.controllers.slack`
   (`consoleURL`, `interval`, `tokenFile`, `appsDir` or `credentialsDir`, `recordsDir`; present is on).
   `controllerX.config.{policy,release,log,ports,platform,preset,adapters,audit,probes}` are the one
   document's own keys now. `controllerX.resources`, `replicas`, `strategy`, `minReadySeconds` and
   `podDisruptionBudget` go to the top-level `resources`, `replicaCount`, `strategy` and so on.
2. The `<release>-github-roster` and `<release>-slack-roster` Deployments, ServiceAccounts,
   ConfigMaps and PodDisruptionBudgets are deleted by the upgrade. The controllers run as the
   release's own ServiceAccount, which gets the controllers' Role.
3. **Policy:** `exchange` and the audit installation's `workloadIdentity` map must admit the one
   ServiceAccount (`system:serviceaccount:<ns>:<release>`) where they admitted the two controller
   accounts, or the controllers are nobody at the console. Ship that policy change before the upgrade.
4. `replicaCount` above 1 with a controller needs `config.ports.adapter: dynamodb`; the chart refuses
   to render otherwise.
5. Dashboards and alerts that select `service_name` `github-roster` or `slack-roster` select
   `access-issuer`.
