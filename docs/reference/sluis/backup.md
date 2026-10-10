# Backup

The backup module writes the installation, encrypted, to S3 ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md)).

## Configuration

| Key | Meaning |
|---|---|
| `apiVersion` | `sluis.truvity.github.io/sluis-backup/v1`, schema `schemas/config/sluis-backup.schema.json`. Layout v5 only: `secrets.layout: v5`, `ports.dynamodb.tables`, the installation's `ports.blob` |
| `backup.key` | The alias that seals each backup's data key; the same as `keys.archive`, give one. The adapter is `keys.adapter`, default `kms` |
| `backup.target` | The archive bucket: `bucket`, `prefix`, `region`, `kmsKey`, `endpoint`, `pathStyle`. Credentials are the role's. Object Lock retention must not exceed `retention.maxAge` |
| `backup.retention` | The `keep` newest backups (default 7) always stay. Another goes when older than `maxAge` (default 720h) |
| `instance` | Names the archive path `backup/<instance>/<id>/` and the key's encryption context |

## Commands

| Command | Does |
|---|---|
| all | Take `--config <file>` and `--json`. A run that finds the lease held, or the module under maintenance, says so and exits 0 |
| `sluis backup run [--resume]` | A backup under the lease `lease.backup:run`. `--resume` only continues a paused run |
| `sluis backup list` | The backups in the archive, newest first, from their unverified manifests |
| `sluis backup status` | The latest runs and the last prune |
| `sluis backup prune [--dry-run]` | Applies the retention rule: manifest first, then chunks, then those of runs with no manifest after 7 days. A refused delete is counted, not retried |

## Runs

| Record | Content |
|---|---|
| `rec.backup.run.<id>` | State (`running`, `paused`, `completed`, `failed`), counts, an error line, the checkpoint while unfinished. The id is `20261010T020000Z-3fa9c1`: UTC start and a suffix |
| `rec.backup.retention` | The last prune: kept, removed, cleaned, refused |
| Pause | Under three minutes left, a run pauses at a unit boundary; the next continues it for 24 hours |

## Calls

| Event or method | Callers | Answers |
|---|---|---|
| `{"kind":"backup"}` | EventBridge Scheduler, daily | `outcome`: `completed`, `paused`, `idle`, `contended` or `maintenance`. A failed run fails the invocation |
| `{"kind":"backup","resume":true}` | EventBridge Scheduler, every few minutes | Continues a paused run; one State read when none |
| `backup.status` | console, admin, breakglass | `latest`, `lastCompleted`, `unfinished`, `retention` |
| `backup.list` | console, admin, breakglass | `backups`, newest first; `limit` |
| `backup.run` | admin, breakglass | A run's result; `resume` only continues |
| Audit | `roster.backup.completed`, `.failed`, `.pruned` ([audit actions](audit-actions.md)) | Counts and a reason, never a value |
