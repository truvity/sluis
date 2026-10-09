# Deployment shapes

audit is three parts installed independently (writer, notary, observe and query), tied together by the bucket layout
([0058](../../decisions/0058-three-parts-installed-independently.md)). A shape is where they run. One installation
serves one application ([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

This page owns the list of shapes. There are **two peer shapes**, which differ in where audit's services run. Everything
else (a chart option, a wiring of readers) is a variation of one of them.

| | Services in the cluster | AWS serverless |
|---|---|---|
| Runs | writer, receiver, notary job, observe and query as pods from the chart | writer and notary as Lambda functions, built by the Pulumi library |
| Carries the stream | NATS | SQS |
| Holds secrets | Kubernetes Secrets (delivered by an operator, for example from OpenBao) | SSM Parameter Store |
| Deduplicates in | PostgreSQL | DynamoDB |
| Signs seals with | AWS KMS or OpenBao transit | AWS KMS |
| Start at | [Getting started, services in the cluster](in-cluster.md) | [Getting started on AWS Lambda](aws-lambda.md) |

**Identical in both:** the archive layout ([bucket contract](../../reference/audit/bucket-contract.md)), the catalogue rules, the
record, and `audit verify`, which an auditor runs against the archive alone.

Variations and combinations:

| Variation | State | What it is | Start at |
|---|---|---|---|
| Kubernetes with AWS storage | available | the chart in the cluster, with the bucket, key and workload identity on AWS | [Getting started on Kubernetes](kubernetes.md) |
| Kubernetes, stream | available | a receiver publishing to a stream, N consumers | [Run stream mode](../../guides/audit/operate/run-stream-mode.md) |
| Writer on Lambda, readers in the cluster | available | the AWS shape for ingest and sealing; observe and query as pods | [Run readers in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md) |
| AWS Lambda behind an edge module | *planned* | audit's front door from the sluis edge modules | below |

"Beside sluis" is a *connection*, not a shape: a sluis installation records to an audit installation of either shape
([getting started, sluis-connected](sluis.md)).

## What stays outside the cluster

In the services-in-the-cluster shape the chart brings the writer, receiver, indexer and query service; the operator
brings:

- **a key**: an AWS KMS key, or OpenBao transit, for the seals (and for the archive's encryption, where used);
- **an S3 store**: AWS S3 or Cloudflare R2. Use AWS S3 where Object Lock may be needed;
- **secrets as Kubernetes Secrets**: database passwords, store access keys, tokens;
- **a PostgreSQL database**, with an owner for the migration and a role each for the writer, observe and query.

Both shapes need a key and an S3 store. The `attested` preset (Object Lock in compliance mode) is **AWS S3 only**: R2
and other S3-compatible stores take `operational` and `standard`.

The readers (observe, query and migrate) use PostgreSQL in Kubernetes today. RDS or Aurora PostgreSQL is possible, since
they take a `database.url`; the implementation is held until someone needs it.

**Where the archive lives** is the same in every shape: one store per install preset (`operational`, `standard`, `attested`), each with its own bucket, prefix, region, endpoint (empty is AWS S3, set is R2 or another S3-compatible store), optional key alias and credentials. Each profile lands on the preset derived from its frameworks, and Object Lock COMPLIANCE is written only on an attested preset's S3 bucket ([0068](../../decisions/0068-storage-is-configured-per-preset.md), [profiles](../../reference/audit/profiles.md#presets-and-their-storage)).

The explanations behind the choice: [deployment shapes](../../concepts/audit/deployment-shapes.md),
[direct mode](../../concepts/audit/direct-mode.md), [stream mode](../../concepts/audit/stream-mode.md),
[AWS Lambda](../../concepts/audit/aws-lambda.md), [levels](../../concepts/audit/levels.md).

## Planned

[0041](../../decisions/0041-the-secret-contract.md) decides that audit's keys and State are chosen by the shared
[adapter block](../../concepts/storage/adapter-block.md), so the Kubernetes shape can use OpenBao transit for the
`seal`, `pseudonym`, `conceal` and `archive` purposes and OpenBao KV for State. The backends exist in the
[storage module](../../concepts/storage/README.md); the chart and the Pulumi library do not take the block yet, and the pages
that describe key providers today ([key custody](../../concepts/audit/key-custody.md),
[configure OpenBAO keys](../../guides/audit/operate/configure-openbao-keys.md)) describe the present behaviour.
