# sluis on AWS Lambda

sluis runs as three AWS Lambda functions from one zip. Nothing is in a VPC: every
dependency (DynamoDB, S3, KMS, SSM, SQS, Lambda) is an AWS API the function reaches
over its role, and the only inbound path is an API Gateway HTTP API.

The Kubernetes build is unchanged and stays what kernel and hive run where they run
it. This page is the other platform
([decision 0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)).

## One binary, one zip, three functions

The release attaches `sluis-lambda_<version>_linux_arm64.zip`. It holds one file,
`bootstrap`, at the zip root: an arm64 Linux binary for the `provided.al2023` runtime.
It is about 50 MB, and the zip about 14 MB (for reference, the Kubernetes binary is
about 80 MB). The same zip is deployed as three functions, and the environment
variable `SLUIS_ROLE` chooses what each one is:

| Function | `SLUIS_ROLE` | What it runs | Invoked by |
|---|---|---|---|
| `http` | `http` | The issuer and the console: the same `net/http` mux the Kubernetes server serves | API Gateway HTTP API, payload format 2.0 |
| `github` | `github` | The GitHub roster controller: ONE pass for ONE organisation | EventBridge Scheduler, and an async invoke from `http` |
| `slack` | `slack` | The Slack roster controller: ONE pass for ONE workspace | EventBridge Scheduler, and an async invoke from `http` |

