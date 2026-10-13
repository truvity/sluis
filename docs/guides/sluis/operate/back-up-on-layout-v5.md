# Back up and restore on layout v5

Run the backup module: one age-encrypted `tar.gz` a day in the archive bucket, a restore you can rehearse, and a key that opens a backup without sluis.

## Before you start

- Set `backup.recipients` before the backup function starts. A backup role with no recipient writes nothing.

- Losing every identity loses every backup. Keep two, held apart: the installation's in SSM and one offline.

- `NewRestore` needs `NewBackup` of the same instance in the stack, one admin role and one break-glass role. An account root is refused.

## 1. Make the break-glass key

```sh
age-keygen -o breakglass.txt     # the identity: keep the file off the platform
age-keygen -y breakglass.txt     # prints the recipient, age1..., for the configuration
```

The Pulumi library generates the installation's own keypair and keeps that identity at `/sluis/<instance>/internal/backup/restore-identity`.

## 2. Deploy the backup and the restore

```go
common := sluispulumi.BackupCommon{
	Region: "eu-central-1", AccountID: accountID, Instance: "acme",
	Storage: store.Grant(), BlobBucketName: "acme-sluis-blobs",
	State:   states.Grant(), ParameterKeyArn: secretsKeyArn,
	Archive: sluispulumi.ArchiveArgs{Bucket: "acme-sluis-archive"}, // Create makes it, under Object Lock
}
backup, _ := sluispulumi.NewBackup(ctx, "acme", &sluispulumi.BackupArgs{
	BackupCommon:    common,
	ExtraRecipients: []string{"age1..."}, // step 1
}, pulumi.Providers(awsProvider))
_, _ = sluispulumi.NewRestore(ctx, "acme", &sluispulumi.RestoreArgs{
	BackupCommon:    common,
	RestoreInvokers: sluispulumi.RestoreInvokers{AdminRoleArns: adminArns, BreakglassRoleArns: breakglassArns},
}, pulumi.Providers(awsProvider))
ctx.Export("backupRecipients", backup.Recipients)
```

Admit `args.KeyPolicyStatements` in the shared key's policy and the roles in the audit queue's ([Pulumi library](../../../reference/sluis/pulumi-library.md#backup)).

| Platform | Do |
|---|---|
| Lambda | The arguments above; `Archive.Create` makes the bucket; `Schedule.Daily` is `cron(0 2 * * ? *)` |
| Kubernetes | `backup.enabled: true`; `backup.config.backup.recipients` lists both public keys; seed the second identity as `internal/backup/restore-identity`, `{"value":"<base64 of the key file>"}` (`charts/sluis/examples/backup-v5.yaml`) |

## 3. Take a backup

```sh
sluisctl backup run --function sluis-<instance>-backup --region <region>
sluisctl backup list --function sluis-<instance>-backup --region <region>
```

Expect one id, such as `20261013T020000Z-ab12cd`, and in the bucket `backup/<instance>/<id>.tar.gz.age` with its `<id>.json` sidecar.

## 4. Verify a backup

```sh
sluis backup verify <id> --config backup.yaml --identity breakglass.txt
```

Run it where the archive is readable, with the backup document. Without `--identity` it checks the size and SHA-256 against the sidecar only.

## 5. Restore

```sh
sluisctl restore preview <id> --function sluis-<instance>-restore --region <region>
sluisctl restore start <id> --confirm <instance> --function sluis-<instance>-restore --region <region> --wait
```

The preview names what a restore creates, overwrites, leaves and skips. A destination that holds different data is refused without `--overwrite`. A restore is a rotation: restart afterwards ([rotate keys and credentials](rotate-keys-and-credentials.md)).

## 6. Open a backup without sluis

```sh
aws s3 cp "s3://<bucket>/<prefix>backup/<instance>/<id>.tar.gz.age" .
age -d -i breakglass.txt <id>.tar.gz.age | tar xz
```

`manifest.json` lists the SHA-256 of every member. `<module>/secrets.jsonl` holds that module's secrets in clear.

## Retention and alarms

| Signal | Meaning |
|---|---|
| `backup.retention` | the `keep` newest (7) stay; another goes past `maxAge` (720h). `sluis backup prune --dry-run` prints the plan |
| `<function>-no-backup-36h` | no backup was written for 36 hours |
| `<function>-failed-run` | the backup function errored |
| `<function>-schedule-dlq` | a schedule event was not delivered |
| `<restore function>-invoked` | a restore was invoked |
| `roster.backup.completed`, `.failed`, `.pruned`, `roster.restore.*` | the audit trail: counts and a reason, never a value |

## Verify

`sluisctl backup status` shows `lastCompleted` within a day. `sluis backup verify` with the offline identity passes.

## Roll back

A failed restore leaves the maintenance flag on the module: run it again, or `sluis maintenance off --force` once it is abandoned. To rotate the installation's identity, `pulumi up --replace` on `<name>-restore-identity-seed`; older backups then open with the offline identity only.

## Decided in

[ADR 0071](../../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../../decisions/0072-storage-layout-v5-module-first.md), [ADR 0073](../../../decisions/0073-backup-format-2-age-encrypted-tar.md).
