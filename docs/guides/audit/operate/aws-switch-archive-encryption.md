# Switch the archive encryption from a KMS key to `aws-managed` or `s3`

Change `Archive.Encryption` on a running installation without making old objects unreadable.

## Before you start

- The installation runs with `Encryption: kms` (the default) and holds objects.

- Keep a principal of your own that can use the old archive key, for the copy.

- Record why the AWS-managed key or SSE-S3 satisfies your key policy. See [the encryption table](../../../reference/audit/aws-pulumi-library.md#encryption).

- S3 does not re-encrypt existing objects. The change sets only the bucket default for new writes.

- The roles lose their grant on the old key. The writer and observe cannot read old objects until you rewrite them.

- The library neither creates nor deletes a KMS key: the old key is the estate's own. Do not remove it yet.

- A copy makes a new version. Old versions keep their Object Lock retention under the old key, so the key must outlive the longest of them.

## Steps

1. Preview the change. It updates the bucket default encryption and drops the roles' grants on the old key.

2. Apply, then copy each object over itself so it is rewritten under the new default.

   ```sh
   aws s3 cp s3://<bucket>/<prefix>/ s3://<bucket>/<prefix>/ --recursive \
     --sse aws:kms --metadata-directive REPLACE
   ```

   For a large bucket use S3 Batch Operations "Copy". `REPLACE` drops user metadata unless you restate it, and S3 refuses a copy that changes nothing. Set the new versions' retention to match, so nothing is shortened.

3. Check that the current versions report `ServerSideEncryption: aws:kms` and no `SSEKMSKeyId` of the old key, then verify the trail.

   ```sh
   aws s3api head-object --bucket <bucket> --key <key>
   audit verify
   ```

4. Schedule the old key's deletion only after every version it encrypted has expired or been rewritten. An earlier deletion makes those objects permanently unreadable.

## Roll back

None after step 4. Record the date and the old key's alias in your installation's decision record before you schedule the deletion.
