# Backup

The backup module writes the installation, encrypted, to S3 ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md)).

## Configuration

| Key | Meaning |
|---|---|
| `apiVersion` | `sluis.truvity.github.io/sluis-backup/v1`, schema `schemas/config/sluis-backup.schema.json`. Layout v5 only: `secrets.layout: v5`, `ports.dynamodb.tables`, the installation's `ports.blob` |
| `backup.role` | `backup` (default) or `restore`: which function the zip is. The restore function's role may write every module's table |
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

## Restore

| Command | Does |
|---|---|
| all | The restore role of this zip; on Lambda it is `backup.role: restore`. Take `--config <file>` and `--json`. Only an administrator or the break-glass role may invoke it |
| `sluis restore preview <id>` | Per module and section: records to create, overwrite, leave, skip. Names, never values. Writes nothing, takes no lease |
| `sluis restore start <id> --confirm <instance> [--overwrite] [--by] [--note]` | Restores. A destination that holds different data is refused unless `--overwrite` |
| `sluis restore start --resume` | Continues a restore that paused |
| `sluis restore status` | The latest restores and the modules still under maintenance |

| Step of a restore | Detail |
|---|---|
| Lease | `lease.restore:run`: one restore at a time. The record is `rec.backup.restore.<id>` (`running`, `paused`, `completed`, `failed`) |
| Maintenance | The flag is set in every module's own table, then 15 seconds pass for the modules' cache |
| Write | Verify the archive, write, read every record back. A pause starts the next invocation itself |
| Lift | Only after a clean read-back |
| Failure | The flag stays. Reasons: `not-empty`, `verify`, `archive`, `integrity`, `key`, `maintenance`, `clear`. Run again to retry |

| Event or method | Callers | Answers |
|---|---|---|
| `{"kind":"restore","backup":"<id>","overwrite":false}` | admin, breakglass; invoke asynchronously | `completed`, `paused`, `failed`, `contended` or `refused`. A failed restore does not fail the invocation |
| `{"kind":"restore","resume":true}` | the function itself, admin, breakglass | Continues the unfinished restore; `idle` when none |
| `{"kind":"restore","backup":"<id>","preview":true}` | admin, breakglass; invoke synchronously | The preview report |
| `restore.status` | console, admin, breakglass | `latest`, `unfinished`, `lastCompleted`, `maintenance` |
| Audit | `roster.restore.started`, `.completed`, `.failed` | Counts and a reason, never a value |

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

## On Kubernetes

| Value | Does |
|---|---|
| `backup.enabled` | A CronJob for `sluis backup run` (`backup.schedule`, `concurrencyPolicy: Forbid`) and one for `sluis backup prune` (`backup.prune`), as their own account (`backup.serviceAccount`) |
| `backup.config` | The `sluis-backup/v1` document of this page, rendered as it stands. The chart refuses a missing `backup.target`, a missing key, `backup.role: restore`, a layout other than v5 and a missing `ports.dynamodb.tables` |
| `restore.enabled` | One Job, `sluis restore start <backupId> --confirm <confirm>` or `--resume`, as an account of its own. Never a CronJob. `confirm` must be `backup.config.instance` |
| Example | `charts/sluis/examples/backup-v5.yaml`. The chart has one Deployment, not one per module |
| `config.secrets.layout: v5` | The service on layout v5 needs `config.ports.dynamodb.tables`, and the chart then projects no Secret: its secrets are in SSM |
