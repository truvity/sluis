# AWS Lambda

One function serves the issuer, the console, the controllers and the scheduled passes; DynamoDB holds the State, an S3
bucket holds reports and directory snapshots, and KMS signs and wraps. The Pulumi library
(`github.com/truvity/sluis/deploy/pulumi`) builds all of it and exposes the HTTP API as `Lambda.FrontDoor()`, so
anything can stand in front.

Start with [sluis on AWS Lambda](../../getting-started/aws-lambda.md); it takes an AWS account and a Pulumi stack from
nothing to a signed-in console. The reasons for the shape are in [Lambda](../../explanation/lambda.md), every input
and output of the library is in [the Lambda reference](../../reference/lambda.md) and
[the Pulumi library](../../reference/pulumi-library.md), and signing on KMS is in
[signing on AWS](../../explanation/signing-on-aws.md).

## What is yours to decide

- **DNS and certificate.** The core library stops at the HTTP API. A domain in front of it comes from an edge module:
  [behind Cloudflare](aws-behind-cloudflare.md) today, [AWS-native](edge-aws.md) planned.
- **Blobs.** The bucket may be S3 or S3-compatible (R2); see [R2 storage](../../how-to/connect/r2-storage.md).
- **Audit.** The library installs audit by default; see [audit on AWS Lambda](../../audit/deployment/README.md).

## Running it

[Day two](../../how-to/day-two.md), [recover on Lambda](../../how-to/recover-on-lambda.md) and
[the operations index](../operations/README.md).
