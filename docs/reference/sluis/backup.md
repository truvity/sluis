# Backup

The backup module writes the installation, encrypted with age, to S3 ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md), [ADR 0073](../../decisions/0073-backup-format-2-age-encrypted-tar.md)).

## Configuration

| Key | Meaning |
|---|---|
| `apiVersion` | `sluis.truvity.github.io/sluis-backup/v1`, schema `schemas/config/sluis-backup.schema.json`. Layout v5 only: `secrets.layout: v5`, `ports.dynamodb.tables`, the installation's `ports.blob` |
| `backup.role` | `backup` (default) or `restore`: which function the zip is. The restore function's role may write every module's table |
| `backup.recipients` | The age X25519 public keys (`age1...`) every backup is encrypted to. Required in the backup role, at least one |
| `backup.identity` | The restore role's secret holding the age identity (`AGE-SECRET-KEY-1...`): `internal/backup/<identity>`, default `restore-identity`, stored as `{"value":"<base64>"}`. Only the restore role reads it |
| `backup.key`, `backup.region`, `keys.archive` | Deprecated and ignored since format 2; accepted for one release with a warning |
| `backup.target` | The archive bucket: `bucket`, `prefix`, `region`, `kmsKey`, `endpoint`, `pathStyle`. Credentials are the role's. Object Lock retention must not exceed `retention.maxAge` |
| `backup.target.checksum` | The integrity header of every upload, as `ports.blob.s3.checksum`: `sha256` (default, `x-amz-checksum-sha256`, precomputed), `md5` (`Content-MD5`) or `none`. An Object Lock bucket refuses an upload without one |
| `backup.retention` | The `keep` newest backups (default 7) always stay. Another goes when older than `maxAge` (default 720h) |
| `instance` | Names the archive path `backup/<instance>/` |

## Format 2

| Object | Content |
|---|---|
| `backup/<instance>/<id>.tar.gz.age` | The backup: a PAX tar, gzip, age to the recipients, binary. The id is `20261013T020000Z-ab12cd`: UTC start and a suffix |
| `backup/<instance>/<id>.json` | The sidecar, written last: `format`, `instance`, `id`, `created`, `creator`, `layout`, `object` (`key`, `size`, `sha256`), `recipients`, and per module `stateRecords`, `secretRecords`, `blobs`, `blobBytes`. No secret names, no values |

| Tar member, in order | Content |
|---|---|
| `manifest.json` | `format`, `instance`, `id`, `layout`, `created`, `creator`, per module `state` and `secrets` (`records`, `sha256`) and `blobs` (`count`, `bytes`), and `files`: the SHA-256 of every other member |
| `<module>/state.jsonl` | One State or Index record per line. Modules come in the order oidc, github, slack, cloudflare, google |
| `<module>/secrets.jsonl` | One secret document per line |
| `<module>/blobs/<key>` | One blob, its bytes. Sorted by name |
| Every member | Mode 0600, the backup's start as its time, no owner names |
| Blob key escaping | Per `/`-segment: a byte other than a letter, a digit, `-`, `_` or `.` becomes `%XX`. So does a leading `.`. An empty segment is `%` |
| Verification | Object size and SHA-256 against the sidecar, then age, then every member against the manifest and the counts, before the first write |

```sh
# Offline, without sluis:
age -d -i key.txt 20261013T020000Z-ab12cd.tar.gz.age | tar xz
```

## Commands

| Command | Does |
|---|---|
| all | Take `--config <file>` and `--json`. A run that finds the lease held, or the module under maintenance, says so and exits 0 |
| `sluis backup run` | A backup under the lease `lease.backup:run`, in one invocation. `--resume` answers `idle` |
| `sluis backup list` | The backups in the archive, newest first, from their unverified sidecars |
| `sluis backup status` | The latest runs and the last prune |
| `sluis backup prune [--dry-run]` | Applies the retention rule: the sidecar first, then the object; the object of a run with no sidecar after 7 days. A refused delete (Object Lock) is counted, not retried |
| `sluis backup verify <id> [--identity <file>]` | Read-only. Checks the object's size and SHA-256 against its sidecar; with an age identity file, decrypts it and checks every member against the manifest |

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
| Write | Read the identity, download, verify and decrypt the archive, write, read every record back. A pause starts the next invocation itself |
| Lift | Only after a clean read-back |
| Failure | The flag stays. Reasons: `not-empty`, `verify`, `archive`, `integrity`, `key` (the identity does not open the backup), `maintenance`, `clear`. Run again to retry |

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
| `rec.backup.run.<id>` | State (`running`, `completed`, `failed`), counts, the object's size, an error line. A run still `running` when the next takes the lease is closed as `abandoned` |
| `rec.backup.retention` | The last prune: kept, removed, cleaned, refused |
| Not exported | The backup module's own records and its secrets, the restore identity among them |

## Calls

| Event or method | Callers | Answers |
|---|---|---|
| `{"kind":"backup"}` | EventBridge Scheduler, daily | `outcome`: `completed`, `contended` or `maintenance`. A failed run fails the invocation |
| `{"kind":"backup","resume":true}` | a `<function>-resume` schedule left by an earlier release (an apply of v1.76.5 or later deletes it) | `idle`, touching nothing |
| `backup.status` | console, admin, breakglass | `latest`, `lastCompleted`, `unfinished`, `retention` |
| `backup.list` | console, admin, breakglass | `backups`, newest first; `limit` |
| `backup.run` | admin, breakglass | A run's result; `resume` answers `idle` |
| Audit | `roster.backup.completed`, `.failed`, `.pruned` ([audit actions](audit-actions.md)) | Counts and a reason, never a value |

## On Kubernetes

| Value | Does |
|---|---|
| `backup.enabled` | A CronJob for `sluis backup run` (`backup.schedule`, `concurrencyPolicy: Forbid`) and one for `sluis backup prune` (`backup.prune`), as their own account (`backup.serviceAccount`) |
| `backup.config` | The `sluis-backup/v1` document of this page, rendered as it stands. The chart refuses a missing `backup.target`, missing `backup.recipients` for the CronJob, `backup.role: restore`, a layout other than v5 and a missing `ports.dynamodb.tables` |
| `restore.enabled` | One Job, `sluis restore start <backupId> --confirm <confirm>` or `--resume`, as an account of its own. Never a CronJob. `confirm` must be `backup.config.instance` |
| Example | `charts/sluis/examples/backup-v5.yaml`. The chart has one Deployment, not one per module |
| `config.secrets.layout: v5` | The service on layout v5 needs `config.ports.dynamodb.tables`, and the chart then projects no Secret: its secrets are in SSM |
