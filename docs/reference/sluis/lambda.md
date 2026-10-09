# AWS Lambda

See [sluis on AWS Lambda](../../concepts/sluis/lambda.md) and [the Pulumi library](pulumi-library.md). Decided in [ADR 0026](../../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md), [ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md), [ADR 0037](../../decisions/0037-one-process-everywhere.md).

## Package

| Item | Value |
|---|---|
| Asset | `sluis-lambda_<version>_linux_arm64.zip` |
| Content | One file, `bootstrap`, an arm64 Linux binary for `provided.al2023` (about 50 MB, 14 MB zipped) |
| Deployment | One function. The library checks `PackageSHA256` and deploys it unchanged |
| Build | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda,lambda.norpc -ldflags '-s -w' -o bootstrap ./cmd/sluis-lambda` |
| Minimum | `MinPackageVersion` 1.63; binary and library share a minor ([v1.63](../../guides/sluis/upgrade/v1.63.md)) |
| Import guard | `cmd/sluis-lambda/imports_test.go` fails on `k8s.io/`, the controller runtime and key-value-store clients |

## Events

| Limit | Value |
|---|---|
| Timeout | `Function.TimeoutSeconds`, default 300 s; API Gateway cuts a request at 30 s whatever it says |
| Memory | `Function.MemoryMB`, default 512 MB |
| Other `kind` | Refused |
| Retries | None (`MaximumRetryAttempts: 0`); the next tick runs the pass again |
| State | Must be shared: a pass refuses `memory`, so such a function fails at its first event |
| Source | `internal/lambdaapp/events.go` |

| Event | Runs | Sent by |
|---|---|---|
| API Gateway HTTP API, payload 2.0 | Issuer and console mux. REST API and ALB shapes are refused | API Gateway |
| `{"kind":"tick","target":"<id>"}` | One pass of one target | EventBridge Scheduler, one schedule per target |
| `{"kind":"run","target":"<id>"}` | The same pass, "run now" | The console, through the `invoke` trigger adapter (`InvocationType: Event`) |
| `{"kind":"refresh"}` | One directory refresh under the refresh lease (`DirectoryRefresh.Rate`, 15 minutes) | EventBridge Scheduler |

A target is a GitHub organisation, `github:links` or a Slack workspace. A pass holds its DynamoDB lease and returns `{"kind":"tick","target":"acme","outcome":"ran"}`.

| `outcome` | Meaning |
|---|---|
| `ran` | The pass finished |
| `contended` | Another invocation holds the lease; success, no retry |
| `unknown` | A `run` for an undeclared target |

A failed pass, or a `tick` for an unrun target, returns an error.

| HTTP mapping | Behavior |
|---|---|
| Request | `rawPath`, `rawQueryString`, method, `host`; headers lower-cased; `cookies` joined to `Cookie`; base64 body decoded; source IP is `RemoteAddr` |
| Response | Header values comma-joined; `Set-Cookie` goes in `cookies`; non-UTF-8 or encoded bodies are base64 |
| Failures | Panic 500, unreadable request 400, response over 6 MB 502 |

## Environment

| Variable | Meaning |
|---|---|
| `SLUIS_CONFIG` | Service document, `/opt/sluis/sluis.yaml`. The library sets it |
| `OTEL_*` | OpenTelemetry's own. Use the [OTLP Lambda layer](https://github.com/truvity/observability/blob/master/docs/integrations/aws-lambda.md); `Telemetry.Env` also accepts `OPENTELEMETRY_*`, `ACCESS_ROSTER_*` and `AWS_LAMBDA_EXEC_WRAPPER` |

No other variable, flag or secret exists. The start refuses `SLUIS_ROLE`, `SLUIS_CONFIG_FILE`, `SLUIS_SECRET_FILES`, `ssm:<path>` values and [retired variables](configuration.md#retired-environment-variables).

## Configuration is a layer

One immutable layer, `<prefix>-config`, mounted last. A policy change needs a manual `pulumi up`.

```text
/opt/sluis/sluis.yaml               the service document (`sluis/v3`, controllers included)
/opt/sluis/policy.yaml              the policy document `policy.file` names
/opt/sluis/verify-keys/<index>.pem  with `VerifyOnly`, the public keys `signingKey.verifyOnly` names
```

The library renders both from `LambdaArgs.Installation` ([inputs](pulumi-library.md#inputs-lambdaargs)) and writes these keys. A document that sets them is refused.

| Key | Written value |
|---|---|
| `apiVersion`, `policy.file` | Fixed |
| `secrets` | `{source: ssm, root: /sluis/<instance>, region}`, plus `kmsKeyId: <ParameterKeyArn>` when set. Another `kmsKeyId` is refused |
| `recovery.passwordSecret`, `recovery.enabled` | From `Recovery` |
| State secret name under `signingKey.kms` or `signingKey.kmsWrapped` | Generated |
| `adapters.trigger.settings.github`, `.slack` | With `invoke`: this function |
| `signingKey.verifyOnly` | From `VerifyOnly`: `file` under `/opt/sluis/verify-keys/`, `kid`, `alg`, `until` (RFC 3339) |

```yaml
# /opt/sluis/sluis.yaml
apiVersion: sluis.truvity.github.io/sluis/v3
platform: { aws: true, runtime: lambda }   # or: preset: aws-serverless
adapters:
  state:   { adapter: dynamodb, settings: { table: sluis } }
  blobs:   { adapter: s3,       settings: { bucket: sluis-blobs, prefix: blobs/ } }
  trigger: { adapter: invoke,   settings: { github: "sluis:live", slack: "sluis:live" } }
