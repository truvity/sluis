# AWS Lambda

See [sluis on AWS Lambda](../../concepts/sluis/lambda.md) and [the Pulumi library](pulumi-library.md). Decided in [ADR 0026](../../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md), [ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md), [ADR 0037](../../decisions/0037-one-process-everywhere.md).

## Package

| Asset | Content |
|---|---|
| `sluis-issuer_<version>_linux_arm64.zip` | Issuer, signer, console, providers |
| `sluis-cloudflare_<version>_linux_arm64.zip` | Cloudflare |
| `sluis-backup_<version>_linux_arm64.zip` | Backup, and restore (`backup.role`) |
| `sluis-google_<version>_linux_arm64.zip` | The Google module: the directory refresh, the console's Workspace service and the sign-in |
| Build | `bootstrap` pinned to its module: a zip refuses another's document |
| Minimum | `MinPackageVersion` 1.63; binary and library share a minor |

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
| `{"kind":"tick","controller":"github"\|"slack","target":"<id>"}` | One pass of one target | EventBridge Scheduler, one schedule per target |
| `{"kind":"run","controller":"github"\|"slack","target":"<id>"}` | The same pass, "run now" | The console, through the `invoke` trigger adapter (`InvocationType: Event`) |
| `{"kind":"clients"}` | One reconcile of the generated client secrets, and nothing else | EventBridge Scheduler |
| `{"kind":"refresh"}` | The name before `clients`, accepted for one release: the same reconcile, and one directory refresh under the refresh lease (`DirectoryRefresh.Rate`, 15 minutes) where the directory is hosted in the process, none where `google.remote` is set | EventBridge Scheduler |
| `{"kind":"check"}` | Each declared secret is read; a missing one fails it | A deploy |
| `{"kind":"backup"}`, `{"kind":"backup","resume":true}` | The [backup module](backup.md)'s function only: a backup, or the rest of a paused one | EventBridge Scheduler |

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

## The Google module's calls

| Method | Caller class (alias) | Does |
|---|---|---|
| `console.list_workspaces`, `begin_connect`, `reconnect`, `complete_consent`, `upload_key`, `set_served_domains`, `set_synced_groups`, `probe`, `refresh`, `disconnect` | `console` (`live-console`) | The Workspace pages: list, connect by consent or by service-account key, change a connected workspace |
| `identify`, `sign_in_url` | `issuer` (`live-issuer`) | The Google sign-in |

| Behavior | Value |
|---|---|
| Who may call | A console method refuses every class but `console`, the sign-in every class but `issuer`; the class is the alias, never the payload |
| Refusal | `not authorised` in every case; the reason is in the module's log |
| Module owns | The OAuth client secret, the service-account keys, the workspace records, the refresh writer; the issuer holds none and reads only the snapshot |
| Document keys | `google.oauthClient`, `google.publicURL` (the address the redirects are built from), `audit` |
| **Sign-in depends on the function** | Every Google sign-in calls `sign_in_url` and `identify`; there is no provisioned concurrency, so the first sign-in after idle waits for the cold start (storage, secrets, every workspace reader) |
| Sign-in failure | Each call is bounded at 25 s; the issuer shows a "Sign-in unavailable" page with a retry. A throttled or removed function fails every Google sign-in; recovery and a gateway's forwarded identity do not use it |
| Cold start | Provisioned concurrency of 1 on `live-issuer` removes it; the estate decides |
| Consent callback | The issuer verifies the signed state and forwards the `code` to `complete_consent` once: no platform retry, deadline about 25 s, because a spent code cannot be used again |
| Key upload | The key passes through the issuer in memory, at most 64 KB, never logged |
| Residual | A compromised issuer can still call these methods (connect or disconnect a directory); it cannot read a key or a client secret |

## Environment

| Variable | Meaning |
|---|---|
| `SLUIS_CONFIG` | Service document, `/opt/sluis/sluis.yaml`. The library sets it |
| `OTEL_*` | OpenTelemetry's own. Use the [OTLP Lambda layer](https://github.com/truvity/observability/blob/master/docs/integrations/aws-lambda.md); `Telemetry.Env` also accepts the layer's `SLUIS_*` settings, `OPENTELEMETRY_*` and `AWS_LAMBDA_EXEC_WRAPPER`. The layer's old `ACCESS_ROSTER_*` spelling is accepted until v1.76 |

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
/sluis/<instance>/internal/config/...        seeded by an operator or generated by the stack
/sluis/<instance>/internal/credentials/...   written by sluis
/sluis/<instance>/external/<kind>/<id>       documents consumers read
```

Each name under `internal/config/` is read when first used and every `secrets.refresh` (1 minute) after. See [secrets](secrets.md#the-names).

| Generated secret | Value | Rotation |
|---|---|---|
| `config/issuer/state-secret` | Base64 of 32 random bytes; output `StateSecretParameter` | `pulumi up --replace` on the `RandomBytes` resource; signs out all in-flight sign-ins. An apply never regenerates it |
| `config/recovery/password` | 40 random letters and digits | Read with `aws ssm get-parameter --with-decryption` ([recovery](../../guides/sluis/operate/recover-on-lambda.md)). An apply never regenerates it |

## IAM: one role

One role and inline policy named `<FunctionName>` (`sluis-<instance>-issuer`) serve the issuer. `<root>` is `/sluis/<instance>`; `PermissionsBoundaryArn` sets a boundary.

| Grant | Scope |
|---|---|
| `logs:CreateLogStream`, `logs:PutLogEvents` | Own log group |
| S3 `GetObject`, `PutObject`, `DeleteObject`; `ListBucket` | Blob bucket objects under the prefix; the bucket |
| DynamoDB `GetItem`, `PutItem`, `UpdateItem`, `DeleteItem`, `Query`, `Scan`, `DescribeTable` | The table. `Scan` serves `sluis migrate` and dotless listings |
| SSM `GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter` | `<root>/internal/credentials/*` and `<root>/external/*`, with `GetParameterHistory` |
| SSM `GetParameter`, `GetParameters`, `GetParametersByPath` | `<root>/internal/config/*`; read only |
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
