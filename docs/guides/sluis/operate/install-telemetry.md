# Install telemetry

Point the pod at an OpenTelemetry collector, then install the alerts and Grafana dashboard. [Telemetry](../../../reference/sluis/telemetry.md) lists the metrics and alerts.

## Before you start

- You need an OTLP collector (`http/protobuf`) and a Grafana sidecar that reads labelled ConfigMaps.

- You need a ruler that reads `VMRule`, or `PrometheusRule` with `alerts.format: prometheusrule`.

- The chart refuses a non-http(s) endpoint, an `extraEnv` name without the `OTEL_` prefix, and `OTEL_EXPORTER_OTLP_ENDPOINT` in `extraEnv`; `helm template` names the key.

- `extraEnv` renders as plain values; put `OTEL_EXPORTER_OTLP_HEADERS` with a token in a Secret via `secretEnv`.

- Alerts and dashboards are separate releases: each renders only its own objects.

- Series use the name `sluis` ([ADR 0037](../../../decisions/0037-one-process-everywhere.md)); v1.75 also publishes the old metric names.

## Steps

1. Add this to the release values, then `helm template` and `helm upgrade`; an empty `endpoint` renders nothing.

   ```yaml
   telemetry:
     otlp:
       endpoint: http://otlp-gateway.observability.svc:4318
       protocol: http/protobuf                                 # the default
       extraEnv:                                               # any other OTEL_* variable
         OTEL_TRACES_SAMPLER: parentbased_traceidratio         # optional: a tenth of root traces
         OTEL_TRACES_SAMPLER_ARG: "0.1"
   ```

2. Install the alerts. `alerts.ruleLabels` adds labels to every rule beside its `severity`; `alerts.rules.<rule>.labels` to one rule. `alerts.runbookBaseUrl` sets the [runbook](../../../reference/sluis/telemetry.md#runbook) link (empty: none). Thresholds are `alerts.rules.<rule>.*`.

   ```yaml
   # alerts.yaml
   renders: alerts
   alerts:
     namespace: sluis              # where the service runs
     ruleLabels:
       k8s_cluster_name: prod      # for the Alertmanager routing tree
   ```

   ```sh
   helm template sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
   helm install   sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
   ```

3. Install the dashboard.

   ```yaml
   # dashboards.yaml
   renders: dashboards
   dashboards:
     namespace: monitoring         # the cluster Grafana runs on
   ```

   ```sh
   helm install sluis-dashboards oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f dashboards.yaml
   ```

## Verify

- The pod has `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_SERVICE_NAME` and your `extraEnv`; a minute on, `sluis_http_requests_total` has series.

- The ruler lists one group of twelve rules, none erroring.

- The Grafana `Access` folder has the overview dashboard (a ConfigMap per file) with a `$cluster` variable.
- A log line written inside a request carries `trace_id`, `span_id` and `trace_flags` (W3C hex), the trace's own id; a line outside a span has none. Logs stay on stdout. In Lambda the OTLP layer forwards only platform events, so function logs reach CloudWatch Logs only.

A rule with no series does not fire; the platform's scrape alert covers a service that is down.

## Roll back

Empty `endpoint` and upgrade, then `helm uninstall sluis-alerts -n monitoring` and `helm uninstall sluis-dashboards -n monitoring`.