```

Lambda refuses `legacy` adapters; audit uses `sqs`.

## Instance and the SSM root

`Instance` is lower-case letters, digits and dashes, not `private` or `export`. See [storage layout](storage-layout.md#ssm-the-ssm-secrets-adapter).

```text
/sluis/<instance>/private/config/...        seeded by an operator or generated by the stack
/sluis/<instance>/private/credentials/...   written by sluis
/sluis/<instance>/internal/...              layout v4: sluis only
/sluis/<instance>/external/<kind>/<id>      layout v4: documents consumers read
```

`private/config/` is read at cold start and every `secrets.refresh` (5 minutes). See [secrets](secrets.md#the-names).

| Generated secret | Value | Rotation |
|---|---|---|
| `config/issuer/state-secret` | Base64 of 32 random bytes; output `StateSecretParameter` | `pulumi up --replace` on the `RandomBytes` resource; signs out all in-flight sign-ins. An apply never regenerates it |
| `config/recovery/password` | 40 random letters and digits | Read with `aws ssm get-parameter --with-decryption` ([recovery](../../guides/sluis/operate/recover-on-lambda.md)). An apply never regenerates it |

## IAM: one role

One role and inline policy named `<FunctionName>` serve issuer and controllers. `<root>` is `/sluis/<instance>`; `PermissionsBoundaryArn` sets a boundary.

| Grant | Scope |
|---|---|
| `logs:CreateLogStream`, `logs:PutLogEvents` | Own log group |
| S3 `GetObject`, `PutObject`, `DeleteObject`; `ListBucket` | Blob bucket objects under the prefix; the bucket |
| DynamoDB `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` | The table. `Scan` serves `sluis migrate` and dotless listings |
| SSM `GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter` | `<root>/private/credentials/*`; layout v4 `<root>/internal/credentials/*` and `<root>/external/*`, with `GetParameterHistory` |
| SSM `GetParameter`, `GetParameters`, `GetParametersByPath` | `<root>/private/config/*`, `<root>/internal/config/*`; read only |
| `kms:Encrypt`, `Decrypt`, `GenerateDataKey` | `ParameterKeyArn`, through SSM only, for the role's own parameters |
| `sqs:SendMessage` | The audit ingest queue |
| `kms:Sign`, `kms:GetPublicKey` | Both signing keys; not with `WrappedSigning` |
| `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | The symmetric key under context `purpose=sluis-signing`, with `WrappedSigning` ([signing key policy](aws-signing-key.md)) |
| `lambda:InvokeFunction` | The function itself |
| `sts:GetWebIdentityToken` | `*`; with `WebIdentityAudience`, that audience only |

The library makes `<prefix>-scheduler` with `lambda:InvokeFunction`. Consumers are granted on their own side ([ADR 0041](../../decisions/0041-the-secret-contract.md)).

## How a controller authenticates to the console

A controller presents the role's web identity token as bearer, reused under four minutes. The issuer refuses an `iat` older than `maxAge` (5 minutes).

| Door | Audience | Set in |
|---|---|---|
| Token exchange (`/token`) | `exchange.aws.audience` | Policy document |
| Console API | `console.awsAudience`, default `<issuerURL>/console` | Service document |

Both controllers' `console.auth.aws.audience` must equal `console.awsAudience`.

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

Allow `sts:GetWebIdentityToken` for `WebIdentityAudience` and enable outbound federation:

   ```json
   {"Effect":"Allow","Action":"sts:GetWebIdentityToken","Resource":"*",
    "Condition":{"ForAllValues:StringEquals":{"sts:IdentityTokenAudience":["https://sluis.example/console"]}}}
   ```

List the account in `exchange.aws.accounts` (`aws iam get-outbound-web-identity-federation-info`) and entitle the role:

   ```yaml
   groups:
     all:sluis:viewer:
       matchers:
         - aws: { account: "111122223333", role: sluis }   # <FunctionName>
   ```

| Setting | Behavior |
|---|---|
| `AdditionalWebIdentityAudiences` | Extra exact audiences after the console's. Empty, duplicate, or with empty `WebIdentityAudience`: refused. Any code under the role can mint all listed audiences |
| Same audience for both doors | `console.awsAudience` equal to `exchange.aws.audience`: the issuer refuses to start |
| Outbound federation | Enable it in the account; the library does not. Otherwise STS answers `OutboundWebIdentityFederationDisabled` |
| `aws` matcher without `role`, or `*` | Admits every role of the account. The issuer warns at start |
