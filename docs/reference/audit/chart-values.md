# Chart values

The values of `charts/audit/values.yaml`, which comments every one, and what the chart refuses. To install, follow [the Kubernetes tutorial](../../get-started/audit/kubernetes.md).

Each component has a `config:` block, rendered with `toYaml` into the ConfigMap `<fullname>-<component>-config` and mounted at `/etc/audit/config.yaml`. A key under `config:` is the binary's key. `values.schema.json` and the binary validate it; the chart translates none of it.

| Component | `config:` configures | Notes |
|---|---|---|
| `writer` | `audit-writer` | Direct mode: the one pod. Stream mode: `writer.consumers` consumers |
| `receiver` | `audit-writer` with `mode: receiver` | Stream mode only |
| `query` | `audit-query` | `query.enabled` |
| `observe` | `audit-observe` | `observe.enabled`: Deployment `<fullname>-observe`, own ServiceAccount, no Service |
| `migrate` | `audit migrate` | `migrate.enabled`: pre-install and pre-upgrade hook Job |
| `jobs.notary` | `audit-notary`, own image | Hourly CronJob, off by default (`jobs.notary.enabled`), own ServiceAccount that must not be the writer's |
| `jobs.verify`, `jobs.purge`, `jobs.clockSync` | `audit verify`, `purge`, `clock-sync` | One CronJob each. `<fullname>-verify` covers the `profiles` its config lists, or all |

<!-- generated: chart-values -->
Everything else under a component is the platform's, not the binary's:

| value | meaning |
|---|---|
| `secretFiles` | a list of `{name, secretName, key}`: a Secret's key as the file `/etc/audit/secrets/<name>`. The config names `name` in a `...Secret` field (`passwordSecret`, `credentialsSecret`, `tokenSecret`) and says `secrets: {source: file, root: /etc/audit/secrets}`, which the chart checks. `optional: true` allows a missing key |
| `secretEnv` | **deprecated**, for version-1 configs only ([upgrade](../../guides/audit/upgrade/v0.13.md)): environment variables taken from a Secret's keys. `optional: true` allows a missing key |
| `secretMounts` | a list of `{secretName, mountPath}`: a Secret mounted read-only as a directory, for a key or a root the config names by path (`local.rootFile`, a key file) |
| `tokens` | a list of `{audience, mountPath, expirationSeconds, path}`: a projected service-account token of that audience (lifetime 3600 by default), a file named `token` (or `path`) in the directory `mountPath`, which the config names (`tokenFile`, `jwtFile`). It is read afresh by whatever names it, because the kubelet replaces it before it expires |
| `serviceAccount` | the identity of the component, for Pod Identity or IRSA annotations. Every component has its own, `{create, name, annotations}`: `receiver.serviceAccount`, `query.serviceAccount`, `jobs.*.serviceAccount`. Created, it is `<fullname>-<component>` unless `name` says otherwise; with `create: false` the component runs as `name`, or as the release's own top-level `serviceAccount` (the writer's) when `name` is empty. In stream mode the chart refuses a receiver and a writer with the same name |
| `replicas`, `writer.consumers`, `query.replicas` | pod counts. `replicas` is the front door's: the writer in direct mode, the receiver in stream mode |
| `schedule`, `enabled` | for each job |
| `image`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, security contexts | the pods |
| `terminationGracePeriodSeconds`, `preStopSleepSeconds` | how a write-path pod stops. The writer takes up to 30s to stop taking records and write what it holds, so the grace period (default 45) must outlast that plus the front door's preStop sleep (default 5, a sleep action, 0 turns it off); the chart refuses a grace period that does not |

The values that are not configuration of a binary:

