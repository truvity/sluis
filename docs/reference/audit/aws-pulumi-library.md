# AWS Pulumi library reference

The inputs, outputs, resources, IAM and alarms of `github.com/truvity/sluis/audit/deploy/pulumi`. Mocks test it (`just pulumi-test`); it has not run in an account ([capabilities](capabilities.md)). See [AWS Lambda](../../concepts/audit/aws-lambda.md) and [the tutorial](../../get-started/audit/aws-lambda.md).

## The AWS provider

The one invoke, `aws.GetCallerIdentity`, uses the provider passed to `New`, not the default, which a stack may disable (`pulumi:disable-default-providers`). Every resource is a child of the component and takes the same provider.

```go
prov, _ := aws.NewProvider(ctx, "audit-account", &aws.ProviderArgs{Region: pulumi.String("eu-west-1")})
a, err := auditpulumi.New(ctx, "audit", args, pulumi.Provider(prov)) // or pulumi.Providers(prov)
```

`Args.AccountID` skips the invoke, as does `Notary.Disabled`.

## Optional parts

| | `Ingest.Disabled` | `Notary.Disabled` | Both |
|---|---|---|---|
| Left out | Queue and DLQ, DynamoDB table, writer function, role, log group, event source mapping, the writer's and queue's alarms (4) | Seal key and alias, notary function, role, log group, schedule and its role, the notary's alarms (3) | All of it |
| Empty outputs | `QueueURL`, `QueueArn`, `DlqURL`, `DlqArn`, `DedupeTableName`, `WriterFunctionArn`, `WriterRoleArn` | `SealKeyArn`, `SealKeyAlias`, `NotaryFunctionArn`, `NotaryRoleArn`, `ScheduleArn` | And `AlarmTopicArn` |
| No longer required | `Writer.Package`, `Writer.PackageSHA256`, `Writer.DeploymentYAML` | `Notary.Package`, `Notary.PackageSHA256` | |

| Rule | Detail |
|---|---|
| Alarm topic | Exists when at least one alarm does |
| Resource counts | The tests hold, for the test installation (Governance, telemetry, alerts, observe) and the component itself: both parts 44, ingest only 30, notary only 29, neither 13 |
| Use | Ingest without the notary where seals are made elsewhere (for example a notary on Talos signing with OpenBao transit). Neither where both are deferred |
| Turning a part off later | A plain removal on the next `pulumi up`, except the seal key and the bucket. They are protected and stop the update until you lift the protection by hand |

## Encryption

`Archive.Encryption` has three modes; `Archive.KeyArn` refines `kms`.

| Mode | Bucket encryption | Key | Role grants | Functions' `kmsKey` | `ArchiveKeyArn` |
|---|---|---|---|---|---|
| `kms` (default) | SSE-KMS, bucket keys on | The estate's, named by `Keys.Archive` (an alias, looked up). The library creates none | `kms:GenerateDataKey`, `kms:Decrypt` (read role: `Decrypt`) | The alias | The key the alias points at |
| `kms` with `KeyArn` | SSE-KMS under `KeyArn`, bucket keys on | Yours, by ARN, instead of `Keys.Archive` | Same, on `KeyArn` | `KeyArn` | `KeyArn` |
| `aws-managed` | SSE-KMS under `aws/s3`, bucket keys on | None created | None | None: the bucket default applies | Empty |
| `s3` | SSE-S3 (`AES256`) | None | None | None | Empty |

| Rule | Detail |
|---|---|
| `KeyArn` | Refused with `aws-managed` and `s3`. Must be a key ARN (`arn:<partition>:kms:<region>:<account>:key/<id>`), not an alias ARN, which IAM cannot grant on |
| Seal key | A different key; it never changes. Written objects keep their encryption |
| Your own key | The roles get the key through IAM policies, so the key policy must let IAM grant access. The default `arn:aws:iam::<account>:root` statement does; without it every put and get fails with `AccessDenied`. The library neither edits nor protects a key it did not create. In another account the key policy must also name the roles |
| `aws-managed` | S3 uses `aws/s3` and decrypts for any account principal holding `s3:GetObject`, so the bucket's IAM and policy are the only plaintext access control. Use is logged in CloudTrail under the account |
| `s3` | Gives up the key policy and the CloudTrail record of each use |
| ISO 27001 A.8.24 | Requires a documented cryptography and key-management policy, not a customer-managed key. AWS-managed keys pass when the policy records who rotates (AWS, yearly), who can use the key and how use is evidenced. Choose `kms` when the ISMS or a customer contract requires control of the key: policy, rotation, separated key administrators and users, or disabling. Use `KeyArn` when keys are managed centrally |

