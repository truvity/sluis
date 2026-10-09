# audit chart

The chart deploys one installation of the audit trail in the namespace of the application that owns it.
The application's chart instantiates it as a dependency.

Start with [audit on Kubernetes](../../docs/get-started/audit/kubernetes.md). Values: [chart values](../../docs/reference/audit/chart-values.md).

## What it deploys

- `audit-writer`: serves the sink and `RegistryService`. With `mode: stream` a receiver publishes and consumers store.

- `audit migrate`: a hook Job for the schema.

- `audit-observe` (`observe.enabled`): the indexer.

- `audit-query` (`query.enabled`): search, export and tail.

- `audit-notary` (`jobs.notary.enabled`): an hourly CronJob that seals closed hours.

- CronJobs `audit verify`, `purge` and `clock-sync`.

Modes: [direct](../../docs/concepts/audit/direct-mode.md) and [stream](../../docs/concepts/audit/stream-mode.md). Each `config:` block is the binary's file: [configuration](../../docs/reference/audit/configuration.md).

## Publishing the query service

Enable `query.route` only when the calling console runs outside the cluster.

```yaml
query:
  route:
    enabled: true
    parentRefs:                       # a Gateway or a ListenerSet, passed through
      - group: gateway.networking.k8s.io
        kind: Gateway
        name: public
        namespace: gateway
        sectionName: https
    hostnames: [audit.example.com]
    pathPrefix: /myapp                # optional: the path is the installation
    annotations: {}
    securityPolicy: {}                # optional Envoy Gateway SecurityPolicy spec, without targetRefs
```

The route needs `query.enabled`, `parentRefs` and `hostnames`.
Write each parent reference out in full, or GitOps diffs forever.
The route carries only `/audit.v1.QueryService`.
With `networkPolicy.enabled`, list the gateway's namespace in `networkPolicy.queryIngressFrom`, or the route answers 503.

## Refusals

`templates/_checks.tpl` holds the checks and `tests/invalid/audit/` a case for each.

## Check it

```sh
just chart-lint
just audit-chart
audit verify --profile <p> --last 24h --bucket <b>
```

## More

- [Bucket](../../docs/guides/audit/operate/prepare-the-bucket.md) and [database](../../docs/guides/audit/operate/prepare-the-database.md)
- [In-cluster services](../../docs/get-started/audit/in-cluster.md)
- [OpenBao keys](../../docs/guides/audit/operate/configure-openbao-keys.md)
- [Connect an application](../../docs/guides/audit/connect/connect-an-application.md)
- Decided in [ADR 0053](../../docs/decisions/0053-one-installation-per-service-or-product.md)
