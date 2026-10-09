# Prepare the bucket

Create a bucket on the right tier, with a prefix for this installation.

## Before you start

- Pick the tier from the profiles: [which profiles to compose](../../../concepts/audit/which-profiles-to-compose.md).

- Enable Object Lock at creation. On a versioned AWS bucket you can turn it on later, one way: see [turn on the lock](aws-turn-on-object-lock.md).

- Choose the prefix once: changing it moves every key. `prefix: ""` forecloses a second installation.

- The writer reads `schema/` on every start. Allow get as well as put, or it loops on 403.

## Choose the tier

| tier | preset | the store must answer | enough for |
|---|---|---|---|
| record | `attested` (compliance Object Lock) | `PutObject` with lock headers, `PutObjectRetention`, `PutObjectLegalHold`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | every profile |
| no lock | `operational` or `standard` | `PutObject`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | profiles from framework profiles that demand no lock: `security`, `history`, `billing-nl` |

## Steps

1. Create the bucket. For the no-lock tier, omit the last flag.

   ```sh
   aws s3api create-bucket --bucket example-audit \
     --create-bucket-configuration LocationConstraint=eu-example-1 \
     --object-lock-enabled-for-bucket
   ```

   Configure the bucket once per environment:

   - Versioning on, and a default compliance retention equal to the shortest profile's.

   - SSE-KMS with a customer-managed key. The writer encrypts, the query service and verify role decrypt.

   - Public access blocked, and access logging on.

   - A policy denying delete, shortening of the lock configuration and `s3:BypassGovernanceRetention` to everyone.

   - Cross-region replication to another account, with retention metadata.

2. Set the installation's prefix once per preset in the deployment document. Filter lifecycle rules on `<prefix>/records/<profile>/`, one per profile per application.

   ```yaml
   presets:
     standard: {bucket: audit-eu-example-1, region: eu-example-1, prefix: audit/<application>/}
   ```

3. Scope each role's IAM to the prefix. See [archive prefixes and IAM](../../../reference/audit/archive-prefixes-and-iam.md).

## No-lock tier

The writer sends no lock header and `audit hold place` is refused. Replace the lock with:

- The notary, whose seals under a managed key prove what the archive holds ([0061](../../../decisions/0061-seals.md)).

- No delete permission on any component, and versioning where the store offers it.

- A bucket no-delete rule where the store has one. It is not compliance mode.

- Retention as a lifecycle rule.

`audit verify --deployment <file>` reports each object as `unlocked`. Under a profile that demands a lock it is `INVALID`.

## Verify

On the record tier, `aws s3api get-object-lock-configuration` shows `Enabled`. A test put under `audit/<application>/` succeeds and a delete is refused. Then `audit verify` reports the objects.

## Roll back

Delete an empty bucket; one with versions under compliance retention cannot be deleted. Remove the prefix's roles.

## Also

Keep exports in a separate bucket without Object Lock and expire `export/` by lifecycle. For an S3-compatible store, see [put the archive on an S3-compatible store](archive-on-r2.md).

## Decided in

[0053 One installation per service](../../../decisions/0053-one-installation-per-service-or-product.md), [0056 Lock modes and store tiers](../../../decisions/0056-lock-modes-and-store-tiers.md).
