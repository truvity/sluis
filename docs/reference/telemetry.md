# Telemetry

What sluis publishes, what it never publishes, the alerts and the
dashboard that read it, and how to install them. The contract it follows is
[0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)
to
[0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md) and
[ports](../explanation/ports.md): telemetry is the OpenTelemetry
environment and nothing else, it is exported only when a collector is named, and
it carries no personal data.

## Configuration

There is none in the configuration file. The platform sets the OpenTelemetry
variables on the pods and the SDK reads them:

| Variable | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Names a collector for metrics and traces. Unset (and neither signal's own variable below set), nothing is exported and every instrument records into a no-op. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | The same, for one signal. |
| `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_METRIC_EXPORT_INTERVAL`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | The SDK's own. The one process names itself `access-issuer` when `OTEL_SERVICE_NAME` is unset (the resource's historic name, kept so series and dashboards keep their identity). Since v1.63 the controllers' series carry it too: **a dashboard or alert that selects `service_name` `github-roster` or `slack-roster` must select `access-issuer`** (the controllers' own metric names, and the `kind` and `target` labels, are unchanged). |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | The sampler. **Unset, it is parent based with `always_on`**: a caller's decision wins and every root trace is kept. |

**The sampler default is provisional.** truvity/audit keeps a tenth of root
traces (`parentbased_traceidratio` at 0.1) because its write path is frequent and
repetitive. This service's traffic is a sign-in and a tick per interval, which a
trace store can hold whole, so the default here is `always_on`. Which of the two
this service should ship is not decided. It is one function
(`defaultSampler` in `internal/telemetry/telemetry.go`), and
`OTEL_TRACES_SAMPLER=parentbased_traceidratio` with `OTEL_TRACES_SAMPLER_ARG=0.1`
on the pods gives audit's behaviour without a release.

## Wiring it with the chart

