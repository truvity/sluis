# The Pulumi library

The AWS shape of sluis as a Pulumi Go library, `github.com/truvity/sluis/deploy/pulumi`, a module of its own so that Pulumi
is not in the root module's dependency graph. The infrastructure lives here, in a versioned library next to the code it
serves, and gitops only wires it: a stack calls the constructors and renders the processes' configuration from the same
names. Source: `deploy/pulumi`; the exported identifiers are in [Go packages](go-module.md).

**Lambda is the main path** (decision of 2026-10-04: both estates run sluis on AWS Lambda). `NewLambda` is the
whole of it: ONE function, one role, the HTTP API with a mutual-TLS custom domain, the signing key and the schedules.
`NewKubernetesIdentity` (EKS Pod Identity) is kept for an installation that still runs the Deployment, and is not
extended. Since v1.63 sluis is one process everywhere ([decision 0037](../decisions/0037-one-process-everywhere.md)): one
function, one role, and on Kubernetes one Deployment, one ServiceAccount, one role.

**Nothing here is deployed by this repository.** The library is tested against Pulumi's mocks (`just pulumi-test`): it
declares the right resources with the right arguments and creates none, and the `ports:` block it renders is validated
against the schemas in `schemas/config`.

## The shape

Four components, each usable alone:

| Component | Type token | Creates |
|---|---|---|
| `NewStorage` | `sluis:aws:Storage` | the blob bucket |
| `NewState` | `sluis:aws:State` | the DynamoDB table of the DynamoDB adapter |
| `NewLambda` | `sluis:aws:Lambda` | the function, its role, the API, the signing key, the schedules |
| `NewKubernetesIdentity` | `sluis:aws:KubernetesIdentity` | the one EKS Pod Identity role of the one pod (not the main path) |

and `RenderPorts` (`RenderPortsYAML`), which renders the `ports:` block. The Lambda stack is [below](#lambda); the
example here is the storage, the table and the Pod Identity role.

```go
store, _ := sluispulumi.NewStorage(ctx, "acme", &sluispulumi.StorageArgs{
	BucketName: "acme-prod-sluis",
}, pulumi.Providers(awsProvider))
state, _ := sluispulumi.NewState(ctx, "acme", &sluispulumi.StateArgs{
	TableName: "acme-sluis",
}, pulumi.Providers(awsProvider))
ids, _ := sluispulumi.NewKubernetesIdentity(ctx, "acme", &sluispulumi.KubernetesIdentityArgs{
	ClusterName: "acme", ClusterArn: clusterArn, AccountID: accountID,
	Namespace:              "sluis",
	PermissionsBoundaryArn: boundaryArn,
	ServiceAccount:         "sluis",
	Storage:                store.Grant(),
	State:                  state.Grant(),
}, pulumi.Providers(awsProvider))
```

The AWS provider is the caller's: pass `pulumi.Providers(p)` (or
`pulumi.Provider(p)`) as an option and the components' children use it. No
constructor makes a call to AWS to find out something it was not told.

## Storage

### Inputs (`StorageArgs`)

| Field | Default | Meaning |
|---|---|---|
| `BucketName` | required | The bucket's name. It is in the configuration, so it is known before anything is created, and a bucket name is global. |
| `Versioning` | off | S3 versioning. The bucket holds the controllers' last reports and the directory snapshots, which the next tick regenerates and which are never a credential; an ETag is the compare-and-swap's version. |
| `Tags` | none | On the bucket. |

### Outputs

`BucketName`, `BucketArn`, and `Grant()`, which is what `NewLambda` and
`NewKubernetesIdentity` take as `Storage`.

### What is created

- **The bucket**, protected, encrypted with S3-managed keys (AES256), with every public-access block on, and a bucket
  policy that denies every action to every principal when the transport is not
  TLS (`aws:SecureTransport` false) on the bucket and its objects. Versioning when
  asked for.

There is no key: sealing is retired. A stack that an earlier release created has
the Sealer's key (`<name>-sealer-key`) and its alias (`<name>-sealer-alias`) in
its state, both protected; the first apply of this release removes them from the
program, so **unprotect them first** (`pulumi state unprotect <urn>`), and the
apply then schedules the key's deletion after its 30-day window.

## State

The table of the DynamoDB adapter (`internal/port/dynamodb`, [ADR 0027](../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md)); State is DynamoDB on AWS.

### Inputs (`StateArgs`)

