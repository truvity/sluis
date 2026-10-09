# Getting started: Kubernetes with AWS storage

Install the chart in direct mode, where the writer is the receiver and there is no stream, and archive, index and verify a record.
For a stream see [run stream mode](../../guides/audit/operate/run-stream-mode.md). To run NATS, PostgreSQL and Secrets in the cluster see [services in the cluster](in-cluster.md).

An application's own chart instantiates this chart as a dependency, in the application's namespace
([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

## What you need

The chart takes references to all of these and refuses to render when one is missing.

| You need | How |
|---|---|
| A bucket and a prefix | [prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md) |
| A Postgres database and a role per part | [prepare the database](../../guides/audit/operate/prepare-the-database.md) |
| A reference clock | an NTP address, `169.254.169.123` on AWS or `pool.ntp.org` |
| A workload identity | Pod Identity or IRSA annotations on each component's ServiceAccount |
| The images | `ghcr.io/truvity/audit/`: `audit-writer`, `audit-query`, `audit-observe`, `audit-notary` and `audit`; one tag also stamps the chart |

The application registers its catalogue with the receiver at start-up. Install nothing for it.

## 1. Add the dependency

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: <version>
    repository: oci://ghcr.io/truvity/charts
```

## 2. Write the values

The smallest useful `audit:` block composes the `security` profile and runs the writer, the indexer and the query service.
Each `config:` names a secret (`passwordSecret`); `secretFiles` projects it from a Kubernetes Secret into `/etc/audit/secrets`.

```yaml
audit:
  mode: direct
  replicas: 2                              # writer.config.replicas must say the same
  profiles:
    security:
      frameworks: [security]                  # each is a framework profile
  externalIdentifiersAreOpaque: true
  workloadIdentity:
    issuers: [{url: https://oidc.example.com/id/CLUSTER}]
    workloads: [{subject: "system:serviceaccount:app:api", source: app}]
  migrate:
    enabled: true
    config:
      apiVersion: audit.truvity.github.io/audit-migrate/v2
      secrets: {source: file, root: /etc/audit/secrets}
      database: {url: "postgres://audit_owner@db.example.com:5432/audit?sslmode=verify-full", passwordSecret: database-password}
      writer: audit_writer
      observe: audit_observe
      reader: audit_query
    secretFiles: [{name: database-password, secretName: audit-db-owner, key: password}]
  writer:
    config:
      apiVersion: audit.truvity.github.io/audit-writer/v2
      secrets: {source: file, root: /etc/audit/secrets}
      deployment: /etc/audit/deployment.yaml
      workloads: /etc/audit/workloads.yaml
      replicas: 2
      archive: {}
      database: {url: "postgres://audit_writer@db.example.com:5432/audit?sslmode=verify-full", passwordSecret: database-password}
    secretFiles: [{name: database-password, secretName: audit-db, key: password}]
  # observe, query, and the verify, purge and clock-sync jobs follow the same pattern:
  # see charts/audit/examples/direct.yaml
```

[`charts/audit/examples/direct.yaml`](../../../charts/audit/examples/direct.yaml) holds every component, and the chart's tests render it.
Each `config:` key is the binary's own ([configuration](../../reference/audit/configuration.md)); the chart's additions are in [chart values](../../reference/audit/chart-values.md).
For the profile choice see [which profiles to compose](../../concepts/audit/which-profiles-to-compose.md).

To seal each closed hour, enable `jobs.notary`, which is off by default. It needs a P-384 key that is not the writer's
([key custody](../../concepts/audit/key-custody.md#signing-key)).

## 3. Install

```sh
helm dependency build ./charts/<application>
helm upgrade --install <application> ./charts/<application> -n <app> -f values.yaml
```

The chart refuses to render a configuration its binaries reject or misread ([chart README](../../../charts/audit/README.md)).
Examples: a key that fails the schema, a password in a URL, or `replicas` that differs from the writer pod count.
It cannot catch a profile that needs a stricter lock than its preset's bucket gives, such as `pci-dss` on `standard`.
The writer refuses to start and names the profile and both modes.

## 4. Check it works

1. Run `kubectl -n <app> rollout status deploy/<release>-audit`. `/readyz` is ready when the database answers, the catalogue registry reads and a consumer runs; a 503 names the failed check.
2. Check the application logged its catalogue registration ([connect an application](../../guides/audit/connect/connect-an-application.md)).
3. Perform an action the catalogue declares, or run [the example application](../../../audit/examples/emit/main.go).
4. List the archive: `aws s3 ls s3://audit-eu-example-1/audit/app/records/security/ --recursive`.
5. Verify it: `audit verify --profile security --last 24h --bucket audit-eu-example-1 --prefix audit/app`.
6. Once records land, run `audit conformance --query https://audit-query.<app>.svc:8080 --profile security --token-file token`.
7. With `OTEL_EXPORTER_OTLP_ENDPOINT` set on the pods, page on `audit.observe.index.deferred`, `audit.observe.index.lag`, the dead-letter counter and `audit.emit.records.dropped` ([telemetry](../../reference/audit/telemetry.md)).

The writer refuses to start and logs the reason when the schema version is unknown or a profile demands a lock the store lacks.

## Next

- Failures: [recover from an outage](../../guides/audit/operate/recover-from-an-outage.md), [rebuild the index](../../guides/audit/operate/rebuild-the-index.md), [diagnose a scheduled job](../../guides/audit/operate/diagnose-a-scheduled-job.md).
- [Place a legal hold](../../guides/audit/operate/place-a-legal-hold.md), [verify the trail](../../guides/audit/operate/verify-the-trail.md).
- [Billing](../../guides/audit/operate/enable-billing.md), [usage quotas](../../guides/audit/operate/enable-usage-quotas.md).
- [Upgrade](../../guides/audit/upgrade/v0.13.md).
