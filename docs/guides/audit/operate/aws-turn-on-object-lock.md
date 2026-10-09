# Turn the Object Lock on, and move to COMPLIANCE

Take an installation from no lock (`NONE`) to `GOVERNANCE`, then to `COMPLIANCE` as a new installation.

## Before you start

- Set `Archive.ObjectLockMode`. It has no default.

- Turning the lock on is one way. S3 never turns it off, and setting `NONE` again only removes the resource from the program.

- Objects written before the switch stay unlocked. Lock them with `PutObjectRetention` or an S3 Batch Operations job.

- `COMPLIANCE` is a new bucket, not an edit. An object keeps the mode it was written with, and nobody, including the account root, can shorten a retention.

- Sign off the retentions, the keys and the lifecycle before `COMPLIANCE`.

## NONE to GOVERNANCE

1. Change `ObjectLockMode` from `auditpulumi.None` to `auditpulumi.Governance` and set `DefaultRetentionDays`, required with a lock.

2. Preview.

   ```sh
   pulumi preview
   ```

   The preview creates one resource, `aws:s3/bucketObjectLockConfiguration`, and updates the two functions (`archive.lockMode` is `governance`) and the two role policies in place. The bucket, its versioning and the keys show no change. A bucket replace means you edited more than the mode: stop.

3. Apply.

   ```sh
   pulumi up
   ```

## GOVERNANCE to COMPLIANCE

1. Run GOVERNANCE until you have seen the retentions, keys and lifecycle behave, and sign them off.

2. Create a second installation with a new component name and a new `BucketName`.

   ```go
   ObjectLockMode:        auditpulumi.Compliance,
   AcknowledgeCompliance: true,
   ```

3. Point the receivers and observe at the new queue and bucket. Empty the trial bucket, or keep it until its retentions lapse. The trial doubles storage for that time.

## Verify

`audit verify` reports a lock that ends sooner than the profile requires. A profile whose framework demands a stricter mode than the bucket's makes the writer refuse to start, so the first rollout shows it.

## Roll back

None for the lock. Before `pulumi up`, revert the edit. The modes are explained in [AWS Lambda](../../../concepts/audit/aws-lambda.md#the-lock-modes).

## Decided in

[0065 Archive retention and lifecycle](../../../decisions/0065-archive-retention-and-lifecycle.md), [0056 Lock modes and store tiers](../../../decisions/0056-lock-modes-and-store-tiers.md).
