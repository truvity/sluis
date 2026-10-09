# How does an installation run?

An installation belongs to one application and runs in its namespace. The application's chart renders it with this repository's chart as a dependency.

The [deployment page](../../get-started/audit/README.md) lists the shapes: services in the cluster and AWS serverless. Both write the same archive under the same catalogue rules and bucket layout. An auditor verifies either with the same command.

## Two modes in the cluster

| mode | for | receiver | writers | `async` loss window |
|---|---|---|---|---|
| [direct](direct-mode.md) | internal service, or no stream | is the writer | the receiver's pods | one flush interval plus the batch in flight |
| [stream](stream-mode.md) | a product with many pods, metering or quotas | publishes to JetStream | N consumers, scaled apart | milliseconds |

Switching modes changes the receiver's configuration, not any record.

## AWS Lambda and the hybrid

[AWS Lambda](aws-lambda.md) runs the writer and the notary as Lambda functions behind an SQS queue, built by a Pulumi library. Its status is on the [capabilities](../../reference/audit/capabilities.md) page.

The two combine: the writer on Lambda, observe and query in Kubernetes. Set `writer.enabled: false` and the release renders no writer, receiver, stream consumer or writer Service. It keeps the indexer, the query service, the migration hook and the jobs. Each of them that records sends to the Lambda's ingest queue with `sink.sqs` under its own pod identity.

The chart refuses a sink that names the release's own front door. It also refuses a `writer.enabled: false` release whose components would run as the writer's ServiceAccount.

See [run observe and query in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md).

No shape runs the writer inside the application. The packages `writer.Open` and `query.New` stay public for the binaries and for tests.

How much of the stack an installation runs is a separate axis: the [levels](levels.md) `full`, `lite` and `log`.

## Where each mode stores things

| storage | direct | stream | holds |
|---|---|---|---|
| S3, locked where a profile demands | the environment's bucket, the application's prefix | the same | the record only |
| Postgres | one database, in its own cluster if needed | one database in the application's cluster | index, cursors, rollups, dedupe table, registry |
| JetStream | not used | one stream on the application's account | records not yet archived |
| Valkey or another cache | not used | the application's cache, with quotas | counters, corrected hourly |
| OpenBAO | not used unless a key provider is chosen | the same | pseudonymisation keys, off by default |
| pod disk | nothing | nothing | nothing |

The index is a projection. `audit reindex` rebuilds it from the archive, so losing it costs search time and no evidence, and it needs no backup.

The bucket belongs to the environment. It has Object Lock, replication and a deny-delete policy, and each application writes its own prefix.

## Extensions

Both extensions are switched on per installation. Neither adds anything to the request path.

| extension | adds | needs |
|---|---|---|
| [billing](billing.md) | rollups at index time, an immutable monthly statement | a metering profile |
| [usage quotas](usage-quotas.md) | a usage consumer, a counter cache, an hourly reconciler | stream mode |

## Before either mode


- A bucket on the right tier, with a prefix for the application: [prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md).

- A Postgres database, a migration owner, and a role each for the writer, the indexer and the query service: [prepare the database](../../guides/audit/operate/prepare-the-database.md).

- A reference clock for the clock-synchronisation job. Every framework profile with a compliance obligation asks for a daily offset record.

- A home for the [Audit page](audit-page.md): the application's console, which calls the query service with its own token.

To start, pick [services in the cluster](../../get-started/audit/in-cluster.md), [Kubernetes with AWS storage](../../get-started/audit/kubernetes.md) or [AWS Lambda](../../get-started/audit/aws-lambda.md). To connect a product that already runs sluis, see [a sluis-connected install](../../get-started/audit/sluis.md).

## Decided in

- [0053 One installation per service or product](../../decisions/0053-one-installation-per-service-or-product.md)
- [0058 Three parts installed independently](../../decisions/0058-three-parts-installed-independently.md)
