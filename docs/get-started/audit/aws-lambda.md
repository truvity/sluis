# Getting started on AWS Lambda

Build a writer Lambda that archives records into S3 and a notary Lambda that seals each hour, with the Pulumi library. It takes about an hour.

Records flow from SQS through the writer, which deduplicates in DynamoDB, to S3. The notary seals with KMS.
See [AWS Lambda](../../concepts/audit/aws-lambda.md) and the [library reference](../../reference/audit/aws-pulumi-library.md).

## What you need

| You need | Detail |
|---|---|
| A Pulumi (Go) program, an AWS account and a region | |
| The release zips and checksums | `audit-writer-lambda_<version>_linux_arm64.zip`, `audit-notary-lambda_<version>_linux_arm64.zip`, `checksums.txt` from the GitHub release |
| Two KMS keys, each with an alias | a symmetric key for the archive (`Keys.Archive`) and an `ECC_NIST_P384` `SIGN_VERIFY` key for the seals (`Keys.Seal`); the library creates none ([keys](../../reference/audit/aws-pulumi-library.md#keys-state-and-an-s3-compatible-archive)) |
| The library at the same release | `go get github.com/truvity/sluis/audit/deploy/pulumi@v<version>` |
| The ARN of the IAM role of each sender | a role or user ARN, not a session ARN |
| The application's catalogue directory | its document and JSON schemas; optional for a trial |

## 1. Write the profile document

Save this as a string for `Writer.DeploymentYAML`. `security` is the profile every installation composes
([which profiles to compose](../../concepts/audit/which-profiles-to-compose.md)).

```yaml
apiVersion: audit.truvity.github.io/audit-deployment/v2
external_identifiers_are_opaque: true
profiles:
  security:
    frameworks: [security]   # each is a framework profile
```

## 2. Write the stack

```go
import auditpulumi "github.com/truvity/sluis/audit/deploy/pulumi"

a, err := auditpulumi.New(ctx, "audit", &auditpulumi.Args{
	// The estate's keys, by alias: the library looks them up and creates none.
	Keys: auditpulumi.KeysArgs{Archive: "alias/audit-archive", Seal: "alias/audit-seal"},
	// Required: the storage of each install preset. `security` needs `standard`.
	Presets: map[string]auditpulumi.PresetStorage{
		"standard": {Bucket: "acme-audit-trial", Create: true},
	},
	Ingest: auditpulumi.IngestArgs{
		// Required: who may send. Role or user ARNs, never session ARNs.
		Senders: []pulumi.StringInput{emitterRoleArn},
		// An operator's break-glass role, for redriving the DLQ (optional).
		Redrivers: []pulumi.StringInput{breakGlassRoleArn},
	},
	Writer: auditpulumi.WriterArgs{
		Package:        "dist/audit-writer-lambda_<version>_linux_arm64.zip",
		PackageSHA256:  writerSHA, // the line of checksums.txt, pinned in this file
		DeploymentYAML: deploymentYAML,
		CatalogueDirs:  []string{"catalogue/shop"}, // a document and its *.json schemas
	},
	Notary: auditpulumi.NotaryArgs{
		Package:       "dist/audit-notary-lambda_<version>_linux_arm64.zip",
		PackageSHA256: notarySHA,
	},
})
ctx.Export("queueUrl", a.QueueURL)
```

Three traps:

- `Senders` is required. A stack that names none fails at preview ([sender rules](../../guides/audit/operate/aws-run-readers-in-kubernetes.md)).

- The writer refuses to start without the schemas its catalogue document references ([change what a source records](../../guides/audit/connect/change-what-a-source-records.md)).

- The library and the zips must be the same release, or the preview fails ([ship a release](../../guides/audit/operate/aws-ship-a-release.md)).

A writer secret, such as an OpenBAO token for pseudonymisation keys, is an SSM SecureString under `/audit/<name>/private/config`
([store the writer's secrets in SSM](../../guides/audit/operate/aws-store-secrets-in-ssm.md)). A trial with no `keys` block, the default, needs none.

## 3. Deploy

```sh
pulumi preview   # the guards run first: package digest, release match, catalogues
pulumi up
```

The stack creates a bucket, the ingest queue and its DLQ, and a DynamoDB table.
It also creates two functions, an hourly schedule and the alarms. It grants on your two keys and creates no key.
`pulumi stack output queueUrl` prints the queue URL.

## 4. Send a record

Point an emitter at the queue. A recording component takes `sink: {sqs: {queueUrl, region}}`. Its pod or role must be one of `Senders`.
A minute later, list the archive:

```sh
aws s3 ls "s3://acme-audit-trial/records/security/" --recursive
```

`audit verify` checks only closed hours. After the hour closes, run:

```sh
audit verify --profile security --last 1h --bucket acme-audit-trial
```

The trail holds an `audit.writer.started` record. If the `audit-ingest-dlq-not-empty` or `audit-writer-unknown-catalogue` alarm fires, see
[redrive the ingest DLQ](../../guides/audit/operate/redrive-the-ingest-dlq.md).

## Next

- [Turn the Object Lock on](../../guides/audit/operate/aws-turn-on-object-lock.md) when retentions are signed off.
- [Run readers in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md).
- [Send the Lambda functions' telemetry](../../guides/audit/operate/aws-send-lambda-telemetry.md).
- [Upgrade from an earlier release](../../guides/audit/upgrade/v0.13.md).