| value | meaning |
|---|---|
| `mode` | `direct` (one process: the front door and the write path) or `stream` (a receiver in front, `writer.consumers` writers behind). It decides which Deployments are rendered; the binaries' own `mode` is in `receiver.config` |
| `preset` | the install preset ([presets](profiles.md#install-presets)), derived from `profiles` and best left unset; a weaker one than they need is refused, and `jobs.notary.enabled` and `renders: alerts` are refused under `operational`. With no `profiles` the chart renders a `history` profile, which is `operational`. When set it is in the profile document too |
| `profiles`, `externalIdentifiersAreOpaque` | rendered as the profile document, `/etc/audit/deployment.yaml`, which every config's `deployment` names. `externalIdentifiersAreOpaque` declares that the identifiers the installation receives for external people mean nothing outside its own database, which relaxes a profile's `external: pseudonym` to `clear` ([framework profiles](profiles.md#what-a-deployment-can-relax)) |
| `workloadIdentity.issuers`, `.audience`, `.workloads` | `audience` is the audience an issuer entry takes when it names none. Rendered as `/etc/audit/workloads.yaml`, which the writer's `workloads` names. The chart refuses an installation that keeps an index and verifies callers with no `workloads` mapping |
| `query.grants` | rendered as `/etc/audit/grants.yaml`, which the query service's `grants` names |
| `catalogues` | catalogue documents by name, mounted at `/etc/audit/catalogues` for the writer's `catalogues` |
| `keysVolume.enabled`, `.size`, `.storageClass`, `.accessModes`, `.existingClaim` | the volume for the `local` key provider, mounted at `/var/lib/audit/keys` on every pod that writes. The keys are random, not derived, so this is the only copy: back it up, and use ReadWriteMany for more than one replica. `query.keysVolume: true` mounts it read-only on the query service for resolve |
| `trust.configMap`, `trust.key` | a CA bundle trusted beside the system roots, e.g. trust-manager's for a private chain, mounted at `/etc/audit/trust/<key>` on every pod, for the `ca` and `caFile` keys and a Postgres URL's `sslrootcert` to name |
| `extensions.billing.enabled`, `extensions.quotas.enabled` | the two projections, both off. The toggles are here so that a deployment's values need not change when the work behind them lands; today each renders nothing |
| `query.route.enabled`, `.parentRefs`, `.hostnames`, `.pathPrefix`, `.annotations`, `.securityPolicy` | an HTTPRoute (Gateway API) to the query Service, and with `securityPolicy` an Envoy Gateway SecurityPolicy on it. Off by default. `parentRefs` and `hostnames` are required; `pathPrefix` is stripped before the service sees the request; `securityPolicy` is the SecurityPolicy's `spec` without `targetRefs`, `targetRef` and `targetSelectors`. See the [chart README](../../../charts/audit/README.md#publishing-the-query-service) |
| `networkPolicy.enabled`, `.ingressFrom`, `.queryIngressFrom` | who may reach the writer's sink and the query service. Empty `queryIngressFrom` leaves the query service open in the cluster; once set, it must include the gateway's namespace when `query.route` is on |

<!-- /generated -->

## Mount points

A config names these paths.

| path | what | from |
|---|---|---|
| `/etc/audit/config.yaml` | the component's own configuration | `<component>.config` |
| `/etc/audit/deployment.yaml` | the profile configuration | `profiles`, `externalIdentifiersAreOpaque` |
| `/etc/audit/workloads.yaml` | the issuers and workloads | `workloadIdentity` |
| `/etc/audit/grants.yaml` | the query service's grants | `query.grants` |
| `/etc/audit/catalogues/` | the catalogues | `catalogues` |
| `/etc/audit/trust/<key>` | the CA bundle | `trust` |
| `/var/lib/audit/keys` | the local key directory | `keysVolume` |

`secretMounts` and `tokens` mount anything else a config names, such as a token or key file, at the path you give.

## What the chart refuses

`charts/audit/templates/_checks.tpl` checks what only the platform sees. A top-level value such as `bucket` or `lockMode` fails against `values.schema.json`, naming the key.

| Area | Refused |
|---|---|
| `mode` | Anything but `direct` or `stream` |
| `renders` | Anything but `app`, `alerts` or `dashboards`; `alerts` when no configured preset has alarms; an `alerts.format` other than `vmrule` or `prometheusrule`; every `alerts.rules` disabled |
| `presets` | Empty; a key other than `operational`, `standard` or `attested`; no `bucket`; a `key_alias` that is not `alias/<name>`; a `prefix` that starts with `/` or does not end with one; an `endpoint` that is not http(s); `key_alias` or the attested preset with an `endpoint`; `credentials`, `credentials_ref` or `path_style` without one; `credentials_ref` beside `credentials` or `credentials_preset`; `credentials_preset` unless `acknowledgeMinterCustody: true` |
| Profiles | A framework profile the chart does not know; a `preset` other than the three; a preset weaker than its framework profiles need; a preset that `presets` does not configure |
| `telemetry.otlp` | An `endpoint` that is not http(s); `extraEnv` holding `OTEL_EXPORTER_OTLP_ENDPOINT` or a name not starting with `OTEL_` |
| `secretFiles` | The component's `config.secrets` is not `{source: file, root: /etc/audit/secrets}` |
| Receiver | Running as the writer's ServiceAccount |
| `writer.enabled: false` | With `mode: stream`, `keysVolume.enabled`, `workloadIdentity.issuers` or `extensions.billing.enabled`; a query service with no `config.sink`; a sink URL naming this release's own Service; an indexer, query service or job with `serviceAccount.create: false` and no `name` |
| Keys | The `local` provider without `keysVolume.enabled`, unless `keysVolume.ephemeralIsAcceptable` (losing the directory re-keys every tenant); `query.config.keys` signing in to OpenBAO as the writer, because resolving and writing are separate privileges |
| Writer count | `writer.config.replicas` differs from the pods rendered (`replicas` in direct mode, `writer.consumers` in stream mode) |
| Several writers | No `database` in `writer.config`; a `keysVolume` without `ReadWriteMany` |
| Stream mode | Missing `receiver.config` with `mode: receiver`, `writer.config.stream` (or `consume`) or writer `database`; `writer.config.mode: receiver` |
| Direct mode | A receiver writer |
| `query.keysVolume` | No enabled `keysVolume`, or no `ReadWriteMany` |
| Workload identity | `workloads` without `workloadIdentity.issuers`, and the reverse; an index with issuers but no `workloadIdentity.workloads`; a workload naming no issuer while several are trusted |
| Query | Empty `query.grants.issuers`; a `database.url` equal to the writer's, because an owner bypasses the tenant policies |
| `query.route` | Enabled without `query.enabled`, `parentRefs` or `hostnames`; a `securityPolicy` setting `targetRefs`, `targetRef` or `targetSelectors` |
| Indexer (`observe.enabled`) | Running as the writer's, query service's or receiver's ServiceAccount; connecting as the writer's, query service's or migration's role |
| Writer | Connecting as the migration's role |
| Notary | Running as the writer's ServiceAccount (`serviceAccount.create: false`) or with its annotations; OpenBAO sign-in under the writer's role or token; enabled when no configured preset has a notary (`standard` or `attested`) |
| `extensions.billing` | No profile composed from a metering framework profile |
| `extensions.quotas` | Without `mode: stream` |

Each part keeps its own identity, at the cloud role and at the database role.

The chart does not check that the Envoy Gateway CRDs exist when `query.route.securityPolicy` is set.

## Examples

The chart's tests render these complete value files, all in configuration version 2.

| file | for |
|---|---|
| [`direct.yaml`](../../../charts/audit/examples/direct.yaml) | an internal service: the front door and the write path in one process, the index in a small Postgres |
| [`stream.yaml`](../../../charts/audit/examples/stream.yaml) | a product: receivers, JetStream, N writers |
| [`external-writer.yaml`](../../../charts/audit/examples/external-writer.yaml) | writer on Lambda, observe and query here (`writer.enabled: false`) |
| [`sqs.yaml`](../../../charts/audit/examples/sqs.yaml) | a receiver or writer over SQS |

The sink URL names the writer's Service: `audit`, or `<release>-audit` under an application's own release.
