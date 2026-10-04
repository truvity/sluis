# AWS

The AWS shape of sluis as a Pulumi Go library,
`github.com/truvity/sluis/deploy/pulumi`, a module of its own so that Pulumi is
not in the root module's dependency graph. The infrastructure lives here, in a
versioned library next to the code it serves, and gitops only wires it: a stack
calls the constructors and renders the processes' configuration from the same
names.

**Lambda is the main path** (decision of 2026-10-04: Truvity and hive both run
sluis on AWS Lambda). `NewLambda` is the whole of it: three functions, a role
each, the HTTP API with a mutual-TLS custom domain, the signing key and the
schedules. `NewKubernetesIdentity` (EKS Pod Identity) is kept for an installation
that still runs the Deployments, and is not extended.

**Nothing here is deployed by this repository.** The library is tested against
Pulumi's mocks (`just pulumi-test`): it declares the right resources with the
right arguments and creates none, and the `ports:` block it renders is validated
against the schemas in `schemas/config`. What has not happened is a run in an
account.

## The shape

Four components, each usable alone, so that an installation which keeps its
State on NATS simply omits the table:

| Component | Type token | Creates |
|---|---|---|
| `NewStorage` | `sluis:aws:Storage` | the blob bucket |
| `NewState` | `sluis:aws:State` | the DynamoDB table of the DynamoDB adapter |
| `NewLambda` | `sluis:aws:Lambda` | the three functions, their roles, the API, the signing key, the schedules |
| `NewKubernetesIdentity` | `sluis:aws:KubernetesIdentity` | one EKS Pod Identity role per process (not the main path) |

