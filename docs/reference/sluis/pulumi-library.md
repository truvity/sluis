# The Pulumi library

Source: `deploy/pulumi`. Identifiers: [Go packages](../../sdk/go/sluis.md). Decided in [ADR 0037](../../decisions/0037-one-process-everywhere.md).

| Component | Type token | Creates |
|---|---|---|
| `NewStorage` | `sluis:aws:Storage` | The blob bucket |
| `NewState` | `sluis:aws:State` | The DynamoDB table |
| `NewLambda` | `sluis:aws:Lambda` | Function, role, HTTP API, signing key, schedules. The main path |
| `NewKubernetesIdentity` | `sluis:aws:KubernetesIdentity` | The EKS Pod Identity role of the one pod |

Pass the provider with `pulumi.Providers(p)`. `RenderPorts` and `RenderPortsYAML` render the `ports:` block.

```go
store, _ := sluispulumi.NewStorage(ctx, "acme", &sluispulumi.StorageArgs{BucketName: "acme-prod-sluis"}, pulumi.Providers(awsProvider))
state, _ := sluispulumi.NewState(ctx, "acme", &sluispulumi.StateArgs{TableName: "acme-sluis"}, pulumi.Providers(awsProvider))
ids, _ := sluispulumi.NewKubernetesIdentity(ctx, "acme", &sluispulumi.KubernetesIdentityArgs{
	ClusterName: "acme", ClusterArn: clusterArn, AccountID: accountID,
	Namespace:              "sluis",
	PermissionsBoundaryArn: boundaryArn,
	ServiceAccount:         "sluis",
	Storage:                store.Grant(),
	State:                  state.Grant(),
}, pulumi.Providers(awsProvider))
```

## Storage

| `StorageArgs` | Default | Meaning |
|---|---|---|
| `BucketName` | Required without `Blobs` | Bucket name; global |
| `Blobs` | None | `ExternalBlobs` for an S3-compatible endpoint (R2). One of `BucketName` and `Blobs`; with `Blobs` no bucket is created and `Versioning`, `ProtectedPrefixes`, `Tags` are refused |
| `Versioning` | Off | S3 versioning |
| `Tags` | None | On the bucket |

| Detail | Behavior |
|---|---|
| Outputs | `BucketName`, `BucketArn` (empty with `Blobs`), `Grant()` |
| Bucket | Protected, AES256, public access blocked, non-TLS denied |
| Old sealer | Run `pulumi state unprotect <urn>` on `<name>-sealer-key` and `<name>-sealer-alias` before the first apply |

| `ExternalBlobs` | Meaning |
|---|---|
| `Bucket`, `Endpoint` (https), `Region` (default `auto`), `PathStyle`, `Prefix` | The store |
| `CredentialsRef` | Address `internal/<kind>/<id>`, for example `internal/blobs/r2`. It names the secret, never holds it |

| With `Blobs` | Behavior |
|---|---|
| Document | Writes `ports.blob` (`adapter: s3`). A document naming `ports.blob`, `adapters.blobs` or `endpoint` is refused |
| IAM | `ssm:GetParameter` on `/sluis/<instance>/<CredentialsRef>`, decrypted through SSM with `ParameterKeyArn` |
| Seed | The `s3-credentials/v1` parameter, out of band ([blobs on R2](../../guides/sluis/operate/blobs-on-r2.md)) |

## State

