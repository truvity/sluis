# Telemetry

What the audit processes publish and how to install the alerts and dashboard. OpenTelemetry's environment configures telemetry, never a configuration file ([0063](../../decisions/0063-one-validated-configuration-file.md)). Without a collector a process publishes nothing.

| Variable | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | The collector for metrics and traces, such as `http://<gateway>:4318` |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | One signal only |
| `OTEL_SERVICE_NAME` | Defaults to `audit-writer` or `audit-query` |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | Unset: parent-based `always_on`, so a caller's decision wins and every new trace is kept. For a tenth, set `parentbased_traceidratio` and `0.1` |

### The chart sets the environment

`telemetry.otlp` renders the variables above:

```yaml
telemetry:
  otlp:
    endpoint: http://gateway.observability.svc:4318   # empty: render nothing
    protocol: http/protobuf                           # or http/json; the exporters are OTLP/HTTP
    extraEnv:                                         # OTEL_* only
      OTEL_TRACES_SAMPLER: parentbased_traceidratio
      OTEL_TRACES_SAMPLER_ARG: "0.1"
```

With an `endpoint`, each pod gets the endpoint, the protocol, its own `OTEL_SERVICE_NAME`, then each `extraEnv` entry, sorted.

| Pod | `OTEL_SERVICE_NAME` |
|---|---|
| Front door, receiver, consumers | `audit-writer` |
| Query service | `audit-query` |
| Indexer | `audit-observe` |
| Notary | `audit-notary` |
| `audit` toolchain jobs | `audit` (they export nothing) |

The chart refuses an `extraEnv` key outside `OTEL_*` or named `OTEL_EXPORTER_OTLP_ENDPOINT`, and a non-http(s) endpoint. On Lambda, see [AWS telemetry](../../guides/audit/operate/aws-send-lambda-telemetry.md).

## Metrics

