# Telemetry

Telemetry is the OpenTelemetry environment, exported only to a named collector, without personal data.

## Configuration

The platform sets these variables on the pods; the configuration file has no telemetry key.

| Variable | Effect |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Names a collector for metrics and traces. With neither this nor a signal's own variable set, nothing is exported. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | The same, for one signal. |
| `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_METRIC_EXPORT_INTERVAL`, `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | The SDK's own. With `OTEL_SERVICE_NAME` unset the process names itself `sluis`, and the controllers' series carry that name too: select `service_name` `sluis`, not a controller name. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | The sampler. Unset, it is parent based with `always_on`: a caller's decision wins and every root trace is kept. The default is provisional (`defaultSampler` in `internal/telemetry/telemetry.go`). `parentbased_traceidratio` with argument `0.1` gives truvity/audit's behaviour. |

## Wiring it with the chart

The chart sets them from `telemetry.otlp`; an empty `endpoint` renders nothing ([install telemetry](../../guides/sluis/operate/install-telemetry.md)).

| Variable | From |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `telemetry.otlp.endpoint`, an http(s) URL |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `telemetry.otlp.protocol`, default `http/protobuf` |
| `OTEL_SERVICE_NAME` | `sluis` |
| `SLUIS_LEGACY_METRICS` | `telemetry.legacyMetrics`, default `true`: also publish the old metric names (below) |
| each `telemetry.otlp.extraEnv` entry, sorted, last | `OTEL_*` names only, never `OTEL_EXPORTER_OTLP_ENDPOINT` |

## Traces

A tracer exists only when a collector is named for traces.

| Where | Span |
|---|---|
| The issuer's listener (everything on the public port, the console included) | A server span per request, **named for the route** (`POST token`, `GET login_callback`) and never for the path. The route is one of a fixed set (`issuer.Route`): `discovery`, `jwks`, `authorize`, `authorize_callback`, `token`, `userinfo`, `introspect`, `revoke`, `end_session`, `device_authorization`, `login_chooser`, `login_start`, `login_callback`, `login_recovery`, `logout`, `signed_out`, `account`, `grants`, `sessions_rpc`, `connect_callback`, `console_assets`, `console_rpc`, `console`, `other`. The caller's `traceparent` is continued. |
| The console's and the session service's Connect handlers | A server span per call (`rpc.method`). |
| The controllers' clients of the console | A client span per call, and the `traceparent` carried to the console, so a tick's trace continues into the console's own spans. |
| A module call (`internal/modcall`) | A client span in the caller and a server span in the callee, `modcall <module>.<method>` (`rpc.system.name` is `sluis.modcall`, `rpc.method` the module and method). The envelope carries the `traceparent` and `tracestate`, so the callee's span is the caller's child on every transport. |
| A tick | `tick <kind>`, with the target kind, the target and the outcome (`ok` or `failed`). The kinds are `github-tick`, `github-links` and `slack-tick`. |
| A storage port call | `port <port> <operation>`, with the port, the operation and the outcome. Only inside a trace that is already being recorded, so a loop that started none makes no root span per call. |

### What leaves the process

Spans leave through the allowlist exporter `telemetry.FilterExporter`; only these attributes survive (`telemetry.SpanAttributeAllowlist`):

```text
access_roster.target.kind  access_roster.target.id  access_roster.outcome
access_roster.port  access_roster.operation  rpc.system.name  rpc.method
rpc.response.status_code  error.type  http.request.method
http.response.status_code  http.route
```

Events, link attributes and status text are removed. A port call's key is never an attribute. A test asserts no personal marker reaches the next exporter.

## Metrics

Prometheus names turn dots into underscores and add `_total` to counters: `sluis.http.requests` is `sluis_http_requests_total`. Only cluster, namespace and tier come from the resource.

| Old name | Published as | Until |
|---|---|---|
| `access_issuer.<name>`, `access_roster.<name>` | `sluis.<name>`, with the same type, unit and labels | Both are recorded while `telemetry.legacyMetrics` is on, the default in v1.75. v1.76 removes the old names and the setting, so move dashboards and rules first. The `sluis-overview` dashboard selects both. |

### The issuer

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_issuer.http.requests` | counter | `route`, `status_class` | Requests the listener answered. `status_class` is `2xx` to `5xx`. |
| `access_issuer.http.request.duration` | histogram, `s` | `route`, `status_class` | Time from a request's arrival to the handler returning. |
| `access_issuer.tokens.issued` | counter | `client_id`, `grant_type` | Access tokens signed. `grant_type` is `authorization_code`, `refresh_token`, `token_exchange`, `client_credentials` or `console_mint`. |
| `access_issuer.login.failures` | counter | `reason` | Sign-ins that did not complete. |
| `access_issuer.login.successes` | counter | `method` | Sign-ins that completed: a directory's kind, `recovery`, `browser_session` or `agent_consent` (an agent connection accepted on the consent page). |
| `access_issuer.reuse_detected` | counter | `kind` | A spent credential presented again: `authorization_code` or `refresh_token`. |
| `access_issuer.dead_refresh_token_hits` | counter | none | A refresh token refused from the issuer's in-process negative cache: one read as naming no live session twice, at least 60 s apart, and presented again within 5 minutes of the second, refused with no State read. A steady rate is a client looping on an ended chain; the WARN `refused a refresh token that names no live session` names it once per cache entry, with the client id the request named (empty for `private_key_jwt`) and an 8-hex `token_fingerprint`, at most 30 such lines a minute. |
| `access_issuer.spent_mark_ahead` | counter | none | A spent refresh token mark read that is dated more than 2 s ahead of the replica's clock. The replicas' clocks disagree, which moves the 30-second grace window. The issuer logs a WARN and keeps the grace. |
| `access_issuer.signing_keys_published` | gauge | `algorithm` | Keys in the JWKS, per algorithm. Healthy is at least one. |
| `access_issuer.signing_key.active_since_timestamp` | gauge, `s` | `algorithm` | When the active key became active (Unix seconds). |
| `access_issuer.signing_key_transitions` | counter | `event`, `algorithm` | Keys seen, activated, retired. |
| `access_issuer.kms_signatures` | counter | `kid`, `result` | `kms:Sign` calls of a KMS signer: `ok`, `throttled` or `error` ([signing with KMS](../../guides/sluis/operate/sign-with-aws-kms.md)). |

