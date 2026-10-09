# Run observe and query in Kubernetes with the writer on Lambda

Keep the write path on Lambda and run the indexer, query service, migration and jobs in a cluster that reaches the archive by IRSA, Pod Identity or another account.

## Before you start

- Deploy the Lambda stack first ([getting started on AWS Lambda](../../../get-started/audit/aws-lambda.md)) so `QueueURL`, `QueueArn` and `BucketName` exist. You also need a [Postgres for the index](prepare-the-database.md).

- Name every sender in `Ingest.Senders` by IAM role or user ARN. Assumed-role ARNs, account ids and wildcards fail at preview. The sender's own policy must allow `sqs:SendMessage` on `QueueArn`.

- Give each component its own ServiceAccount. The chart refuses `serviceAccount.create: false` with no `name`.

## Steps

1. Let the cluster assume roles. Pick one mechanism per ServiceAccount; the library refuses both on the same one.

   For IRSA on a cluster that is not EKS, create an IAM OIDC provider, then set `IRSA`. The trust policy pins `<IssuerHost>:aud` and `<IssuerHost>:sub` to one ServiceAccount. Set `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE` on the workload.

   ```go
   Observe: &auditpulumi.ObserveArgs{
   	IRSA: &auditpulumi.IRSAArgs{
   		OIDCProviderArn: oidcProviderArn,        // arn:aws:iam::<account>:oidc-provider/k8s.example.test
   		IssuerHost:      "k8s.example.test",    // the provider's URL, no scheme
   		Namespace:       "audit",
   		ServiceAccount:  "audit-observe",
   		// Audience defaults to "sts.amazonaws.com"
   	},
   },
   ```

   For EKS Pod Identity, set `PodIdentity`. The library creates the role, its trust and the association. Leave the chart's `eks.amazonaws.com/role-arn` annotation off: it is for IRSA.

   ```go
   Observe: &auditpulumi.ObserveArgs{
   	PodIdentity: &auditpulumi.PodIdentityArgs{
   		ClusterName:    pulumi.String("acme"),
   		ClusterArn:     cluster.Arn, // arn:aws:eks:<region>:<account>:cluster/acme
   		Namespace:      "audit",
   		ServiceAccount: "audit-observe",
   		// PermissionsBoundaryArn: "arn:aws:iam::<account>:policy/<boundary>",
   	},
   },
   Query: &auditpulumi.QueryArgs{
   	PodIdentity: auditpulumi.PodIdentityArgs{ /* the same fields, audit-query's ServiceAccount */ },
   	RecordReads: true, // sqs:SendMessage on the ingest queue, for the chart's query sink.sqs
   },
   ```

   `Observe.PodIdentity` and `Query.PodIdentity` must name different ServiceAccounts. `ArchiveWriter` creates `<name>-archive-writer` for a workload outside AWS that writes `seals/` and `keys/`. The rights of each role are in [IAM roles](../../../reference/audit/aws-pulumi-library.md#iam-roles).

2. Point the chart at the queue. Render the chart with the writer off and `mode: direct`. See `charts/audit/examples/external-writer.yaml`.

   ```yaml
   writer:
     enabled: false
   query:
     enabled: true
     serviceAccount:
       annotations:
         eks.amazonaws.com/role-arn: <role with the IAM below>
     config:
       require: queued
       sink:
         sqs:
           queueUrl: <QueueURL>
           region: eu-west-1
   jobs:
     notary:
       enabled: true
       serviceAccount:
         annotations:
           eks.amazonaws.com/role-arn: <the notary's role>
       config:
         require: queued
         sink:
           sqs: {queueUrl: <QueueURL>, region: eu-west-1}
         signer:
           transit: {key: audit-seal, openbao: {address: ..., login: {mount: ..., role: audit-notary, jwtFile: /var/run/openbao/token}}}
   ```

   Each component below sends what it records to the ingest queue through `sink.sqs`, with `require: queued`.

   | component | what it records | where |
   |---|---|---|
   | query | every read of the trail | `query.config.sink` |
   | notary | `audit.seal.written` | `jobs.notary.config.sink` |
   | verify | `audit.seal.verified`, `audit.seal.failed` | `jobs.verify.config.sink` |
   | clock-sync | the clock's offset | `jobs.clockSync.config.sink` |

   The chart refuses `mode: stream`, a query service with no sink, and the writer-only `workloadIdentity.issuers`, `keysVolume` and `extensions.billing`.

   | component | rights |
   |---|---|
   | query | `sqs:SendMessage` on `QueueArn`; `s3:GetObject` and `s3:ListBucket` on the archive prefix; `kms:Decrypt` on the archive key; put on its exports bucket |
   | notary | `sqs:SendMessage`; read the archive; `PutObject` and retention on `seals/` and `keys/` (the `ArchiveWriter` role); OpenBao Transit through its JWT role, or KMS `Sign` |
   | verify, clock-sync | `sqs:SendMessage` when they have a `sink`; verify also reads the archive |
   | observe | read the archive; no queue |

3. To read from another AWS account, set `Observe.TrustedPrincipalArn`. The library creates `<name>-observe-reader` in the archive's account. Add `ExternalID` to require `sts:ExternalId`. The principal needs `sts:AssumeRole` on the role ARN.

## Verify

Run `audit conformance` against the query service and `audit verify` against the archive. See [verify the trail](verify-the-trail.md).

## Roll back

Remove the block from the stack and run `pulumi up`. Add each new sender to `Ingest.Senders` in the same change that gives it `QueueArn`.

## Decided in

[0062 Observe follows the bucket](../../../decisions/0062-observe-follows-the-bucket.md).
