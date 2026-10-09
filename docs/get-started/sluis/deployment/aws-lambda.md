# AWS Lambda

One function serves the issuer, the console, the controllers and the scheduled passes; DynamoDB holds the State, an S3
bucket holds reports and directory snapshots, SSM holds the secrets, and a KMS key the estate supplies wraps the signing
keys the function generates. The Pulumi library
(`github.com/truvity/sluis/deploy/pulumi`) builds all of it and exposes the HTTP API as `Lambda.FrontDoor()`, so
anything can stand in front.

Start with [sluis on AWS Lambda](../aws-lambda.md); it takes an AWS account and a Pulumi stack from
nothing to a signed-in console. The reasons for the shape are in [Lambda](../../../concepts/sluis/lambda.md), every input
and output of the library is in [the Lambda reference](../../../reference/sluis/lambda.md) and
[the Pulumi library](../../../reference/sluis/pulumi-library.md), and signing on KMS is in
[signing on AWS](../../../concepts/sluis/signing-on-aws.md).

## What is yours to decide

- **DNS and certificate.** The core library stops at the HTTP API; it has no custom domain. A domain in front of it comes
  from an edge module (a Go module of its own): [behind Cloudflare](aws-behind-cloudflare.md) today,
  [AWS-native](edge-aws.md) planned.
- **Keys.** The estate supplies the KMS keys by alias (`Keys.Sign`, and `Keys.Secrets` for the SSM parameters); the library
  looks them up and grants on them, and creates none. Moving from keys the library created is
  [a state operation, not a replacement](../../../guides/sluis/migrate/supply-your-own-signing-keys.md).
  See [signing on AWS](../../../concepts/sluis/signing-on-aws.md).
- **Secrets layout.** `secrets.layout` is `v3` (the default), `transition` or `v4`; see
  [move the secrets to layout v4](../../../guides/sluis/migrate/migrate-secrets-layout.md). A consumer of an `external/` secret reads
  it with `ExternalReadPolicy`.
- **Blobs.** The bucket may be S3 or S3-compatible (R2, with credentials from an `internal/` address, which needs layout
  `transition` or `v4`); see [R2 storage](../../../guides/sluis/connect/r2-storage.md).
- **Audit.** Audit comes with this shape by default: `LambdaArgs.Audit` installs an operational audit installation beside
  the function (writer, ingest queue and one archive store per install preset it needs, each on S3 or on R2 through `Audit.Presets`; the default is one operational bucket). `Audit.Use` sends to an installation
  that exists, `Audit.Enabled: false` keeps records in the log only. See [the Pulumi library](../../../reference/sluis/pulumi-library.md#audit)
  and [audit on AWS Lambda](../../audit/README.md).

## Running it

[Day two](../../../guides/sluis/operate/day-two.md), [recover on Lambda](../../../guides/sluis/operate/recover-on-lambda.md) and
[the operations index](../../../guides/sluis/operate/README.md).
