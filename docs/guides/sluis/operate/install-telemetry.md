# Install telemetry

Point the pod at an OpenTelemetry collector, then install the alert rules and the Grafana dashboard that read what it publishes. [Telemetry](../../../reference/sluis/telemetry.md) lists the metrics and alerts.

## Before you start

- You need an OTLP collector reachable from the pod over `http/protobuf`, and a Grafana sidecar that reads labelled ConfigMaps.

- You need a ruler that reads `VMRule`, or `PrometheusRule` with `alerts.format: prometheusrule`.

- Telemetry is the OpenTelemetry environment only. With no endpoint set, nothing is exported.

- The chart refuses a non-http(s) endpoint, an `extraEnv` name without the `OTEL_` prefix, and `OTEL_EXPORTER_OTLP_ENDPOINT` in `extraEnv`. `helm template` fails naming the key.

- `extraEnv` renders as plain values. Put `OTEL_EXPORTER_OTLP_HEADERS` with a token in a Secret and reach the pod through `secretEnv`.

- Alerts and dashboards are separate releases. `renders: alerts` and `renders: dashboards` render only those objects, where the ruler and Grafana look.

- Series report under the one process's service name, `access-issuer`: a legacy identifier, renamed in v1.75–v1.76. Dashboards select it ([ADR 0037](../../../decisions/0037-one-process-everywhere.md)).

## Steps

1. Export from the pod. Add this to the sluis release values, then run `helm template` and `helm upgrade`. An empty `endpoint` renders nothing.

   ```yaml
   telemetry:
     otlp:
       endpoint: http://otlp-gateway.observability.svc:4318
       protocol: http/protobuf                                 # the default
       extraEnv:                                               # any other OTEL_* variable
         OTEL_TRACES_SAMPLER: parentbased_traceidratio         # optional: a tenth of root traces
         OTEL_TRACES_SAMPLER_ARG: "0.1"
   ```

2. Install the alerts. `alerts.ruleLabels` is added to every rule beside its `severity`, and `alerts.rules.<rule>.labels` to one rule. `alerts.runbookBaseUrl` sets the [runbook](../../../reference/sluis/telemetry.md#runbook) link (empty renders none). Every threshold is `alerts.rules.<rule>.*`.

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

- The pod has `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_SERVICE_NAME` and your `extraEnv`. After a minute `access_issuer_http_requests_total` has series for the cluster.

- The ruler lists one group of twelve rules, none in error.

- The Grafana `Access` folder has the overview dashboard, one ConfigMap per dashboard file, with a `$cluster` variable.

A rule whose series is absent does not fire. The platform's own scrape alert covers a service that does not run.

## Roll back

Empty `endpoint` and upgrade. Run `helm uninstall sluis-alerts -n monitoring` and `helm uninstall sluis-dashboards -n monitoring`.
