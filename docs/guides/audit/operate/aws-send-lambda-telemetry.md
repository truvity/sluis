# Send the Lambda functions' telemetry

Have the writer and notary functions export OpenTelemetry with the function role's identity and no secret.

## Before you start

- Publish a layer version of the OTLP extension in the account and region, by convention named `audit-otlp`, from the sluis release.

- You need an issuer that trades the function role's STS identity token for a short-lived token audienced for the OTLP door, and an OTLP/HTTP endpoint.

- Enable outbound identity federation on the account.

- The issuer must admit the two roles: a group matcher on `arn:aws:iam::<account>:role/audit/audit-*`, or the exact role ARNs.

- The extension is fail-open. A broken setup is silent until you look for the signal.

- `Telemetry.ExtraEnv` refuses `OTEL_*HEADERS*` and any key naming a token, secret, password or credential.

## Steps

1. Set `Telemetry` in the stack: `ExtensionLayerArn`, `IssuerURL` and `OTLPEndpoint`. `STSAudience` and `OTLPAudience` default to `otlp`.

   ```text
   OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
   OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
   OTEL_SERVICE_NAME=audit-writer            # audit-notary for the notary
   SLUIS_ISSUER, SLUIS_AUDIENCE, SLUIS_OTLP_ENDPOINT, SLUIS_OTLP_AUDIENCE
   ```

   The functions gain the extension layer and these variables. The roles gain `sts:GetWebIdentityToken` limited to that audience, ES384 and 300 seconds.

2. Apply, then invoke the writer once.

## Verify

The metric `audit_queue_message_age_seconds` with label `transport=sqs` arrives at the collector. The function flushes at the end of every invocation and on SIGTERM.

## Roll back

Remove `Telemetry`. The layer, the variables and the statement go together.

## Legacy variables

The library also sets the deprecated `ACCESS_ROSTER_*` spellings of the four extension settings (legacy identifier, renamed in v1.75–v1.76), for a function that runs a layer older than v0.69.0. Set `Telemetry.OmitLegacyEnv` to drop them once the layer is v0.69.0 or later; they are removed in v1.76.

## See also

The alarm set is CloudWatch's: see [alarms](../../../reference/audit/aws-pulumi-library.md#alarms). The OTLP signals are in [telemetry](../../../reference/audit/telemetry.md).
