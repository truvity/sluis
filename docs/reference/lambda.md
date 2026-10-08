# AWS Lambda: reference

What the Lambda function takes in, reads and may do. sluis runs as ONE function from one zip; the shape and the
reasons are in [sluis on AWS Lambda](../explanation/lambda.md), the library that deploys it in
[the Pulumi library](pulumi-library.md), and moving between releases in the upgrade pages
([v1.63](../how-to/upgrade/v1.63.md), [v1.62](../how-to/upgrade/v1.62.md)). The Kubernetes build is the other platform
([decision 0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)).

## The package

The release attaches `sluis-lambda_<version>_linux_arm64.zip`: one file, `bootstrap`, at the root, an arm64 Linux binary
for the `provided.al2023` runtime (about 50 MB, 14 MB zipped). The zip is deployed as ONE function (v1.63;
[decision 0037](../decisions/0037-one-process-everywhere.md)). The Pulumi library checks its SHA-256
(`PackageSHA256`) and deploys it unchanged.

## Events

The function takes four kinds of event:

| Event | What it runs | Sent by |
|---|---|---|
| An API Gateway HTTP API event, payload format 2.0 | The issuer and the console: the same `net/http` mux the Kubernetes server serves | API Gateway |
| `{"kind":"tick"\|"run","target":"<id>"}` | ONE pass of ONE target, run by the controller the policy says the target belongs to (the GitHub controller's organisations and `github:links`, the Slack controller's workspaces) | EventBridge Scheduler (`tick`), and an async invoke from the console (`run`) |
| `{"kind":"exports"}` | Every declared export, once | EventBridge Scheduler |
| `{"kind":"refresh"}` | One directory refresh | EventBridge Scheduler |

Any other `kind` is refused. The function's timeout is 300 s by default (API Gateway still cuts a request at 30 s) and
its memory is one setting (512 MB); a controller pass shares both with the issuer. `SLUIS_ROLE` is retired: a function
that still sets it is refused at cold start, and the log line says why.

### An API Gateway event

An API Gateway HTTP API event, payload format 2.0 (a REST API or an ALB sends another shape and is refused). The
function turns it into an `http.Request` and runs the handler:

- the method, `rawPath` and `rawQueryString` are the request line, and `host` the Host;
- the headers arrive lower-cased, and the `cookies` array becomes one `Cookie` header;
- a base64 body is decoded;
- the source IP is `RemoteAddr`.

The response carries the status and the headers, with several values of one header joined by a comma. `Set-Cookie`
goes in the response's `cookies`, as the gateway requires: it drops a header of that name. A body that is not UTF-8
text, or has a `Content-Encoding`, is base64 with `isBase64Encoded`. A handler that panics answers 500, a request that
cannot be read 400, and a response over the platform's 6 MB limit 502 with a log line that says why.

### A controller pass

Asked for by one of two JSON events, which run the same pass:

```json
{"kind":"tick","target":"<id>"}
{"kind":"run","target":"<id>"}
```

`tick` is what an EventBridge Scheduler schedule sends, one schedule per target. `run` is "run a pass now" (below). A
target is a GitHub organisation's login (or `github:links`, the link check) for the GitHub controller, and a
workspace's key for the Slack controller.

Each invocation assembles the controller, runs ONE pass of the target under the target's lease in DynamoDB, closes it
(which flushes the audit queue) and returns:

```json
{"kind":"tick","target":"acme","outcome":"ran"}
```

| `outcome` | Meaning |
|---|---|
| `ran` | The pass ran to its end. |
| `contended` | Another invocation holds the target's lease. This one ends cleanly: it returns success, so the platform does not retry it or count an error. The other invocation's pass is the one that counts. |
| `unknown` | A `run` for a target the policy does not declare. Only a `run`. |

A failed pass returns an error, which the scheduler's retry policy sees. A `tick` for a target no controller runs is
also an error: a schedule that names nobody's target is a mistake to see. A `run` is a hint, and the one for an unknown
target ends cleanly as `unknown`.

The leases are `Create` and `Update` with a TTL on the State port, which is DynamoDB here, so two invocations (a
schedule firing while somebody pressed "run now") cannot both run. The controller refuses to run its pass when the
State is not shared, so a function configured with `memory` fails at its first event and not by acting twice.

### Exports

