# Capabilities

What runs where, and how far each piece has got, on the two permanent platforms. The shape is in
[ports and adapters](../explanation/ports.md) (decisions
[0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md) to
[0034](../decisions/0034-exports-go-to-openbao-directly.md)); this page is the status of each part against it, and is
updated in the change that moves a cell. **The adapters themselves** (what exists, what it needs, which presets name it,
built or on request) are in the generated [adapters](adapters.md): this page does not repeat that table.

| Mark | Meaning |
|---|---|
| 📄 | designed: a record or a specification exists, nothing is built |
| 🧪 | built: code exists and is tested, but is not yet released as a supported way to run |
| ✅ | supported: released, documented, and covered by CI |
| — | not applicable on this platform |

Two platforms are supported permanently, so a blank is a gap and not a choice. A cell reads for master, which is the
Unreleased section of the [changelog](../../CHANGELOG.md).

## Ports and adapters

Which adapter fills each concern is [adapters](adapters.md); the presets that name them are unavailable for `server`,
`k8s-minimal` and `k8s-openbao` and available for `aws-serverless`, `aws-hybrid` and `k8s-aws`. The rows here are what
is built around the adapters.

| Piece | Kubernetes | AWS Lambda |
|---|---|---|
| Ports as Go interfaces (`internal/port`) and the apps depending on them | ✅ | ✅ |
| Domain stores on the ports: workspaces and credentials, GitHub organisations and Apps, a person's GitHub link (the token pair in Secrets, compare-and-swap refresh), runner and catalogue Apps, the Slack records, the console's session key (`internal/portstore`; any `ports.adapter` but `legacy`, which needs a Secrets adapter) | ✅ | ✅ |
| Secrets in OpenBao (`adapters.secrets: openbao`, layout v3, compare-and-swap by KV `cas`; in the Unreleased section of the changelog) | 🧪 | 🧪 |
| Export: copies of the secrets the console keeps (a Slack App's bot token, the runner and catalogue Apps, recovery bundles) written by the service itself, asynchronously, per-export lease, retried with backoff (`internal/exports`, `ports.export` and `exports`, [0034](../decisions/0034-exports-go-to-openbao-directly.md)); to OpenBao KV, or through the Secrets port (SSM) | ✅ | ✅ |
| Export: the `jwt` login with the web identity token of AWS outbound federation, for a function in the VPC reaching OpenBao through its internal load balancer (the adapter takes a `TokenSource`; no Lambda wiring yet) | — | 📄 |
| Export: the External Secrets `PushSecret`s of the chart (`slackApps[].push`, `directory.push`, `githubApps.push`, `githubApps.catalogue[].push`, `slackState.push`; need `config.store: kubernetes`) | deprecated, replaced by the above | — |
| Inputs: mounted ConfigMaps and Secrets | ✅ | — |
| Inputs: the configuration layer, or a parameter store | — | ✅ |
| Port conformance suite: in-memory and legacy | ✅ | — |
| Port conformance suite: Export (in-memory, and OpenBao against a fake KV mount) | 🧪 | 🧪 |
| Port conformance suite: DynamoDB (LocalStack and an in-memory fake of the API; `migrate` into and out of it) | ✅ | ✅ |

## Runtime

| Piece | Kubernetes | AWS Lambda |
|---|---|---|
| Issuer, console and directory hub | ✅ | ✅ |
| GitHub reconciler | ✅ (one replica; two with a DynamoDB State, 🧪) | ✅ |
| Slack reconciler | ✅ (one replica; two with a DynamoDB State, 🧪) | ✅ |
| `Tick(target)` with a lease per target, a report per target, and a trigger that ticks only its target (on the legacy adapter: the lease is exclusive across pods only with a shared State, and a controller has none; the console reaches a controller through the mounted records, polled) | 🧪 | 📄 |
| Two replicas of a reconciler | 🧪 (chart `replicas`: refused unless `ports.adapter` is `dynamodb`) | — |
| Slack Connect handoff: the host's tick notifies the guest's; the guest-side probe is the host's tick's | 🧪 | 📄 |
| Slack Connect handoff by pending-share record (`share.<host>.<channel>`: 14 days while pending, 7 days once accepted; needs a State both runners share) | 🧪 | 📄 |
| Slack `users.info` cache on the State (`cache.slack.user.<workspace>.<id>`, 24 h; `slack_roster.user_cache` counts hits and misses) | 🧪 | 📄 |
| Shared inputs of the Slack controller in the State (`cache.<digest>.<name>`) | 📄 (stays in memory) | 📄 |
| Controllers reading the console's records from the State instead of mounted files (`ports.adapter` other than `legacy`) | 🧪 | 📄 |
| One Lambda function: the issuer and console behind an API Gateway HTTP API, and the controllers' ticks from EventBridge Scheduler (v1.63, [0037](../decisions/0037-one-process-everywhere.md)) | — | ✅ |
| One binary `sluis` (v1.63, [0037](../decisions/0037-one-process-everywhere.md): `serve`, which runs the GitHub and Slack controllers in-process; `controller github|slack` deprecated, removed in the next release; `tick <github|slack> <target>` runs one tick once; `migrate --from <config> --to <config>` copies the State between storages) | 🧪 | 📄 |
| One chart `sluis` (one Deployment, `sluis serve`; the controllers are `config.controllers.*`) | 🧪 | — |
| One service document (`sluis/v3`) validated against a schema | ✅ | ✅ |

## Identity

| Mechanism | Kubernetes | AWS Lambda |
|---|---|---|
| Verify a ServiceAccount token (a cluster declared in the installation) | ✅ | 📄 |
| Verify an AWS outbound-federation token (`sts:GetWebIdentityToken`) | ✅ | ✅ |
| Give the service an AWS identity (annotation or Pod Identity on the ServiceAccount) | ✅ | — |
| The function role's identity to an OTLP endpoint (the extension layer) | — | ✅ |

The federation verifier is the same code on both platforms, so its Lambda cell
is the part of the runtime that is built; the Lambda runtime itself is not.

## Migration and operations

| Piece | Kubernetes | AWS Lambda |
|---|---|---|
| `sluis migrate --from <config> --to <config>`: every domain store through its business interface, with `--dry-run`, create-if-absent and `--overwrite`, a plan before any write, and a read-back verification reported as JSON ([operations/migrate.md](../how-to/migrate-state.md)) | 🧪 | 📄 |
| Backup and export through the same command (a file as one end) | 📄 (a follow-up: there is no file adapter yet) | 📄 |
| Copy of Valkey sessions, refresh tokens and the keyring schedule into the new store, each with its remaining lifetime | 🧪 | — |
| Rollback by the same command in the other direction (`--from dynamodb --to legacy`) | 🧪 | — |
| Existing backup: a copy of named Secrets | ✅ | — |

## Telemetry

| Piece | Kubernetes | AWS Lambda |
|---|---|---|
| Metrics over OTLP, configured by `OTEL_*` | ✅ | ✅ |
| The chart sets the `OTEL_*` environment on every pod from `telemetry.otlp` (endpoint, protocol, a service name per component, extra `OTEL_*`); unset renders nothing | 🧪 | — |
| Platform logs over OTLP (observability's otlp-lambda layer) | — | ✅ |
| Traces: HTTP and Connect spans, a span per tick and per port call, the trace continued into the console (trace context across queues waits for the queues) | 🧪 | 📄 |
| Metrics: the issuer's requests, tokens, sign-ins and keys; ticks and leases; port calls; exports; rate limits ([operations/telemetry.md](../operations/telemetry.md)) | 🧪 | 📄 |
| Chart modes `renders: alerts` and `renders: dashboards`: twelve rules, unit-tested with `vmalert-tool`, and a dashboard held to `dashboardlint` | 🧪 | — |
| No personal data in a span: an allowlist exporter, tested by planting markers; no person or group in a label | 🧪 | 📄 |

Telemetry is configured by the OpenTelemetry environment variables and nothing
else ([0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md)); the
signals, the alerts and how to install them are in
[operations/telemetry.md](../operations/telemetry.md). On
Lambda observability's otlp-lambda layer sends it with the function role's identity
([integrations/aws-lambda.md](lambda.md)).
