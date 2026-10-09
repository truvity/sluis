# `audit verify`: what it checks

The checks on records and seals, the flags and the output. The auditor's procedure is [verify the trail](../../guides/audit/operate/verify-the-trail.md).

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --prefix audit/<application> [--json]
```

The range is of ingest time: from the hour `--from` is in, up to but not including the hour `--to` is in. Give a date, an hour (`2026-09-17T10`) or a full timestamp. The command needs only the archive and read-only credentials.

Name an application's `--prefix` to verify only its records. Verify one profile per run.

## Record checks

For every object under `records/<profile>/` in the range, in key order, against the [bucket contract](bucket-contract.md):

| Check | Passes when |
|---|---|
| Key | It has the contract's grammar: profile, tenant, ingest hour, ULID |
| Metadata | `format` is `1`; `sha256` and `count` are present and well formed |
| Bytes | The SHA-256 of the stored bytes is the object's `sha256` |
| Records | The object decompresses, has `count` lines, and each `hash` is the SHA-256 of its `record`'s canonical encoding |
| Lock | Given the deployment: an object without retention is `unlocked` under a profile that demands no lock and `INVALID` under one that does ([0056](../../decisions/0056-lock-modes-and-store-tiers.md)) |

These checks cannot show that nothing was removed or added beside the objects. Seals show that ([0061](../../decisions/0061-seals.md)); `--root` checks them.

## Seal checks

```
audit verify --profile security --from 2026-09-01 --to 2026-09-17 \
  --bucket <name> --prefix audit/<application> \
  --root <thumbprint>[,<thumbprint>...]
```

With `--root` the command checks the seals of every tenant of the profile. The thumbprints are the only keys it trusts: get them from the operator out of band (`audit key public --thumbprint`). Nothing in the bucket, `keys/roots.jwks` included, can add one.

| Check | Passes when |
|---|---|
| Exists | Each due hour has a seal. An hour is due once it has ended and the settle window (`--settle`, default 10m) and grace (`--grace`, default 1h) have passed. Quiet hours have a seal too, from the tenant's first object on. A missing seal finds a removed seal or a stopped notary |
| Signed | By a pinned root, or a key it delegated to for this profile and tenant, within the delegation window (at most 25 hours), not revoked as of the seal. The bucket's write time is checked beside `sealed_at` |
| Chains | `prev` is the SHA-256 of the previous hour's seal. A tenant's first seal has none |
| Current | Count, first and last key and Merkle root, recomputed from the bucket, match. An added, removed or changed object breaks the seal, including one whose metadata was made to agree |
| Lock | Given the deployment: the seal is locked as long as the records it covers |

A range in the middle of a chain checks its first seal against the seal before the range.

| Finding | Meaning |
|---|---|
| `seal.signature`, `seal.chain`, `seal.root`, `seal.missing`, `seal.key`, `seal.payload`, `seal.jws`, `seal.lock`, `keys.roots`, `keys.statement` | The rule broken. Each finding also names the seal's key and the tenant |

## Output

| Item | Value |
|---|---|
| Lines | One per object and per seal: `valid`, `unlocked` or `INVALID: <reason>` |
| Summary | Objects and records checked |
| Exit code | Non-zero on any invalid entry. `unlocked` is information |

<!-- generated: verify-flags -->
| flag | what |
|---|---|
| `--profile <p>` | required; the profile to check |
| `--from`, `--to` | the range of ingest time, `--to` not included |
| `--last 24h` | the objects ingested in the last this long, ending at the hour that has closed; instead of `--from` and `--to` |
| `--prefix <p>` | the installation's prefix in the bucket, as `archive.prefix` of the chart's configuration names it; omit only where the installation is at the bucket's root |
| `--root <thumbprints>` | also check the seals, trusting only the root keys with these thumbprints, comma separated; `--settle` and `--grace` set when a seal is due |
| `--sink <writer>` | record what was checked through the writer. The scheduled job does; an auditor's run by hand should not |
| `--deployment <file>` | the profile configuration, so the check knows what lock the profile demands. The scheduled job passes it; an auditor without it still gets keys, bytes and hashes checked, with nothing said about locks |
| `--json` | print the report as JSON |
| `--endpoint`, `--path-style` | an S3-compatible store that is not AWS, as [put the archive on an S3-compatible store](../../guides/audit/operate/archive-on-r2.md) has it |
<!-- /generated -->

## Scheduled job

The nightly in-cluster run is `audit verify --config`; see [configuration: the jobs](configuration-jobs.md#audit-verify). When it checks seals (`seals.roots`), it records `audit.seal.verified` or `audit.seal.failed` per ingest hour through the writer. The target is `records/<profile>/<yyyy>/<mm>/<dd>/<hh>`. A run that checked no seal records none.

The archive written before the v1 layout is not read. Verify it with the previous release's CLI (v0.6.x).

For a failed verification, see [investigate a failed verification](../../guides/audit/operate/investigate-a-failed-verification.md).
