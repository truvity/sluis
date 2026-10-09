# Deploy from a mirrored release

Deploy the Lambda functions from your own versioned S3 bucket, with the release files mirrored there. The deploy then needs no GitHub access, and the digest from the release's `checksums.txt` names what runs.

## Before you start

- You need a versioned S3 bucket the deploying identity may write to: `s3:PutObject` and `s3:GetObject`, plus `s3:GetObjectVersion` for Lambda. An unversioned bucket fails the apply with "is not versioned".
- Use the library at the release you deploy: `github.com/truvity/sluis/deploy/pulumi`.

## Steps

1. Set the bucket on the Lambda arguments. Keys land under `sluis/<version>/`.

   ```go
   Artifacts: &sluispulumi.ArtifactsArgs{Bucket: "example-artifacts"},
   ```

2. Pin the zip and its digest from `checksums.txt`. The package may be the mirror's https URL.

   ```go
   Package:       "sluis-lambda_1.74.0_linux_arm64.zip",
   PackageSHA256: "<digest from checksums.txt>",
   ```

   Leave `Package` empty to deploy the release the library is, with the digest read from that release's `checksums.txt`. To use a mirror of the release page, set `Release: &sluispulumi.ReleaseArgs{BaseURL: "https://mirror.example.test/releases"}`. A set `GITHUB_TOKEN` is sent to github.com.

3. Run `pulumi preview`. The library downloads the zip once (cached by SHA-256), checks the digest and uploads it to `sluis/<version>/<sha256>-<file name>`. The function is created from that object version.

4. Run `pulumi up`.

## Verify

The output `CodeSha256Matches` is `true`. The base64 digest in `checksums.txt` equals:

```sh
aws lambda get-function --function-name <name> --query Configuration.CodeSha256
```

## If it fails

- "is not versioned": enable versioning on the bucket.
- "is a development build" or "replaced by a local copy": the program has no release to fetch. Set `Package` or `Release.Version`.
- "has the SHA-256 ... not the ... asked for": the bytes are not the release's. Do not deploy them.

The audit functions take the same fields on the audit library's `Args` ([reference](../../../reference/audit/aws-pulumi-library.md)). `LambdaArgs.Audit` passes the bucket on with its own `audit/` prefix.
