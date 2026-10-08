# Tutorial: sluis on AWS Lambda

By the end you have one sluis function on AWS Lambda behind a mutual-TLS custom domain, with its state in DynamoDB,
its blobs in S3, its secrets in SSM and its tokens signed under a KMS-wrapped key. You write the facts of the
installation once, in an installation file, and the Pulumi library does the rest. This is the preset `aws-hybrid`
(`aws-serverless` differs only in having no Kubernetes beside it). Allow about an hour, most of it waiting for AWS.

Not here: why it is built this way ([ports](../explanation/ports.md)), every library argument
([Pulumi library](../reference/pulumi-library.md)), every key ([configuration](../reference/configuration.md)).

## What you need

- An AWS account and credentials that can create IAM, Lambda, API Gateway, DynamoDB, S3, KMS, SSM and EventBridge
  resources; the account id and a region (`111122223333` and `eu-central-1` below), and the ARN of the IAM role you apply
  with (`applyRoleArn` below): only it may write the truststore.
- An ACM certificate in that region for the host you will serve (`access.example.test` below), and its ARN. You supply
  it; the library does not issue one.
- An SQS ingest queue of an audit installation ([truvity/audit](https://github.com/truvity/audit)), and its ARN. The
  function sends every audit record to it.
- Go, `pulumi` logged in to a backend, `gh`, `openssl`, `curl`.
- `sluisctl` of the release you deploy, from the release's `sluisctl_<version>_<os>_<arch>` archive.

All names below are placeholders; replace them.

## 1. Write the installation

`installation.yaml` holds what you know about this installation and no secret. Its shape is
`apiVersion: sluis.truvity.github.io/installation/v1`, held to `schemas/config/installation.schema.json`.

```yaml
apiVersion: sluis.truvity.github.io/installation/v1
instance: demo
shape: lambda
preset: aws-hybrid

issuer:
  url: https://access.example.test
  secureCookies: true

aws:
  account: "111122223333"
  region: eu-central-1
  table: demo-sluis
  bucket: demo-sluis-111122223333
  auditQueueURL: https://sqs.eu-central-1.amazonaws.com/111122223333/demo-audit-ingest

signingKey:
  kmsWrapped:
    keyId: alias/sluis-signing-wrapped   # the library's default alias for the key it creates

recovery: {enabled: true}                # the way in before any directory is connected
console: {client: access-console}

access:
  groups:
    all:access-roster:operator:
      members: [admin@example.test]
    all:access-roster:viewer:
      matchers: [{email_domain: example.test}]
  clients:
    access-console:
      kind: public
      redirects: [https://access.example.test/console/callback]
      requires: [all:access-roster:viewer]
```

`instance` names the installation; its SSM root is `/sluis/demo`. `table` and `bucket` are the names the library
creates in step 4, and the adapters are configured to them.

## 2. Render it and read the result

```sh
sluisctl render --installation installation.yaml --out rendered
```

Expect no output and exit status 0. `rendered/` now holds the two documents the function will read: `sluis.yaml`
(the service document) and `policy.yaml`. Open `sluis.yaml` and check that `preset: aws-hybrid`, the DynamoDB table,
the S3 bucket and `secrets: {source: ssm, root: /sluis/demo}` are what you meant. A mistake in the installation stops
here, naming the key. The library renders the same documents itself in step 4 (the same function), so what you read is
what runs. Do not deploy `rendered/`; it is for reading.

## 3. Fetch the release and pin its digest

The Pulumi library and the binary move together, so use one release for both. `Installation` needs v1.64 or later.

```sh
VERSION=1.64.0        # the release you chose, without the v
gh release download "v$VERSION" --repo truvity/sluis --dir dist \
  --pattern "sluis-lambda_${VERSION}_linux_arm64.zip" --pattern checksums.txt
grep "sluis-lambda_${VERSION}_linux_arm64.zip" dist/checksums.txt
```

Expect one line: a 64-character SHA-256 and the file name. Copy the digest into `main.go` (step 4) as a constant and
commit it. The library checks the zip against it and deploys the zip byte for byte. A digest fetched at deploy time
beside the zip proves nothing about it, so do not read it from `dist/checksums.txt` in the program.

## 4. Write the Pulumi program

In an empty Pulumi Go project (`pulumi new aws-go`), add the library at the release's tag and copy `installation.yaml`
beside `main.go`. A truststore is the PEM of the CAs a client certificate must chain to; for this tutorial make your
own CA and a client certificate to test with:

```sh
go get github.com/truvity/sluis/deploy/pulumi@v1.64.0 github.com/truvity/sluis/deploy/pulumi/edge/cloudflare@v1.64.0 \
  github.com/truvity/sluis@v1.64.0
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
  -keyout ca.key -out truststore.pem -subj "/CN=demo-client-ca"
openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -keyout client.key -out client.csr -subj "/CN=demo-client"
openssl x509 -req -in client.csr -CA truststore.pem -CAkey ca.key -CAcreateserial -days 30 -out client.crt
```

`main.go`:

```go
package main

import (
	"os"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	sluisconfig "github.com/truvity/sluis/config"
	sluispulumi "github.com/truvity/sluis/deploy/pulumi"
	edge "github.com/truvity/sluis/deploy/pulumi/edge/cloudflare"
)

const (
	region       = "eu-central-1"
	account      = "111122223333"
	version      = "1.64.0"
	lambdaSHA256 = "<the digest from step 3>"
	certArn      = "<the ACM certificate's ARN>"
	applyRoleArn = "<the ARN of the IAM role you apply with>"
	auditQueue   = "<the audit ingest queue ARN>"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		provider, err := aws.NewProvider(ctx, "aws", &aws.ProviderArgs{Region: pulumi.String(region)})
		if err != nil {
			return err
		}
		withAWS := pulumi.Providers(provider)

		// The blob bucket also holds the truststore, so it is versioned and guards
		// that prefix: only the apply role may write it.
		store, err := sluispulumi.NewStorage(ctx, "demo", &sluispulumi.StorageArgs{
			BucketName: "demo-sluis-" + account, Versioning: true,
			ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edge.Guard(applyRoleArn)},
		}, withAWS)
		if err != nil {
			return err
		}
		state, err := sluispulumi.NewState(ctx, "demo", &sluispulumi.StateArgs{TableName: "demo-sluis"}, withAWS)
		if err != nil {
			return err
		}

		in, err := sluisconfig.LoadInstallation("installation.yaml")
		if err != nil {
			return err
		}
		pem, err := os.ReadFile("truststore.pem")
		if err != nil {
			return err
		}
		l, err := sluispulumi.NewLambda(ctx, "demo", &sluispulumi.LambdaArgs{
			Installation:   in,
			Package:        "dist/sluis-lambda_" + version + "_linux_arm64.zip",
			PackageSHA256:  lambdaSHA256,
			Storage:        store.Grant(),
			State:          state.Grant(),
			AuditQueueArn:  pulumi.String(auditQueue),
			WrappedSigning: &sluispulumi.WrappedSigningArgs{}, // the library creates the symmetric key
		}, withAWS)
		if err != nil {
			return err
		}
		// The front door: the custom domain with mutual TLS, in front of the API.
		front, err := edge.NewEdge(ctx, "demo", &edge.Args{
			FrontDoor:      l.FrontDoor(),
			DomainName:     "access.example.test",
			CertificateArn: pulumi.String(certArn),
			TruststorePEM:  string(pem),
			Storage:        store,
		}, withAWS)
		if err != nil {
			return err
		}
		ctx.Export("domainTarget", front.DomainTarget)
		ctx.Export("functionName", l.FunctionName)
		return nil
	})
}
```

The installation names the account, region, instance and function name; `NewLambda` fills in what it leaves out and
refuses one that disagrees with the arguments, naming the argument. It replaces the deprecated `Config`, `Policy` and
`PolicyPath` arguments.

## 5. Preview, then apply

```sh
pulumi preview
```

Read it. Expect the bucket, the table, the KMS key and its alias, the function, its role and layer, the HTTP API, the
custom domain with the truststore object, the SSM parameters for the recovery password and the state secret, and the schedules, all as
creates and nothing else. Then:

```sh
pulumi up
```

Expect `domainTarget` and `functionName` in the outputs. If the apply refuses the package, the message names the digest
or the version: the zip must be the release the library is.

## 6. Point DNS at it and ask the issuer

Create a CNAME for `access.example.test` to `domainTarget`. The domain is mutual TLS, so a request without a client
certificate never reaches the function. Ask with the one you made:

```sh
curl --cert client.crt --key client.key https://access.example.test/.well-known/openid-configuration
```

Expect JSON whose `issuer` is `https://access.example.test`. A TLS failure means the certificate does not chain to
`truststore.pem` or the CNAME has not propagated. For the function's own view:

```sh
aws logs tail /aws/lambda/sluis --since 15m
```

The log group is `/aws/lambda/<function name>` (`sluis` unless the installation says `aws.functionName`). A document
the loader refuses stops the cold start and is named there.

## 7. Sign in

The library generated the recovery password. Read it from a terminal:

```sh
aws ssm get-parameter --with-decryption --name /sluis/demo/private/config/recovery/password \
  --query Parameter.Value --output text
```

Open `https://access.example.test/console/login` in a browser that presents your client certificate (import
`client.crt` and `client.key` as a PKCS#12 file), expand **Recovery sign-in** and paste it. You are `recovery`, an operator. Details and the
audit trail of the attempt: [Recovery on Lambda](../how-to/recover-on-lambda.md).

## You now have

- one function `sluis` from the released zip, byte for byte, with the installation's two documents in an immutable
  layer: a change to `installation.yaml` is a `pulumi up` that publishes a new layer version;
- DynamoDB state, S3 blobs, SSM secrets under `/sluis/demo`, and tokens signed under a key KMS wraps;
- an issuer that answers its discovery document, and a console you can sign in to.

Next: connect a directory ([Google Workspace](../how-to/connect/google-workspace.md)), then each thing that trusts the
issuer ([how-to index](../sluis/README.md)). Taking a release: [upgrade pages](../how-to/upgrade/v1.64.md). Operations on
Lambda: [Lambda reference](../reference/lambda.md).