`client_id` is bounded by the policy: undeclared is `other`, none is `none`. `reuse_detected` counts codes presented twice, refresh tokens spent past the 30-second grace, unknown refresh tokens, and negative-cache refusals. A reused code, or a spent refresh token of a live session, ends that session.

`login.failures` has these `reason` values:

| Reason | Meaning |
|---|---|
| `bad_state` | the callback or form state is missing, expired, forged or from another browser |
| `unknown_provider` | the provider is not declared |
| `provider_failed` | the directory's exchange failed |
| `directory_refused`, `directory_unreachable` | the directory refused or did not answer |
| `not_entitled` | signed in, but not in a group the application requires |
| `recovery_refused` | the recovery sign-in was refused |
| `unaudited` | the audit trail could not be written |
| `not_waiting` | the authorization request is gone |
| `consent_refused` | an agent connection's consent was not accepted in that browser |
| `bad_request` | the request was malformed |

### Generated client secrets

Source: `internal/clientcreds/telemetry.go`.

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `sluis.client_secret.reconcile` | counter | `outcome` | Generated secrets looked after: `created`, `adopted`, `existing`, `conflict`, `unsupported` or `failed`. |
| `sluis.client_secret.auth` | counter | `slot` | Confidential client authentications at the token endpoint, by the secret that matched: `current`, `previous` or `none`. |
| `sluis.client_secret.rotations` | counter | `outcome` | Rotations a person asked for: `ok`, `busy`, `not_generated`, `no_record` or `failed`. |
| `sluis.client_secret.purges` | counter | `outcome` | Purges a person asked for: `ok`, `still_declared`, `no_record`, `busy` or `failed`. |
| `sluis.client_secret.orphans` | counter | none | Stored records newly found with no generated client in the policy, each counted once. |
| `sluis.client_secret.admin_refused` | counter | `reason` | Requests to the admin endpoint refused before they acted: `unauthenticated`, `forbidden` or `wrong_audience`. |

### Cloudflare credentials

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `sluis.cloudflare.rotation.last_timestamp` | gauge, `s` | `preset` | When a preset's stored credential was minted (Unix seconds). |
| `sluis.cloudflare.rotation.interval` | gauge, `s` | `preset` | The preset's configured `rotation`. |
| `sluis.cloudflare.tokens.minted` | counter | `preset`, `variant`, `outcome` | Credentials minted. `variant` is `stored` or `on_demand`; `outcome` is `ok`, `refused` (a prototype or a grant said no) or `failed`. |
| `sluis.cloudflare.tokens.swept` | counter | `preset` | Expired tokens deleted. |
| `sluis.cloudflare.prototype.refused` | counter | `preset`, `reason` | Prototypes refused at a mint or at the check on start (`prototype_active`, `prototype_forbidden`, `prototype_missing`). Any is an error to look at. |

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