`{"kind":"exports"}` comes from an EventBridge Scheduler schedule (15 minutes by default, `Exports.Rate`). Each
invocation makes every declared export once (the secrets under `/sluis/<instance>/export/...` and the other targets of
the policy document's `exports`), each under its own lease in DynamoDB, and returns:

```json
{"kind":"exports","outcome":"ran","exports":2,"done":2,"contended":0,"failed":0}
```

A pass is idempotent (a copy of what is already there writes nothing). `contended` exports are another invocation's.
`outcome` is `none` when the deployment declares no export. If any export could not be made the invocation FAILS, so
the schedule's retry and an alarm on the function's errors see a copy that is going stale. A change to a source is
picked up at the next schedule, up to one interval later.

### Directory refresh

`{"kind":"refresh"}` takes a new snapshot of every connected directory, under the refresh lease (15 minutes by default,
`DirectoryRefresh.Rate`). A request that finds a snapshot due refreshes it too, so the schedule keeps it warm.
Source: `internal/lambdaapp/events.go`.

### Run now

The console notifies the `trigger` port when a write concerns a target. On Lambda that port is the `invoke` adapter:
`Notify` is an asynchronous invoke (`InvocationType: Event`) of the function itself with
`{"kind":"run","target":"<id>"}`, and returns as soon as the platform queues it. `Subscribe` is unused, since the
platform starts the function. Both `github` and `slack` settings of the adapter name this one function, and the Pulumi
library writes them. The function runs the pass under the controller the policy says the target belongs to (a declared
Slack workspace is `slack`, a bound GitHub organisation is `github`).

## Environment

| Variable | Meaning |
|---|---|
| `SLUIS_CONFIG` | The service document: `/opt/sluis/sluis.yaml` in the configuration layer. The library sets it. |
| `OTEL_*` | OpenTelemetry's own, as on Kubernetes. The telemetry layer sets the endpoint. |

That is the whole environment: there is no other variable and no flag, and no secret, since a document names its
secrets and does not hold them. A retired variable of the old environment configuration (`PORT`, `LOG_LEVEL` and the
rest listed in [configuration](configuration.md#retired-environment-variables)) stops the start, as it does on
Kubernetes, and so do `SLUIS_CONFIG_FILE`, `SLUIS_SECRET_FILES` and any variable whose value is `ssm:<path>`: they belong
to the v1.61 library, and the error names the v1.62 library to deploy with. The run-now function's name is in the
document, not in the environment (`adapters.trigger.settings`).

## Configuration is a layer

The installation's configuration is **not** in the zip. It is one immutable Lambda layer, `<prefix>-config`
(`provided.al2023`, arm64), which the library publishes from the two documents and mounts **last** in the function's
layer list, so nothing the telemetry layer or another brings can shadow it. Lambda extracts a layer under `/opt`, and
this one holds `sluis/`:

```text
/opt/sluis/sluis.yaml               the service document (`sluis/v3`, controllers included)
/opt/sluis/policy.yaml              the policy document `policy.file` names
/opt/sluis/verify-keys/<index>.pem  with `VerifyOnly`, an earlier signer's public keys `signingKey.verifyOnly` names
```

`SLUIS_CONFIG` names the service document, and `policy.file` in it names `/opt/sluis/policy.yaml`. The service document
is the one the Kubernetes build reads ([configuration](configuration.md#the-service-document)), validated
against its schema at cold start, before the function takes an event.

Configuration and policy are **immutable for an instance**
([0036](../decisions/0036-configuration-is-immutable-per-instance.md)): a change publishes a new layer version and
updates the function, and AWS replaces every instance at once. There are no aliases and no canary: the function stays on
`$LATEST`. Old layer versions are kept, so a rollback is pointing the function at the previous one. A policy change is a
manual `pulumi up`; nothing deploys it on merge.

The library holds each document to sluis's own loader (`config.Load`) **before it publishes anything**, since a layer
outlives the deploy that made it. Into the service document it writes what is its own, and refuses a document that says
otherwise, naming the key:

- `apiVersion` and `policy.file`;
- `secrets` (`{source: ssm, root: /sluis/<instance>, region}`, and `kmsKeyId: <ParameterKeyArn>` when `ParameterKeyArn` is set: the key the function's own writes, its credentials and exports, are encrypted with; a document that names another is refused, the same is accepted), `recovery.passwordSecret`, `recovery.enabled` (from
  `Recovery`) and the state secret's name under `signingKey.kms` or `signingKey.kmsWrapped`;
- with the `invoke` trigger, `adapters.trigger.settings.github` and `.slack`: this function;
- `signingKey.verifyOnly`, from `VerifyOnly`: one entry per key, `file` at `/opt/sluis/verify-keys/<index>.pem`, its
  `kid` and `alg` when given and its `until` in RFC 3339, the entries the Kubernetes shape writes. A document that names
  `signingKey.verifyOnly` itself is refused, since the files it names are not in the layer.

The documents are rendered from an `Installation` (`LambdaArgs.Installation`, see
[the Pulumi library](pulumi-library.md#inputs-lambdaargs)); the deprecated `Config` is the service document and `Policy`
or `PolicyPath` the policy.

On Lambda a document selects its adapters by name per concern ([ports](../explanation/ports.md)):

```yaml
# /opt/sluis/sluis.yaml
apiVersion: sluis.truvity.github.io/sluis/v3
platform: { aws: true, runtime: lambda }   # or: preset: aws-serverless
adapters:
  state:   { adapter: dynamodb, settings: { table: sluis } }
  blobs:   { adapter: s3,       settings: { bucket: sluis-blobs, prefix: blobs/ } }
  trigger: { adapter: invoke,   settings: { github: sluis, slack: sluis } }
```

`legacy` (the Kubernetes-objects store) is refused on the Lambda runtime. The issuer and both controllers share the
document's `platform`, `preset` and `adapters`, so they resolve the same table: the state (and so the leases), the blobs
and the audit sink. The audit adapter is `sqs` (`adapters.audit.settings.queueURL`); on Lambda each record is sent
before the call that made it returns, and the function flushes what is still queued before its invocation returns,
since the platform freezes the process afterwards (a controller does it by closing, which delivers its queue).

### `Instance` and the SSM root

The library's `Instance` argument (required: lower-case letters, digits and dashes) names the installation (for
example `acme` or `prod`). Its SSM root is `/sluis/<instance>` (layout v3), so two installations share an account
without colliding. `private` and `export` may not be used as an instance name: they would put the root's parameters
under another tree, and the root is refused at start. The layout is in
[storage layout](storage-layout.md#ssm-the-ssm-secrets-adapter).

```text
/sluis/<instance>/private/config/...        what an operator seeds and the stack generates
/sluis/<instance>/private/credentials/...   what sluis writes: its records' credentials
/sluis/<instance>/export/...                what sluis copies out, for consumers
```

### Secrets

A document names its secrets and holds none of them: `signingKey.kmsWrapped.stateSecret: issuer/state-secret`,
`recovery.passwordSecret: recovery/password`, `oauthClient.provider`, a declared workspace's `keySecret`. The
document's `secrets` (`source: ssm`, which the library writes) delivers each name from the SecureString
`/sluis/<instance>/private/config/<name>`: at cold start the function reads **every parameter under that prefix at
once**, decrypted and paged, and again once the five minutes of `secrets.refresh` have passed, so a rotated secret
reaches a running function without a deployment. A missing parameter stops the start naming the path and never a
value. The names are listed in [configuration](secrets.md#the-names).

The library generates the two that are its own:

- `config/issuer/state-secret`: base64 of 32 random bytes (no keepers, so an apply never rotates it), read by the
  function. Rotating it is `pulumi up --replace` on the `RandomBytes` resource, which signs everyone's in-flight
  sign-in out. The output `StateSecretParameter` is its name;
- `config/recovery/password`: 40 random letters and digits with no look-alikes, which an operator reads with
  `aws ssm get-parameter --with-decryption` ([Recovery on Lambda](../how-to/recover-on-lambda.md)).

Both keep their values across an upgrade; the parameters are replaced in place by name, and the library overwrites a
value `sluis migrate ssm-layout` copied first instead of reporting a conflict. What an operator seeds (a Google OAuth
client's `providers/google/<id>/client-id` and `client-secret`, a confidential client's `clients/<id>/secret`) is
`put-parameter` as a SecureString under `/sluis/<instance>/private/config/`.

## IAM: one role

The function has ONE role and an inline policy, both named `<FunctionName>` (decision
[0037](../decisions/0037-one-process-everywhere.md)): the issuer's permissions and the controllers' in one policy. **The
controllers' code runs with the issuer's permissions**, so the per-role isolation of v1.62 is gone by decision. Nothing
is granted on `*` but the one action that takes no resource, `sts:GetWebIdentityToken`. Every SSM grant is under
`/sluis/<instance>/` (`<root>` below), and the IAM test holds that no grant has a `*` but a trailing `/*`. Source:
`deploy/pulumi/policy.go`.

| Grant |
|---|
| Logs: `logs:CreateLogStream`, `logs:PutLogEvents` on its own log group |
| S3: `GetObject`, `PutObject`, `DeleteObject` on the blob bucket's objects (under the prefix); `ListBucket` on the bucket |
| DynamoDB: `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` on the table; with a customer key, its use through DynamoDB only |
| SSM: `GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter` on `<root>/private/credentials/*` and `<root>/export/*`, for the secrets adapter (`ssm`) that keeps the service's credentials and exports |
| SSM: `GetParameter`, `GetParameters`, `GetParametersByPath` on `<root>/private/config/*`: the secrets the document names, read by path, never written |
| `kms:Encrypt`, `Decrypt`, `GenerateDataKey` on `ParameterKeyArn` (the key the parameters the library creates and the ones the function writes use), through SSM only and only for the parameters under the role's own prefixes (when a customer key is set) |
| `sqs:SendMessage` on the audit ingest queue |
| `kms:Sign`, `kms:GetPublicKey` on both signing keys (remote signing; not declared with `WrappedSigning`) |
| `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` on the symmetric key under the context `purpose=sluis-signing` (`WrappedSigning`; the conditions are in [the signing key policy](aws-signing-key.md)) |
| `lambda:InvokeFunction` on the function itself ("run now"), and nothing else |
| `sts:GetWebIdentityToken` (on `*`; with `WebIdentityAudience`, only for that audience): the controllers' proof to the console, see below |

`Scan` is `sluis migrate` and a listing by a prefix with no dot; `DescribeTable` is the start-up check. There are no
explicit denials: the key ring is writable by the one role. Every grant ends at the instance, so a second installation
in the account is out of reach of the first. The role carries a permissions boundary when `PermissionsBoundaryArn` is
set.

A consumer of the exports (an External Secrets Operator role) attaches `ExportReadPolicy(region, account, instance,
key)` (the output `ExportReadPolicyJSON`), which reads `<root>/export/*` and, with a customer-managed key, `kms:Decrypt`
on it through SSM, and nothing else; it grants nothing under `<root>/private`. EventBridge Scheduler needs its own role
(`<prefix>-scheduler`), which the library makes, with `lambda:InvokeFunction` on the one function and nothing else.

## How a controller authenticates to the console

A controller reads the console's API (who holds a group, the organisations' credentials) as a workload. In a cluster
that is the pod's projected ServiceAccount token. On Lambda it is the function role's **outbound web identity token**:
the controller calls `sts:GetWebIdentityToken` for the console's own audience, presents the token as the bearer of every
call, and reuses it for under four minutes (a warm execution environment reuses it across invocations). The same
function's issuer verifies it against the account's published key set, with the same per-account verifiers token
exchange uses, and takes the role as the caller.

The cache is by **age**, not by expiry. The issuer refuses a token whose `iat` is older than the federation file's
`maxAge` (default 5 minutes) whatever its `exp` says, so the controller requests the shortest lifetime it can reuse (5
minutes, STS's default; AWS allows from 1 minute to 1 hour) and mints a new token once the old one is four minutes old.
If you raise `maxAge`, the four minutes stay safe.

### Two audiences, two doors

An AWS role's token is a proof at two doors, and each has its own audience, so a token minted for one is no proof at the
other:

| Door | Audience | Set in |
|---|---|---|
| Token exchange (`/token`) | `exchange.aws.audience` of the policy document | the policy document |
| The console's API (a controller's bearer) | `console.awsAudience`, default `<issuerURL>/console` | the service document |

The controllers request the console audience: `controllers.github.console.auth.aws.audience` and
`controllers.slack.console.auth.aws.audience` must equal the issuer's `console.awsAudience`. The issuer refuses to start
with the same value for both doors.

```yaml
# the service document
console:
  awsAudience: https://sluis.example/console   # optional: this is the default
controllers:
  github:
    console: { auth: { aws: { audience: https://sluis.example/console } } }   # = console.awsAudience
  slack:
    console: { auth: { aws: { audience: https://sluis.example/console } } }
```

```yaml
# the policy document
exchange:
  aws:
    audience: sluis-exchange
    accounts: [...]
```

Absent, the controller reads `tokenFile`, as on Kubernetes. Three things must agree:

1. **IAM.** The function's role is allowed `sts:GetWebIdentityToken` (the account has outbound identity federation
   enabled, which the library does not do; STS answers `OutboundWebIdentityFederationDisabled` otherwise), and the
   permission can be held to the console audience with `sts:IdentityTokenAudience` (`WebIdentityAudience`), so the role
   can mint a console bearer and nothing for token exchange:

   ```json
   {"Effect":"Allow","Action":"sts:GetWebIdentityToken","Resource":"*",
    "Condition":{"ForAllValues:StringEquals":{"sts:IdentityTokenAudience":["https://sluis.example/console"]}}}
   ```

   `ForAllValues` is the operator the key needs (the API takes a list of audiences) and is safe only because STS
   requires an audience on every call. `AdditionalWebIdentityAudiences` appends further exact audiences to that list,
   after the console's, for code in the function that needs its own AWS-minted token, such as an OpenTelemetry layer
   authenticating to a collector through the issuer's token exchange (`exchange.aws.audience`, typically the issuer
   URL). The console audience stays required and first. Empty entries, duplicates (the console audience included) and
   use while `WebIdentityAudience` is empty (any audience) are refused; unset, the policy is unchanged.

   **What this gives up.** The role is then no longer "console bearer only": any code running with the function role
   (the function, its layers and their dependencies) can mint a token for every audience listed. A policy document rule
   that matches the role for an exchange must therefore grant only what that audience's consumer needs.

2. **The issuer.** The policy document's `exchange.aws.accounts` lists the account (`issuer` from
   `aws iam get-outbound-web-identity-federation-info`). The console door uses the same accounts with
   `console.awsAudience`.
3. **The policy.** The role is entitled to what the policy's `aws` matchers say, and to nothing without one. Declare the
   function's role as a viewer (enough to read who holds a group):

   ```yaml
   groups:
     all:sluis:viewer:
       matchers:
         - aws: { account: "111122223333", role: sluis }   # <FunctionName>
   ```

A role the policy does not name is nobody at the console, however well its token verifies. An `aws` matcher with no
`role`, or a bare `*`, admits **every** role of its account, including roles created later. It is not refused (a policy
may rely on it), but the issuer logs a warning at start naming the groups that have one: name the role unless that is
meant.

## Version coupling

The binary and the Pulumi library move together. A binary of 1.63 refuses `SLUIS_ROLE` (and a v2 `controller` document
is not a function's), and a binary of 1.62 refuses the v1.61 library's environment (`SLUIS_CONFIG_FILE`,
`SLUIS_SECRET_FILES`, `ssm:` values). The library refuses a package older than its own minor (read from
`sluis-lambda_<version>_linux_<arch>.zip`, or from `PackageVersion`; `MinPackageVersion` is 1.63), because an older
binary cannot read the configuration layer. So a function is on binary 1.63 **and** library 1.63, never one without the
other. The library's module (`deploy/pulumi`, which requires the root module at the same version) is tagged by the
release with that require pinned automatically ([releasing](../../CONTRIBUTING.md)).

Moving an installation from an earlier release: [v1.63](../how-to/upgrade/v1.63.md) (one function) and
[v1.62](../how-to/upgrade/v1.62.md) (the v3 SSM layout, `Instance`, `WrappedSigning`).

## Telemetry

The only telemetry layer is `truvity/observability`'s OTLP Lambda layer (`otlp-lambda-layer_<version>_linux_<arch>.zip`
in its releases, documented in
[its integration page](https://github.com/truvity/observability/blob/master/docs/integrations/aws-lambda.md)). It runs
the OTLP proxy on the loopback address, authenticates with the function role's identity, and sends the platform's own
logs. sluis ships no layer and no extension: the one this repository used to build was removed once nothing deployed
it. Set the layer's `OTEL_EXPORTER_OTLP_ENDPOINT` as the layer's page says; the function exports metrics and traces with
the OpenTelemetry SDK it already has, and flushes them at the end of every invocation, because the platform freezes the
process when one returns. `Telemetry.Env` takes `OTEL_*`, the layer's own (`ACCESS_ROSTER_*`, `OPENTELEMETRY_*`) and
`AWS_LAMBDA_EXEC_WRAPPER`, and nothing else.

### Platform logs

Lambda's platform logs (`START`, `END`, `REPORT`, init and runtime errors) are the telemetry layer's to send, not
sluis's: sluis ships no extension, and the `ACCESS_ROSTER_PLATFORM_LOGS` switch of the extension this repository once
built went with it. See the layer's page above for what it exports.

## Building and checking the binary

`cmd/sluis-lambda` is built with `-tags lambda,lambda.norpc`. The `lambda` tag leaves out the Kubernetes and Valkey
storage the other binary carries (see `internal/store/store_lambda.go`), and `lambda.norpc` is the Lambda library's own
tag for the `provided` runtimes. `cmd/sluis-lambda/imports_test.go` lists the binary's transitive imports and fails on
`k8s.io/`, the Kubernetes controller runtime, the key-value-store clients and the packages that wrap them, and on a
sweep that finds nothing. It runs in `just test`, so a client-go import three packages down is a red build and not 30 MB
of cold start.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda,lambda.norpc \
  -ldflags '-s -w' -o bootstrap ./cmd/sluis-lambda
```