The binary reads `SLUIS_ROLE` once, at cold start. A missing or unknown role stops the
start (an Init error in the platform's terms) and the log line says which.

The zip is the release, **byte for byte**: the Pulumi library deploys the file it
was given, after checking its SHA-256 (`PackageSHA256`, from the release's
checksums), so an installation can show that the code that runs is the code that
was released. What makes the three functions an installation's is not in the
zip but in a configuration layer ([below](#configuration-is-a-layer)).

```mermaid
flowchart LR
    GW[API Gateway HTTP API] -->|payload 2.0| H[http function]
    SCH[EventBridge Scheduler<br/>one schedule per target] -->|tick| G[github function]
    SCH -->|tick| S[slack function]
    H -->|async invoke: run| G
    H -->|async invoke: run| S
    H & G & S --> D[(DynamoDB<br/>state, sessions, leases)]
    H & G & S --> B[(S3 blobs)]
    H -->|kms:Sign| K[KMS]
    H -->|secrets, at cold start and every 5m| P[SSM Parameter Store]
    G & S -->|credentials| P
    L[config layer<br/>/opt/sluis] -.->|read at start| H & G & S
    H & G & S -->|emit| Q[SQS audit]
```

## Events

**`http`** receives an API Gateway HTTP API event, payload format 2.0 (a REST API or an
ALB sends another shape and is refused). The function turns it into an `http.Request`
and runs the handler:

- the method, `rawPath` and `rawQueryString` are the request line, and `host` the Host;
- the headers arrive lower-cased, and the `cookies` array becomes one `Cookie` header;
- a base64 body is decoded;
- the source IP is `RemoteAddr`.

The response carries the status and the headers, with several values of one header
joined by a comma. `Set-Cookie` goes in the response's `cookies`, as the gateway
requires: it drops a header of that name. A body that is not UTF-8 text, or has a
`Content-Encoding`, is base64 with `isBase64Encoded`. A handler that panics answers
500, a request that cannot be read 400, and a response over the platform's 6 MB limit
502 with a log line that says why.

**`github` and `slack`** receive one of two JSON events, which run the same pass:

```json
{"kind":"tick","target":"<id>"}
{"kind":"run","target":"<id>"}
```

`tick` is what an EventBridge Scheduler schedule sends, one schedule per target.
`run` is "run a pass now": the `http` function sends it with an asynchronous invoke
when a console write concerns the target. A target is a GitHub organisation's login
(or `github:links`, the link check) for `github`, and a workspace's key for `slack`.

Each invocation assembles the controller, runs ONE pass of the target under the
target's lease in DynamoDB, closes it (which flushes the audit queue) and returns:

```json
{"kind":"tick","target":"acme","outcome":"ran"}
```

| `outcome` | Meaning |
|---|---|
| `ran` | The pass ran to its end. |
| `contended` | Another invocation holds the target's lease. This one ends cleanly: it returns success, so the platform does not retry it or count an error. The other invocation's pass is the one that counts. |
| `unknown` | A `run` for a target this controller does not run. Only a `run`: see below. |

A failed pass returns an error, which the scheduler's retry policy sees. A `tick` for
a target the controller does not run is also an error: a schedule that names nobody's
target is a mistake to see. A `run` is a hint, and the one for an unknown target ends
cleanly as `unknown`.

The leases are `Create` and `Update` with a TTL on the State port, which is DynamoDB
here, so two invocations (a schedule firing while somebody pressed "run now") cannot
both run. The controller refuses to run its pass when the State is not shared, so a
function that was configured with `memory` fails at its first event and not by
acting twice.

**`http` also receives `{"kind":"exports"}`**, from an EventBridge Scheduler
schedule (15 minutes, on the `sluis-http` function; the infrastructure library's default). The
Kubernetes process keeps the exports current with a loop that also watches the
sources; a function has neither, so each invocation makes every declared export
once (the secrets under `/sluis/<instance>/export/...` and the other targets of the policy document's `exports`),
each under its own lease in DynamoDB, and returns:

```json
{"kind":"exports","outcome":"ran","exports":2,"done":2,"contended":0,"failed":0}
```

A pass is idempotent (a copy of what is already there writes nothing), and two
invocations cannot both make one export. `contended` exports are another
invocation's. `outcome` is `none` when the deployment declares no export. If any
export could not be made the invocation FAILS, so the schedule's retry and an
alarm on the function's errors see a copy that is going stale. A change to a
source is picked up at the next schedule, up to one interval later. Any other
`kind` on `http` is refused.

### Run now

The `http` function's console notifies the `trigger` port when a write concerns a
target. On Lambda that port is the `invoke` adapter: `Notify` is an asynchronous
invoke (`InvocationType: Event`) of the controller function with
`{"kind":"run","target":"<id>"}`, and returns as soon as the platform queues it.
`Subscribe` is unused, since the platform starts the function. The adapter is told
which kind a target is by the policy that declares it (a declared Slack workspace is
`slack`, a bound GitHub organisation is `github`), so one function is invoked. A target
the policy does not declare invokes both, and the one that does not run it ends
as `unknown`.

## Environment

| Variable | Function | Meaning |
|---|---|---|
| `SLUIS_ROLE` | all | `http`, `github` or `slack`. Required. |
| `SLUIS_CONFIG` | all | The function's service document: `/opt/sluis/<role>.yaml` in the configuration layer. The library sets it. |
| `OTEL_*` | all | OpenTelemetry's own, as on Kubernetes. The telemetry layer sets the endpoint. |

That is the whole environment: there is no other variable and no flag, and no
secret, since a document names its secrets and does not hold them. A retired
variable of the old environment configuration (`PORT`, `LOG_LEVEL` and the rest
listed in [configuration](../reference/configuration.md#migrating-from-environment-variables))
stops the start, as it does on Kubernetes, and so do `SLUIS_CONFIG_FILE`,
`SLUIS_SECRET_FILES` and any variable whose value is `ssm:<path>`: they belong to
the v1.61 library, and the error names the v1.62 library to deploy with. The
run-now functions' names are in the document, not in the environment
(`adapters.trigger.settings`).

## Configuration is a layer

The installation's configuration is **not** in the zip. It is one immutable
Lambda layer, `<prefix>-config` (`provided.al2023`, arm64), which the library
publishes from the four documents and mounts **last** in each function's layer
list, so nothing the telemetry layer or another brings can shadow it. Lambda
extracts a layer under `/opt`, and this one holds `sluis/`:

```text
/opt/sluis/http.yaml     the http function's document (a `serve` document, v2)
/opt/sluis/github.yaml   the github function's document (a `controller-github` document, v2)
/opt/sluis/slack.yaml    the slack function's document (a `controller-slack` document, v2)
/opt/sluis/policy.yaml   the policy document every function's `policy.file` names
```

A function's `SLUIS_CONFIG` names its own document, and `policy.file` in each
names `/opt/sluis/policy.yaml`. The documents are the same ones the Kubernetes
build reads ([configuration](../reference/configuration.md#the-configuration-file)),
each validated against its schema at cold start, before the function takes an
event.

Configuration and policy are **immutable for an instance**
([0036](../decisions/0036-configuration-is-immutable-per-instance.md)): a change
publishes a new layer version and updates the functions, and AWS replaces every
instance at once. There are no aliases and no canary: the function stays on
`$LATEST`. Old layer versions are kept, so a rollback is pointing a function at the
previous one. A policy change is a manual `pulumi up`; nothing deploys it on merge.

The library holds each document to sluis's own loader (`config.Load`) **before it
publishes anything**, since a layer outlives the deploy that made it, so a
document the binary would refuse is refused there. Into the documents it writes
what is its own, and refuses a document that says otherwise, naming the key:

- every document: `apiVersion` and `policy.file`;
- the http document: `secrets` (`{source: ssm, root: /sluis/<instance>, region}`),
  `recovery.passwordSecret`, `recovery.enabled` (from `Recovery`) and the state
  secret's name under `signingKey.kms` or `signingKey.kmsWrapped`.

The policy is `Policy` (a document) or `PolicyPath` (a file or a directory of
layers, rendered by sluis's own renderer, the one `sluisctl policy render` runs).
The three service documents are `Config`, `GitHubConfig` and `SlackConfig`.

On Lambda a document selects its adapters by name per concern
([ports](../design/ports.md)):

```yaml
# /opt/sluis/http.yaml (the http function)
apiVersion: sluis.truvity.github.io/serve/v2
platform: { aws: true, runtime: lambda }   # or: preset: aws-serverless
adapters:
  state:   { adapter: dynamodb, settings: { table: sluis } }
  blobs:   { adapter: s3,       settings: { bucket: sluis-blobs, prefix: blobs/ } }
  trigger: { adapter: invoke,   settings: { github: sluis-github, slack: sluis-slack } }
```

`legacy` is refused on the Lambda runtime. The controllers' documents take the
same `platform`, `preset` and `adapters` keys as the service's, so that all three
functions resolve the same table: the state (and so the leases), the blobs and the
audit sink. The audit adapter is `sqs` (`adapters.audit.settings.queueURL`); on Lambda
each record is sent before the call that made it returns, and every function flushes
what is still queued before its invocation returns, since the platform freezes the
process afterwards (a controller does it by closing, which delivers its queue).

### `Instance` and the SSM root

The library's `Instance` argument (required: lower-case letters, digits and dashes)
names the installation (hive: `hive`, Truvity's: `kernel`). Its SSM root is
`/sluis/<instance>` (layout v3), so two installations share an account without
colliding. `private` and `export` may not be used as an instance name: they would
put the root's parameters under another tree, and the root is refused at start.

```text
/sluis/<instance>/private/config/...        what an operator seeds and the stack generates
/sluis/<instance>/private/credentials/...   what sluis writes: its records' credentials
/sluis/<instance>/export/...                what sluis copies out, for consumers
```

### Secrets

A document names its secrets and holds none of them: `signingKey.kmsWrapped.stateSecret:
issuer/state-secret`, `recovery.passwordSecret: recovery/password`,
`oauthClient.provider`, a declared workspace's `keySecret`. The http document's
`secrets` (`source: ssm`, which the library writes) delivers each name from the
SecureString `/sluis/<instance>/private/config/<name>`: at cold start the function
reads **every parameter under that prefix at once**, decrypted and paged, and again
once the five minutes of `secrets.refresh` have passed, so a rotated secret reaches
a running function without a deployment. A missing parameter stops the start naming
the path and never a value. The names are listed in
[configuration](../reference/configuration.md#secrets).

The library generates the two that are its own, in the paths above:

- `config/issuer/state-secret`: base64 of 32 random bytes, read by the http function;
- `config/recovery/password`: 40 random letters and digits with no look-alikes, which
  an operator reads with `aws ssm get-parameter --with-decryption`
  ([Recovery on Lambda](../operations/recovery-on-lambda.md)).

Both keep their values across the upgrade; the parameters are replaced in place by
name, and the library overwrites a value `sluis migrate ssm-layout` copied first
instead of reporting a conflict. What an operator seeds (a Google OAuth client's
`providers/google/<id>/client-id` and `client-secret`, a confidential client's
`clients/<id>/secret`) is `put-parameter` as a SecureString under
`/sluis/<instance>/private/config/`.

## IAM: one role per function

Each function has its own role (decision D5a), only `http` can sign a token, and
every SSM grant is under `/sluis/<instance>/`:

| Permission | `http` | `github` | `slack` |
|---|:-:|:-:|:-:|
| `kms:Sign`, `kms:GetPublicKey` on the token-signing key (remote signing), or `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` on the symmetric key under the context `purpose=sluis-signing` (`kms-wrapped`) | yes | no | no |
| `dynamodb:GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query` on the table | yes | yes | yes |
| `s3:GetObject`, `PutObject`, `DeleteObject`, `ListBucket` on the blob bucket and prefix | yes | yes | yes |
| `ssm:GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter` on `<root>/private/credentials/*` and `<root>/export/*`, for the secrets adapter (`ssm`) that keeps the service's credentials and exports | yes | yes | yes |
| `ssm:GetParameter`, `GetParameters`, `GetParametersByPath` on `<root>/private/config/*`: the secrets its document names, read by path, never written | yes | **denied** | **denied** |
| `kms:Decrypt` through SSM on the key encrypting the parameters, when a customer key is set | yes | yes | yes |
| `sqs:SendMessage` on the audit queue | yes | yes | yes |
| `lambda:InvokeFunction` on the `github` and `slack` functions | yes | no | no |
| `sts:GetWebIdentityToken` (the controller's proof to the console, see below) | no | yes | yes |
| `logs:*` as usual | yes | yes | yes |

`<root>` is `/sluis/<instance>`. A controller reads and writes its credentials and
nothing of `private/config`: the recovery password, the state secret, the OAuth
client and the declared clients are the http function's, and an explicit Deny on
`<root>/private/config`, which wins over any Allow, keeps a leaked controller role
from reading, replacing or deleting them. (The v1.61 library allowed a controller `/sluis/private/*`, which let
`GetParametersByPath` reach `config/*` past its Deny; the v1.62 library grants
only `private/credentials` and `export`, and a test asserts that no controller
Allow names a parent of `private/config`.) Every grant ends at the instance, so a
second installation in the account is out of reach of the first.

A consumer of the exports (an External Secrets Operator role) attaches
`ExportReadPolicy(region, account, instance, key)`, which reads `<root>/export/*`
and nothing else. EventBridge Scheduler needs its own role, with
`lambda:InvokeFunction` on the function it schedules.

## How a controller authenticates to the console

A controller reads the console's API (who holds a group, the organisations'
credentials) as a workload. In a cluster that is its projected ServiceAccount
token. On Lambda it is the function role's **outbound web identity token**: the
controller calls `sts:GetWebIdentityToken` for the console's own audience (see below),
presents the token as the bearer of every call, and reuses it for under four
minutes (a warm execution environment reuses it across invocations). The `http`
function's issuer verifies it against the account's published key set, with the
same per-account verifiers token exchange uses, and takes the role as the caller.

The cache is by **age**, not by expiry. The issuer refuses a token whose `iat` is
older than the federation file's `maxAge` (default 5 minutes) whatever its `exp`
says, so the controller requests the shortest lifetime it can reuse (5 minutes,
STS's default; AWS allows from 1 minute to 1 hour) and mints a new token once the
old one is four minutes old. If you raise `maxAge`, the four minutes stay safe.

### Two audiences, two doors

An AWS role's token is a proof at two doors, and each has its own audience, so a
token minted for one is no proof at the other:

| Door | Audience | Set in |
|---|---|---|
| Token exchange (`/token`) | `exchange.aws.audience` of the policy document | the policy document |
| The console's API (a controller's bearer) | `console.awsAudience`, default `<issuerURL>/console` | the `http` document |

The controllers request the console audience: `console.auth.aws.audience` in the
`github` and `slack` documents must equal the issuer's `console.awsAudience`.
The issuer refuses to start with the same value for both doors.

```yaml
# the http document
console:
  awsAudience: https://sluis.example/console   # optional: this is the default
```

```yaml
# the policy document
exchange:
  aws:
    audience: sluis-exchange
    accounts: [...]
```

```yaml
# the github and slack documents
console:
  auth:
    aws:
      audience: https://sluis.example/console  # = the issuer's console.awsAudience
```

Absent, the controller reads `tokenFile`, as on Kubernetes. Three things must agree:

1. **IAM.** The `github` and `slack` roles are allowed `sts:GetWebIdentityToken`
   (the account has outbound identity federation enabled), and the permission can be
   held to the console audience with the condition key
   `sts:IdentityTokenAudience`:

   ```json
   {"Effect":"Allow","Action":"sts:GetWebIdentityToken","Resource":"*",
    "Condition":{"ForAnyValue:StringEquals":{"sts:IdentityTokenAudience":"https://sluis.example/console"}}}
   ```

   So a controller role can mint a console bearer and nothing for token exchange.
   Only a role that is meant to exchange gets the exchange audience.
2. **The issuer.** The policy document's `exchange.aws.accounts` lists the account
   (`issuer` from `aws iam get-outbound-web-identity-federation-info`). The console
   door uses the same accounts with `console.awsAudience`.
3. **The policy.** The role is entitled to what the policy's `aws` matchers say,
   and to nothing without one. Declare the controllers as viewers (enough to read
   who holds a group):

```yaml
groups:
  all:sluis:viewer:
    matchers:
      - aws: { account: "111122223333", role: sluis-github }
      - aws: { account: "111122223333", role: sluis-slack }
```

A role the policy does not name is nobody at the console, however well its token
verifies. An `aws` matcher with no `role`, or a bare `*`, admits **every** role of
its account, including roles created later. It is not refused (a policy may rely on
it), but the issuer logs a warning at start naming the groups that have one: name
the role unless that is meant.

## Version coupling

The binary and the Pulumi library move together. A binary of 1.62 refuses the v1.61
library's environment (`SLUIS_CONFIG_FILE`, `SLUIS_SECRET_FILES`, `ssm:` values) and
will not start on a function deployed that way; the library refuses a package older
than its own minor (read from `sluis-lambda_<version>_linux_<arch>.zip`, or from
`PackageVersion`), because an older binary cannot read the configuration layer. So a
function is on binary 1.62 **and** library 1.62, never one without the other, and the
library's module (`deploy/pulumi`, which requires the root module at the same
version) is pinned to the same release.

## Moving an installation to v1.62

Deploy the library and the binary in one apply, after the secrets are where the
new documents will look for them:

1. **Copy the configuration secrets** to the instance's root, under the names the
   documents give them. The old parameters stay until the end:

   ```sh
   sluis migrate ssm-layout --to-root /sluis/<instance> --dry-run
   sluis migrate ssm-layout --to-root /sluis/<instance>
   ```

   It renames `oauth/client-id` and `client-secret` to
   `providers/google/default/client-id` and `client-secret` and `clients/<id>` to
   `clients/<id>/secret`; the report is JSON and names, never a value.
2. **Copy the credentials**, which are the Secrets port's, with `sluis migrate`
   from a document whose Secrets adapter names `/sluis` to one whose names
   `/sluis/<instance>` ([migrate](../operations/migrate.md)). The exports are
   written again by the next exports pass.
3. **Unprotect the old asymmetric signing keys**, when the installation signs with
   `WrappedSigning`. The library no longer declares the two asymmetric keys, and
   they are protected in the stack, so removing them is refused until
   `pulumi state unprotect` has been run on `<name>-signing-key` and
   `<name>-signing-key-rs256`. The apply then schedules their deletion, with
   KMS's 30 days of grace. Do it only when no verifier needs a token they signed.
4. **Apply** the 1.62 library with the release zip, its `PackageSHA256`, `Instance`
   and the documents.
5. **Delete the v2 parameters** under `/sluis/private` and `/sluis/export` once the
   installation runs on v3.

## Cold start, and what is not here

- The configuration is the layer's, read once; the secrets are read from SSM at cold start
  and again every five minutes. The state, sessions, leases and the target's reports
  are in DynamoDB and S3, so no invocation depends on another's memory. A sign-in's half-finished state is in the
  session records, not the process.
- The directory snapshot is refreshed on read when it is stale, as the hub already
  does. The Kubernetes process also refreshes it on a timer; a function has no process
  between invocations to keep one in, and a refresh a request started is finished
  before the response is returned to the platform.
- `http` is one assembled service per execution environment, kept across
  invocations. A controller is assembled per invocation, as `sluis tick` does.
- The exports' runner, a background loop, is not started: the `exports` event makes the copies on a schedule instead.

## Telemetry

The only telemetry layer is `truvity/observability`'s OTLP Lambda layer
(`otlp-lambda-layer_<version>_linux_<arch>.zip` in its releases, documented in
[its integration page](https://github.com/truvity/observability/blob/master/docs/integrations/aws-lambda.md)).
It runs the OTLP proxy on the loopback address, authenticates with the function role's
identity, and sends the platform's own logs. sluis ships no layer and no extension:
the one this repository used to build (`sluis-lambda-layer`, installed as
`extensions/access-roster-otlp`) was removed once nothing deployed it. Set the layer's
`OTEL_EXPORTER_OTLP_ENDPOINT` as the layer's page says; the function exports metrics
and traces with the OpenTelemetry SDK it already has, and flushes them at the end of
every invocation, because the platform freezes the process when one returns.

## Building and checking the root

`cmd/sluis-lambda` is built with `-tags lambda,lambda.norpc`. The `lambda` tag leaves
out the Kubernetes, NATS and Valkey storage the other binary carries (see
`internal/store/store_lambda.go`), and `lambda.norpc` is the Lambda library's own tag
for the `provided` runtimes. `cmd/sluis-lambda/imports_test.go` lists the root's
transitive imports and fails on `k8s.io/`, `github.com/nats-io/`,
`github.com/valkey-io/` and the packages that wrap them, and on a sweep that finds
nothing. It runs in `just test`, so a client-go import three packages down is a red
build and not 30 MB of cold start.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda,lambda.norpc \
  -ldflags '-s -w' -o bootstrap ./cmd/sluis-lambda
```