Each tick emits `access_roster.ticks`, `access_roster.tick.duration` and, when ok, `access_roster.tick.last_success_timestamp` (`kind`: `github-tick`, `github-links`, `slack-tick`). A vanished series needs `absent(...)` over both names. `AccessRosterTickStale` looks back a day.

```text
absent(last_over_time(sluis_tick_last_success_timestamp_seconds[1d]) or last_over_time(access_roster_tick_last_success_timestamp_seconds[1d]))
```

The other controller series:

```text
github_roster.passes (org, outcome)  .changes  .link_changes  .breaker_trips  .rows
.seats_free  .seats_short  .links  .rate_limited (kind)  .rate_limit_remaining (resource)
slack_roster.passes (workspace, outcome)  and the same family
audit.emit.*  (the audit component's, alerted there)
```

### The ports

| Metric | Type | Labels | What it says |
|---|---|---|---|
| `access_roster.port.operation.duration` | histogram, `s` | `port`, `operation`, `outcome` | One storage port call. |

| Label | Values |
|---|---|
| `port` | `state`, `index`, `blob` |
| `operation` | `get`, `put`, `create`, `update`, `delete`, `delete_if_revision`, `peek_revision`, `list`, `add`, `remove`, `members`, `read`, `write`, `write_if_version`, `replace`, `read_all` |
| `outcome` | `ok`, `not_found`, `exists`, `conflict`, `unavailable` (store down), `canceled` (caller gave up), `error` |

A conflict is a lease or session rotation working, not an error. Issuer and port series carry no workspace, organisation, person or group label; controller series carry `target`, `org` or `workspace`.

## Alerts

Eleven rules, each also as `Sluis…`, rendered with `renders: alerts`. A threshold is `alerts.rules.<rule>`. A rule whose series is absent does not fire.

<!-- generated: telemetry-alerts -->

Source: `tests/golden/sluis/alerts.yaml`, the render of `charts/sluis/templates/alerts.yaml` with default values (22 rules). The expressions carry the default thresholds; every one is a value under `alerts.rules`.