| Field | Default | Meaning |
|---|---|---|
| `TableName` | required | The table's name (`ports.dynamodb.table`). |
| `KeyArn` | none | A customer-managed key to encrypt the table with. Absent, the AWS-owned key: free and needing no grant. |
| `Tags` | none | On the table. |

### Outputs

`TableName`, `TableArn`, and `Grant()` (the table's ARN and the key's, if any).

### What is created

One table, protected and with DynamoDB's own deletion protection, shaped as the
adapter's documentation says:

| Attribute | Type | |
|---|---|---|
| `pk` | S | the hash key: the record kind (`directory`, `github-org`, `issuer-token`) |
| `sk` | S | the range key: the record's id, `/`-separated when compound (see [storage layout](storage-layout.md)) |
| `rev` | N | the revision, written by the adapter on every write |
| `expires` | N | the TTL attribute, epoch seconds |

Only the two key attributes are declared; DynamoDB takes the rest schemaless.
Billing is on-demand, point-in-time recovery is on and TTL is on `expires`. The
adapter judges expiry itself on every read, so DynamoDB's own sweep, which can
be days late, is housekeeping and not a correctness matter. There is no
secondary index: the adapter's Index is items of the same table.

## Kubernetes identity

ONE EKS Pod Identity role, for the one pod: the service and the GitHub and Slack
controllers run in one process, in one Deployment, as one ServiceAccount (v1.63,
[decision 0037](../decisions/0037-one-process-everywhere.md)). The per-process roles
of v1.62 are gone, and with them the isolation between the issuer and a controller:
the controllers' code runs with the issuer's permissions.

### Inputs (`KubernetesIdentityArgs`)