The gateway stores dots as underscores and adds `_total` and unit suffixes. It keeps only cluster, namespace and tier as resource labels ([emitting](https://github.com/truvity/observability/blob/master/docs/emitting.md)).

| Series | Type | Labels | Answers |
|---|---|---|---|
| `audit_sink_records_acknowledged_total` | counter | `transport`, `durability` | Ingest rate and durability mix |
| `audit_sink_records_rejected_total` | counter | `transport` | Records refused for their own sake |
| `audit_sink_write_duration_seconds` | histogram | `transport`, `outcome` | Write time per hop |
| `audit_sink_consume_failures_total` | counter | `transport` | Batches a consumer's target failed, to be redelivered |
| `audit_observe_index_lag_seconds` | histogram | `profile` | Seconds from an object's put to its rows in the index; `settle` is the floor. Only for objects that arrived |
| `audit_observe_index_deferred_total` | counter | `profile`, `reason` | Objects not indexed: `retry` is tried again, `unreadable` was skipped |
| `audit_observe_passes_total` | counter | `outcome` | Indexing passes, `succeeded` or `failed` |
| `audit_observe_pass_since_success_seconds` | gauge | | Seconds since a pass last succeeded (since start, before the first) |
| `audit_observe_objects_indexed_total`, `audit_observe_records_indexed_total` | counter | `profile` | Indexer output |
| `audit_queue_message_age_seconds` | histogram | `transport` | Seconds from send to the writer receiving it (AWS: `SentTimestamp`). Redelivery wait is included. Depth and the dead-letter queue are CloudWatch's ([AWS](aws-pulumi-library.md#alarms)) |
| `audit_writer_dead_lettered_total` | counter | | Records the writer could not process |
| `audit_writer_catalogue_unknown_total` | counter | `source`, `catalogue_version` | Records naming an unregistered catalogue version, also dead-lettered. After 20 distinct pairs the rest count as `other`; the log line `event=unknown_catalogue` has all. On AWS the library alarms on it |
| `audit_seal_age_seconds` | gauge | `profile` | Seconds since the end of the newest sealed hour, of the tenant furthest behind, as of the notary's last run |
| `audit_seal_written_total` | counter | `profile` | Seals written |
| `audit_seal_failures_total` | counter | `profile` | Tenants a notary run could not seal further |
| `audit_writer_objects_written_total`, `audit_writer_records_written_total` | counter | `profile` | Writer output |
| `audit_emit_records_dropped_total` | counter | `action` | Records an emitter's queue gave up |
| `audit_emit_queue_pending` | gauge | | Records an emitter holds, unacknowledged |
| `audit_emit_batches_failed_total`, `audit_emit_records_written_total`, `audit_emit_records_refused_total` | counter | `delivery` or `action` | The emitter's other counts |

| Label | Values |
|---|---|
| `transport` | `connect-server`, `connect-client`, `nats`, `sqs` |
| `durability` | `archived`, `queued`, `logged`, `unspecified` |

| Topic | Behaviour |
|---|---|
| Cardinality | No series is labelled by tenant: the store drops a series with too many labels while the write answers 200. The tenant is on the span |
| Seal age | The hourly notary pushes it at exit, then the series stops. Alert and dashboard add the time since the last sample: `last_over_time(m[1d]) + (time() - tlast_over_time(m[1d]))`. A notary that never reported leaves no series; the CronJob's state is the evidence |
| Index lag | Never reads below `settle` (default 2 minutes, [0062](../../decisions/0062-observe-follows-the-bucket.md)); a healthy p99 sits a little above it. The writer counts no index metrics |

## Traces

One span per write at each hop, continued across processes.

| Span | Kind | Where |
|---|---|---|
| HTTP server, then the RPC | server | Writer, receiver, query service (`otelhttp`, `otelconnect`) |
| `audit.sink.write <transport>` | client, producer, internal | `sink.Client`, NATS and SQS publishers, the front door |
| `audit.sink.consume <transport>` | consumer | NATS and SQS consumers |

`traceparent` and `tracestate` cross a queue in NATS headers and SQS message attributes. A consumer's span is a child of the first message's trace and links up to sixteen others. The liveness probe is not traced.

| No personal data | Rule |
|---|---|
| Enforced where spans leave the process | The exporter drops every attribute not on `telemetry.SpanAttributeAllowlist`, and every span event, status text and link attribute |
| Allowed | action, outcome, tenant id, durability, delivery, record and rejection counts, transport, the shape of the RPC or HTTP request (method, route, status, path) |
| Never | The actor, a subject, a client address, record data. A test enforces the list |

## Alerts

Nine rules in the group `audit.write-path`. Each rule's comment in `charts/audit/templates/alerts.yaml` gives its threshold and reason.

<!-- generated: alert-rules -->
| alert | fires when | threshold and why | severity |
|---|---|---|---|
| `AuditRecordsDeadLettered` | any increase in 15m | zero is the only healthy count: the writer accepts every well-formed record | critical |
| `AuditEmitterDroppingRecords` | any increase in 15m, any namespace | a dropped record never exists | critical |
| `AuditSealStale` | the newest sealed hour of a profile older than 3h, for 10m | the hourly notary seals an hour about ten minutes after it ends, so a healthy newest seal is at most about 1h20m old and one missed run leaves it near 2h20m; three hours fires on the second, when a gap in the chain has opened that no later seal can close | critical |
| `AuditIndexLagHigh` | p99 index lag above 600s, for 10m | the settle window (default 2m) is the floor of the lag, so ten minutes is an indexer that has stopped or is stuck; raise it with `settle` | warning |
| `AuditIndexRowsDeferred` | any increase in 15m | an object the indexer could not take: search is late or missing it | warning |
| `AuditIndexStalled` | no successful indexing pass for 15m (`maxAgeSeconds`), for 5m | the pod of an indexer that cannot index stays Ready; the gauge is the seconds since a pass last succeeded, so a stall behind any cause is caught. 15m is three polls at the default 5m interval: raise it with `interval` | critical |
| `AuditIndexPassesFailing` | over half the passes in 30m failed and at least 3, for 10m | one failed pass is a blink and is retried at the next poll; this is passes that keep failing while some may still succeed | warning |
| `AuditWriterRejectingRecords` | over 5% of records refused and at least 10, for 10m | a share, so one buggy producer on a busy stream is seen and one bad record on a quiet one is not | warning |
| `AuditQueueConsumerFailing` | a NATS or SQS consumer failing for 15m | one failure is a restart or an election; fifteen minutes is batches going round | critical |
<!-- /generated -->

### Installing them

`renders: alerts` renders only the rules. Install them from the platform that runs the ruler, beside the write-path release.

```yaml
renders: alerts
presets:                        # the write path's; one must be standard or attested
  standard: {bucket: example-audit-main}
alerts:
  namespace: audit              # where the write path runs: the writer rules' series
  objectNamespace: monitoring   # where the ruler reads rule objects from
  emitterNamespace: ".+"        # the applications' namespaces: their series carry theirs
  ruleLabels:                   # on every rule, for routing
    k8s_cluster_name: prod
  runbookBaseUrl: https://github.com/truvity/sluis/blob/master/docs/guides/audit/operate/respond-to-alerts.md
```

| Value | Effect |
|---|---|
| `alerts.format: prometheusrule` | Renders a `PrometheusRule` instead of a `VMRule` |
| `alerts.rules.<name>.enabled\|severity\|for\|labels` | Tunes each rule; a release with every rule off is refused |
| `presets` | Required: the write path's release's. Refused unless one is `standard` or `attested`, the presets with alarms |
| `alerts.namespace` | Empty is this release's namespace; set it when the alerts release runs elsewhere |

Responses: [respond to an audit alert](../../guides/audit/operate/respond-to-alerts.md).

## The dashboard

`hack/dashboards/audit-overview.py` generates `charts/audit/dashboards/audit-overview.json`, "Audit overview - $cluster". It follows truvity/observability's dashboard contract: `datasource`, `cluster` and `namespace` variables chained in that order, and `$cluster` in the title and every query.

| Panels | Content |
|---|---|
| Tiles | Dead letters, rejections, index rows deferred, newest seal age, emitter drops, consumer failures |
| Series | Ingest rate by transport, durability mix, rejection and dead-letter rates, write latency, index lag, newest seal age by profile, each application's emitter queue and drops |

Install it where Grafana runs:

```yaml
renders: dashboards
dashboards:
  namespace: monitoring   # Grafana's
  folder: Audit
```

The chart renders one ConfigMap per dashboard, labelled `grafana_dashboard: "1"`. `folder` applies only when the sidecar reads `k8s-sidecar-target-directory` (`sidecar.dashboards.folderAnnotation`).

## How it is proven

`just telemetry` runs in CI with pinned tools:

| Check | Detail |
|---|---|
| Dashboard | Regenerated; must equal the committed file |
| Lint | `dashboardlint` from truvity/observability at a pinned release, over the dashboard and over a copy with a literal datasource, which it must refuse |
| Rules | Each is rendered and unit-tested with `vmalert-tool unittest`, from the VictoriaMetrics release the observability stack runs, checksum verified. Each rule has a test that fires it and one that must not |
| Chart | Refusals in `tests/invalid/audit/alerts-*.yaml`; golden renders in `tests/golden/audit/alerts.yaml` and `dashboards.yaml` |
