# Tutorial: sluis on AWS Lambda

You deploy one sluis function on AWS Lambda behind a mutual-TLS custom domain (preset `aws-hybrid`; `aws-serverless` has no Kubernetes beside it). State is in DynamoDB, blobs in S3, secrets in SSM. Tokens are signed by key pairs the function generates and wraps under a KMS key you supply. Allow about an hour, most of it waiting for AWS.

Design: [ports](../../concepts/sluis/ports.md). Library arguments: [Pulumi library](../../reference/sluis/pulumi-library.md). Keys: [configuration](../../reference/sluis/configuration.md).

## What you need

All names are placeholders; replace them.

| Item | Detail |
|---|---|
| AWS account | Credentials that create IAM, Lambda, API Gateway, DynamoDB, S3, KMS, SSM and EventBridge resources. Below: `111122223333`, `eu-central-1`. |
| Apply role | The ARN of the IAM role you apply with (`applyRoleArn`). Only it may write the truststore. |
| KMS key | Symmetric, alias `alias/demo-sluis-sign`. Step 0 creates it. The library looks the alias up and creates no key. |
| ACM certificate | In that region, for `access.example.test`. You supply its ARN. |
| Audit queue | URL and ARN of an existing audit installation's SQS ingest queue (`Audit.Use`, v1.74 or later). Without it the library installs audit beside the function: see [Audit](../../reference/sluis/pulumi-library.md#audit). |
| Tools | Go, `pulumi` logged in to a backend, `gh`, `openssl`, `curl`, and `sluisctl` from the release's `sluisctl_<version>_<os>_<arch>` archive. |

## 0. Create the signing key

The key exists before the stack that grants on it. A symmetric key with an alias and the account's default key policy is enough. Once the library grants it, the function's role uses the key under the context `{instance, purpose: sign}`.

```sh
KEY=$(aws kms create-key --description "demo sluis: wraps the signing keys" --query KeyMetadata.KeyId --output text)
aws kms enable-key-rotation --key-id "$KEY"
aws kms create-alias --alias-name alias/demo-sluis-sign --target-key-id "$KEY"
```

## 1. Write the installation

`installation.yaml` holds what you know about the installation and no secret. It is held to `schemas/config/installation.schema.json`.

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

signingKey:
  kmsWrapped: {}                         # the key is LambdaArgs.Keys.Sign (step 4), written as keys.sign

recovery: {enabled: true}                # the way in before any directory is connected
console: {client: access-console}

access:
  groups:
    all:sluis:operator:
      members: [admin@example.test]
    all:sluis:viewer:
      matchers: [{email_domain: example.test}]
  clients:
    access-console:
      kind: public
      redirects: [https://access.example.test/console/callback]
      requires: [all:sluis:viewer]
```

`instance` names the installation, and its SSM root is `/sluis/demo`. `table` and `bucket` are the names the library creates in step 4.

## 2. Render it and read the result

```sh
sluisctl render --installation installation.yaml --out rendered
```

Expect no output and exit status 0. `rendered/` holds `sluis.yaml` (the service document) and `policy.yaml`. In `sluis.yaml`, check `preset: aws-hybrid`, the DynamoDB table, the S3 bucket and `secrets: {source: ssm, root: /sluis/demo}`. A mistake in the installation stops here and names the key. The library renders the same documents in step 4. Do not deploy `rendered/`.

## 3. Fetch the release and pin its digest

Use one release for the Pulumi library and the binary. This tutorial needs v1.74 or later, where `Audit.Use` and the edge module start.

```sh
VERSION=1.74.0        # the release you chose, without the v
gh release download "v$VERSION" --repo truvity/sluis --dir dist \
  --pattern "sluis-lambda_${VERSION}_linux_arm64.zip" --pattern checksums.txt
grep "sluis-lambda_${VERSION}_linux_arm64.zip" dist/checksums.txt
```

Expect one line: a 64-character SHA-256 and the file name. Copy the digest into `main.go` as a constant and commit it. Do not read it from `dist/checksums.txt` in the program. The library checks the zip against the constant and deploys it byte for byte.

## 4. Write the Pulumi program

In an empty Pulumi Go project (`pulumi new aws-go`), add the library at the release's tag and copy `installation.yaml` beside `main.go`. The truststore is the PEM of the CAs a client certificate must chain to. Make a test CA and client certificate:

```sh
go get github.com/truvity/sluis/deploy/pulumi@v1.74.0 github.com/truvity/sluis/deploy/pulumi/edge/cloudflare@v1.74.0 \
  github.com/truvity/sluis@v1.74.0
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
	version      = "1.74.0"
	lambdaSHA256 = "<the digest from step 3>"
	certArn      = "<the ACM certificate's ARN>"
	applyRoleArn = "<the ARN of the IAM role you apply with>"
	auditQueueURL = "<the audit ingest queue URL>"
	auditQueueArn = "<the audit ingest queue ARN>"
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
			Audit: &sluispulumi.AuditArgs{Use: &sluispulumi.AuditUse{QueueURL: auditQueueURL, QueueArn: auditQueueArn}},
			Keys:           &sluispulumi.KeysArgs{Sign: "alias/demo-sluis-sign"}, // the key from step 0, by alias
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

`NewLambda` fills in what the installation leaves out and refuses a value that disagrees with the arguments, naming the argument. It replaces the deprecated `Config`, `Policy` and `PolicyPath` arguments.

## 5. Preview, then apply

```sh
pulumi preview
```

Expect only creates:

- the bucket and the table

- the function with its role and layer

- the HTTP API

- the custom domain with the truststore object

- the SSM parameters for the recovery password and the state secret

- the schedules

Then apply:

```sh
pulumi up
```

Expect `domainTarget` and `functionName` in the outputs. If the apply refuses the package, the message names the digest or version: the zip must match the library's release.

## 6. Point DNS at it and ask the issuer

Create a CNAME from `access.example.test` to `domainTarget`. A request without a client certificate never reaches the function. Ask with the certificate you made:

```sh
curl --cert client.crt --key client.key https://access.example.test/.well-known/openid-configuration
```

Expect JSON whose `issuer` is `https://access.example.test`. A TLS failure means the certificate does not chain to `truststore.pem` or the CNAME has not propagated. Read the function's log:

```sh
aws logs tail /aws/lambda/sluis --since 15m
```

The log group is `/aws/lambda/<function name>`, `sluis` unless the installation sets `aws.functionName`. A document the loader refuses stops the cold start and is named there.

## 7. Sign in

The library generated the recovery password. Read it from a terminal:

```sh
aws ssm get-parameter --with-decryption --name /sluis/demo/internal/config/recovery/password \
  --query Parameter.Value --output text
```

Import `client.crt` and `client.key` into a browser as a PKCS#12 file. Open `https://access.example.test/console/login`, expand **Recovery sign-in** and paste the password. You are `recovery`, an operator. The attempt's audit trail is in [Recovery on Lambda](../../guides/sluis/operate/recover-on-lambda.md).

## Next

- Connect a directory: [Google Workspace](../../guides/sluis/connect/google-workspace.md). Then connect each relying party: [how-to index](../../concepts/sluis/README.md).

- Take a release: [upgrade pages](../../guides/sluis/upgrade/v1.64.md).

- Operate on Lambda: [Lambda reference](../../reference/sluis/lambda.md).
