# How much of audit does an installation run?

The [shapes](deployment-shapes.md) say how a record reaches the bucket. The level says what protects it there.

| level | archive | seals | index | for |
|---|---|---|---|---|
| `full` | S3 with Object Lock | signed by KMS or OpenBAO Transit | Postgres (observe, query) | a record that must stand as evidence |
| `lite` | S3 or S3-compatible, no lock | a local key, or none | Postgres | an internal service, a homelab, a trial |
| `log` | none | none | none | the emitter's `logsink` writes each record as a log line |

`log` is the entry point. An application emits with the SDK and its log pipeline keeps the lines. Moving up changes configuration, not records.

## Framework profile names

These names are vocabulary for choosing a deployment. They are not chart options and not compliance [profiles](../../reference/audit/profiles.md).

| name | what it is | level | built from |
|---|---|---|---|
| `aws-serverless` | writer and notary Lambdas, S3, DynamoDB dedupe | `full` (lock on) or `lite` | [AWS library](../../reference/audit/aws-pulumi-library.md) |
| `aws-eks` | the chart on EKS, Pod Identity, S3, notary job on KMS | `full` | chart, [direct](direct-mode.md) or [stream](stream-mode.md) |
| `aws-hybrid` | Lambdas for ingest and notary; observe and query on Kubernetes with IRSA | `full` | AWS library plus chart with `writer.enabled: false` |
| `k8s-openbao` | the chart on any cluster; seals on OpenBAO Transit; S3 or compatible | `full` with a lock, else `lite` | chart (`jobs.notary` with `signer.transit`) |
| `in-cluster` | the chart with NATS, PostgreSQL and Kubernetes Secrets; archive on S3 or R2 | `full` with a lock, else `lite` | [chart in the cluster](../../get-started/audit/in-cluster.md) |
| `k8s-minimal` | the chart, S3-compatible store, no lock, no notary | `lite` | chart |
| `server` | the binaries on a host, a local seal key | `lite` | [direct](direct-mode.md) |

The `aws-hybrid` writer on Lambda with readers in Kubernetes is described in [deployment shapes](deployment-shapes.md#aws-lambda-and-the-hybrid). Both Lambdas run outside a VPC.

## Two example estates

A `full` estate runs `aws-hybrid`: writer and notary Lambdas, SQS and DynamoDB dedupe, KMS ES384 seals, and Object Lock in `GOVERNANCE` first and `COMPLIANCE` later.

A `lite` estate runs `aws-hybrid` with the `k8s-openbao` notary. The writer Lambda uses S3 with SSE-S3 and no lock. The notary is a Kubernetes CronJob signing with OpenBAO Transit, using `Notary.Disabled` and an `ArchiveWriter` role for its pod. Observe and query run on CloudNativePG.

## What is implemented

"On request" means designed and not built.

| capability | code | Pulumi | chart |
|---|---|---|---|
| Writer on Lambda, SQS, DynamoDB dedupe | implemented | implemented | not applicable |
| Notary on Lambda with a KMS key | implemented | implemented | not applicable |
| Notary as a CronJob on OpenBAO Transit | implemented (`keys.TransitSigner`) | `Notary.Disabled`, `ArchiveWriter` | implemented (`jobs.notary`, `signer.transit`; golden `transit`) |
| Notary with a local key | implemented (`signer.file`) | on request | implemented |
| Archive without a lock | implemented (`lockMode: none`) | implemented (`NONE`) | implemented |
| Lock in GOVERNANCE, then COMPLIANCE | implemented | implemented | not applicable (the bucket's) |
| SSE-S3 or SSE-KMS | implemented | implemented (`Archive.Encryption`) | not applicable |
| The application's catalogue in the writer Lambda | implemented (`catalogues`) | implemented (`Writer.Catalogues`, `CataloguePaths`) | not applicable |
| Observe by IRSA from a non-EKS cluster | implemented | implemented (`Observe.IRSA`) | implemented |
| Observe and query with Postgres | implemented | not applicable | implemented |
| Readers and notary in Kubernetes, writer on Lambda | implemented (`sink.sqs`) | `QueueURL`, `QueueArn`, `Ingest.Senders` | implemented (`writer.enabled: false`; golden `external-writer`) |
| An S3-compatible store other than AWS (R2, MinIO) | implemented | not applicable | implemented |
| `log` level (`logsink`) | implemented | not applicable | not applicable |
| Pseudonymisation keys on Transit from a Lambda | implemented, needs a public Transit address | `Writer.Keys` | not applicable |

## Decided in

- [0056 Lock modes and store tiers](../../decisions/0056-lock-modes-and-store-tiers.md)
- [0058 Three parts installed independently](../../decisions/0058-three-parts-installed-independently.md)
- [0026 Two platforms permanently](../../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)
