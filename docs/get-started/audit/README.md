# Deployment shapes

audit installs as three independent parts: writer, notary, and observe with query
([0058](../../decisions/0058-three-parts-installed-independently.md)). One installation serves one application
([0053](../../decisions/0053-one-installation-per-service-or-product.md)). A shape is where the parts run.

## Choose a shape

| | Services in the cluster | AWS serverless |
|---|---|---|
| Runs | writer, receiver, notary job, observe and query as chart pods | writer and notary as Lambda functions from the Pulumi library |
| Carries the stream | NATS | SQS |
| Holds secrets | Kubernetes Secrets | SSM Parameter Store |
| Deduplicates in | PostgreSQL | DynamoDB |
| Signs seals with | AWS KMS or OpenBao transit | AWS KMS |
| Start at | [services in the cluster](in-cluster.md) | [AWS Lambda](aws-lambda.md) |

Both shapes share the archive layout ([bucket contract](../../reference/audit/bucket-contract.md)), the catalogue rules, the record and `audit verify`.

## Variations

| Variation | Start at |
|---|---|
| Chart in the cluster, bucket, key and workload identity on AWS | [Kubernetes](kubernetes.md) |
| Receiver publishing to a stream, N consumers | [Run stream mode](../../guides/audit/operate/run-stream-mode.md) |
| Writer on Lambda, observe and query as pods | [Run readers in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md) |
| Beside sluis (a connection, not a shape) | [sluis-connected](sluis.md) |

## What stays outside the cluster

The chart does not create these. Supply them in both shapes:

- A key: AWS KMS, or OpenBao transit in the cluster shape.
- An S3 store: AWS S3 or Cloudflare R2. The `attested` preset (Object Lock compliance mode) needs AWS S3; R2 and other S3-compatible stores take `operational` and `standard`.
- A PostgreSQL database in the cluster shape, with an owner and a role each for the writer, observe and query.

The archive has one store per preset, each with its own bucket, prefix, region, endpoint and credentials
([0068](../../decisions/0068-storage-is-configured-per-preset.md), [profiles](../../reference/audit/profiles.md#presets-and-their-storage)).

Explanations: [deployment shapes](../../concepts/audit/deployment-shapes.md), [direct mode](../../concepts/audit/direct-mode.md),
[stream mode](../../concepts/audit/stream-mode.md), [AWS Lambda](../../concepts/audit/aws-lambda.md), [levels](../../concepts/audit/levels.md).

## Planned

[0041](../../decisions/0041-the-secret-contract.md) moves keys and State to the shared
[adapter block](../../concepts/storage/adapter-block.md). The chart and the Pulumi library do not take the block yet.
[Key custody](../../concepts/audit/key-custody.md) describes today's behaviour.