| Alert | Severity | For | What it says | Default expression |
|---|---|---|---|---|
| `AccessRosterNoSigningKeyPublished` | critical | 5m | The issuer's key ring holds no key to publish for this algorithm, so its JWKS is empty and no relying party can verify a token. | `(min by (k8s_cluster_name, namespace, algorithm) (sluis_signing_keys_published{namespace="sluis"}) < 1) or (min by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_keys_published{namespace="sluis"}) < 1)` |
| `SluisNoSigningKeyPublished` | critical | 5m | The issuer's key ring holds no key to publish for this algorithm, so its JWKS is empty and no relying party can verify a token. | `(min by (k8s_cluster_name, namespace, algorithm) (sluis_signing_keys_published{namespace="sluis"}) < 1) or (min by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_keys_published{namespace="sluis"}) < 1)` |
| `AccessRosterSigningKeyRotationStalled` | warning | 1h | The active signing key is older than 30240000s and has not rotated. | `(time() - max by (k8s_cluster_name, namespace, algorithm) (sluis_signing_key_active_since_timestamp_seconds{namespace="sluis"}) > 30240000) or (time() - max by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_key_active_since_timestamp_seconds{namespace="sluis"}) > 30240000)` |
| `SluisSigningKeyRotationStalled` | warning | 1h | The active signing key is older than 30240000s and has not rotated. | `(time() - max by (k8s_cluster_name, namespace, algorithm) (sluis_signing_key_active_since_timestamp_seconds{namespace="sluis"}) > 30240000) or (time() - max by (k8s_cluster_name, namespace, algorithm) (access_issuer_signing_key_active_since_timestamp_seconds{namespace="sluis"}) > 30240000)` |
| `AccessRosterIssuer5xx` | critical | 10m | More than 5% of the requests in the last 10 minutes failed on the server side. | `(( sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) / sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis"}[10m])) ) > 0.05 and sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) >= 5) or (( sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) / sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis"}[10m])) ) > 0.05 and sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) >= 5)` |
| `SluisIssuer5xx` | critical | 10m | More than 5% of the requests in the last 10 minutes failed on the server side. | `(( sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) / sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis"}[10m])) ) > 0.05 and sum by (k8s_cluster_name, namespace) (increase(sluis_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) >= 5) or (( sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) / sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis"}[10m])) ) > 0.05 and sum by (k8s_cluster_name, namespace) (increase(access_issuer_http_requests_total{namespace="sluis",status_class="5xx"}[10m])) >= 5)` |
| `AccessRosterTokenEndpointSlow` | warning | 15m | Token requests are taking longer than 2s at the 99th percentile. | `(histogram_quantile(0.99, sum by (k8s_cluster_name, namespace, le) (rate(sluis_http_request_duration_seconds_bucket{namespace="sluis",route="token"}[10m]))) > 2) or (histogram_quantile(0.99, sum by (k8s_cluster_name, namespace, le) (rate(access_issuer_http_request_duration_seconds_bucket{namespace="sluis",route="token"}[10m]))) > 2)` |
| `SluisTokenEndpointSlow` | warning | 15m | Token requests are taking longer than 2s at the 99th percentile. | `(histogram_quantile(0.99, sum by (k8s_cluster_name, namespace, le) (rate(sluis_http_request_duration_seconds_bucket{namespace="sluis",route="token"}[10m]))) > 2) or (histogram_quantile(0.99, sum by (k8s_cluster_name, namespace, le) (rate(access_issuer_http_request_duration_seconds_bucket{namespace="sluis",route="token"}[10m]))) > 2)` |
| `AccessRosterTickFailing` | warning | 0m | The controller's tick of this target failed 3 or more times within 45m. | `(sum by (k8s_cluster_name, namespace, kind, target) (increase(sluis_ticks_total{namespace="sluis",outcome="failed"}[45m])) >= 3) or (sum by (k8s_cluster_name, namespace, kind, target) (increase(access_roster_ticks_total{namespace="sluis",outcome="failed"}[45m])) >= 3)` |
| `SluisTickFailing` | warning | 0m | The controller's tick of this target failed 3 or more times within 45m. | `(sum by (k8s_cluster_name, namespace, kind, target) (increase(sluis_ticks_total{namespace="sluis",outcome="failed"}[45m])) >= 3) or (sum by (k8s_cluster_name, namespace, kind, target) (increase(access_roster_ticks_total{namespace="sluis",outcome="failed"}[45m])) >= 3)` |
| `AccessRosterTickStale` | critical | 10m | This target has not completed a tick for longer than 3600s. | `(time() - max by (k8s_cluster_name, namespace, kind, target) (last_over_time(sluis_tick_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 3600) or (time() - max by (k8s_cluster_name, namespace, kind, target) (last_over_time(access_roster_tick_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 3600)` |
| `SluisTickStale` | critical | 10m | This target has not completed a tick for longer than 3600s. | `(time() - max by (k8s_cluster_name, namespace, kind, target) (last_over_time(sluis_tick_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 3600) or (time() - max by (k8s_cluster_name, namespace, kind, target) (last_over_time(access_roster_tick_last_success_timestamp_seconds{namespace="sluis"}[1d])) > 3600)` |
| `AccessRosterLeaseLost` | warning | 0m | Controllers lost their lease on a target 3 or more times within 1h. | `(sum by (k8s_cluster_name, namespace, kind) (increase(sluis_leases_lost_total{namespace="sluis"}[1h])) >= 3) or (sum by (k8s_cluster_name, namespace, kind) (increase(access_roster_leases_lost_total{namespace="sluis"}[1h])) >= 3)` |
| `SluisLeaseLost` | warning | 0m | Controllers lost their lease on a target 3 or more times within 1h. | `(sum by (k8s_cluster_name, namespace, kind) (increase(sluis_leases_lost_total{namespace="sluis"}[1h])) >= 3) or (sum by (k8s_cluster_name, namespace, kind) (increase(access_roster_leases_lost_total{namespace="sluis"}[1h])) >= 3)` |
| `AccessRosterCloudflareRotationStale` | critical | 5m | The stored credential of this preset is older than 2 times its rotation. | `time() - max by (k8s_cluster_name, namespace, preset) (last_over_time(sluis_cloudflare_rotation_last_timestamp_seconds{namespace="sluis"}[1d])) > 2 * max by (k8s_cluster_name, namespace, preset) (last_over_time(sluis_cloudflare_rotation_interval_seconds{namespace="sluis"}[1d]))` |
| `SluisCloudflareRotationStale` | critical | 5m | The stored credential of this preset is older than 2 times its rotation. | `time() - max by (k8s_cluster_name, namespace, preset) (last_over_time(sluis_cloudflare_rotation_last_timestamp_seconds{namespace="sluis"}[1d])) > 2 * max by (k8s_cluster_name, namespace, preset) (last_over_time(sluis_cloudflare_rotation_interval_seconds{namespace="sluis"}[1d]))` |
| `AccessRosterGitHubRateLimitLow` | warning | 30m | The GitHub budget for this resource has been under 100 requests for 30m. | `min by (k8s_cluster_name, namespace, resource) (github_roster_rate_limit_remaining{namespace="sluis"}) < 100` |
| `SluisGitHubRateLimitLow` | warning | 30m | The GitHub budget for this resource has been under 100 requests for 30m. | `min by (k8s_cluster_name, namespace, resource) (github_roster_rate_limit_remaining{namespace="sluis"}) < 100` |
| `AccessRosterSeatsShort` | warning | 30m | The controller could not invite everyone the policy admits to this organisation because it has no free seats. | `max by (k8s_cluster_name, namespace, org) (github_roster_seats_short{namespace="sluis"}) > 0` |
| `SluisSeatsShort` | warning | 30m | The controller could not invite everyone the policy admits to this organisation because it has no free seats. | `max by (k8s_cluster_name, namespace, org) (github_roster_seats_short{namespace="sluis"}) > 0` |
| `AccessRosterPortErrors` | critical | 10m | More than 5% of the calls to this storage port failed in the last 5 minutes. | `(( sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) / sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis"}[5m])) ) > 0.05 and sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) >= 5) or (( sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) / sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis"}[5m])) ) > 0.05 and sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) >= 5)` |
| `SluisPortErrors` | critical | 10m | More than 5% of the calls to this storage port failed in the last 5 minutes. | `(( sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) / sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis"}[5m])) ) > 0.05 and sum by (k8s_cluster_name, namespace, port) (increase(sluis_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) >= 5) or (( sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) / sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis"}[5m])) ) > 0.05 and sum by (k8s_cluster_name, namespace, port) (increase(access_roster_port_operation_duration_seconds_count{namespace="sluis",outcome=~"unavailable\|error"}[5m])) >= 5)` |
<!-- /generated -->

