# Deploy from a mirrored release

## Purpose

Deploy sluis's Lambda functions from the estate's own versioned S3 bucket, with the release's files mirrored there, so that the deploy does not depend on GitHub at apply time and what runs is provably what the release's checksums name.

## Preconditions

- A versioned S3 bucket the deploying identity may write to (`s3:PutObject`, `s3:GetObject`; `s3:GetObjectVersion` for Lambda). Without versioning the apply fails with "is not versioned".
- The library at the release you deploy (`github.com/truvity/sluis/deploy/pulumi`).

## Steps

1. Set the bucket on the Lambda arguments:

   ```go
   Artifacts: &sluispulumi.ArtifactsArgs{Bucket: "example-artifacts"}, // keys under sluis/<version>/
   ```

2. Name the release. Either pin the zip and its digest from the release's `checksums.txt`:

   ```go
   Package:       "sluis-lambda_1.74.0_linux_arm64.zip", // or the mirror's https URL
   PackageSHA256: "<digest from checksums.txt>",
   ```

   or leave `Package` empty to deploy the release the library itself is (its version from the program's build information), with the digest read from that release's `checksums.txt`. For a mirror of the release page, set `Release: &sluispulumi.ReleaseArgs{BaseURL: "https://mirror.example.test/releases"}`; with `GITHUB_TOKEN` set it is sent to github.com.

3. `pulumi preview`. The zip is downloaded once (cached by SHA-256), checked against the digest, and uploaded as it is to `sluis/<version>/<sha256>-<file name>`; the function is created from that object version.

4. After `pulumi up`, check the output `CodeSha256Matches` is `true`.

## Verify

`aws lambda get-function --function-name <name> --query Configuration.CodeSha256` is the base64 of the digest in `checksums.txt`.

## If it fails

- "is not versioned": enable versioning on the bucket.
- "is a development build" / "replaced by a local copy": the program has no release to fetch; set `Package` or `Release.Version`.
- "has the SHA-256 ... not the ... asked for": the bytes are not the release's; do not deploy them.

For the audit functions the same fields exist on the audit library's `Args` ([reference](../audit/reference/aws-pulumi-library.md)); `LambdaArgs.Audit` passes the bucket on, with its own `audit/` prefix.
