# Backup

The backup module writes the installation to S3, age-encrypted ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md), [ADR 0073](../../decisions/0073-backup-format-2-age-encrypted-tar.md)).

## Configuration

| Key | Meaning |
|---|---|
| `apiVersion` | `sluis.truvity.github.io/sluis-backup/v1`, schema `schemas/config/sluis-backup.schema.json`. Layout v5 only: `secrets.layout: v5`, `ports.dynamodb.tables`, the installation's `ports.blob` |
| `backup.role` | `backup` (default) or `restore`: which function the zip is. The restore function's role may write every module's table |
| `backup.recipients` | The age X25519 public keys (`age1...`) every backup is encrypted to. Required in the backup role, at least one. In the restore role they are the recipients a backup's sidecar must list, no more and no fewer; see [Provenance](#provenance) |
| `backup.verify` | Restore role: where the audit trail is and how to ask it. `query` (the query service's address) and `tokenFile` are required; `profile` (default `activity`) and `observer` are optional |
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
| `sluis restore preview <id> [--allow-unverified]` | Per module and section: records to create, overwrite, leave, skip. Names, never values. Writes nothing, takes no lease |
| `sluis restore start <id> --confirm <instance> [--overwrite] [--allow-unverified] [--by] [--note]` | Restores. A destination that holds different data is refused unless `--overwrite`. A backup the sealed audit trail does not vouch for is refused unless `--allow-unverified` |
| `sluis restore start --resume` | Continues a restore that paused |
| `sluis restore status` | The latest restores and the modules still under maintenance |

| Step of a restore | Detail |
|---|---|
| Lease | `lease.restore:run`: one restore at a time. The record is `rec.backup.restore.<id>` (`running`, `paused`, `completed`, `failed`) |
| Maintenance | The flag is set in every module's own table, then 15 seconds pass for the modules' cache |
| Verify | Download the backup and check it against the audit trail ([Provenance](#provenance)) before the identity is read |
| Write | Read the identity, decrypt the archive, verify every member, write, read every record back. A pause starts the next invocation itself |
| Lift | Only after a clean read-back |
| Failure | The flag stays. Reasons: `not-empty`, `verify`, `archive`, `integrity`, `key` (the identity does not open the backup), `maintenance`, `clear`. Run again to retry |
| Refusal | A start that changes nothing is `refused`: `backup` (no such backup), `key`, `unverified` (the audit trail does not vouch for it), `recipients` (the sidecar lists other recipients than `backup.recipients`) |

| Event or method | Callers | Answers |
|---|---|---|
| `{"kind":"restore","backup":"<id>","overwrite":false,"allowUnverified":false}` | admin, breakglass; invoke asynchronously | `completed`, `paused`, `failed`, `contended` or `refused`. A failed restore does not fail the invocation |
| `{"kind":"restore","resume":true}` | the function itself, admin, breakglass | Continues the unfinished restore; `idle` when none |
| `{"kind":"restore","backup":"<id>","preview":true}` | admin, breakglass; invoke synchronously | The preview report. `allowUnverified` applies |
| `restore.status` | console, admin, breakglass | `latest`, `unfinished`, `lastCompleted`, `maintenance` |
| `restore.preview` | console, admin, breakglass | The preview report as counts per module and section. Without record names; a refusal carries no cause. The backup is verified; there is no `allowUnverified` here |
| Audit | `roster.restore.started`, `.completed`, `.failed` | Counts and a reason, never a value. `unverified` and `verification` when `allowUnverified` was used |

## Provenance

A restore asks the audit trail whether a backup is genuine, because the archive bucket cannot say.

| Step | Rule |
|---|---|
| Record | `roster.backup.completed` carries `object_key`, `object_size`, `object_sha256`, `sidecar_sha256` and `recipients` (the SHA-256 of each `age1...`). It is written after the sidecar |
| Lookup | The query service, `backup.verify.query`, searches `backup.verify.profile` for `roster.backup.completed` records whose target is the backup |
| Match | A record exists, every record agrees, and its installation, object key, size, hashes and recipients are those of the fetched backup. An old record without hashes never matches |
| Sealed | The hour holding each record has a seal. Before that the restore is refused with `not sealed yet`: wait for the notary and run it again |
| Observer | With `backup.verify.observer`, the writer must have stamped the record with that id |
| Recipients | With `backup.recipients`, the sidecar lists only those, verified or not. No flag overrides it |
| Order | All of this happens before the identity is read |
| Token | The query token's grants must let it search and get on `backup.verify.profile` |
| Sealed means | A seal object exists for the hour, as the query service reports. `audit verify --root` checks the seal's signature |
| Rotation | After `backup.recipients` changes, an older backup is refused. Set the earlier list to restore it, or use `--allow-unverified` |

| Case | Outcome |
|---|---|
| `backup.verify` unset, query service down, no record, a mismatch, an unsealed hour | `refused`, reason `unverified`. Nothing is changed and nothing is recorded but a log line |
| `--allow-unverified` | The restore goes ahead. It needs `backup.recipients`. `roster.restore.started` and `.completed` carry `unverified: true` and `verification`: `unattested`, `mismatch`, `unsealed` or `unavailable` |
| A restore that paused | Continues against the hashes it began with and does not ask the trail again. A backup replaced while it was paused is refused |

## Routes

| Route | Credentials | Commands | Who may |
|---|---|---|---|
| Through the issuer | The `sluisctl login` token. The issuer calls the module as `console`, over the module transport the installation already uses | `backup run\|status\|list`, `restore preview\|status` | Status, list and `restore status`: the viewer role. `restore preview`: the operator role. `backup run`: a group in `backup.runGroups` of the policy |
| Direct | The caller's own AWS credentials, as `admin` or `breakglass` | Every command | IAM on the function's `live-<class>` alias |
| Direct only | The same | `restore start`, `restore resume` | The same. They work when the issuer is broken or lost |

| Policy key | Meaning |
|---|---|
| `backup.runGroups` | Groups of the policy whose holders may start a backup through the issuer. Unset lets nobody. The operators group is not implied |

| Detail | Behavior |
|---|---|
| Served | The console's `BackupService`, to the access token of a live `sluisctl` session and to no other service. A token of a session that ended, of an agent session, of another client, or an ID token is refused |
| Maintenance | `RunBackup` is refused while the backup module is under maintenance |
| Start | `RunBackup` hands the run to the module as an asynchronous call (a Lambda event invocation, or a background call to a Service) and answers `started` with the issuer's clock. `sluisctl` reads `GetBackupStatus` until the run ends, or stops with `--no-wait` |
| Throttle | `contended` when a run is already going, or when the same person started one in the last 2 minutes (per issuer process; the lease is the guard across replicas) |
| Policy | `backup.runGroups` is read when the issuer starts, like the rest of the policy. Removing a group takes a restart |
| Restore | No call of the console or the issuer starts or resumes a restore |

## Discovery

| Detail | Behavior |
|---|---|
| Endpoint | `GET /.well-known/sluis-configuration` on the issuer |
| Callers | A person signed in at `sluisctl`: the access token of a live, non-agent session at that client. Anyone else, and an issuer without `console.backup`, gets `404` |
| `issuer` | The issuer's URL |
| `instance` | `instance`, else `release`, else `sluis`. `restore start --confirm` must equal it |
| `backup.endpoint`, `restore.endpoint` | A URI whose scheme names the transport. `arn:aws:lambda:<region>:<account>:function:<name>` is the only one `sluisctl` reaches. `k8s://` and `https://` are reserved, and refused as a usage error |
| `region` | The region of a Lambda endpoint |
| Not listed | A bare function name in `console.backup.function`, and `console.backup.url`: a laptop cannot invoke either |
| `sluisctl` | `login` saves the document in `backup:` of `config.yaml`. A direct command fetches it again, and falls back to the saved copy with a note on stderr |

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
| `backup.run` | console, admin, breakglass | A run's result; `resume` answers `idle`. From `console`, `by` names the person: the issuer checked their group first, and the audit records name them. The issuer starts it as an asynchronous call and does not wait. From `admin` and `breakglass`, `by` is ignored and the actor is the workload `lambda:<class>` |
| Audit | `roster.backup.completed`, `.failed`, `.pruned` ([audit actions](audit-actions.md)) | Counts and a reason, never a value. A completed backup also names its hashes and recipients' fingerprints |

## On Kubernetes

| Value | Does |
|---|---|
| `backup.enabled` | A CronJob for `sluis backup run` (`backup.schedule`, `concurrencyPolicy: Forbid`) and one for `sluis backup prune` (`backup.prune`), as their own account (`backup.serviceAccount`) |
| `backup.config` | The `sluis-backup/v1` document of this page, rendered as it stands. The chart refuses a missing `backup.target`, missing `backup.recipients` for the CronJob, `backup.role: restore`, a layout other than v5 and a missing `ports.dynamodb.tables` |
| `restore.enabled` | One Job, `sluis restore start <backupId> --confirm <confirm>` or `--resume`, as an account of its own. Never a CronJob. `confirm` must be `backup.config.instance` |
| Example | `charts/sluis/examples/backup-v5.yaml`. The chart has one Deployment, not one per module |
| `config.secrets.layout: v5` | The service on layout v5 needs `config.ports.dynamodb.tables`, and the chart then projects no Secret: its secrets are in SSM |