The chart sets those variables on the pod from one value block, `telemetry.otlp` (`endpoint`, `protocol`, `extraEnv`).
With `endpoint` empty it renders nothing and the pod exports nothing
([policy ADR 0006](https://github.com/truvity/policy/blob/master/docs/decisions/0006-telemetry-is-the-sdk-environment.md)).
The steps, and what the chart refuses, are in [install telemetry](../how-to/install-telemetry.md).

## Traces

A tracer exists only when a collector is named for traces.

| Where | Span |
|---|---|
| The issuer's listener (everything on the public port, the console included) | A server span per request, **named for the route** (`POST token`, `GET login_callback`) and never for the path. The route is one of a fixed set (`issuer.Route`): `discovery`, `jwks`, `authorize`, `authorize_callback`, `token`, `userinfo`, `introspect`, `revoke`, `end_session`, `device_authorization`, `login_chooser`, `login_start`, `login_callback`, `login_recovery`, `logout`, `signed_out`, `account`, `grants`, `sessions_rpc`, `connect_callback`, `console_assets`, `console_rpc`, `console`, `other`. The caller's `traceparent` is continued. |
| The console's and the session service's Connect handlers | A server span per call (`rpc.method`). |
| The controllers' clients of the console | A client span per call, and the `traceparent` carried to the console, so a tick's trace continues into the console's own spans. |
| A tick | `tick <kind>`, with the target kind, the target and the outcome (`ok` or `failed`). The kinds are `github-tick`, `github-links` and `slack-tick`. |
| A storage port call | `port <port> <operation>`, with the port, the operation and the outcome. Only inside a trace that is already being recorded, so a loop that started none makes no root span per call. |

### What leaves the process

Spans are read by everyone with a grant on the trace store, so the rule that
they hold no personal data is a property of the exporter and not a promise by
each call site. Every span leaves through an allowlist exporter
(`telemetry.FilterExporter`, the one truvity/audit uses):

- **Attributes.** Only the names in `telemetry.SpanAttributeAllowlist` survive:
  `access_roster.target.kind`, `access_roster.target.id`, `access_roster.outcome`,
  `access_roster.port`, `access_roster.operation`, `rpc.system.name`,
  `rpc.method`, `rpc.response.status_code`, `error.type`,
  `http.request.method`, `http.response.status_code` and `http.route`. The HTTP
  and RPC instrumentation record the client's address, the user agent, the raw
  URL path and the peer; they are dropped, whoever set them.
- **Events, links' attributes and the status text** are removed outright: a
  recorded error's message is free text and may quote an address or a group.
- **The key of a port call is never an attribute or a label.** A key names a
  person (`ses.<person>.`) or a target.

The one identifier a span may carry is the target's own name, a GitHub
organisation or a Slack workspace the policy declares. Never an email, a
subject, a group name, a token or its hash, or an address.

`internal/telemetry` has a test that plants those markers in attributes, events,
errors, status text and links, and in a real HTTP request and a real Connect
call, and asserts that none of them reaches the next exporter.

## Metrics

The names below are the OpenTelemetry names; the Prometheus names are what the
metrics gateway makes of them: dots to underscores, `_total` on a counter, the
unit as a suffix (`access_issuer.http.requests` is
`access_issuer_http_requests_total`). Only the cluster, the namespace and the
tier become labels from the resource; every label below is a metric attribute.

Source: the instruments in `internal/issuer/metrics.go`, `internal/rails` and the controllers' packages.

### The issuer

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_issuer.http.requests` | counter | `route`, `status_class` | Requests the listener answered. `status_class` is `2xx` to `5xx`. |
| `access_issuer.http.request.duration` | histogram, `s` | `route`, `status_class` | Time from a request's arrival to the handler returning. |
| `access_issuer.tokens.issued` | counter | `client_id`, `grant_type` | Access tokens signed. `grant_type` is `authorization_code`, `refresh_token`, `token_exchange`, `client_credentials` or `console_mint`. |
| `access_issuer.login.failures` | counter | `reason` | Sign-ins that did not complete. |
| `access_issuer.login.successes` | counter | `method` | Sign-ins that completed: a directory's kind, `recovery` or `browser_session`. |
| `access_issuer.reuse_detected` | counter | `kind` | A spent credential presented again: `authorization_code` or `refresh_token`. |
| `access_issuer.dead_refresh_token_hits` | counter | none | A refresh token refused from the issuer's in-process negative cache: one read as naming no live session twice, at least 60 s apart, and presented again within 5 minutes of the second, refused with no State read. A steady rate is a client looping on an ended chain; the WARN `refused a refresh token that names no live session` names it once per cache entry, with the client id the request named (empty for `private_key_jwt`) and an 8-hex `token_fingerprint`, at most 30 such lines a minute. |
| `access_issuer.spent_mark_ahead` | counter | none | A spent refresh token mark read that is dated more than 2 s ahead of the replica's clock. The replicas' clocks disagree, which moves the 30-second grace window. The issuer logs a WARN and keeps the grace. |
| `access_issuer.signing_keys_published` | gauge | `algorithm` | Keys in the JWKS, per algorithm. Healthy is at least one. |
| `access_issuer.signing_key.active_since_timestamp` | gauge, `s` | `algorithm` | When the active key became active (Unix seconds). |
| `access_issuer.signing_key_transitions` | counter | `event`, `algorithm` | Keys seen, activated, retired. |
| `access_issuer.kms_signatures` | counter | `kid`, `result` | `kms:Sign` calls of a KMS signer: `ok`, `throttled` or `error` ([signing with KMS](../how-to/sign-with-aws-kms.md)). |

**Why `client_id` is a safe label.** The policy declares every client, so an
installation has tens, not thousands, and the label cannot grow with its users.
The code holds that bound itself: an id the policy does not declare is `other`,
and a request with no client (a workload's exchange) is `none`, so a request
naming a client nobody declared cannot mint a series.

**The `login.failures` reasons** are a fixed set: `bad_state` (a callback or
a form whose state is missing, expired, forged or not from this browser),
`unknown_provider`, `provider_failed` (the directory's own exchange),
`directory_refused`, `directory_unreachable`, `not_entitled` (signed in, but not
in a group the application requires), `recovery_refused`, `unaudited` (the audit
trail could not be written), `not_waiting` (the authorization request is gone)
and `bad_request`.

**`reuse_detected` has two kinds with two meanings.** An authorization code
presented twice is a certain reuse, and the session it opened is ended. A
refresh token presented after its 30-second grace is either spent in a live
session or unknown. A spent one ends that session, but only once the library has
authenticated the client and matched it to the session's; the count does not say
which of the two it was, and a forged or unsealable mark counts as unknown.
A burst of either is a client bug or a stolen credential; one is noise.
A refresh token refused from the negative cache is still counted here, so the
rate does not drop when the cache answers; `dead_refresh_token_hits` says how
much of it cost no State read.

### Generated client secrets

Source: `internal/clientcreds/telemetry.go`. No client id is ever a label, so a guessed id cannot mint a series.

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `sluis.client_secret.reconcile` | counter | `outcome` | Generated secrets looked after: `created`, `adopted`, `existing`, `conflict`, `unsupported` or `failed`. |
| `sluis.client_secret.auth` | counter | `slot` | Confidential client authentications at the token endpoint, by the secret that matched: `current`, `previous` or `none`. |
| `sluis.client_secret.rotations` | counter | `outcome` | Rotations a person asked for: `ok`, `busy`, `not_generated`, `no_record` or `failed`. |
| `sluis.client_secret.purges` | counter | `outcome` | Purges a person asked for: `ok`, `still_declared`, `no_record`, `busy` or `failed`. |
| `sluis.client_secret.orphans` | counter | none | Stored records newly found with no generated client in the policy, each counted once. |
| `sluis.client_secret.admin_refused` | counter | `reason` | Requests to the admin endpoint refused before they acted: `unauthenticated`, `forbidden` or `wrong_audience`. |

### The controllers and the rails

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_roster.ticks` | counter | `kind`, `target`, `outcome` | Ticks. `outcome` is `ok` (the tick ran to its report, whatever the report says about the target) or `failed`. |
| `access_roster.tick.duration` | histogram, `s` | `kind`, `outcome` | How long a tick took. |
| `access_roster.tick.last_success_timestamp` | gauge, `s` | `kind`, `target` | When a target's last ok tick ended (Unix seconds). |
| `access_roster.leases.acquired` | counter | `kind` | Leases taken by this runner. |
| `access_roster.leases.contended` | counter | `kind` | Leases asked for and held by another runner. |
| `access_roster.leases.lost` | counter | `kind` | Leases held and lost: taken over, or not renewable for a whole lifetime. The tick stopped before its next write. |
| `access_roster.leases.held` | up-down counter | `kind` | Leases this runner holds now. |

**What a controller emits on every tick, to alert on its absence.** Each tick
(one target, under its lease) emits `access_roster.ticks` and
`access_roster.tick.duration` and, when it ended ok,
`access_roster.tick.last_success_timestamp`, with the target in `target`
(`github-tick` and `github-links` for GitHub, `slack-tick` for Slack, in `kind`). The
per-controller series that move with each pass over a target are
`github_roster.passes` (by `org` and `outcome`) and `slack_roster.passes` (by
`workspace` and `outcome`), with the `*.rows` gauges recorded in the same call.
A controller that has stopped shows as `access_roster_tick_last_success_timestamp_seconds`
ageing past two intervals, or as `increase(github_roster_passes_total[1h]) == 0`; a
series that has gone altogether needs `absent_over_time(...)`, which the chart's
`AccessRosterTickStale` does not do (it looks back a day). With more than one replica the lease counters
(`access_roster.leases.contended`) rise by design.

The controllers' own metrics are the existing ones: `github_roster.passes`,
`.changes`, `.link_changes`, `.breaker_trips`, `.rows`, `.seats_free`,
`.seats_short`, `.links`, and the rate-limit pair `github_roster.rate_limited`
(waits, by `kind`) and `github_roster.rate_limit_remaining` (the budget left, by
`resource`), and the Slack equivalents `slack_roster.*`. The audit emitter's
`audit.emit.*` instruments are the audit component's, and its own alerts read
them.

### The ports

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_roster.port.operation.duration` | histogram, `s` | `port`, `operation`, `outcome` | One storage port call. |

`port` is `state`, `index` or `blob`. `operation` is the call (`get`, `put`,
`create`, `update`, `delete`, `delete_if_revision`, `peek_revision`, `list`,
`add`, `remove`, `members`, `read`, `write`, `write_if_version`, `replace`,
`read_all`).
`outcome` is `ok`, `not_found`, `exists`, `conflict`, `unavailable` (the store
is down), `canceled` (the caller gave up) or `error`. The histogram's count by
outcome is the call rate, the error rate and the **compare-and-swap conflicts**
(`outcome="conflict"`): a lost conflict is a lease or a session rotation
working, and is not an error.

### The exports

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_roster.export.attempts` | counter | `export`, `outcome` | Export attempts ([0034](../decisions/0034-exports-go-to-openbao-directly.md)). `outcome` is `ok` (the copy is in the store: written, or already as it should be), `failed` (retried with backoff; the copy is stale) or `skipped` (the source has nothing to copy yet: an App created and not installed, an empty bundle). |
| `access_roster.export.duration` | histogram, `s` | `outcome` | How long an attempt took. |
| `access_roster.export.last_success_timestamp` | gauge, `s` | `export` | When the export last had its copy in the store (Unix seconds). |
| `access_roster.export.contended` | counter | `export` | Attempts another replica held the lease for. The normal answer of the replica that did not win. |

`export` is the export's name, which the deployment declares (`slack-app.alerts`,
`runner-app.stable.truvity`, `bundle.github-apps`), so the label is bounded by the
configuration and never carries a path, a namespace or a value. An attempt is also a
span (`export`, with the target kind and the outcome). The log names the export and
the target on every failure and never a value.


### No workspace or organisation label on the issuer's series

Nothing on the issuer's or the ports' series is labelled by workspace,
organisation, person or group. The controllers' series carry the target
(`target`, `org`, `workspace`) for one reason: an alert on "this organisation
has stopped" has to say which. A target is a GitHub organisation or a Slack
workspace the policy declares (`acts_in`), so the label is bounded by the
installation's own declaration, a handful, and does not grow with its people.
Nothing a person controls becomes a label.

## Alerts

Twelve rules, in one group, rendered by the chart with `renders: alerts`. Every
threshold is a value (`alerts.rules.<rule>`) and its reason is in the comment
above the rule in `charts/sluis/templates/alerts.yaml`. Every
aggregation keeps the cluster label, since one store holds many clusters.

<!-- generated: telemetry-alerts -->

Source: `tests/golden/sluis/alerts.yaml`, the render of `charts/sluis/templates/alerts.yaml` with default values (12 rules). The expressions carry the default thresholds; every one is a value under `alerts.rules`.

| Alert | Severity | For | What it says | Default expression |
|---|---|---|---|---|
| `AccessRosterNoSigningKeyPublished` | critical | 5m | The issuer's key ring holds no key to publish for this algorithm, so its JWKS is empty and no relying party can verify a token. | `min by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_keys_published{namespace="sluis"}) < 1` |
| `AccessRosterSigningKeyRotationStalled` | warning | 1h | The active signing key is older than 30240000s and has not rotated. | `time() - max by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_key_active_since_timestamp_seconds{namespace="sluis"}) > 30240000` |
| `AccessRosterIssuer5xx` | critical | 10m | More than 5% of the requests in the last 10 minutes failed on the server side. | `( sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) / sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis"}[10m])) ) > 0.05 and sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) >= 5` |
| `AccessRosterTokenEndpointSlow` | warning | 15m | Token requests are taking longer than 2s at the 99th percentile. | `histogram_quantile(0.99, sum by (k8s_cluster_name, namespace, le) (rate(access_issuer_http_request_duration_seconds_bucket{namespace="sluis",route="token"}[10m]))) > 2` |
| `AccessRosterTickFailing` | warning | 0m | The controller's tick of this target failed 3 or more times within 45m. | `sum by (k8s_cluster_name, namespace, kind, target) (increase(access_roster_ticks_total{namespace="sluis",outcome="failed"}[45m])) >= 3` |
| `AccessRosterTickStale` | critical | 10m | This target has not completed a tick for longer than 3600s. | `time() - max by (k8s_cluster_name, namespace, kind, target) (last_over_time(access_roster_tick_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 3600` |
| `AccessRosterLeaseLost` | warning | 0m | Controllers lost their lease on a target 3 or more times within 1h. | `sum by (k8s_cluster_name, namespace, kind) (increase(access_roster_leases_lost_total{namespace="sluis"}[1h])) >= 3` |
| `AccessRosterGitHubRateLimitLow` | warning | 30m | The GitHub budget for this resource has been under 100 requests for 30m. | `min by (k8s_cluster_name, namespace, resource) (github_roster_rate_limit_remaining{namespace="sluis"}) < 100` |
| `AccessRosterSeatsShort` | warning | 30m | The controller could not invite everyone the policy admits to this organisation because it has no free seats. | `max by (k8s_cluster_name, namespace, org) (github_roster_seats_short{namespace="sluis"}) > 0` |
| `AccessRosterPortErrors` | critical | 10m | More than 5% of the calls to this storage port failed in the last 5 minutes. | `( sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) / sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis"}[5m])) ) > 0.05 and sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) >= 5` |
| `AccessRosterExportFailing` | warning | 15m | The copy of this secret into OpenBao failed 3 or more times within 30m, so what a consumer reads there is stale. | `sum by (k8s_cluster_name, namespace, export) (increase(access_roster_export_attempts_total{namespace="sluis",outcome="failed"}[30m])) >= 3` |
| `AccessRosterExportStale` | warning | 10m | This export has not had its copy in the store for longer than 10800s. | `time() - max by (k8s_cluster_name, namespace, export) (last_over_time(access_roster_export_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 10800` |
<!-- /generated -->

A rule whose series is absent does not fire: whether the issuer or the
controller is running at all is the platform's alert on its own scrape, not
this chart's.

### Runbook

#### AccessRosterNoSigningKeyPublished

The issuer's key ring holds no key to publish for an algorithm, so its JWKS is
empty: no relying party can verify a token, and a new one cannot be signed. Look
at the Secret the chart mounts at `config.signingKey.file` (cert-manager's
Certificate, or `signingKey.existingSecret`) and the issuer's log for why the
file was not read ("the active signing key changed" and "a signing key was seen"
are the lines that say a key arrived). A rollout shows a zero for seconds; five
minutes is not a rollout.

#### AccessRosterSigningKeyRotationStalled

The active key has not been replaced. cert-manager replaces the certificate
`signingKey.certificate.renewBefore` ahead of its end (720h before 8760h by
default), so a key is at most about 335 days old and the rule fires at 350. Look
at the Certificate (`kubectl describe certificate`), its issuer, and whether the
issuer is reading the mounted file (`config.signingKey.pollInterval`). The day
the certificate expires every token stops verifying. If you changed `duration`
or `renewBefore`, set `maxAgeSeconds` to their difference plus two weeks; if the
key is rotated by hand (`signingKey.existingSecret`), turn the rule off or set it
to your cadence.

#### AccessRosterIssuer5xx

The listener answers server errors. The dashboard's 5xx panel names the route,
the issuer's log has the error. If `AccessRosterPortErrors` is also firing, the
store is the cause: look there first. Otherwise a directory that cannot be
reached (`directory_unreachable` in the sign-in failures) or a policy that does
not load are the usual causes.

#### AccessRosterTokenEndpointSlow

Token requests are slow at the 99th percentile. A token is signed in memory and
costs one store write, so look at the port latency panel (a slow DynamoDB, or a
slow API server on the namespace's objects with the legacy store), then the size of the policy.

#### AccessRosterTickFailing

A controller's ticks of one target failed three times in 45 minutes. The
controller's page in the console shows the report and the error; in the log it
is "a pass over an organisation failed" (GitHub) or the Slack equivalent. An
uninstalled App, a revoked credential and GitHub being down are the causes. The
console answering under another policy during a rollout is retried within
seconds and does not reach this rule.

#### AccessRosterTickStale

A target has completed no tick for an hour. This is the absence rule: it fires
when the controller is not running, when nobody can take the lease, and when the
loop hangs. Check the sluis pod (the controllers run in it, one process), whether a lease is held by a
runner that is gone (it expires on its own after its lifetime), and the log. A
target the policy no longer declares fires for at most a day and then leaves,
because the last value is looked back over a day; remove it from the policy
first.

#### AccessRosterLeaseLost

A lease is lost when another runner takes it over, or it could not be renewed
for a whole lifetime. One loss is the design working: the tick stopped before its
next write. Repeated losses are two runners on one target (more than one replica where the State is not shared, which the chart
refuses; see [high availability](../how-to/high-availability.md)) or a State that cannot be reached
to renew: see `AccessRosterPortErrors`.

#### AccessRosterGitHubRateLimitLow

GitHub's budget for a resource has been under 100 requests for half an hour.
`github_roster_rate_limited_total` says how often a call already waited. Lengthen
the controller's `interval`, or look for something else spending the App's budget.

#### AccessRosterSeatsShort

An organisation has fewer free seats than the invitations the policy admits. The
controller is healthy; buy a seat or remove a member who no longer belongs and
the next pass sends the invitations.

#### AccessRosterPortErrors

More than 5% of the calls to a storage port failed. State and index are DynamoDB, or the
namespace's ConfigMaps and Secrets with the legacy store; blobs are S3, or the reports' ConfigMaps.
`unavailable` is the store being down (check its own health and the
NetworkPolicy to it); `error` is anything else, and the log line beside it names
the call. Lost conflicts and missing keys are not counted.

#### AccessRosterExportFailing

An export's copy into OpenBao failed three times in half an hour, held for fifteen
minutes. What a consumer reads there is stale; nothing live is affected, which is why
this is a warning. The log line "an export failed, so the copy is stale" names the
export, the target and the error. A `403` naming `permission denied` on `log in` is a
role that does not exist or is not bound to this workload's identity; on a path it is a
policy that lacks `read`, `create`, `update` or `patch` on `kv/data/<prefix>/*` in that
namespace; `unavailable` is OpenBao being down, sealed or unreachable, or its
certificate not trusted (`ports.export.openbao.caFile`). An export that fails from the
first attempt has no last-success series, which is why this rule exists beside the next.

#### AccessRosterExportStale

An export has not had its copy in the store for three hours, with an interval of one.
It catches what the failure counter cannot: the service is not running its exports
(`exports` is empty, or the process is down), nobody can take the export's lease
(`lease.export:<name>` is held by a replica that is gone; it expires on its own), or a
loop hangs. A source that has nothing to copy is `skipped` and stamps nothing: an App
that is declared in `exports` and not yet installed fires this rule once it has once
been copied and is then removed, and not before.

## Installing the modes

The chart's `renders` value chooses what a release is: `app` (the default), `alerts` or `dashboards`. The last two render only
the named objects. Installing them is [install telemetry](../how-to/install-telemetry.md).

## How it is held

`just telemetry` (its own CI job, and part of `check`):

- regenerates the dashboard from `hack/dashboards/access-roster-overview.py` and
  fails on a difference from the committed JSON;
- runs `dashboardlint` from truvity/observability at a pinned version over it, and
  proves the lint is real by feeding it the same dashboard with a literal
  datasource, which must fail;
- unit-tests every rule with `vmalert-tool` (a pinned, checksum-verified
  VictoriaMetrics release, the engine of the estate's own ruler) against
  `tests/rules/sluis-alerts.test.yaml`. Every rule has a case that fires
  it, with its labels and text, and at least one that must not, and
  `tests/chart/alerts_test.go` refuses a rule without both.

`just chart-lint` holds the goldens for both modes
(`tests/golden/sluis/alerts.yaml`, `dashboards.yaml`) and that the
default render did not change.