The table of the [DynamoDB adapter](storage-layout.md#dynamodb-the-dynamodb-state-index-and-trigger-adapter) ([ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md)).

| `StateArgs` | Default | Meaning |
|---|---|---|
| `TableName` | Required | `ports.dynamodb.table` |
| `KeyArn` | None | Customer-managed key; otherwise the AWS-owned key |
| `Tags` | None | On the table |

| Detail | Behavior |
|---|---|
| Outputs | `TableName`, `TableArn`, `Grant()` |
| Table | Protected, on-demand, point-in-time recovery, TTL on `expires`; only `pk` and `sk` declared; no secondary index |

## Kubernetes identity

One role for the one pod ([switch the serving pod role](../../guides/sluis/operate/switch-serving-pod-role.md)). Outputs: `RoleArn`, `RoleName`.

| `KubernetesIdentityArgs` | Default | Meaning |
|---|---|---|
| `ClusterName` | Required | EKS cluster |
| `ClusterArn`, `AccountID` | Required | Pin the trust policy to the cluster |
| `Namespace`, `ServiceAccount` | Required | The one ServiceAccount; it takes one association |
| `Region` | Provider's | On each association |
| `PermissionsBoundaryArn` | None | Boundary of the role |
| `RoleNamePrefix` | Component name | Role and managed policy are `<prefix>-sluis` |
| `Description` | Generated | Policy description; a change replaces the policy |
| `Instance`, `Region`, `ParameterKeyArn` | None | With `Instance`, the role has the Lambda role's SSM grants under `/sluis/<instance>/`; `Region` becomes required |
| `Storage` | Required | `Storage.Grant()` |
| `State` | None | `State.Grant()`; nil for the `legacy` store |
| `SigningKeyArns` | None | `SigningKeyArn`, `SigningKeyRS256Arn`: allows `kms:Sign`, `kms:GetPublicKey` |
| `WrappedSigningKeyArn` | None | Symmetric key for `kms-wrapped` ([signing on AWS](../../concepts/sluis/signing-on-aws.md)). List this role in `WrappedSigning.AdditionalSigningRoleArns` |

The role trusts `pods.eks.amazonaws.com` for this cluster and ServiceAccount.

| Sid | Actions | Resource |
|---|---|---|
| `SluisBlobs` | `s3:GetObject`, `PutObject`, `DeleteObject` | Bucket objects |
| `SluisBlobList` | `s3:ListBucket` | The bucket; without it a missing key reads 403, not 404 |
| `SluisSigning` | `kms:Sign`, `kms:GetPublicKey` | Signing keys |
| `SluisWrappedSigning` | `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | Symmetric key; only with `kms:EncryptionContext:purpose` = `sluis-signing` and no context keys besides `purpose`, `alg`, `kid` |
| `SluisState` | `dynamodb:GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` | The table |
| `SluisStateKey` | `kms:Encrypt`, `Decrypt`, `GenerateDataKey`, `DescribeKey` | The table key, only through DynamoDB (`kms:ViaService`) |

## Ports configuration

`RenderPorts` never renders `create`, writes only the blob without `TableName`, and refuses an `Adapter` other than `dynamodb`.

```go
y, _ := sluispulumi.RenderPortsYAML(sluispulumi.PortsArgs{BucketName: "acme-prod-sluis", TableName: "acme-sluis", Region: "eu-west-1"})
```

```yaml
ports:
  adapter: dynamodb
  blob:
    adapter: s3
    s3: {bucket: acme-prod-sluis, region: eu-west-1}
  dynamodb: {region: eu-west-1, table: acme-sluis}
```

## Lambda

See [AWS Lambda](lambda.md) and [the concept](../../concepts/sluis/lambda.md).

```go
l, _ := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
	Region: "eu-central-1", AccountID: accountID,
	Instance:      "acme",    // the SSM root /sluis/acme (layout v3)
	Package:       "dist/sluis-lambda_1.63.0_linux_arm64.zip", // or an https URL
	PackageSHA256: "<the release's digest, pinned here>",
	Installation:  installation, // *sluisconfig.Installation: the library renders both documents from it
	Storage:       store.Grant(),
	State:         state.Grant(),
	Audit: &sluispulumi.AuditArgs{
		WriterPackage: "dist/audit-writer-lambda_<version>_linux_arm64.zip", WriterPackageSHA256: "<its digest>",
		CatalogueDir: "dist/sluis-audit-catalogue", // the release's sluis-audit-catalogue bundle, unpacked
	},
	Schedule: sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}, SlackWorkspaces: []string{"T0ACME"}},
}, pulumi.Providers(awsProvider))
```

| Rule | Behavior |
|---|---|
| Package | Deployed byte for byte from a path or https URL. `PackageSHA256` is required and checked; pin it in source. Library minor or newer (1.63) |
| Configuration | Immutable layer `<prefix>-config` at `/opt/sluis`, rendered from `Installation`, loader-checked ([layer](lambda.md#configuration-is-a-layer)). A change publishes a new version |
| Endpoints | Refused unless `AllowEndpoints` |

### Inputs (`LambdaArgs`)

| Field | Default | Meaning |
|---|---|---|
| `Region`, `AccountID` | Required | Name the SSM parameters and function in the role policy |
| `Instance` | Required | Lower-case letters, digits, dashes; not `private` or `export`. SSM root `/sluis/<instance>` |
| `Package`, `PackageSHA256`, `PackageVersion` | Required, required, from the file name | Zip, pinned digest, release when the name does not say |
| `Installation` | Required without `Config` | The [installation document](installation-document.md). Rendered like `sluisctl render`; the library fills shape `lambda`, `Instance`, `Region`, `AccountID` and the function name when absent and refuses disagreement |
| `Config`, `Policy`, `PolicyPath` | Deprecated | Service document; policy or a layer directory (one of the last two). Removed after one minor; `NewLambda` warns |
| `AllowEndpoints` | false | LocalStack tests only; an R2 preset's endpoint needs no flag |
| `Storage`, `State` | Required | The grants |
| `Artifacts`, `Release` | Unset | [Artifacts bucket](#artifacts-bucket) |
| `Audit` | Install | [Audit](#audit). Needs `Installation` |
| `AuditQueueArn` | None | Deprecated; exclusive with `Audit`. Required with `Config` |
| `ParameterKeyArn` | None | Customer key for SecureStrings, written as `secrets.kmsKeyId`; another is refused. The role may use it through SSM only |
| `Keys` | nil | [Keys the estate supplies](#keys-the-estate-supplies). Exclusive with the four deprecated key inputs |
| `SigningKeyAlias` | `alias/sluis-signing` | Deprecated: ES384 key alias |
| `SigningKeyRS256Alias`, `DisableSigningKeyRS256` | `alias/sluis-signing-rs256`, false | Deprecated: RSA key alias; created unless disabled |
| `WrappedSigning` | nil | Deprecated: `KeyArn` (unset creates one), `KeyAlias` (`alias/sluis-signing-wrapped`). The asymmetric keys are not declared; `signingKey.kms` beside it is refused |
| `VerifyOnly` | None | `[]VerifyOnlyKeyArgs` of an earlier signer: `PEM` (one public key or certificate, RSA or ECDSA), `KeyID` (default RFC 7638 thumbprint), `Alg`, `Until` (required). Written to `/opt/sluis/verify-keys/<index>.pem` and `signingKey.verifyOnly`. Private keys, duplicates, a mismatched `Alg` and zero `Until` are refused |
| `Recovery` | Enabled | `RecoveryArgs.Enabled` (`*bool`) writes `recovery.enabled`; the password parameter exists either way ([recovery](../../guides/sluis/operate/recover-on-lambda.md)) |
| `FunctionNamePrefix` | `sluis` | Names `<prefix>-scheduler`, `<prefix>-config` and the function default |
| `FunctionName` | `FunctionNamePrefix` | Function, role, policy and log group. A v1.62 installation sets `<prefix>-http` ([upgrade v1.63](../../guides/sluis/upgrade/v1.63.md)) |
| `Function` | 512 MB; 300 s | `FunctionArgs`: `MemoryMB`, `TimeoutSeconds` |
| `LogRetentionDays` | 30 | Function log group |
| `AccessLogs` | nil (off) | `RetentionDays` (7). Declares log group `/aws/apigateway/<FunctionName>` and `$default` access logs: `requestTime`, `requestId`, `httpMethod`, `path`, `status`, `responseLatency`, `integrationLatency`. The query string, headers, address and identity are never logged. The applying principal needs `logs:CreateLogDelivery`, `logs:PutResourcePolicy` and related actions |
| `PermissionsBoundaryArn` | None | Boundary of the role and the scheduler's |
| `API.DomainName`, `.CertificateArn`, `.TruststorePEM`, `.TruststoreBucketName` | Deprecated | All four together keep the mutual-TLS domain with a warning. Move to the [edge module](#the-edge-modules) ([cutover](../../guides/sluis/migrate/cutover.md#moving-a-stack-from-the-core-librarys-domain-to-the-edge-module)) |
| `API.KeepDefaultEndpoint` | false | Keeps the `execute-api` endpoint for the acceptance suite; turn it off after, it bypasses the client certificate |
| `Schedule.GitHubOrgs`, `.SlackWorkspaces` | None | Targets, one schedule each |
| `Schedule.Rate` | `rate(5 minutes)` | EventBridge Scheduler expression |
| `Schedule.Paused`, `DirectoryRefresh.Paused` | false | Declares the schedules `DISABLED`, keeping roles and grants. `Paused` with `Disabled` is refused |
| `DirectoryRefresh.Rate`, `.Disabled` | `rate(15 minutes)`, false | Invokes `{"kind":"refresh"}` |
| `CloudflareRotation.Rate`, `.Disabled`, `.Paused` | `rate(1 minute)`, false, false | Invokes `{"kind":"cloudflare"}`. Exists only when `Installation.Cloudflare` declares presets, which also grants SSM read on `internal/cloudflare/*`, read and write on `internal/cloudflare-minted/*`, write on `external/cloudflare/*` ([Cloudflare tokens](../../guides/sluis/cloudflare-tokens.md)) |
| `WebIdentityAudience` | Any | Restricts the audience of the role's web identity token |
| `AdditionalWebIdentityAudiences` | None | Extra exact audiences after it. Any code under the role can mint them. Empty, duplicate, or with empty `WebIdentityAudience`: refused |
| `Telemetry.LayerArn`, `.Env` | nil | The `otlp-lambda` layer; `Env` accepts `OTEL_*`, `OPENTELEMETRY_*`, `ACCESS_ROSTER_*`, `AWS_LAMBDA_EXEC_WRAPPER` and nothing else. `OTEL_SERVICE_NAME` defaults to the function name |
| `Tags` | None | On everything that takes tags |

### Outputs

| Output | Meaning |
|---|---|
| `SigningKeyArn`, `SigningKeyID`, `SigningKeyAlias` | ES384 signing key |
| `SigningKeyRS256Arn`, `SigningKeyRS256ID`, `SigningKeyRS256Alias` | RS256 key; empty when disabled |
| `WrappedSigningKeyArn`, `WrappedSigningKeyAlias` | Symmetric key of `WrappedSigning` |
| `CodeSha256Matches` | Lambda's code hash equals the verified zip |
| `FunctionArn`, `FunctionName`, `RoleArn`, `RoleName` | Function and role |
| `AccessLogGroupName` | Empty without `AccessLogs` |
| `APIID`, `APIStageName`, `APIURL` | HTTP API; `APIURL` answers only with `KeepDefaultEndpoint`. `FrontDoor()` returns the first two and the component name |
| `DomainTarget`, `DomainHostedZoneID`, `TruststoreBucketName`, `TruststoreURI` | Empty unless the deprecated `API.DomainName` is set |
| `SchedulerRoleArn`, `ScheduleNames` | Scheduler role and schedules |
| `LiveAliasArn`, `LiveVersion` | Alias `live` and its version |
| `ConfigLayerArn` | Configuration layer version |
| `StateSecretParameter` | SSM parameter of the OAuth-state secret |
| `Audit` | `*auditpulumi.Audit` the library installed; nil with `Use`, `Enabled: false` or `AuditQueueArn` |
| `AuditQueueURL`, `AuditQueueArn` | The publish queue; empty when audit is off |

### Artifacts bucket

`Artifacts` uploads the verified zip as is to a versioned bucket; function and layer use that object version.

| Input | Default | Meaning |
|---|---|---|
| `Artifacts.Bucket` | Unset (direct upload) | Must be versioned, else the apply fails |
| `Artifacts.Prefix` | `sluis/` | Keys are `<prefix><version>/<sha256>-<file name>` |
| `Release.ResolveChecksums` | false | Reads an empty `PackageSHA256` from `<BaseURL>/v<version>/checksums.txt` |
| `Release.Version` | From the file name | Names the release when `Package` is empty; `(devel)` is refused |
| `Release.BaseURL` | Project GitHub releases | For a mirror |

| Behavior | Rule |
|---|---|
| `Package` empty | Deploys the library's own release from `<BaseURL>/v<version>/sluis-lambda_<version>_linux_arm64.zip`. A pinned digest wins. `(devel)`, pseudo-versions and replaced modules are refused |
| Cache | `os.UserCacheDir()/sluis/artifacts`; `GITHUB_TOKEN` goes to github.com |

### Audit

`LambdaArgs.Audit` (`AuditArgs`) chooses where records go; a document naming another adapter is refused.

| `Audit` | The library |
|---|---|
| Unset | Installs audit beside the function with `github.com/truvity/sluis/audit/deploy/pulumi` at the same release, named `audit-<instance>` (`Name`). The function is the queue's only sender |
| `Use: &AuditUse{QueueURL, QueueArn}` | Sends to an existing installation. The URL is known when the program runs and in the function's region; `QueueArn` defaults to the URL's ARN |
| `Enabled: &false` | Installs nothing and grants no queue; the `log` adapter writes records to the log only |

| Install input | Meaning |
|---|---|
| `WriterPackage`, `WriterPackageSHA256` | Audit release `audit-writer-lambda_<version>_linux_arm64.zip` and digest; `Guards` holds it to the library release. Required |
| `CatalogueDir` | Directory the release's `sluis-audit-catalogue_<version>.tar.gz` unpacks to. Required. It travels in the writer's layer, and a catalogue version bump redeploys the writer |
| `Profiles` | Destination to framework profiles. Default `security: [history]` (`DefaultAuditProfiles`), preset `operational`: writer, archive, deduplication and queue intake, no notary, seal key, alarm or Object Lock. Others need `Presets` |
| `DeploymentYAML` | The whole deployment document. Exclusive with `Profiles` |
| `Notary`, `Keys`, `Alerts`, `Telemetry`, `Observe` | Passed through above operational |
| `Presets`, `AuditPreset` | One store per preset; never the blob bucket |

Missing inputs are refused before anything is created. A preset store is one of:

| Store | Behavior |
|---|---|
| AWS bucket created (`Create: true`) | Named `<name>-<account>-<region>-<preset>` unless `Bucket` is set; optional `Prefix`; `KeyAlias` is looked up, never created. SSE-S3 without `Keys.Archive` (`Archive.Encryption`: `kms`, `s3`, `aws-managed`). Object Lock (`ObjectLockMode`, `AcknowledgeCompliance`, `DefaultRetentionDays`) only for the attested preset |
| Existing AWS bucket (`Create: false`) | Grants only |
| S3-compatible (`Endpoint`) | No bucket created; `Bucket` required. Credentials come from the state store at `CredentialsAddress` (default `internal/archive/<preset>`), written by an operator. No Object Lock. `ReuseBlobStore` takes `Endpoint`, `Region`, `PathStyle` from `StorageArgs.Blobs`; naming the blob bucket is refused |

To adopt an existing audit stack, import it under the new component ([resource names](../../guides/audit/operate/archive-on-r2.md)).

### Keys the estate supplies

`LambdaArgs.Keys` (`KeysArgs`) takes aliases of symmetric keys you own; the library creates none, and the key policy must admit the function's role. A document naming `keys` is refused. To adopt library-created keys, supply their aliases and `pulumi state delete` the old `kms.Key` resources after unprotecting ([steps](../../guides/sluis/migrate/cutover.md#moving-a-stack-from-library-created-keys-to-supplied-ones)).

| `Keys` field | Grant |
|---|---|
| `Sign` (required) | `kms:Encrypt`, `Decrypt`, `GenerateDataKey` with context `{instance: <Instance>, purpose: sign}` and no other context key. The runtime wraps ring key pairs locally; no `kms:Sign` |
| `Secrets` (optional) | The same actions through SSM only (`kms:ViaService` `ssm.<region>.amazonaws.com`), for the installation's parameters (`kms:EncryptionContext:PARAMETER_ARN` StringLike `…:parameter/sluis/<instance>/*`). Written as `secrets.kmsKeyId`; exclusive with `ParameterKeyArn` |
| `LegacySigningContext` (nil is true) | Also keeps `GenerateDataKeyPair` and `Decrypt` on `Sign` under `purpose=sluis-signing` for older ring entries (`WrappedKeyPolicyStatements` belongs in the key policy). Set false after the ring rotates |

### The edge modules

`deploy/pulumi/edge/cloudflare` (package `edgecloudflare`) builds the mutual-TLS custom domain, its API mapping, the truststore and optionally the ACM certificate. It is tagged with the core.

```go
store, _ := sluispulumi.NewStorage(ctx, "access", &sluispulumi.StorageArgs{
	BucketName: "acme-sluis", Versioning: true, // the domain pins the truststore's version
	ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edgecloudflare.Guard(cdRoleArn, operatorsAdminArn, breakglassArn)},
}, pulumi.Providers(awsProvider))
l, _ := sluispulumi.NewLambda(ctx, "access", lambdaArgs, pulumi.Providers(awsProvider))
front, _ := edgecloudflare.NewEdge(ctx, "access", &edgecloudflare.Args{
	FrontDoor:      l.FrontDoor(),
	DomainName:     "access.example.test",
	CertificateArn: originCertArn, // or Certificate: &edgecloudflare.CertificateArgs{...}
	TruststorePEM:  cloudflareOriginPullCA,
	Storage:        store, // or TruststoreBucket: ... for blobs on R2
}, pulumi.Providers(awsProvider))
```

| `edgecloudflare.Args` | Meaning |
|---|---|
| `FrontDoor`, `DomainName` | Required |
| `CertificateArn` or `Certificate` | One of the two. `CertificateArn` is a caller-supplied ACM certificate in the function's region. `Certificate` requests one with DNS validation and calls `CreateValidationRecord(ctx, record)` (`Name`, `Type`, `Value`) to create the record in your DNS and return its name. The edge holds no DNS credential |
| `TruststorePEM` | Required: CAs a client certificate chains to; for Authenticated Origin Pulls, Cloudflare's origin-pull CA |
| `Storage` or `TruststoreBucket` | One of the two |
| `Tags` | On what the edge creates |

| `Edge` output | Meaning |
|---|---|
| `DomainTarget`, `DomainHostedZoneID` | DNS target (CNAME or alias) |
| `CertificateArn` | Certificate in use |
| `TruststoreBucketName`, `TruststoreURI`, `TruststoreVersion` | Client-CA bundle location and pinned version |

| Truststore | Behavior |
|---|---|
| Object | `truststore/client-ca.pem` in a versioned bucket; the domain pins the version, so a new PEM redeploys |
| Guard | The bucket policy denies write, delete and re-label under `truststore/` to everyone except the roles you name (`ArnNotEquals` on `aws:PrincipalArn`). Declare it on `StorageArgs.ProtectedPrefixes` with `edgecloudflare.Guard(...)`. With `Storage` the edge refuses an unversioned or unguarded bucket |
| Blobs on R2 | Leave `Storage` unset and give `TruststoreBucket` (`Name`, `ApplyPrincipalArns`): a small protected, versioned, encrypted, private, TLS-only S3 bucket with the same guard |
| Authenticated Origin Pulls | Turn on the zone's `tls_client_auth` in your own Cloudflare program, else the domain refuses every request. The DNS record for `DomainTarget` is also yours |

### More behavior

| Part | Behavior |
|---|---|
| Live alias | Every change publishes a version; alias `live` points at the newest. API, schedules, scheduler grant, self-invoke grant and the `invoke` trigger use it. Asynchronous invoke has no retries. Rollback repoints the alias |
| API | HTTP API (payload 2.0), `$default` route and stage. The `execute-api` endpoint is off unless `KeepDefaultEndpoint`. `Lambda.FrontDoor()` gives a front door the API id, stage and name |
| Schedules | `<prefix>-github-<org>` and `<prefix>-slack-<workspace>` invoke `{"kind":"tick","target":"<id>"}`; a target is letters, digits and `- _ . :`, at most 40, with `:` becoming `-` in the name. `<prefix>-directory-refresh` invokes `{"kind":"refresh"}`. Role `<prefix>-scheduler` may invoke the function only. No retries |
| `ExternalReadPolicy(ExternalReadPolicyArgs)` | IAM policy: `ssm:GetParameter` on exact `external/<kind>/<id>` ARNs under `/sluis/<instance>/`; with `SecretsKeyArn` or `ParameterKeyArn`, `kms:Decrypt` through SSM bound to those ARNs. Wildcards, prefixes, repeats and non-`external/` addresses are refused |
| `NewExternalReader(ctx, name, &ExternalReaderArgs{RoleName, Region, AccountID, Instance, Addresses, SecretsKeyArn, ParameterKeyArn})` | Attaches that policy to the role inline; output `PolicyJSON` |
| Related | [environment](lambda.md#environment), [IAM](lambda.md#iam-one-role), [signing on AWS](../../concepts/sluis/signing-on-aws.md), [key policy](aws-signing-key.md), [SSM root](lambda.md#instance-and-the-ssm-root) |

Upgrades: [v1.63](../../guides/sluis/upgrade/v1.63.md), [v1.62](../../guides/sluis/upgrade/v1.62.md). Releasing: [CONTRIBUTING](../../../CONTRIBUTING.md#releasing).
