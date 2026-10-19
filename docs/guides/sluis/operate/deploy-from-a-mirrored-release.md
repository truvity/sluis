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

2. Pin the zip and its digest, or let the library read the digest from the signed `checksums.txt`. Leave `Package` empty to deploy the release the library is.

   ```go
   Package:       "sluis-issuer_1.77.0_linux_arm64.zip",   // may be the mirror's https URL
   PackageSHA256: "<digest from checksums.txt>",           // or leave it out and set Release
   Release: &sluispulumi.ReleaseArgs{
       ResolveChecksums: true,                              // verifies checksums.txt.sigstore.json first
       BaseURL: "https://mirror.example.test/releases",     // a mirror copies both files unchanged
       // TrustedRootJSON: "...",                           // air-gapped: a Sigstore root from a verified source
   },
   ```

   A set `GITHUB_TOKEN` is sent to github.com.

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
- "checksums.txt.sigstore.json ...": the mirror lacks the bundle. Copy it, or pass `PackageSHA256` from [a verified release](verify-a-release.md).
- "has the SHA-256 ... not the ... asked for": the bytes are not the release's. Do not deploy them.

The audit functions take the same fields on the audit library's `Args` ([reference](../../../reference/audit/aws-pulumi-library.md)). `LambdaArgs.Audit` passes the bucket on with its own `audit/` prefix.