and `RenderPorts` (`RenderPortsYAML`), which renders the `ports:` block.
[The Lambda section](#lambda) has the Lambda stack; the example below is the
storage and the Pod Identity roles.

```go
store, _ := sluispulumi.NewStorage(ctx, "kernel", &sluispulumi.StorageArgs{
	BucketName: "acme-kernel-sluis",
}, pulumi.Providers(awsProvider))
state, _ := sluispulumi.NewState(ctx, "kernel", &sluispulumi.StateArgs{
	TableName: "kernel-sluis",
}, pulumi.Providers(awsProvider))
ids, _ := sluispulumi.NewKubernetesIdentity(ctx, "kernel", &sluispulumi.KubernetesIdentityArgs{
	ClusterName: "kernel", ClusterArn: clusterArn, AccountID: accountID,
	Namespace:              "sluis",
	PermissionsBoundaryArn: boundaryArn,
	Serve:                  sluispulumi.ProcessArgs{ServiceAccount: "sluis"},
	GitHub:                 sluispulumi.ProcessArgs{ServiceAccount: "sluis-github"},
	Slack:                  sluispulumi.ProcessArgs{ServiceAccount: "sluis-slack"},
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

The table of the DynamoDB adapter (`internal/port/dynamodb`, ADR 0027).

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
| `sk` | S | the range key: the record's id, `/`-separated when compound (see [storage layout](../reference/storage-layout.md)) |
| `rev` | N | the revision, written by the adapter on every write |
| `expires` | N | the TTL attribute, epoch seconds |

Only the two key attributes are declared; DynamoDB takes the rest schemaless.
Billing is on-demand, point-in-time recovery is on and TTL is on `expires`. The
adapter judges expiry itself on every read, so DynamoDB's own sweep, which can
be days late, is housekeeping and not a correctness matter. There is no
secondary index: the adapter's Index is items of the same table.

## Kubernetes identity

One EKS Pod Identity role per process, so that a grant for one is never a grant
for another.

### Inputs (`KubernetesIdentityArgs`)

| Field | Default | Meaning |
|---|---|---|
| `ClusterName` | required | The EKS cluster the associations are made in. |
| `ClusterArn`, `AccountID` | required | Pin each trust policy to the cluster: the source ARN and the source account EKS stamps on every assume. |
| `Region` | provider's | Set on each association. |
| `Namespace` | required | The namespace of the three ServiceAccounts. |
| `PermissionsBoundaryArn` | none | The boundary of every role. The estate's rule is that a role has one (gitops uses `pb@default`). |
| `RoleNamePrefix` | the component's name | Roles are `<prefix>-sluis-serve`, `<prefix>-sluis-github` and `<prefix>-sluis-slack`; each role's managed policy has the role's name. |
| `Serve`, `GitHub`, `Slack` | | A `ProcessArgs`: `ServiceAccount` (empty creates no role; required for `Serve`) and `Description` (the policy's, which IAM cannot change once set). |
| `Storage` | required | `Storage.Grant()`. |
| `SigningKeyArns` | none | The Lambda stack's signing keys (`SigningKeyArn`, `SigningKeyRS256Arn`). Set, the serve role (and only it) may `kms:Sign` and `kms:GetPublicKey` with them. |
| `WrappedSigningKeyArn` | none | The symmetric key of the `kms-wrapped` signing adapter ([Signing on AWS](#signing-on-aws)): the Lambda stack's `WrappedSigningKeyArn`: a key whose policy reserves the signing context to the signing roles (list this role in `WrappedSigning.AdditionalSigningRoleArns`, or in the denial merged into a shared key). Set, the serve role (and only it) may `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` with it, under the conditions below. |
| `State` | none | `State.Grant()`. Nil when the State is on NATS: the roles then carry no DynamoDB grant. |

### Outputs

`ServeRoleArn`, `ServeRoleName`, `GitHubRoleArn`, `GitHubRoleName`,
`SlackRoleArn`, `SlackRoleName`; empty for a role that was not asked for.

### What is created, per process

A customer-managed policy and a role with the same name, the attachment, and one
`PodIdentityAssociation` of the ServiceAccount with the role. The trust policy
lets `pods.eks.amazonaws.com` assume the role (`sts:AssumeRole` and
`sts:TagSession`) only for this account and cluster, this namespace and this one
ServiceAccount. A ServiceAccount takes one association, so two processes on one
ServiceAccount are refused.

### IAM, whole

Every role has the same grants; they are the whole of its policy.

| Sid | Actions | Resource |
|---|---|---|
| `SluisBlobs` | `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject` | the bucket's objects |
| `SluisBlobList` | `s3:ListBucket` | the bucket (a read of an absent key is a 404 only with it, a 403 without) |
| `SluisSigning`, serve only, with `SigningKeyArns` | `kms:Sign`, `kms:GetPublicKey` | the signing keys |
| `SluisWrappedSigning`, serve only, with `WrappedSigningKeyArn` | `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | the symmetric key, only with `kms:EncryptionContext:purpose` = `sluis-signing` and no context keys beside `purpose`, `alg`, `kid` |
| `SluisState`, with State | `dynamodb:GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` | the table |
| `SluisStateKey`, with a table key | `kms:Encrypt`, `kms:Decrypt`, `kms:GenerateDataKey`, `kms:DescribeKey` | the table's key, only through DynamoDB (`kms:ViaService`) |

`Scan` is `sluis migrate` and a listing by a prefix with no dot; `DescribeTable`
is the start-up check and the readiness probe. Nothing is granted on `*`.

## The configuration

`RenderPorts` renders the `ports:` block of the file `serve` and both controllers
read, from names the components were given, so none waits for a resource:

```go
y, _ := sluispulumi.RenderPortsYAML(sluispulumi.PortsArgs{
	BucketName: "acme-kernel-sluis",
	TableName:  "kernel-sluis",
	Region:     "eu-west-1",
})
```

```yaml
ports:
  adapter: dynamodb
  blob:
    adapter: s3
    s3: {bucket: acme-kernel-sluis, region: eu-west-1}
  dynamodb: {region: eu-west-1, table: kernel-sluis}
```

`create` is never rendered: the table is the infrastructure's, and the adapter
binds to it and checks it. Credentials are the platform's. Without `TableName`
no State adapter is written (the schema's default) and only the blob is. Sealing
is retired, so `KeyID` is optional: set, it renders the `sealer:` block of an
installation still on a key of its own, and left empty no sealer is written. The NATS adapter's own block is the identity stack's: `RenderPorts`
refuses it. The tests hold the block to the schemas of all three binaries, so a
key renamed in `schemas/config` fails there.

## Switching the serving pod to its own role

The serving pod gets a role of its own, `<prefix>-sluis-serve`, replacing the
shared `kernel-access-issuer-audit` role (decision N9a). That role also carries
the audit-events writer's grants, which are the audit side's and are not in this
library. What gitops does at the switch:

1. Create the new stack from the library: the roles, the bucket, the key and the
   table are new `sluis` resources. **No state move is needed**: the earlier
   sluis resources in eso-iam are empty and are deleted, not adopted.
2. A ServiceAccount takes one association. Delete the serving
   ServiceAccount's association with the old role in the same apply that creates
   the library's, or the create fails; the pod loses its credentials for the
   moment between them, so do it in a quiet window.
3. Keep the audit events' grants. Attach gitops's own managed policy for them to
   the library's serve role, with a `RolePolicyAttachment` on `ServeRoleName`,
   or give the audit writer another path: the library neither carries nor
   removes them.
4. Retire `kernel-access-issuer-audit` and its policy when the last object under
   its prefix has expired.

## Lambda

`NewLambda` is the Lambda shape: **three functions from one zip**, a role each,
the HTTP API in front of one of them, the token-signing key, and a schedule per
controller target.

```go
l, _ := sluispulumi.NewLambda(ctx, "access", &sluispulumi.LambdaArgs{
	Region: "eu-central-1", AccountID: accountID,
	Package:        "dist/sluis-lambda_1.58.0_linux_arm64.zip", // or an https URL
	Config:         sluisYAML,   // config/sluis.yaml: the http function's `serve` file
	GitHubConfig:   githubYAML,  // config/github.yaml
	SlackConfig:    slackYAML,   // config/slack.yaml
	CataloguePaths: []string{"catalogues/github-apps.yaml"},
	Storage:        store.Grant(),
	State:          state.Grant(),
	AuditQueueArn:  audit.QueueArn,
	API: sluispulumi.APIArgs{
		DomainName:           "access.example.test",
		CertificateArn:       originCertArn,  // an ACM certificate the caller supplies
		TruststorePEM:        cloudflareOriginPullCA,
		TruststoreBucketName: "acme-sluis-truststore",
	},
	Schedule: sluispulumi.ScheduleArgs{GitHubOrgs: []string{"acme"}, SlackWorkspaces: []string{"T0ACME"}},
}, pulumi.Providers(awsProvider))
```

### The functions

`sluis-http`, `sluis-github` and `sluis-slack` (`FunctionNamePrefix`, default
`sluis`) are one `bootstrap` on `provided.al2023`, arm64, **in no VPC**. The role
a function plays is its environment: `SLUIS_ROLE` is `http`, `github` or `slack`,
and `SLUIS_CONFIG_FILE` is its own file: `/var/task/config/sluis.yaml` (http),
`/var/task/config/github.yaml` or `/var/task/config/slack.yaml`. `Env` and each
function's `Env` add to these; the library owns those two.

**The package** is the released `sluis-lambda_<version>_linux_arm64.zip` (with
`bootstrap` at its root), read from a path or an https URL when the stack is
evaluated, and `PackageSHA256` pins it. The library adds the three configuration files, `Config`, `GitHubConfig` and
`SlackConfig`, at `config/sluis.yaml`, `config/github.yaml` and
`config/slack.yaml`, and each catalogue at `config/<name>` (`Catalogues` by name,
`CataloguePaths` by file, merged under their base names; a name in both with other
content, an empty file and a name that is a path are refused before anything is
created). The added files are part of the package: **a change to the
configuration or to a catalogue changes the package and redeploys the three
functions on the next `pulumi up`**, deliberately and never silently. The file is
not a secret: secrets are SSM parameters, below.

### Inputs (`LambdaArgs`)

| Field | Default | Meaning |
|---|---|---|
| `Region`, `AccountID` | required | Name the SSM parameters and the other functions in the roles' policies. |
| `Package`, `PackageSHA256` | required, none | The released zip; its expected digest. |
| `Config`, `GitHubConfig`, `SlackConfig` | required | The three configuration files (`serve`, `controller-github`, `controller-slack`). The http file's `adapters.trigger.settings` must name the controllers, `github: <prefix>-github` and `slack: <prefix>-slack`: the functions take no environment variable for it. |
| `Catalogues`, `CataloguePaths` | none | Catalogue files at `config/<name>`. |
| `Storage`, `State` | required | `Storage.Grant()` and `State.Grant()`. |
| `AuditQueueArn` | required | The audit stack's ingest queue. |
| `ParameterKeyArn` | none | A customer-managed key the SecureString parameters use. Absent, the AWS-managed key, which needs no grant. Present, each role may use it through SSM only. |
| `SigningKeyAlias` | `alias/sluis-signing` | The ES384 signing key's alias. |
| `SigningKeyRS256Alias`, `DisableSigningKeyRS256` | `alias/sluis-signing-rs256`, false | The RSA signing key's alias; the key is created unless disabled. |
| `WrappedSigning` | nil | Signing with the `kms-wrapped` adapter ([Signing on AWS](#signing-on-aws)): `KeyArn` (an existing symmetric key; unset creates one), `KeyAlias` (default `alias/sluis-signing-wrapped`), `KeepRemoteSigningKeys` (also create the two asymmetric keys and their grant). Set, the two asymmetric keys are not created unless kept. |
| `FunctionNamePrefix` | `sluis` | `<prefix>-http`, `-github`, `-slack`, and `<prefix>-scheduler`. |
| `HTTP`, `GitHub`, `Slack` | 512 MB; 30 s for http, 300 s for a controller | A `FunctionArgs`: `MemoryMB`, `TimeoutSeconds`, `Env`. |
| `Env` | none | On all three functions. |
| `LogRetentionDays` | 30 | Each function's log group. |
| `PermissionsBoundaryArn` | none | The boundary of every role. |
| `API.DomainName`, `API.CertificateArn` | required | The custom domain and the ACM certificate for it, in the region (the caller supplies it, for example a Cloudflare Origin CA certificate imported to ACM). |
| `API.TruststorePEM`, `API.TruststoreBucketName` | required | The client-CA bundle for mutual TLS, and the bucket the library uploads it to. |
| `API.KeepDefaultEndpoint` | false | Leaves the default `execute-api` endpoint on, for the cutover's acceptance suite to run against `ApiUrl` before the DNS switch. Turn it off again after: it is a way round the client certificate. |
| `Schedule.GitHubOrgs`, `Schedule.SlackWorkspaces` | none | The targets, one schedule each. |
| `Schedule.Rate` | `rate(5 minutes)` | The EventBridge Scheduler expression. |
| `Exports.Function`, `Exports.Rate`, `Exports.Disabled` | `http`, `rate(15 minutes)`, false | The exports schedule: which function owns the exports (`http`, so `<prefix>-http`, `github` or `slack`) and how often it is invoked with `{"kind":"exports"}`. |
| `WebIdentityAudience` | any | Restricts the audience of the outbound web identity token the github and slack roles may ask STS for. |
| `HTTP.SecretFiles` (and `GitHub`, `Slack`) | none | SSM parameters written to files under `/tmp/` at cold start, as `SLUIS_SECRET_FILES`. The http function always lists the issuer's state secret at `/tmp/sluis/state-secret`. |
| `Telemetry.LayerArn`, `Telemetry.Env` | nil: no layer | The observability `otlp-lambda` layer and its `OTEL_*` settings. Optional, so an estate whose collector is not ready leaves it out. `OTEL_SERVICE_NAME` is the function's name unless given. |
| `Tags` | none | On everything that takes tags. |

### Outputs

| Output | |
|---|---|
| `SigningKeyArn`, `SigningKeyID`, `SigningKeyAlias` | The ES384 token-signing key. |
| `SigningKeyRS256Arn`, `SigningKeyRS256ID`, `SigningKeyRS256Alias` | The RS256 token-signing key (empty when disabled). |
| `WrappedSigningKeyArn`, `WrappedSigningKeyAlias` | The symmetric key of `WrappedSigning` (`KeyArn` when given; the alias is empty then, and without `WrappedSigning`). |
| `HTTPFunctionArn`, `GitHubFunctionArn`, `SlackFunctionArn` and the `...FunctionName`s | The functions. |
| `HTTPRoleArn`, `GitHubRoleArn`, `SlackRoleArn` and the `...RoleName`s | Their roles. |
| `APIID`, `APIURL` | The HTTP API and its default endpoint (it answers only with `KeepDefaultEndpoint`). |
| `DomainTarget`, `DomainHostedZoneID` | What DNS for the custom domain points at (a CNAME or an alias record). |
| `TruststoreBucketName`, `TruststoreURI` | The client-CA bundle. |
| `SchedulerRoleArn`, `ScheduleNames` | The scheduler's role and the schedules. |
| `StateSecretParameter` | The SSM parameter of the issuer's OAuth-state secret. |
| `ExportReadPolicyJSON` | The policy document a consumer's External Secrets Operator role attaches (below). |

### The API

An HTTP API (payload format 2.0) with a `$default` route to `sluis-http`, behind
a regional custom domain (TLS 1.2 or later) with **mutual TLS**: the truststore
is `TruststorePEM` in a bucket of its own (versioned, encrypted, closed to the
public and to plain HTTP, protected), not in the blob bucket, because the
functions can write that one and a function that can replace the client CA has
no use for a client certificate. The domain names the object's version, so a
new PEM redeploys it. This is Cloudflare's authenticated origin pulls: the PEM is
Cloudflare's origin-pull CA. The default endpoint is disabled unless
`KeepDefaultEndpoint` is set.

### The schedules

One EventBridge schedule per target, `<prefix>-github-<org>` and
`<prefix>-slack-<workspace>`, each invoking the github or the slack function with
`{"kind":"tick","target":"<id>"}`. A target is letters, digits and `- _ . :`, at
most 40 (`github:links` is the link check; a colon is a `-` in the schedule's name). The scheduler has a role of its own, `<prefix>-scheduler`, that may
invoke those two functions and nothing else; no schedule and no controller
function retries, because the next tick runs the pass again.

### The exports schedule

`<prefix>-exports` invokes the function that owns the exports (`Exports.Function`,
default `http`, that is `sluis-http`) with `{"kind":"exports"}`, every
`Exports.Rate` (default 15 minutes), through the same scheduler role, which may
invoke that function too. Whether the function understands the event is the
app's: the app side lands separately. The controllers' outbound web identity
needs outbound identity federation enabled in the account (STS answers
`OutboundWebIdentityFederationDisabled` otherwise); the library does not enable
it.

### The environment the app reads

Per function the library sets `SLUIS_ROLE`, `SLUIS_CONFIG_FILE` (that function's
own file) and `SLUIS_SECRET_FILES`, a JSON array of `{"parameter","path"}` with
paths under `/tmp/` (SSM parameters under `/sluis/private/` or `/sluis/export/`
written to files at cold start; the http function's list always has the state
secret). `Env` adds the rest, including `<NAME>=ssm:/sluis/private/...` mappings
and `OTEL_*`. The library owns `SLUIS_SECRET_FILES` and refuses it in `Env`. The
http file names the controllers in `adapters.trigger.settings` (`github:
<prefix>-github`, `slack: <prefix>-slack`).

### IAM: one role per function

Each function has a role and an inline policy named after it, and nothing is
granted on `*`.

| Grant | http | github | slack |
|---|---|---|---|
| Logs: `logs:CreateLogStream`, `logs:PutLogEvents` on its own log group | yes | yes | yes |
| S3: `GetObject`, `PutObject`, `DeleteObject` on the bucket's objects; `ListBucket` on the bucket | yes | yes | yes |
| DynamoDB: `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` on the table (and its key, through DynamoDB only) | yes | yes | yes |
| SSM: `GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter` under `/sluis/private/*` | yes | yes | yes |
| SSM: `PutParameter`, `DeleteParameter` under `/sluis/export/*` | yes | yes | yes |
| `kms:Encrypt`, `Decrypt`, `GenerateDataKey` on `ParameterKeyArn`, through SSM only (with the key) | yes | yes | yes |
| `sqs:SendMessage` on the audit ingest queue | yes | yes | yes |
| `kms:Sign`, `kms:GetPublicKey` on both signing keys (remote signing; not created with `WrappedSigning`) | **yes** | no | no |
| `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` on the symmetric key, with the conditions in [Signing on AWS](#signing-on-aws) (`WrappedSigning`) | **yes** | no | no |
| `lambda:InvokeFunction` on the github and slack functions ("run a pass now") | **yes** | no | no |
| `sts:GetWebIdentityToken` (on `*`: the action takes no resource; with `WebIdentityAudience`, only for that audience), so a controller authenticates to the console with its role's outbound token | no | **yes** | **yes** |

With remote signing (no `WrappedSigning`) both estates sign with two keys:
`ECC_NIST_P384` (ES384) and `RSA_3072` (RS256), each usage `SIGN_VERIFY`,
protected, with a 30-day deletion window and AWS's default key policy, so the
http role's policy is what grants its use. With `WrappedSigning` there is one
symmetric key instead (below). The roles carry a permissions boundary when
`PermissionsBoundaryArn` is set.

### Signing on AWS

The aws-serverless and aws-hybrid presets sign tokens with the `kms-wrapped`
adapter; `kms` (remote signing) stays selectable, and aws-eks keeps it.

| | `kms` (remote) | `kms-wrapped` |
|---|---|---|
| Keys | one asymmetric KMS key per algorithm, provisioned by hand or by this library | one symmetric "application" key per estate; the issuer generates the key pairs |
| Signing | one `kms:Sign` per token; the private key never leaves KMS | local, with a private key decrypted into the process's memory (`kms:Decrypt` once per key per process) |
| If the signing role leaks | the holder can sign only while it holds the role (every signature is in CloudTrail) | the holder can decrypt the wrapped keys in the State and sign **offline, with no further trace**, for as long as those keys are published |
| Throughput | the account's `Sign` request quota | none from KMS |
| Rotation | append a key by hand | automatic, every `rotateEvery` (default 24h), free |

The trade is deliberate, and it is larger than "until rotation": a wrapped key
is extractable by whoever may call `kms:Decrypt` on the key with the signing
context, and the ciphertext and that context are in the State. So:

- the **State's write access is part of the trust boundary**. Whoever can write
  the key ring items (`keyring`, `keyring-index`, `keyring-retired`, and the
  `lease` `signing-keygen/<alg>`) can plant a public key into the JWKS, retire a
  key early, or stop rotation, so that one key stays active and published
  indefinitely; rotation is not a bound on a compromise that includes the State;
- a signing key a leaked role has decrypted stays valid for as long as it is
  published: its life as the active key plus `retain` after it (about one
  rotation period plus the retention), **only if** the State is not also written
  to keep it;
- the use of the key is **reserved** to the signing roles by the key policy
  (below), because the root delegation would otherwise let every role with
  `kms:Decrypt` on it, a CI role or a PowerUser, read a wrapped key out of the
  State and forge with no trace but a CloudTrail `Decrypt`.

A remote key is not extractable at all. Choose `kms` where that matters more than
rotation and quota.

**Own key or shared key.** The key is either the one the library creates (only
sluis uses it) or an existing symmetric key you pass as `WrappedSigning.KeyArn`,
which other workloads may share (an auto-unseal key, a secrets-provider key).
Both are first-class. A shared key is safe only with the reserved-context
denial below in its key policy.

**How it works.** For each algorithm (ES384 on `ECC_NIST_P384`, RS256 on
`RSA_3072`) the issuer calls `kms:GenerateDataKeyPairWithoutPlaintext` on the
application key and records the public key and the private key *encrypted under
it* in the key ring of the State port (`issuer:keyring:entry:<alg>:<kid>`; the
private half is the `wrapped` field and is never plaintext). To sign, a process
calls `kms:Decrypt`, keeps the key in memory and signs locally. The `kid` is a
random 128-bit id chosen before the pair is generated, and the encryption
context is `{"purpose": "sluis-signing", "alg": "<alg>", "kid": "<kid>"}`, on
both calls: a ciphertext moved under another kid or algorithm does not decrypt.

**Rotation.** A new pair per algorithm every `rotateEvery`, generated under the
State lease `lease.signing-keygen:<alg>` (a replica that finds it held does
nothing; a lost race costs one more published key, never a wrong token). The new
key is published in the JWKS at once and signs only after `prepublish`; a
replaced key stays published for `retain`. The defaults: `rotateEvery` 24h,
`prepublish` 15m (the issuer sends the JWKS with no `Cache-Control`, and Envoy's
`jwt_authn` caches a key set for 10 minutes and does not refetch on an unknown
`kid`, so a key must be published for longer than that before it signs),
`retain` = `lifetimes.token` + 5m (a token is valid for at most `lifetimes.token`
after it was signed, plus clock skew). The settings are checked at start:
`retain` at least that, `rotateEvery` longer than `prepublish` and at most 168h.
A process looks for work at most every `pollInterval` (30s), on a request, and
on Kubernetes also on a timer, so a Lambda with no traffic rotates at its next
request. The first key of an estate, and the first wrapped key beside keys of
another source (a migrated file or KMS key, which stay published until they
retire), is active at once: there is nothing wrapped to wait behind.

**Algorithms.** `ES384` and `RS256` (the one EKS's OIDC provider and Kargo need).
EdDSA is not supported yet: KMS can generate `ECC_NIST_EDWARDS25519` pairs and
go-jose signs EdDSA, but the policy's `signing_alg`, the issuer's verifiers and
discovery know only RSA and ECDSA; it is refused with a message that says so.

**IAM and key policy.** The http function's role (only it) gets, on the key and
nowhere else:

```json
{
  "Sid": "SluisWrappedSigning", "Effect": "Allow",
  "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
  "Resource": "<the key's ARN>",
  "Condition": {
    "StringEquals": {"kms:EncryptionContext:purpose": "sluis-signing"},
    "ForAllValues:StringEquals": {"kms:EncryptionContextKeys": ["purpose", "alg", "kid"]}
  }
}
```

`WrappedSigning.KeyArn` takes an existing, possibly shared, key. The library then
leaves its key policy alone, and **the estate MUST merge the reserved-context
denial below into it** (`sluispulumi.WrappedKeyPolicyStatements(roleArns)` returns
it, `WrappedKeyReservedDeny` as a map). Unset, the library creates the key (`SYMMETRIC_DEFAULT`, `ENCRYPT_DECRYPT`, rotation
enabled, protected, alias `WrappedSigning.KeyAlias`, default
`alias/sluis-signing-wrapped`) with this key policy, so a broader policy attached
to the role later does not widen what it can do with the key:

| Sid | Effect | Principal | Action | Condition |
|---|---|---|---|---|
| `EnableIAMPolicies` | Allow | the account root | `kms:*` | none (IAM policies govern the key, and key administration is the account's) |
| `SluisSigningContextReserved` | Deny | `*` | `kms:Decrypt`, `kms:Encrypt`, `kms:ReEncrypt*`, `kms:GenerateDataKey*`, `kms:CreateGrant` | `kms:EncryptionContext:purpose` is `sluis-signing`, and `aws:PrincipalArn` is **not** one of the signing roles (the http role, and `WrappedSigning.AdditionalSigningRoleArns`, the Kubernetes serve role) |
| `SluisSigningRolePurposeOnly` | Deny | `*` | `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | `aws:PrincipalArn` is a signing role, and `kms:EncryptionContext:purpose` is not `sluis-signing` |
| `SluisSigningRoleContextKeysOnly` | Deny | `*` | the same two | a signing role, and `ForAnyValue:StringNotEquals kms:EncryptionContextKeys` `["purpose","alg","kid"]` |
| `SluisSigningRoleNothingElse` | Deny | `*` | `NotAction` the same two | a signing role |

The roles are named in a condition, not as principals, so the key can exist
before the roles.

**MANDATORY when `KeyArn` is shared:** merge these four statements into the key's
policy, beside whatever it already has (the library puts the same four in the key
it creates; `sluispulumi.WrappedKeyPolicyStatements(roleArns)` returns them):

```json
[
  {
    "Sid": "SluisSigningContextReserved", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"],
    "Condition": {
      "StringEquals": {"kms:EncryptionContext:purpose": "sluis-signing"},
      "ArnNotEquals": {"aws:PrincipalArn": ["<the http role's ARN>", "<the Kubernetes serve role's ARN, if it signs>"]}
    }
  },
  {
    "Sid": "SluisSigningRolePurposeOnly", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {
      "ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]},
      "StringNotEquals": {"kms:EncryptionContext:purpose": "sluis-signing"}
    }
  },
  {
    "Sid": "SluisSigningRoleContextKeysOnly", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {
      "ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]},
      "ForAnyValue:StringNotEquals": {"kms:EncryptionContextKeys": ["purpose", "alg", "kid"]}
    }
  },
  {
    "Sid": "SluisSigningRoleNothingElse", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "NotAction": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {"ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]}}
  }
]
```

The last three name only the signing roles, so they confine those roles to the two
calls and the one context on this key and touch no other principal. (`Encrypt`
and `GenerateDataKey*` are in the first so that no other principal can mint a
ciphertext of a key it chose under the signing context.)

**Trust boundary of a shared key.** A principal with `kms:PutKeyPolicy` on the
key (its OpenBao or Pulumi administrators) can remove these statements, so they
are inside the signing trust boundary. **A multi-Region key** needs the
statements in the key policy of EVERY replica: a wrapped key made with the
primary decrypts on any replica, and each replica's policy is its own. The
library accepts a `KeyArn` that names an `mrk-` key and does not touch any
replica's policy.

Why this is sufficient on a shared key: a wrapped signing key's ciphertext is
bound to its encryption context, so decrypting it needs a request that presents
`purpose=sluis-signing` (with the right `alg` and `kid`, which are in the State).
The statement denies every principal but the signing roles any request that
presents that purpose, so no one else can ever unwrap a signing key. The other
users of the key (an unseal, a secrets provider) present no context or different
ones, never `purpose=sluis-signing`, and are untouched. The signing roles'
own denials (above) keep them to that context in the other direction.
**Writes to the key ring.** The github and slack roles always carry (with
remote signing too; the Kubernetes identity's non-serve roles with a State)
`SluisNoKeyringWrites`: a Deny of `PutItem`, `UpdateItem`, `DeleteItem` and
`BatchWriteItem` on the table when `dynamodb:LeadingKeys` is `keyring`,
`keyring-index` or `keyring-retired`. Only the signing roles may write them. The
key-generation lease cannot be denied this way: it shares the `lease` partition
with the controllers' own leases. Anyone else with write access to the table is
inside the trust boundary above.

**Monitoring rotation.** A rotation that keeps failing is a warning per attempt
and the key keeps signing, so alert on it: the issuer logs at ERROR, at most
hourly, once the active wrapped key is older than 1.5 times `rotateEvery`, and
the gauge `access_issuer.signing_key.active_since_timestamp`
(`access_issuer_signing_key_active_since_timestamp_seconds` in the store) gives
the age. Alert on
`time() - max by (algorithm) (access_issuer_signing_key_active_since_timestamp_seconds) > rotateEvery + prepublish + margin`
(26h for the defaults). The chart's `AccessRosterSigningKeyRotationStalled` rule
is this rule, with `alerts.rules.signingKeyRotationStalled.maxAgeSeconds`. Its
default (350 days) is for certificate keys, and the chart cannot see the signing
mode, so for wrapped signing **set it to `rotateEvery + prepublish + margin`**:
`93600` (26h) for the defaults. A Lambda deployment has no chart; paste this into
vmalert (or a PrometheusRule), with the cluster label of your store:

```yaml
- alert: SluisWrappedSigningKeyRotationStalled
  expr: time() - max by (algorithm) (access_issuer_signing_key_active_since_timestamp_seconds) > 93600
  for: 15m
  labels: {severity: warning}
  annotations:
    summary: 'The {{ $labels.algorithm }} signing key has been active for {{ $value | humanizeDuration }}: rotation is failing'
```

**Known limits** (follow-ups, not in this release):

- a process publishes a key it reads from the State without having unwrapped it
  and matched it to its public half (only the active key is unwrapped, by the
  process that signs with it);
- foreign entries (public keys another source recorded) are published with no
  migration allowlist, so whoever can write the State can add one;
- the encryption context carries no creation time, so a wrapped key is not
  refused for being older than `rotateEvery` + `prepublish` + `retain`;
- a process that cannot unwrap the newest key signs with an older one, which can
  be past its `retain` (accepted: signing beats not signing; the log says so);
- the first key of a cutover is active at once, with no pre-publish (accepted: a
  verifier that cached the JWKS before it rejects the new `kid` for up to its
  cache lifetime).

**Moving a stack from remote signing:** set `WrappedSigning` with
`KeepRemoteSigningKeys: true` (the two asymmetric keys are protected and cannot
be dropped from the program in one step), switch the configuration
(`signingKey.kmsWrapped`), and drop the flag once nothing signs remotely.

**Configuration** (the http function's file; `preset: aws-hybrid` or
`aws-serverless` makes `kms-wrapped` the signing adapter, and `signingKey.kmsWrapped`
supplies its settings):

```yaml
preset: aws-hybrid
signingKey:
  kmsWrapped:
    keyId: alias/sluis-signing-wrapped   # or the shared key's ARN
    stateSecretFile: /tmp/sluis/state-secret
    # algorithms: [ES384, RS256]         # the first is the default
    # rotateEvery: 24h
    # prepublish: 15m
    # retain: 65m
```

`stateSecretFile` is as for `signingKey.kms`: the sign-in state is derived from
it, not from a key that is replaced daily. The adapter can also be named in
`adapters.signing` (`adapter: kms-wrapped`), with the same settings under
`settings:`.

### The issuer's state secret

With KMS signing the issuer still needs a secret to HMAC-sign OAuth flow state.
The library generates it: a `random.RandomBytes` of 32 bytes (no keepers, so an
apply never rotates it), stored base64 as the SecureString
`/sluis/private/config/issuer/state-secret` (under `ParameterKeyArn` when set), secret
in state and in `pulumi up`'s output. `StateSecretParameter` is its name. Only
`sluis-http` needs it, and it already reads `/sluis/private/*`; the function's
`Env` maps it with `<NAME>=ssm:/sluis/private/config/issuer/state-secret`. Rotating it
is `pulumi up --replace` on the `RandomBytes` resource, which signs everyone's
in-flight sign-in out.

The recovery sign-in has its own parameter, `/sluis/private/config/recovery/password`, generated by the library: see [Recovery on Lambda](../operations/recovery-on-lambda.md).

### SSM layout

`/sluis/private/...` is sluis's alone: its own secrets, and what it writes at
run time. `/sluis/export/...` is for consumers. `ExportReadPolicyJSON` (and
`ExportReadPolicy(region, account, parameterKeyArn)`, which renders the same
document) grants `ssm:GetParameter`, `GetParameters` and `GetParametersByPath` on
`/sluis/export/*` and, with a customer-managed key, `kms:Decrypt` on it through
SSM; attach it to a consumer's External Secrets Operator role. It grants nothing
under `/sluis/private`.

## Releasing

The release workflow tags the library `deploy/pulumi/vX.Y.Z` at the release
commit, beside the `vX.Y.Z` that triggered it, which is how
`go get github.com/truvity/sluis/deploy/pulumi@vX.Y.Z` finds it. The job is
idempotent and treats an annotated or signed tag that peels to the release commit
as already there; a tag at any other commit fails the release.