| Field | Default | Meaning |
|---|---|---|
| `ClusterName` | required | The EKS cluster the associations are made in. |
| `ClusterArn`, `AccountID` | required | Pin each trust policy to the cluster: the source ARN and the source account EKS stamps on every assume. |
| `Region` | provider's | Set on each association. |
| `Namespace` | required | The namespace of the ServiceAccount. |
| `PermissionsBoundaryArn` | none | The boundary of the role. The estate's rule is that a role has one (gitops uses `pb@default`). |
| `RoleNamePrefix` | the component's name | The role is `<prefix>-sluis` (was `<prefix>-sluis-serve`); its managed policy has the role's name. |
| `ServiceAccount` | required | The ServiceAccount the pod runs as, in `Namespace`. |
| `Description` | says what the role is allowed | The policy's description, which IAM cannot change once set (a change replaces the policy). |
| `Instance`, `Region`, `ParameterKeyArn` | none | With `Instance` (the installation's name, as in `NewLambda`; `Region` is then required), the role has the Lambda role's SSM grants under `/sluis/<instance>/`: credentials read/write, config read, exports, and `ParameterKeyArn` through SSM only. |
| `Storage` | required | `Storage.Grant()`. |
| `SigningKeyArns` | none | The Lambda stack's signing keys (`SigningKeyArn`, `SigningKeyRS256Arn`). Set, the role may `kms:Sign` and `kms:GetPublicKey` with them. |
| `WrappedSigningKeyArn` | none | The symmetric key of the `kms-wrapped` signing adapter ([signing on AWS](../explanation/signing-on-aws.md)): the Lambda stack's `WrappedSigningKeyArn`: a key whose policy reserves the signing context to the signing roles (list this role in `WrappedSigning.AdditionalSigningRoleArns`, or in the denial merged into a shared key). Set, the role may `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` with it, under the conditions below. |
| `State` | none | `State.Grant()`. Nil when the pod's State is not DynamoDB (the Kubernetes-objects store, `legacy`): the role then carries no DynamoDB grant. |

### Outputs

`RoleArn`, `RoleName` (v1.62's `ServeRoleArn`, `GitHubRoleArn`, `SlackRoleArn` and the
names are gone).

### What is created

A customer-managed policy and a role with the same name, the attachment, and one
`PodIdentityAssociation` of the ServiceAccount with the role. The trust policy
lets `pods.eks.amazonaws.com` assume the role (`sts:AssumeRole` and
`sts:TagSession`) only for this account and cluster, this namespace and this one
ServiceAccount. A ServiceAccount takes one association.

### IAM, whole

The role's grants are the whole of its policy (with `Instance`, the SSM grants of the
Lambda role are added; see [IAM](lambda.md#iam-one-role)).

| Sid | Actions | Resource |
|---|---|---|
| `SluisBlobs` | `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` | the bucket's objects |
| `SluisBlobList` | `s3:ListBucket` | the bucket (a read of an absent key is a 404 only with it, a 403 without) |
| `SluisSigning`, with `SigningKeyArns` | `kms:Sign`, `kms:GetPublicKey` | the signing keys |
| `SluisWrappedSigning`, with `WrappedSigningKeyArn` | `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | the symmetric key, only with `kms:EncryptionContext:purpose` = `sluis-signing` and no context keys beside `purpose`, `alg`, `kid` |
| `SluisState`, with State | `dynamodb:GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` | the table |
| `SluisStateKey`, with a table key | `kms:Encrypt`, `kms:Decrypt`, `kms:GenerateDataKey`, `kms:DescribeKey` | the table's key, only through DynamoDB (`kms:ViaService`) |

`Scan` is `sluis migrate` and a listing by a prefix with no dot; `DescribeTable`
is the start-up check and the readiness probe. Nothing is granted on `*`.

## The configuration

`RenderPorts` renders the `ports:` block of the service document (`sluis serve` and its controllers read it), from names
the components were given, so none waits for a resource:

```go
y, _ := sluispulumi.RenderPortsYAML(sluispulumi.PortsArgs{
	BucketName: "acme-prod-sluis",
	TableName:  "acme-sluis",
	Region:     "eu-west-1",
})
```

```yaml
ports:
  adapter: dynamodb
  blob:
    adapter: s3
    s3: {bucket: acme-prod-sluis, region: eu-west-1}
  dynamodb: {region: eu-west-1, table: acme-sluis}
```

`create` is never rendered: the table is the infrastructure's, and the adapter binds to it and checks it. Credentials are
the platform's. Without `TableName` no State adapter is written (the schema's default) and only the blob is. `Adapter`
other than `dynamodb` is refused. Sealing is retired: `KeyID` is ignored and nothing renders a `sealer:` block. The tests
hold the block to the schema in `schemas/config`, so a key renamed there fails there.

Moving the serving pod to the library's role: [switch the serving pod to its own role](../how-to/switch-serving-pod-role.md).

## Lambda

`NewLambda` is the Lambda shape: **one function from one zip, with the configuration in a layer**, one role, the HTTP API
in front of it, the token-signing key, and a schedule per controller target. What the function takes in, reads and may
do is in [AWS Lambda: reference](lambda.md); why it is shaped so, in [sluis on AWS Lambda](../explanation/lambda.md).

```go
l, _ := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
	Region: "eu-central-1", AccountID: accountID,
	Instance:      "acme",    // the SSM root /sluis/acme (layout v3)
	Package:       "dist/sluis-lambda_1.63.0_linux_arm64.zip", // or an https URL
	PackageSHA256: "<the release's digest, pinned here>",
	Installation:  installation, // *sluisconfig.Installation: the library renders both documents from it
	Storage:       store.Grant(),
	State:         state.Grant(),
	AuditQueueArn: audit.QueueArn,
	API: sluispulumi.APIArgs{
		DomainName:           "access.example.test",
		CertificateArn:       originCertArn,  // an ACM certificate the caller supplies
		TruststorePEM:        cloudflareOriginPullCA,
		TruststoreBucketName: "acme-sluis-truststore",
	},
	Schedule: sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}, SlackWorkspaces: []string{"T0ACME"}},
}, pulumi.Providers(awsProvider))
```

**The package** is the released `sluis-lambda_<version>_linux_arm64.zip` (with `bootstrap` at its root), read from a path
or an https URL when the stack is evaluated, and **deployed byte for byte**: `PackageSHA256` is required and checked, and
the library adds nothing to the zip. A URL is fetched once and kept under its digest; a local file is copied to a
temporary file first, so what is checked is what is deployed. Take the digest from a reviewed pin in the stack's source,
never from a file fetched at deploy time beside the zip. The package must be of the library's own minor or newer (1.63 or
later), or it is refused: an older binary cannot read the layer.

**The configuration** is one immutable `aws.lambda.LayerVersion`, `<prefix>-config`, mounted last at `/opt/sluis`: the
service document and the policy, rendered from `Installation` (or, deprecated, given as `Config` and `Policy` or
`PolicyPath`). Each is held to sluis's own loader before anything is published, and the library writes what is its own
into them; a document that disagrees is refused, naming the key. A change to a document publishes a new layer version
and updates the function, never silently. Documents may not name an `endpoint`, unless `AllowEndpoints` is set for a
LocalStack test. The rules, the keys the library owns and the secrets are in
[AWS Lambda: configuration is a layer](lambda.md#configuration-is-a-layer).

### Inputs (`LambdaArgs`)

| Field | Default | Meaning |
|---|---|---|
| `Region`, `AccountID` | required | Name the SSM parameters and the function in the role's policy. |
| `Instance` | required | The installation's name (`acme`, `prod`): lower-case letters, digits and dashes, never `private` or `export`. Its SSM root is `/sluis/<instance>` (layout v3), so two installations share an account. |
| `Package`, `PackageSHA256`, `PackageVersion` | required, required, from the file name | The released zip, deployed unchanged; its SHA-256 (a reviewed pin); the release it is, when its name does not say. |
| `Installation` | `Config`, or this | What the estate knows ([the installation document](installation-document.md), `github.com/truvity/sluis/config`): the library renders both documents from it, with the renderer `sluisctl render` runs, and writes what is its own (the shape `lambda`, and `Instance`, `Region`, `AccountID` and the function's name from the arguments, when the installation leaves them out; a disagreement is refused). It replaces `Config`, `Policy` and `PolicyPath`. |
| `Config` (deprecated) | required without `Installation` | The one service document (`sluis/v3`: the `serve` keys at the top level and `controllers.github` / `controllers.slack`), in the configuration layer. With the `invoke` trigger the library writes `adapters.trigger.settings` (`github` and `slack` are this function); a document that names another is refused. `GitHubConfig`, `SlackConfig` are gone. |
| `Policy`, `PolicyPath` (deprecated) | exactly one, without `Installation` | The policy document, or a file or directory of layers rendered by sluis's renderer (`sluisctl policy render`); the layer holds it at `/opt/sluis/policy.yaml`. The GitHub and Slack catalogues are in it (`apps.github.catalogue`, `apps.slack.catalogue`). |
| `AllowEndpoints` | false | Lets the documents name a service `endpoint`, for a LocalStack test. Off, one is refused. |
| `Storage`, `State` | required | `Storage.Grant()` and `State.Grant()`. |
| `AuditQueueArn` | required | The audit stack's ingest queue. |
| `ParameterKeyArn` | none | A customer-managed key the SecureString parameters use. Absent, the AWS-managed key, which needs no grant. Present, the role may use it through SSM only. |
| `SigningKeyAlias` | `alias/sluis-signing` | The ES384 signing key's alias. |
| `SigningKeyRS256Alias`, `DisableSigningKeyRS256` | `alias/sluis-signing-rs256`, false | The RSA signing key's alias; the key is created unless disabled. |
| `WrappedSigning` | nil | Signing with the `kms-wrapped` adapter ([signing on AWS](../explanation/signing-on-aws.md)): `KeyArn` (an existing symmetric key; unset creates one), `KeyAlias` (default `alias/sluis-signing-wrapped`). Set, the two asymmetric keys are no longer declared, and a `Config` naming `signingKey.kms` beside it is refused: see [Moving a stack from remote signing](lambda.md#iam-one-role). |
| `VerifyOnly` | none | `[]VerifyOnlyKeyArgs`: the PUBLIC keys of an earlier signer, published in the JWKS and never signed with, so that the tokens it issued keep verifying for the overlap after a cutover. Each is `PEM` (one `PUBLIC KEY`, `RSA PUBLIC KEY` or `CERTIFICATE` block, RSA or ECDSA), `KeyID` (the `kid` the old tokens carry; unset is the RFC 7638 thumbprint), `Alg` (unset follows the key) and `Until` (required: when it stops being published). The layer holds each at `/opt/sluis/verify-keys/<index>.pem` (`VerifyOnlyKeyPath`), and the library writes `signingKey.verifyOnly` naming them, as the chart's `signingKey.verifyOnly` does; a document or an `Installation` that names `signingKey.verifyOnly` itself is refused. A private key, two keys in one PEM, the same key or `kid` twice, an `Alg` the key does not sign with, and a zero `Until` are refused before anything is published; an error never quotes the key. |
| `Recovery` | enabled | A `RecoveryArgs`: `Enabled` (a `*bool`) writes `recovery.enabled`. The generated password parameter exists whatever it says, so turning recovery off and on is never a rotation ([Recovery on Lambda](../how-to/recover-on-lambda.md)). |
| `FunctionNamePrefix` | `sluis` | Names `<prefix>-scheduler`, and the function when `FunctionName` is not set. |
| `FunctionName` | `Recovery` | enabled | A `RecoveryArgs`: `Enabled` (a `*bool`) writes `recovery.enabled`. The generated password parameter exists whatever it says, so turning recovery off and on is never a rotation ([Recovery on Lambda](../how-to/recover-on-lambda.md)). |
| `FunctionNamePrefix` | The function, its role, policy and log group. **A v1.62 installation sets `<prefix>-http` here**, which keeps its function, role, log group and API integration in place: see [Upgrade to v1.63](../how-to/upgrade/v1.63.md). |
| `Function` | 512 MB; 300 s | A `FunctionArgs`: `MemoryMB`, `TimeoutSeconds`. (`HTTP`, `GitHub` and `Slack` are gone since v1.63.) |
| `LogRetentionDays` | 30 | The function's log group. |
| `AccessLogs` | nil (off) | An `AccessLogsArgs`: `RetentionDays` (default 7). Set, the library declares a log group `/aws/apigateway/<FunctionName>` and the `$default` stage's access log settings. Each request writes one JSON line with exactly `requestTime`, `requestId`, `httpMethod`, `path`, `status`, `responseLatency` and `integrationLatency` (the format is `AccessLogFormat`). The query string is never logged, because OAuth authorization codes and `state` travel in it; no header, source address, user agent or identity field is logged either. An HTTP API needs no account-level CloudWatch role (that is a REST API setting), so this adds no IAM resource; the principal that applies the stack needs API Gateway's log-delivery permissions (`logs:CreateLogDelivery`, `logs:PutResourcePolicy` and the related describe and update actions), and API Gateway adds the log group's resource policy itself. |
| `PermissionsBoundaryArn` | none | The boundary of the role and the scheduler's. |
| `API.DomainName`, `API.CertificateArn` | required | The custom domain and the ACM certificate for it, in the region (the caller supplies it, for example a Cloudflare Origin CA certificate imported to ACM). |
| `API.TruststorePEM`, `API.TruststoreBucketName` | required | The client-CA bundle for mutual TLS, and the bucket the library uploads it to. |
| `API.KeepDefaultEndpoint` | false | Leaves the default `execute-api` endpoint on, for the cutover's acceptance suite to run against `ApiUrl` before the DNS switch. Turn it off again after: it is a way round the client certificate. |
| `Schedule.GitHubOrgs`, `Schedule.SlackWorkspaces` | none | The targets, one schedule each. |
| `Schedule.Rate` | `rate(5 minutes)` | The EventBridge Scheduler expression. |
| `Schedule.Paused`, `Exports.Paused`, `DirectoryRefresh.Paused` | false | Declares those schedules with `state: DISABLED` and keeps everything else: the schedules, the scheduler's role and the function's grants (the `export/*` read and write included), so that turning them on is one setting and the preview shows only each schedule's state. Unset, a schedule's state is left to EventBridge Scheduler's default (enabled), as before. `Paused` and `Disabled` together are refused. |
| `Exports.Rate`, `Exports.Disabled` | `rate(15 minutes)`, false | The exports schedule: how often the function is invoked with `{"kind":"exports"}`. `Disabled` leaves it out, and the function's read of `export/*` with it. (`Exports.Function` is gone.) |
| `DirectoryRefresh.Rate`, `DirectoryRefresh.Disabled` | `rate(15 minutes)`, false | The directory refresh schedule: how often the function is invoked with `{"kind":"refresh"}` to take a new snapshot of every connected directory, under the refresh lease. Lambda has no refresh loop; a request that finds a snapshot due refreshes it too. |
| `WebIdentityAudience` | any | Restricts the audience of the outbound web identity token the role may ask STS for. |
| `Telemetry.LayerArn`, `Telemetry.Env` | nil: no layer | The observability `otlp-lambda` layer and its settings: `Telemetry.Env` holds `OTEL_*`, the layer's own (`ACCESS_ROSTER_*`, `OPENTELEMETRY_*`) and `AWS_LAMBDA_EXEC_WRAPPER`, and nothing else (never `SLUIS_*`, `LD_*` or another `AWS_*`). Optional, so an estate whose collector is not ready leaves it out. `OTEL_SERVICE_NAME` is the function's name unless given. |
| `Tags` | none | On everything that takes tags. |

`Installation` is the way to configure the function. `Config`, `Policy` and `PolicyPath` are deprecated: they keep
working for one minor, are removed after it, and `NewLambda` logs a warning while one is used.

### Outputs

### Outputs

| Output | |
|---|---|
| `SigningKeyArn`, `SigningKeyID`, `SigningKeyAlias` | The ES384 token-signing key. |
| `SigningKeyRS256Arn`, `SigningKeyRS256ID`, `SigningKeyRS256Alias` | The RS256 token-signing key (empty when disabled). |
| `WrappedSigningKeyArn`, `WrappedSigningKeyAlias` | The symmetric key of `WrappedSigning` (`KeyArn` when given; the alias is empty then, and without `WrappedSigning`). |
| `FunctionArn`, `FunctionName` | The function (replace `HTTP|GitHub|SlackFunctionArn` and the names). |
| `RoleArn`, `RoleName` | Its role (replace `HTTP|GitHub|SlackRoleArn` and the names). |
| `AccessLogGroupName` | The API access log group (empty without `AccessLogs`). |
| `APIID`, `APIURL` | The HTTP API and its default endpoint (it answers only with `KeepDefaultEndpoint`). |
| `DomainTarget`, `DomainHostedZoneID` | What DNS for the custom domain points at (a CNAME or an alias record). |
| `TruststoreBucketName`, `TruststoreURI` | The client-CA bundle. |
| `SchedulerRoleArn`, `ScheduleNames` | The scheduler's role and the schedules. |
| `ConfigLayerArn` | The configuration layer version: the documents and the policy. |
| `StateSecretParameter` | The SSM parameter of the issuer's OAuth-state secret. |
| `ExportReadPolicyJSON` | The policy document a consumer's External Secrets Operator role attaches ([lambda reference](lambda.md#iam-one-role)). |

### The API

An HTTP API (payload format 2.0) with a `$default` route to the function, behind a regional custom domain (TLS 1.2 or
later) with **mutual TLS**: the truststore is `TruststorePEM` in a bucket of its own (versioned, encrypted, closed to the
public and to plain HTTP, protected), not in the blob bucket, because the function can write that one and a function that
can replace the client CA has no use for a client certificate. The domain names the object's version, so a new PEM
redeploys it. This is Cloudflare's authenticated origin pulls: the PEM is Cloudflare's origin-pull CA. The default
endpoint is disabled unless `KeepDefaultEndpoint` is set.

### The schedules

One EventBridge schedule per target, `<prefix>-github-<org>` and `<prefix>-slack-<workspace>`, each invoking the one
function with `{"kind":"tick","target":"<id>"}`. A target is letters, digits and `- _ . :`, at most 40 (`github:links` is
the link check; a colon is a `-` in the schedule's name). `<prefix>-exports` invokes it with `{"kind":"exports"}` every
`Exports.Rate`, and `<prefix>-directory-refresh` with `{"kind":"refresh"}` every `DirectoryRefresh.Rate`. A schedule
whose `Paused` is set is declared disabled, so an estate preparing a cutover has every schedule and grant in place and
turns the schedules on with one setting ([cutover](../how-to/cutover.md#before-you-start)). The scheduler
has a role of its own, `<prefix>-scheduler`, that may invoke the one function and nothing else; no schedule retries,
because the next tick runs the pass again. The controllers' outbound web identity needs outbound identity federation
enabled in the account; the library does not enable it.

### The environment, IAM, signing and SSM

The library sets `SLUIS_CONFIG` and, from `Telemetry.Env`, the telemetry layer's `OTEL_*`; the binary refuses every
retired variable ([environment](lambda.md#environment)). The role and its grants are in [IAM: one role](lambda.md#iam-one-role).
Signing with the `kms` or `kms-wrapped` adapter: [Signing on AWS](../explanation/signing-on-aws.md) and
[the key policy](aws-signing-key.md). The SSM layout, the generated state secret and the recovery password, and
`ExportReadPolicyJSON` are in [AWS Lambda: `Instance` and the SSM root](lambda.md#instance-and-the-ssm-root) and
[storage layout](storage-layout.md#ssm-the-ssm-secrets-adapter).

## Upgrading the library

Move the binary and the library together, in one apply: [Upgrade to v1.63](../how-to/upgrade/v1.63.md) (one function; on
EKS, one role) and [Upgrade to v1.62](../how-to/upgrade/v1.62.md). Preview before every apply.

## Releasing

The release workflow tags the library `deploy/pulumi/vX.Y.Z` at a child of the release commit whose `go.mod` requires the
root module at the release (`hack/pin-pulumi-require.sh`), which is how
`go get github.com/truvity/sluis/deploy/pulumi@vX.Y.Z` finds a library that builds for a consumer. Nobody bumps the
require by hand before a tag. A `deploy/pulumi/vX.Y.Z` tag pushed by hand is refused by design. The procedure and its
gates are in [CONTRIBUTING](../../CONTRIBUTING.md#releasing).
