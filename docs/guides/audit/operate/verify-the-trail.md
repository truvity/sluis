# Verify the trail

Check from the archive alone that each record object is the one written, each record is the one hashed, and, with seals, that nothing was removed or added.

## Before you start

- Use read-only credentials scoped to the installation's prefix and your own copy of the `audit` binary. You need the bucket and the `--prefix` (its `archive.prefix`).

- For seals, get the thumbprints of the notary's root keys out of band, from `audit key public --thumbprint` on the operator's side. Nothing in the bucket, `keys/roots.jwks` included, can add one.

- Verify each profile on its own: two profiles are two runs.

- Without `--root` the command checks bytes and hashes only. It cannot show a removed or added object: seals do ([0061](../../../decisions/0061-seals.md)).

- A seal is due after `--settle` (default 10m) and `--grace` (default 1h). A quiet hour has a seal too, so a missing one is a fault.

- On a profile that demands no lock, `unlocked` is information. Under one that does, a missing lock is `INVALID`. Pass `--deployment` for the check to know.

- Do not pass `--sink` by hand: it records the run through the writer, which only the scheduled job should do.

- An archive written before the v1 layout is not read here. Use the v0.6.x CLI.

## Steps

1. Check records.

   ```
   audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
     --bucket <name> --prefix audit/<application> [--json]
   ```

   The output is one line per object, `valid`, `unlocked` or `INVALID: <reason>`, and a summary. The exit code is non-zero on any invalid entry.

2. Check the seals as well.

   ```
   audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
     --bucket <name> --prefix audit/<application> \
     --root <thumbprint>[,<thumbprint>...]
   ```

   The output adds a line per seal.

3. For a store that is not AWS, add `--endpoint` and `--path-style`. See [put the archive on an S3-compatible store](archive-on-r2.md).

## Verify

The summary's counts match the range you asked for and no `seal.*` finding appears. A finding is an incident: see [investigate a failed verification](investigate-a-failed-verification.md).

## Roll back

None. The command only reads.

## See also

The in-cluster job (`audit verify --config`) checks the previous day and records `audit.seal.verified` or `audit.seal.failed` per ingest hour. A run that checked no seal records none. Every flag is in the [`audit verify` reference](../../../reference/audit/verify-command.md).
