# Install telemetry

Point the pod at an OpenTelemetry collector, then install the alerts and Grafana dashboard. [Telemetry](../../../reference/sluis/telemetry.md) lists the metrics and alerts.

## Before you start

- You need an OTLP collector (`http/protobuf`) and, on the cluster that holds Grafana, the grafana-operator with its `GrafanaFolder` and `GrafanaDashboard` kinds.

- You need a ruler that reads `VMRule`, or `PrometheusRule` with `alerts.format: prometheusrule`.

- The chart refuses a non-http(s) endpoint, an `extraEnv` name without the `OTEL_` prefix, and `OTEL_EXPORTER_OTLP_ENDPOINT` in `extraEnv`; `helm template` names the key.

- `extraEnv` renders as plain values; put `OTEL_EXPORTER_OTLP_HEADERS` with a token in a Secret via `secretEnv`.

- Alerts and dashboards are separate releases: each renders only its own objects.

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
     namespace: sluis              # where the service runs; on Lambda: lambda,
     # namespaceLabel: k8s_namespace_name   # with this label, for the function's series
     ruleLabels:
       k8s_cluster_name: prod      # for the Alertmanager routing tree
   ```

   ```sh
   helm template sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
   helm install   sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
   ```

3. Install the dashboards in namespace `sluis` on the cluster that holds Grafana.

   ```yaml
   # dashboards.yaml
   renders: dashboards
   dashboards:
     clusterFolderUID: prod          # folder.uid of the observability-cluster chart; at most 25 characters
     datasources:
       metrics: prod-metrics         # the uids of the Grafana's datasources
       logs: prod-logs
     # instanceSelector: {}          # the shared Grafana by default
   ```

   The uids are `<clusterFolderUID>-sluis-overview` and `-sluis-audit`.

   ```sh
   helm install sluis-dashboards oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n sluis -f dashboards.yaml
   ```

## Verify

- The pod has the `OTEL_*` variables; after a minute `sluis_http_requests_total` has series.

- The ruler lists one group of eleven rules, none erroring.

- The shared Grafana has `<cluster> / sluis` with both dashboards, each with a `$cluster` variable.
- A log line written inside a request carries `trace_id`, `span_id` and `trace_flags` (W3C hex), the trace's own id; a line outside a span has none. Logs stay on stdout. In Lambda the OTLP layer forwards only platform events, so function logs reach CloudWatch Logs only.

A rule with no series does not fire; the platform's scrape alert covers a down service.

## Roll back

Empty `endpoint` and upgrade, then `helm uninstall sluis-alerts -n monitoring` and `helm uninstall sluis-dashboards -n sluis`.
