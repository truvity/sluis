# Keep the blobs on an S3-compatible store (R2)

Keep the Blob port (status reports, directory snapshots) in a bucket of an S3-compatible store such as Cloudflare R2 or MinIO. The service document names static credentials by address and never holds them.

## Before you start

- You need a bucket and a key pair that can read, write, list and delete in it.

- Secrets must be on the `ssm` source ([secrets](../../../reference/sluis/secrets.md)), because the address is in its internal namespace.
- `credentialsRef` without `endpoint` is refused. On AWS S3 the identity is the platform's: Pod Identity, IRSA or a Lambda role.

## Steps

1. Choose an address `internal/<kind>/<id>`: two lower-case segments, for example `internal/blobs/r2`. Any other address is refused at load.

2. Seed the document at `<secrets.root>/internal/blobs/r2` out of band, for example with `aws ssm put-parameter --type SecureString` for the SSM source. It follows the [`s3-credentials/v1` schema](https://github.com/truvity/sluis/blob/master/schemas/internal/s3-credentials.v1.schema.json).

   ```json
   {"schema": "s3-credentials/v1", "access_key_id": "<key id>", "secret_access_key": "<secret>"}
   ```

3. Point the service document at it. `region` defaults to `auto`, which R2 signs with; set it for a store that wants one.

   ```yaml
   ports:
     blob:
       adapter: s3
       s3:
         bucket: example-sluis
         endpoint: https://<account>.r2.cloudflarestorage.com
         pathStyle: true
         credentialsRef: internal/blobs/r2
   ```

   The Pulumi library writes this block from `StorageArgs.Blobs` ([Pulumi library](../../../reference/sluis/pulumi-library.md)).

## Verify

The start log has `blobs are kept in S3` with the bucket and prefix. After the first reconcile, `GET <endpoint>/<bucket>?list-type=2&prefix=<prefix>` shows `reports/` objects.

## Rotate the key

The service reads the document at start. An unreadable or malformed document stops the start and names the address, never a value. After a 403 it rereads the document and retries once, at most once a minute. Put the new document in place and the service follows within a minute of its next refused request.

The Blob writer sends no `Content-Encoding`, and the client computes a checksum only where an operation requires one. R2's refusal of an SDK checksum together with a content encoding does not arise.

## Mint credentials instead

A `credentialsRef` needs no `cloudflare` section and no minter token. To have sluis mint short-lived R2 credentials, set `credentials: {preset: ...}`, exclusive with `credentialsRef`: see [mint short-lived Cloudflare tokens and R2 credentials](../cloudflare-tokens.md). Audit's S3-compatible archive takes its own static `credentials` the same way.
