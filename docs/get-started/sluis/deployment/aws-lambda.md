# AWS Lambda

One function per module: the issuer with the console and the signer, and `github`, `slack`, `google`, `cloudflare`, `backup` and `restore`, each with its own role. DynamoDB holds the State, a table per module, and S3 holds reports and directory snapshots. SSM holds the secrets. A KMS key you supply wraps the signing keys the issuer generates. The Pulumi library, `github.com/truvity/sluis/deploy/pulumi`, builds all of it. It exposes the HTTP API as `Lambda.FrontDoor()`.

Start with [sluis on AWS Lambda](../aws-lambda.md). Design: [Lambda](../../../concepts/sluis/lambda.md) and [signing on AWS](../../../concepts/sluis/signing-on-aws.md). Inputs and outputs: [Lambda reference](../../../reference/sluis/lambda.md) and [Pulumi library](../../../reference/sluis/pulumi-library.md).

## What you decide

- **DNS and certificate.** The core library stops at the HTTP API and has no custom domain. An edge module adds one: [behind Cloudflare](aws-behind-cloudflare.md), or the planned [AWS edge module](README.md#aws-edge-module).

- **Keys.** You supply the KMS keys by alias: `Keys.Sign`, and `Keys.Secrets` for the SSM parameters. The library looks them up, grants on them and creates none. To move from library-created keys, follow [the state operation](../../../guides/sluis/migrate/supply-your-own-signing-keys.md).

- **Secrets layout.** `secrets.layout` is `v5`: a table, a parameter namespace and a blob prefix per module, and a function per module ([module roles](../../../reference/sluis/pulumi-library.md#module-roles)). It is the only layout; a v4 installation moves with v1.77.0 first ([upgrade to v1.77.1](../../../guides/sluis/upgrade/v1.77.1.md)). A consumer of an `external/` secret reads it with `ExternalReadPolicy`.

- **Backup.** `NewBackup` and `NewRestore` write one age-encrypted backup a day to an archive bucket and restore from it ([back up and restore on layout v5](../../../guides/sluis/operate/back-up-on-layout-v5.md)).

- **Blobs.** The bucket is S3 or S3-compatible. R2 takes credentials from an `internal/` address, which needs the `ssm` secrets source. See [R2 storage](../../../guides/sluis/connect/r2-storage.md).

- **Audit.** `LambdaArgs.Audit` installs an operational audit installation beside the function by default. `Audit.Presets` picks S3 or R2 for each archive store. The default is one operational bucket. `Audit.Use` sends to an existing installation. `Audit.Enabled: false` keeps records in the log only. See [the Pulumi library](../../../reference/sluis/pulumi-library.md#audit) and [audit deployment](../../audit/README.md).

## Run it

[Day two](../../../guides/sluis/operate/day-two.md), [recover on Lambda](../../../guides/sluis/operate/recover-on-lambda.md) and [the operations index](../../../guides/sluis/operate/README.md).