### Runbook

| Alert | Cause and check |
|---|---|
| `Sluis…` | The row of the `AccessRoster…` rule of the same name. |
| `AccessRosterNoSigningKeyPublished` | The key ring is empty, so the JWKS is empty. Check the Secret at `config.signingKey.file` (cert-manager's Certificate or `signingKey.existingSecret`) and the issuer log ("the active signing key changed", "a signing key was seen"). A rollout shows zero for seconds, not five minutes. |
| `AccessRosterSigningKeyRotationStalled` | The active key was not replaced. cert-manager renews `signingKey.certificate.renewBefore` early (720h before 8760h), so a key is at most about 335 days old and the rule fires at 350. Check `kubectl describe certificate` and `config.signingKey.pollInterval`. After changing `duration` or `renewBefore`, set `maxAgeSeconds` to their difference plus two weeks. For hand rotation, turn the rule off or set your cadence. |
| `AccessRosterIssuer5xx` | Server errors. The dashboard's 5xx panel names the route. If `AccessRosterPortErrors` also fires, the store is the cause. Otherwise check `directory_unreachable` and policy loading. |
| `AccessRosterTokenEndpointSlow` | Slow p99. A token costs one store write: check the port latency panel (DynamoDB, or the API server with the legacy store), then the policy size. |
| `AccessRosterTickFailing` | A target's ticks failed three times in 45 minutes. The console's controller page and the log ("a pass over an organisation failed") give the error: uninstalled App, revoked credential or GitHub down. |
| `AccessRosterTickStale` | No tick for an hour: the controller is not running, nobody can take the lease, or the loop hangs. Check the sluis pod, a lease held by a gone runner (it expires on its own) and the log. A target removed from the policy fires for at most a day, so remove it from the policy first. |
| `AccessRosterLeaseLost` | One loss is normal. Repeated losses mean two runners on one target (more than one replica without a shared State, which the chart refuses; [high availability](../../guides/sluis/operate/high-availability.md)) or an unreachable State: see `AccessRosterPortErrors`. |
| `AccessRosterCloudflareRotationStale` | A preset's credential is older than twice its `rotation`. The log line "a Cloudflare token was not minted" gives the reason: `prototype_active`, `prototype_forbidden`, `prototype_missing`, `minter_missing`, `store_error` or `cloudflare_error`. On Lambda check that the `{"kind":"cloudflare"}` schedule invokes the function. |
| `AccessRosterGitHubRateLimitLow` | Budget under 100 for half an hour. `github_roster_rate_limited_total` shows waits. Lengthen the controller's `interval` or find what else spends the App's budget. |
| `AccessRosterSeatsShort` | Fewer free seats than invitations. Buy a seat or remove a member; the next pass invites. |
| `AccessRosterPortErrors` | Over 5% of storage calls failed. `unavailable` is the store down: check its health and NetworkPolicy. `error` is anything else: the log line names the call. Conflicts and missing keys do not count. |
