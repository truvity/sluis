# Install telemetry: export, alerts and the dashboard

## Purpose

Point the pod at an OpenTelemetry collector, and install the alert rules and the Grafana dashboard that read what it
publishes. What is published, and the alert catalogue, are in [telemetry](../operations/telemetry.md).

## Preconditions

- An OTLP collector or gateway reachable from the pod, over `http/protobuf` (the estate's convention is
  `http://<gateway>:4318`; the cluster, namespace and tier become labels at the gateway, so none is set here).
- A metrics store with a ruler that reads `VMRule` (or `PrometheusRule`, with `alerts.format: prometheusrule`), and a
  Grafana whose sidecar reads labelled ConfigMaps.
- sluis installed ([install](install-with-helm.md)).

## Before you start

- **Telemetry is the OpenTelemetry environment and nothing else.** There is no key for it in the service document. With no
  endpoint set nothing is exported and every instrument is a no-op.
- **The chart refuses** an endpoint that is not an http(s) URL, an `extraEnv` name that does not start with `OTEL_`, and
  `OTEL_EXPORTER_OTLP_ENDPOINT` in `extraEnv` (it belongs in `endpoint`). Looks like: `helm template` fails naming the key.
- **A secret is not for `extraEnv`**, which renders as plain values: put `OTEL_EXPORTER_OTLP_HEADERS` with a token in a
  Secret and reach the pod through `secretEnv`.
- **Alerts and dashboards are separate releases.** `renders: alerts` and `renders: dashboards` render only those objects and
  ignore the rest of the values; they belong where the ruler and Grafana look, not necessarily the service's cluster.
- **A dashboard or alert that selects `service_name` `github-roster` or `slack-roster` selects `access-issuer`** since
  v1.63: the controllers run in the one process and report under its name ([ADR 0037](../decisions/0037-one-process-everywhere.md)).
- **Preview before every apply, and read the preview**: `helm template` first.

## Steps

### 1. Export from the pod

**Run** add to the values of the sluis release (documents mode keeps this deployment-level block):

```yaml
telemetry:
  otlp:
    endpoint: http://otlp-gateway.observability.svc:4318   # empty: no export, nothing rendered
    protocol: http/protobuf                                 # the default
    extraEnv:                                               # any other OTEL_* variable
      OTEL_TRACES_SAMPLER: parentbased_traceidratio         # optional: a tenth of root traces
      OTEL_TRACES_SAMPLER_ARG: "0.1"
```

then `helm template ...` and `helm upgrade`.

**Expect** the pod gets `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, an `OTEL_SERVICE_NAME` and the
`extraEnv` entries; the rollout completes.

**Verify** in the metrics store, `access_issuer_http_requests_total` has series for the cluster after a minute.

**Rollback** empty `endpoint` and upgrade: the chart renders nothing and the pod exports nothing.

### 2. Install the alerts

**Run**

```yaml
# alerts.yaml
renders: alerts
alerts:
  namespace: sluis              # where the service runs; its series are selected by it
  ruleLabels:
    k8s_cluster_name: prod      # for the Alertmanager routing tree
```

```sh
helm template sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
helm install   sluis-alerts oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f alerts.yaml
```

`alerts.ruleLabels` is added to every rule beside its `severity`; `alerts.rules.<rule>.labels` to one rule;
`alerts.runbookBaseUrl` is where the [runbook](../operations/telemetry.md#runbook) is (empty renders no link). Every
threshold is `alerts.rules.<rule>.*`.

**Expect** one rule group of twelve rules.

**Verify** the ruler lists the group, and no rule is in error.

**Rollback** `helm uninstall sluis-alerts -n monitoring`.

### 3. Install the dashboard

**Run**

```yaml
# dashboards.yaml
renders: dashboards
dashboards:
  namespace: monitoring         # the cluster Grafana runs on
```

```sh
helm install sluis-dashboards oci://ghcr.io/truvity/charts/sluis --version X.Y.Z -n monitoring -f dashboards.yaml
```

**Expect** one ConfigMap per dashboard file, labelled for Grafana's sidecar.

**Verify** the dashboard `access-roster overview - $cluster` appears in the `Access` folder. Its rows: is it healthy; the
issuer's requests, errors and latency by route; tokens, sign-ins and keys; the controllers' ticks and leases; the storage
ports; the exports; GitHub rate limits and seats. It holds to truvity/observability's dashboard contract (a `datasource`
variable every panel uses, `cluster` and `namespace` variables, `$cluster` in the title and every query, no datasource UID).

**Rollback** `helm uninstall sluis-dashboards -n monitoring`.

## Afterwards

- Tell whoever is on call where the [runbook](../operations/telemetry.md#runbook) is, and route the `severity` labels.
- A rule whose series is absent does not fire. Whether the service runs at all is the platform's own scrape alert, not this
  chart's.
- After changing a threshold, re-read the rule's comment in `charts/sluis/templates/alerts.yaml` for why it is what it is.
