# Ship a release to the Lambda functions

Deploy the release's writer and notary zips through the Pulumi library. The guards fail `pulumi preview` instead of a running function.

## Before you start

- Use the library (`github.com/truvity/sluis/audit/deploy/pulumi`) at the same release as the zips. The binary's release is read from the zip's file name, so do not rename the file.

- `Guards.AllowVersionSkew` accepts a deliberate mismatch, such as a build from a checkout. A library with no known release (a `replace` directive) compares nothing.

- Pin the SHA-256 from the release's `checksums.txt` in the stack source, where it is reviewed. Do not fetch `checksums.txt` at deploy time.

- The deploying identity needs `s3:GetObject` on `catalogue/*`, `s3:ListBucket` on the bucket with condition `s3:prefix` = `catalogue/`, `lambda:PublishLayerVersion` and `lambda:GetLayerVersion`. Without `s3:ListBucket`, S3 answers a missing key with 403 and the guard refuses. `Guards.SkipCatalogueCheck` says the identity cannot read the bucket.

## Steps

1. Pin the package and its digest in the stack. A writer zip is not accepted as the notary's, nor the reverse.

   ```go
   Writer: auditpulumi.WriterArgs{
       Package:       "dist/audit-writer-lambda_<version>_linux_arm64.zip",
       PackageSHA256: "<the checksums.txt line for that file>",
   },
   Notary: auditpulumi.NotaryArgs{
       Package:       "dist/audit-notary-lambda_<version>_linux_arm64.zip",
       PackageSHA256: "<...>",
   },
   ```

   The library deploys the file byte for byte and builds no package.

2. Preview. The guards run ahead of every resource.

   ```sh
   pulumi preview
   ```

   The package must match its digest, hold `bootstrap` at its root and nothing outside it, and be an arm64 Linux executable built from the command the field wants. The guard compares each catalogue with the archive copy at `catalogue/<source>/<version>`. A changed document under an unchanged version is refused here instead of at writer start. A missing object or bucket compares nothing.

3. Apply.

   ```sh
   pulumi up
   ```

## Verify

The preview shows the function code and layer version updated and nothing replaced. After apply, the first invocation records `audit.writer.started` in the trail with its digests and the layer ARN, and the queue's oldest-message-age alarm stays quiet.

## Roll back

Run `pulumi up` with the previous program. Layer versions are never destroyed (`SkipDestroy`).

## Trap

A function that fails its init is not a failed deploy. The event source mapping keeps invoking it and the ingest queue drains into the dead-letter queue.

## Build from a checkout

Build as the release does, then pass `Guards.AllowVersionSkew` with the digest of the result.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOEXPERIMENT=jsonv2 go build -trimpath -o bootstrap ./cmd/audit-writer-lambda
zip audit-writer-lambda.zip bootstrap && sha256sum audit-writer-lambda.zip
```

If the library moved, read the release's [upgrade page](../upgrade/v0.13.md) first. `just pulumi-test` runs the library's tests on Pulumi mocks: no cloud, credentials or plugin.