## Inputs

<!-- generated: aws-library-inputs -->
| input | default | meaning |
|---|---|---|
| `Tags` | none | on every resource that takes tags |
| `AccountID` | looked up | the account; empty looks it up through the component's provider, see [the AWS provider](#the-aws-provider) |
| `RolePath` | `/audit/` | the IAM path of every role the library creates |
| `LogRetentionDays` | 30 | each function's log group |
| `Presets` | **required** | the install presets the installation uses, by name (`operational`, `standard`, `attested`), each `PresetStorage{Bucket, Prefix, Region, Endpoint, PathStyle, CredentialsAddress, KeyAlias, Create, Adopt, AcknowledgeLifecycle}`: its own store ([install presets](profiles.md#presets-and-their-storage), [0068](../../decisions/0068-storage-is-configured-per-preset.md)). `Bucket` is required (`PresetBucketName` builds a default name); `Prefix` ends in `/`; `Endpoint` is an S3-compatible store (no bucket is created, `CredentialsAddress` is read from the state store, default `internal/archive/<preset>`); `KeyAlias` is looked up, never created; `Create` makes the AWS bucket (versioned, a lifecycle rule per `<Prefix>records/<profile>/`, compliance Object Lock for `attested`); `Adopt` takes an existing bucket and leaves its lifecycle alone (see [Lifecycle](#lifecycle)); with neither, the bucket is used as it is. Every profile of `Writer.DeploymentYAML` must be kept under a preset configured here, and the library renders the final deployment document with them. Notary, seal key, alarms and pseudonym keys are provisioned when any configured preset needs them: under `operational` alone there are none, and `Notary.Package` and `Alerts.EndpointURL` are refused |
| `Archive.ObjectLockMode` | `COMPLIANCE` | the lock of the `attested` preset's created bucket alone: `GOVERNANCE` (the trial) or `COMPLIANCE`; refused when no attested preset has `Create`. See [the lock modes](../../concepts/audit/aws-lambda.md#the-lock-modes) |
| `Archive.AcknowledgeCompliance` | false | the deliberate step before `COMPLIANCE`; without it the library builds nothing |
| `Archive.DefaultRetentionDays` | **required** (> 0) with `GOVERNANCE` and `COMPLIANCE` | the bucket's default retention, a floor: the writer sets each object's own. 0 is refused with a lock (a lock with no default rule is a trap), and any value with `NONE` |
| `Archive.Encryption` | `kms` | `kms` (SSE-KMS under an archive key), `aws-managed` (SSE-KMS under `aws/s3`) or `s3` (SSE-S3); the last two create no key and grant no `kms` on one; see [encryption](#encryption) |
| `Archive.KeyArn` | empty | an existing KMS key ARN for `Encryption: kms`: no key is created and the roles are granted it; refused with the other modes |
| `Archive.GlacierIRDays`, `.DeepArchiveDays` | 30, 365 | [0065](../../decisions/0065-archive-retention-and-lifecycle.md) |
| `Region` | looked up | the region, for the ARN of the SSM parameters; looked up like `AccountID`, and only when there are secrets to grant |
| `Writer.Secrets.Root`, `.KeyArn` | `/audit/<name>/private/config`, none | where the writer reads the secrets `Writer.Keys` names, and the customer-managed key they are encrypted with; see [secrets](../../guides/audit/operate/aws-store-secrets-in-ssm.md). Unset and `Writer.Keys` naming no secret: no SSM access at all |
| `Ingest.Disabled` | false | leaves out the queue, the table, the writer and their alarms; see [optional parts](#optional-parts) |
| `Ingest.Senders` | **required** unless `Ingest.AnySenderInAccount` | principals (role or user ARNs) allowed to send to the queue. The queue policy allows them `sqs:SendMessage` and denies every other principal: the queue carries no verified caller identity, so this list is the writer's authenticity on this path ([authn](../../concepts/audit/authn-authz.md#on-the-sqs-path)) |
| `Ingest.Redrivers` | none | role or user ARNs (an operator's break-glass role) allowed to send to the queue so that a DLQ redrive (`StartMessageMoveTask`, which sends as the caller) works. Allowed and excepted from the deny, and not a sender the trail relies on; same forms as `Senders`. See [redrive the ingest DLQ](../../guides/audit/operate/redrive-the-ingest-dlq.md) |
| `Ingest.AnySenderInAccount` | false | the acknowledged alternative, for a trial: no sender statement and no deny, so any principal of the account with `sqs:SendMessage` in its identity policy may send. Refused with `Senders` |
| `Ingest.MaxReceiveCount` | 5 | deliveries before a message moves to the DLQ |
| `Ingest.RetentionDays` | 14 | the queue's retention; 14 is SQS's limit and the deduplication window's floor |
| `Writer.Package` | **required** unless `Ingest.Disabled` | the release's `audit-writer-lambda_<version>_linux_arm64.zip`, a path or an https URL; the function's code as released |
| `Writer.PackageSHA256` | **required** with the package | that zip's SHA-256 in hex, from the release's `checksums.txt` |
| `Writer.DeploymentYAML` | **required** unless `Ingest.Disabled` | the profile configuration, the document the chart renders |
| `Writer.Catalogues` | none | application catalogues by file name (`catalogue.yaml`, `catalogue-<name>.yaml`) and content; see [the application's catalogue](../../guides/audit/connect/change-what-a-source-records.md) |
| `Writer.CataloguePaths` | none | the same, read from files on disk under their base names; merged with `Catalogues` |
| `Writer.CatalogueSchemas` | none | the data schemas each catalogue references, by the catalogue's file name, then `<name>.json` and content; a referenced schema missing or a schema unreferenced is refused |
| `Writer.CatalogueDirs` | none | directories each holding one catalogue document and its `.json` schemas; merged into `Catalogues` and `CatalogueSchemas` |
| `Writer.Keys`, `.ForgetIdentities` | none | the `keys` block and `forgetIdentities` of the file |
| `Writer.DedupeWindow` | the profiles' widest | a Go duration |
| `Writer.MemoryMB`, `.TimeoutSeconds` | 512, 120 | |
| `Writer.BatchSize` | 10 | 1 to 10, the sink's limit |
| `Writer.MaxBatchingWindowSeconds` | 5 | how long the mapping gathers a batch: fewer, larger objects for a few seconds of latency |
| `Writer.MaxConcurrency` | 10 | the mapping's concurrency cap, 2 or more |
| `Notary.Disabled` | false | leaves out the seal key, the notary, its schedule and alarms |
| `Notary.Package` | **required** unless `Notary.Disabled` | the release's `audit-notary-lambda_<version>_linux_arm64.zip` |
| `Notary.PackageSHA256` | **required** with the package | its SHA-256 in hex |
| `Guards.AllowVersionSkew`, `.SkipCatalogueCheck` | false | acknowledge a binary of another release than the library, and skip the comparison of catalogues with the archive; see [guards](../../guides/audit/operate/aws-ship-a-release.md) |
| `Notary.Schedule` | `cron(15 * * * ? *)` | EventBridge Scheduler, UTC |
| `Notary.Profiles`, `.Settle` | every profile, `10m` | as `audit-notary` |
| `Notary.MemoryMB`, `.TimeoutSeconds` | 256, 900 | |
| `Telemetry` | nil | nil gives the functions no extension, no `OTEL_*` and no `sts:GetWebIdentityToken` |
| `Telemetry.ExtensionLayerArn` | **required** with `Telemetry` | the OTLP extension, published as a layer in the account and region (by convention `audit-otlp`) |
| `Telemetry.OmitLegacyEnv` | false | leave out the deprecated `ACCESS_ROSTER_*` names of the extension's settings, which are set beside the `AUDIT_OTLP_*` ones for one minor |
| `Telemetry.IssuerURL`, `.OTLPEndpoint` | **required** with `Telemetry` | the issuer's base URL, and the OTLP/HTTP base URL (https) |
| `Telemetry.STSAudience`, `.OTLPAudience` | `otlp` | the audience asked of STS, which the roles' policies pin, and the exchange's audience |
| `Telemetry.ExtraEnv` | none | other `OTEL_*` variables |
| `Alerts.EndpointURL` | none | the HTTPS endpoint of alert-ingress; none creates the topic and the alarms and no subscription |
| `Alerts.OldestMessageAgeSeconds` | 900 | |
| `Alerts.NotarySilenceHours` | 3 | |
| `Observe.TrustedPrincipalArn` | one of this, `IRSA` and `PodIdentity` is **required** with `Observe` | the principal that may assume the read role |
| `Observe.ExternalID` | none | required of the assuming principal when set; does not apply to IRSA |
| `Observe.IRSA` | none | a ServiceAccount that may assume the read role by web identity, see [IRSA](../../guides/audit/operate/aws-run-readers-in-kubernetes.md) |
| `Observe.PodIdentity` | none | `ClusterName`, `ClusterArn`, `Namespace`, `ServiceAccount` (all **required**), `Region`, `PermissionsBoundaryArn`: `Namespace` and `ServiceAccount` are Kubernetes names (DNS-1123, at most 63 and 253 characters). The role also trusts EKS Pod Identity (`pods.eks.amazonaws.com`, pinned to the cluster, its account and the ServiceAccount) and the library creates the `aws.eks.PodIdentityAssociation`. Refused together with `IRSA`; may be given with `TrustedPrincipalArn` |
| `Query` | nil | `PodIdentity` (the same block, **required**) and `RecordReads` (false): a role for audit-query on EKS (its ServiceAccount must differ from `Observe.PodIdentity`'s), `<name>-query`, with the observe reader's read rights, plus `sqs:SendMessage` on the ingest queue with `RecordReads` (refused with `Ingest.Disabled`) |
| `ArchiveWriter` | nil | `IRSA` (the same block) and `Prefixes` (default `seals/`, `keys/`): a write role for a workload outside AWS |
<!-- /generated -->

## Artifacts bucket and the library's own release

With `Artifacts`, the verified zip goes unchanged (a file asset) to a versioned S3 bucket, and the function and layer come from that object version. Without it, the code uploads with the function.

| Input | Default | Meaning |
|---|---|---|
| `Artifacts.Bucket` | unset (direct upload) | The artifacts bucket. It must be versioned: an unversioned bucket returns no version id and the apply fails with a message saying so |
| `Artifacts.Prefix` | `audit/` | Starts every key: `<prefix><version>/<sha256>-<file name>`. The digest in the key means a key never holds two contents and a re-run uploads nothing new |
| `Release.ResolveChecksums` | false | Reads an empty `Writer.PackageSHA256` or `Notary.PackageSHA256` from `<BaseURL>/v<version>/checksums.txt`. A given digest is used as is |
| `Release.Version` | from the file name | The release when the name does not say; names the release when `Writer.Package` or `Notary.Package` is empty. `(devel)` and empty are refused |
| `Release.BaseURL` | the project's GitHub releases | Where the release is published, for a mirror |

| Behaviour | Detail |
|---|---|
| Function fields | `S3Bucket`, `S3Key`, `S3ObjectVersion`, `SourceCodeHash` (the zip's SHA-256, base64) |
| Configuration layer | A zip with identical bytes on every run (sorted names, no timestamps), uploaded under the same prefix |
| No package named | The library deploys its own release: its module version from the build information (or `Release.Version`), fetched from `<BaseURL>/v<version>/audit-<writer\|notary>-lambda_<version>_linux_arm64.zip`, digest from `checksums.txt` unless `PackageSHA256` pins one. A pinned digest always wins and mismatching bytes are refused |
| Refused as a release | `(devel)`, a pseudo-version, a module replaced by a local copy, a program without build information. The message names `Writer.Package` or `Notary.Package` and `Release.Version` |
| Download cache | By SHA-256 under `os.UserCacheDir()/sluis/artifacts`. `GITHUB_TOKEN`, when set, is sent to github.com |
| After deploy | `WriterCodeSha256Matches` and `NotaryCodeSha256Matches` are true when Lambda's code SHA-256 equals the verified zip's |

## The live alias

| Item | Detail |
|---|---|
| Versions | Each function publishes one per code or configuration change. Alias `live` points at the newest |
| Users of the alias | The writer's event source mapping, the notary's schedule, the scheduler role's invoke grant (the alias ARN alone, not `:*`), the notary's asynchronous-invoke configuration (no retries) |
| Rollback | The previous version stays |
| Outputs | `WriterLiveAliasArn`, `NotaryLiveAliasArn`, `WriterLiveVersion`, `NotaryLiveVersion` |
| Rollout | In one step; CodeDeploy canaries are planned |

## Outputs

<!-- generated: aws-library-outputs -->
| output | what |
|---|---|
| `BucketName`, `BucketArn` | the archive |
| `ArchiveKeyArn` | the symmetric key objects are encrypted with (rotation on); the given `Archive.KeyArn` if set; empty with `Encryption: s3` or `aws-managed` |
| `ArchiveCredentialsPath` | the SSM parameter the functions read an S3-compatible store's credentials from; empty on AWS S3 |
| `SealKeyArn`, `SealKeyAlias` | the estate's `ECC_NIST_P384` `SIGN_VERIFY` key (`Keys.Seal`) and its alias. `audit key public` reads its public half for `keys/roots.jwks` and the verifier's pin |
| `QueueURL`, `QueueArn` | the ingest queue a receiver or an application sends to (`forward.sqs.queueUrl`), and what the chart's `sink.sqs` of the query service and the jobs names when the writer runs here ([observe and query in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md)): `QueueURL` is the `queueUrl`, `QueueArn` the resource of `sqs:SendMessage` |
| `DlqURL`, `DlqArn` | the dead-letter queue |
| `DedupeTableName` | the DynamoDB table |
| `WriterFunctionArn`, `NotaryFunctionArn` | the functions |
| `WriterRoleArn`, `NotaryRoleArn`, `ObserveReaderRoleArn` | the roles, see below; `ObserveReaderRoleArn` is empty without `Observe`; the outputs of a part that is turned off are empty too |
| `ArchiveWriterRoleArn` | the IRSA write role, empty without `ArchiveWriter` |
| `QueryRoleArn` | the Pod Identity role of audit-query, empty without `Query` |
| `SecretsRoot` | the SSM parameter path the writer reads secrets from, empty when its configuration names none |
| `AlarmTopicArn` | the SNS topic every alarm publishes to |
| `ScheduleArn` | the notary's schedule |
<!-- /generated -->

## What it creates

| Resource | Notes |
|---|---|
| S3 bucket | Versioning on and stack-destroy protection in every mode. `objectLockEnabled` is never set (it forces replacement). Object Lock is a separate resource unless the mode is `NONE`. SSE-KMS under the archive key with bucket keys (AWS-managed key with `aws-managed`, SSE-S3 with `s3`). All four public-access blocks, bucket-owner-enforced ownership, a policy denying plain HTTP, a lifecycle rule per profile prefix, and one aborting incomplete multipart uploads after 7 days. `ForceDestroy` is never set |
| Scope | The `aws` partition and one region per stack |
| KMS keys | None. The archive, seal, pseudonym and conceal keys are the estate's, named by alias in `Keys` ([below](#keys-state-and-an-s3-compatible-archive)) |
| SQS ingest queue and DLQ | SSE-SQS; visibility timeout six times the writer's timeout; redrive policy to the DLQ and redrive-allow policy on it; a queue policy denying plain HTTP and allowing the named senders |
| DynamoDB `<name>-dedupe` | On-demand, hash key `pk` (string), TTL on `expires_at` |
| Lambda `<name>-writer`, `<name>-notary` | `provided.al2023`, `arm64`, no VPC, a log group each, the extension layer when there is one |
| Event source mapping | Queue to writer, `ReportBatchItemFailures`, scaling capped by `Writer.MaxConcurrency` |
| Scheduler `<name>-notary` | The schedule, a role that may invoke only the notary, no retries, no asynchronous retries on the function |
| IAM roles | [Below](#iam-roles) |
| CloudWatch alarms, SNS topic `<name>-alarms` | [Below](#alarms) |

## IAM roles

Each function has one role under `/audit/`, named `<name>-<part>`. For the default installation `audit`:

| Role | ARN | Runs as |
|---|---|---|
| writer | `arn:aws:iam::<account>:role/audit/audit-writer` | The writer function; the identity the OTLP door sees |
| notary | `arn:aws:iam::<account>:role/audit/audit-notary` | The notary function; the identity the OTLP door sees |
| observe reader | `arn:aws:iam::<account>:role/audit/audit-observe-reader` | Observe in another account or a Kubernetes ServiceAccount, by IRSA or Pod Identity (only with `Observe`) |
| query | `arn:aws:iam::<account>:role/audit/audit-query` | `audit-query` on EKS, by Pod Identity (only with `Query`) |
| archive writer | `arn:aws:iam::<account>:role/audit/audit-archive-writer` | A Kubernetes ServiceAccount, by IRSA (only with `ArchiveWriter`); see [Kubernetes workloads](../../guides/audit/operate/aws-run-readers-in-kubernetes.md) |
| scheduler | `arn:aws:iam::<account>:role/audit/audit-scheduler` | EventBridge Scheduler, to invoke the notary only |

The writer, notary and scheduler roles exist only with their part. The writer and notary never share a role ([0061](../../decisions/0061-seals.md)).

| | writer | notary | observe reader |
|---|---|---|---|
| S3 put | `PutObject`, `PutObjectRetention`, `PutObjectLegalHold` on `records/`, `catalogue/`, `schema/`, `identity/`, `dlq/` | `PutObject`, `PutObjectRetention` on `seals/`, `keys/` | none |
| S3 read | `GetObject` on the same and `holds/`; `ListBucket` | `GetObject` on `records/`, `seals/`, `keys/`; `ListBucket` | `GetObject` on `records/`, `catalogue/`, `schema/`, `seals/`, `keys/`; `ListBucket` under those |
| KMS | `GenerateDataKey`, `Decrypt` on the archive key | The same, and `Sign`, `GetPublicKey`, `DescribeKey` on the seal key | `Decrypt` on the archive key |
| DynamoDB | `GetItem`, `BatchGetItem`, `PutItem` on the dedupe table | none | none |
| SQS | `ReceiveMessage`, `DeleteMessage`, `GetQueueAttributes`, `ChangeMessageVisibility` on the ingest queue | none | none |
| Logs | Its own log group | Its own log group | none |
| STS | `GetWebIdentityToken` with `Telemetry` | Same | none |

| Rule | Detail |
|---|---|
| Object Lock permissions | S3 refuses a put carrying an Object Lock header unless the caller holds `PutObjectRetention` and `PutObjectLegalHold`. With `ObjectLockMode: NONE` the functions send no header and both grants are omitted |
| Deletes | No role has one |
| KMS rows | Empty with `Encryption: s3` or `aws-managed` |
| Seal key policy | The account root administers the key (create, describe, enable, put policy, schedule deletion) and cannot use it. Only the notary's role may `Sign`, `GetPublicKey`, `DescribeKey`. The default key policy would let any principal with a `kms:Sign` allow sign seals |

The web identity statement, for both functions:

```json
{
  "Effect": "Allow",
  "Action": "sts:GetWebIdentityToken",
  "Resource": "*",
  "Condition": {
    "ForAllValues:StringEquals": { "sts:IdentityTokenAudience": ["otlp"] },
    "StringEquals": { "sts:SigningAlgorithm": "ES384" },
    "NumericLessThanEquals": { "sts:DurationSeconds": "300" }
  }
}
```

| Element | Reason |
|---|---|
| Audience | From `Telemetry` (default `otlp`) |
| `ForAllValues:StringEquals` | `sts:IdentityTokenAudience` is multi-valued; a plain `StringEquals` is an implicit deny on a list. An empty set also passes, which is safe because `Audience` is required |
| ES384, 300 s | What the extension asks for |

## Alarms

Alarms publish on ALARM and OK to the SNS topic `<name>-alarms`, subscribed to alert-ingress over HTTPS. `Ingest.Disabled` removes the writer's and queue's alarms; `Notary.Disabled` removes the notary's three.

| Alarm | Metric | Fires when | Means |
|---|---|---|---|
| `<name>-writer-throttles` | `AWS/Lambda` `Throttles`, writer | Any, in 5 minutes | The writer is not keeping up, or the account's concurrency is spent |
| `<name>-notary-throttles` | `AWS/Lambda` `Throttles`, notary | Any, in 5 minutes | |
| `<name>-ingest-dlq-not-empty` | `AWS/SQS` `ApproximateNumberOfMessagesVisible`, DLQ | Above 0 | A record was delivered `MaxReceiveCount` times and is not in the archive |
| `<name>-ingest-oldest-message-age` | `AWS/SQS` `ApproximateAgeOfOldestMessage`, ingest | Above `Alerts.OldestMessageAgeSeconds` (900) | The writer is behind or not running |
| `<name>-writer-errors` | `AWS/Lambda` `Errors`, writer | Any, in 5 minutes | An invocation failed |
| `<name>-writer-unknown-catalogue` | `Audit/<name>` `UnknownCatalogueVersion`, a metric filter on the writer's log field `event=unknown_catalogue` | Any, in 5 minutes | A record named a catalogue version the writer lacks and was dead-lettered and acknowledged, which neither queue alarm sees. The log line names `source` and `catalogue_version`; OTLP carries `audit_writer_catalogue_unknown_total` |
| `<name>-notary-errors` | `AWS/Lambda` `Errors`, notary | Any, in an hour | A tenant could not be sealed, or the signer failed |
| `<name>-notary-silent` | `AWS/Lambda` `Invocations`, notary | Below 1 in each of the last `Alerts.NotarySilenceHours` (3) hours | The schedule or function is gone and the seal chain is growing a gap |

| Rule | Detail |
|---|---|
| Notary silence | Alarms on the platform's `Invocations` metric. Lambda publishes no datapoint for an hour without invocations, so missing data counts as breaching. A stopped function cannot report its own silence |
| Writer silence | Not an alarm: the writer is quiet when nothing is written. The oldest-message-age alarm catches a stopped writer with work waiting |
| Delivery | The subscription has `EndpointAutoConfirms` false. SNS POSTs a `SubscriptionConfirmation` and the subscription stays pending until the endpoint follows its `SubscribeURL`. alert-ingress must handle `SubscriptionConfirmation`, `Notification` and `UnsubscribeConfirmation` and should verify the message signature |
| Topic encryption | None with a customer key, which CloudWatch could not publish to without its own key policy. An alarm body names a queue and a function and carries no record |

## Lifecycle

One rule per `records/<profile>/` prefix:

| Step | When | Note |
|---|---|---|
| Glacier Instant Retrieval | 30 days | Still readable by observe's reindex and `audit verify` without a restore |
| Deep Archive | 1 year | Reading needs a restore; `audit verify` over such a range says so first |

`seals/`, `keys/` and `catalogue/` have no rule.

### An existing bucket whose lifecycle is yours: `Adopt`

| Mode | Behaviour |
|---|---|
| `Create` | The library owns the bucket and derives the rules above from profile retention, including expiration and noncurrent-version expiration for a profile whose framework deletes at the end of a fixed retention |
| neither | The bucket is outside Pulumi |
| `Adopt: true` | The library imports the existing bucket by name (never creates it; `Protect` and `RetainOnDelete`, so destroying the stack leaves it). It manages versioning, default encryption (per `Archive.Encryption`), the public-access block, ownership controls (`BucketOwnerEnforced`) and the bucket policy. The TLS-only deny replaces the bucket's policy as a whole. It declares no lifecycle configuration: existing rules stay as they are, and "never expire" means no expiration rule. Object Lock is untouched |

| Rule | Detail |
|---|---|
| First `pulumi up` | Imports then changes the resources to these settings; read the preview |
| Refused | `Adopt` with `Create`, with `Endpoint`, or with a bucket another preset names |
| Not applicable | `Archive.GlacierIRDays`, `Archive.DeepArchiveDays` and the lock settings; they are refused when no preset has `Create` |
| `AcknowledgeLifecycle` | The library cannot read the lifecycle it leaves alone. `Adopt` is refused when a profile kept in the preset has a fixed minimum retention (security, billing-nl, pci-dss and the like), unless the preset sets `AcknowledgeLifecycle: true`. That is your statement that the bucket's lifecycle and Object Lock keep objects at least that long |

## Not covered

| Item | Status |
|---|---|
| A deployment | Nothing here has run in an account |
| The `lambda` sink | Designed ([capabilities](capabilities.md)); the queue is the transport |
| FIFO ingest | The queue is standard and deduplication is the writer's. A FIFO queue would also absorb a repeat inside its five-minute window and is not what the library creates |
| A signed delegation | The notary signs with a root ([0061](../../decisions/0061-seals.md)) |

## Keys, state and an S3-compatible archive

`Args.Keys` names keys by KMS alias (`alias/<name>`), resolved with `kms.LookupAlias`. An ARN, a key id or an AWS-managed alias is refused.

| Field | Purpose | Required | Role grants |
|---|---|---|---|
| `Keys.Archive` | `archive`: SSE-KMS of the objects | With `Encryption: kms` unless `Archive.KeyArn` | `GenerateDataKey`, `Decrypt` (readers: `Decrypt`); no context condition, S3 binds its own |
| `Keys.Seal` | `seal`: the P-384 key seals are signed with | With the notary; refused without one | Notary: `Sign`, `GetPublicKey`, `DescribeKey` |
| `Keys.Pseudonym` | `pseudonym`: wraps the per-tenant secrets | Never; refused unless the preset is `attested` or a profile of `Writer.DeploymentYAML` pseudonymises | Writer: `GenerateDataKey`, `Decrypt` where `kms:EncryptionContext:purpose` is `pseudonym` |
| `Keys.Conceal` | `conceal`: identities that must be recoverable | Never; needs `Keys.Pseudonym` | Writer: `Encrypt`, `Decrypt`, `GenerateDataKey` where the context is `{instance: Keys.Instance, purpose: conceal}` |

| Item | Detail |
|---|---|
| `Keys.Instance` | Default is the component name. It is the `instance` of the default encryption context of [`storage/keys`](../../../storage/keys/doc.go) and is bound into ciphertexts, so choose it once |
| Function configuration | `keys: {adapter: kms, instance, seal, pseudonym, conceal, state}` and, for the archive, `archive.kmsKey` |
| `SealKeyPolicy(accountRootArn, notaryRoleArn)` | The key policy to put on the seal key: the root administers and cannot sign; only the notary's role signs. The notary role's ARN is `arn:<partition>:iam::<account>:role<RolePath><name>-notary` |
| `Args.State` | The installation's state store: SSM parameters under `Root` (default `/audit/<name>`; `KeyArn` for a customer-managed key). The archive's credentials are at `Root/internal/archive` and pseudonym secrets under `Root/internal/pseudonym/` |
| Parameters | The library creates none. The writer creates the pseudonym secrets (ciphertext under the pseudonym key) and may create but never replace them |
| Preset with `Endpoint` | An S3-compatible store: no bucket, lifecycle, encryption setting or S3 or archive-key statement is created. An `attested` preset there is refused (Object Lock is S3 only). `Observe`, `Query` and `ArchiveWriter` get nothing for it. The writer, and the notary if present, get `ssm:GetParameter` on the preset's credentials parameter only. See [archive on R2](../../guides/audit/operate/archive-on-r2.md) |
