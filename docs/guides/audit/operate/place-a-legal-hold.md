# Place and release a legal hold

Keep objects under a prefix undeletable for as long as a matter lasts, whatever their retention says.

## Before you start

- Use the record tier (Object Lock). On the no-lock tier `audit hold place` is refused and records the attempt.

- The operator's role needs `s3:PutObjectLegalHold`, `s3:GetObjectLegalHold`, `s3:ListBucket` and `s3:PutObject` on `holds/`. Releasing needs the break-glass role. You also need a writer to record through (`--sink`).

- A reason is required.

- A hold covers one prefix within one installation: `records/<profile>/`, or with `--tenant`, `records/<profile>/<tenant>/`. It has no expiry.

- The bucket policy refuses `s3:PutObjectLegalHold` with `OFF` to everyone but the break-glass role. A refused release is still recorded as `audit.hold.released`.

- The command records after the hold changes. If the writer cannot take the record, the command says the hold is placed or released and you must record it by hand.

- `place` sweeps existing objects. The writer sets the hold on later objects, re-reading active holds every minute. It refuses to start if it cannot read them once, and a failed refresh keeps the last answer. Its role needs `s3:PutObjectLegalHold` (on, never off) and read on `holds/`.

- List holds before you erase a tenant's keys.

## Steps

1. List what exists.

   ```
   audit hold list [--profile <p>] --bucket <b>
   ```

2. Place the hold.

   ```
   audit hold place --profile <p> [--tenant <t>] --reason <why> --by <who> --bucket <b> --sink <writer>
   ```

   Every existing object under the prefix gets a legal hold, `holds/<id>/placed.json` exists and `audit.hold.placed` is recorded.

3. Release it, with the break-glass role.

   ```
   audit hold release --id <id> --by <who> --bucket <b> --sink <writer>
   ```

   This writes `holds/<id>/released.json` and records `audit.hold.released`.

## Verify

`audit hold list` shows the hold.

## Roll back

Release the hold with step 3. To undo a release, place the hold again: it is a new hold with a new id.

## Afterwards

Tell the matter's owner which hold ids exist. Holds are append-only records in the archive, under the same lock as everything else.
