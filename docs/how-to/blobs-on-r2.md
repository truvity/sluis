# Keep the blobs on an S3-compatible store (R2)

## Purpose

Keep the Blob port (status reports, directory snapshots) in a bucket of an S3-compatible store such as Cloudflare R2 or
MinIO, with static credentials the installation's secrets store holds. The credentials are named by an address in the
service document and never written in it.

## Preconditions

- A bucket on the store, and an access key pair that can read, write, list and delete in it.
- The installation's secrets on layout `v4` or `transition` (`secrets.layout`, [secrets](../reference/secrets.md)), because
  the address is in the internal namespace of that layout.

## Steps

1. Choose an internal address, `internal/<kind>/<id>`: two lower-case segments, for example `internal/blobs/r2`. Any other
   address (an `external/` one, a path with `..`, another depth) is refused at load.
2. Seed the document at `<secrets.root>/internal/blobs/r2` out of band (the store's own tooling, for example `aws ssm
   put-parameter --type SecureString` for the SSM source). It is JSON of the `s3-credentials/v1` schema
   ([schema](https://github.com/truvity/sluis/blob/master/schemas/internal/s3-credentials.v1.schema.json)):

   ```json
   {"schema": "s3-credentials/v1", "access_key_id": "<key id>", "secret_access_key": "<secret>"}
   ```

3. Point the service document at it:

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

   `region` defaults to `auto` (what R2 signs with) when `credentialsRef` is set; name it for a store that wants one. The
   Pulumi library writes this block from `StorageArgs.Blobs` ([Pulumi library](../reference/pulumi-library.md)).

## Behaviour

- The credentials are read once at start; an unreadable or malformed document stops the start, naming the address and
  never a value.
- After an answer of 403 the service reads the document again and retries the request once, at most once a minute. Rotating
  the key is therefore: put the new document, and the service follows within a minute of its next refused request.
- `credentialsRef` without `endpoint` is refused: on AWS S3 the identity is the platform's (Pod Identity, IRSA, a Lambda
  role), not a key pair.
- The Blob writer sends no `Content-Encoding` (snapshots are gzip bytes in the body), and the client computes a checksum
  only where an operation requires one, so R2's refusal of an SDK checksum together with a content encoding does not arise.

## Check

The start log has `blobs are kept in S3` with the bucket and prefix, and `GET <endpoint>/<bucket>?list-type=2&prefix=<prefix>`
shows `reports/` objects after the first reconcile.
