# Back up and restore the installation

Run the backup module: a daily age-encrypted `tar.gz` in the archive bucket, a restore you can rehearse, and a key that opens a backup without sluis.

## Before you start

- Set `backup.recipients` before the backup function starts: with none it writes nothing.

- Losing every identity loses every backup. Keep two apart: the installation's in SSM and one offline.

- `NewRestore` needs `NewBackup` of the same instance in the stack, an admin role and a break-glass role. An account root is refused.

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
	Recipients:      backup.Recipients,
	RestoreInvokers: sluispulumi.RestoreInvokers{AdminRoleArns: adminArns, BreakglassRoleArns: breakglassArns},
	Recipients:      backup.Recipients, // backup.recipients: the sidecar is compared with them
	Verify: &sluispulumi.RestoreVerifyArgs{ // backup.verify, step 5
		Query: "https://audit.example", TokenFile: "/tmp/audit/token", Observer: "<the backup function's id>",
	},
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
sluisctl login --issuer https://access.example   # once; saves where the functions are
sluisctl backup run              # through the issuer: no AWS credentials, but a group of backup.runGroups
sluisctl backup list
sluisctl backup run --direct     # your own AWS credentials, with or without a sign-in
```

```yaml
# policy: who may start a backup through the issuer
backup:
  runGroups: ["all:acme:operator"]
```

Expect one id, such as `20261013T020000Z-ab12cd`, and `backup/<instance>/<id>.tar.gz.age` with its `<id>.json` sidecar in the bucket.

## 4. Verify a backup

```sh
sluis backup verify <id> --config backup.yaml --identity breakglass.txt
```

Run it where the archive is readable. Without `--identity` it checks only the size and SHA-256 against the sidecar.

## 5. Restore

```sh
sluisctl restore preview <id>                           # through the issuer when signed in
sluisctl restore start <id> --confirm <instance> --wait # always your own AWS credentials
```

The preview names what a restore creates, overwrites, leaves and skips. Different data at the destination needs `--overwrite`. A restore is a rotation: restart afterwards ([rotate keys and credentials](rotate-keys-and-credentials.md)).

A restore checks the backup against the sealed audit trail ([provenance](../../../reference/sluis/backup.md#provenance)). On Kubernetes, set `backup.verify` in the restore document. On Lambda, `NewRestore` renders `Recipients` and `Verify`. Until the audit library renders the writer's senders table, a verified restore on Lambda needs `Verify.AnyObserver`.

```yaml
backup:
  role: restore
  recipients: ["age1..."]
  verify: {query: "https://audit.example", tokenFile: /var/run/secrets/audit/token, observer: "<the backup function's id>"}
```

The notary must seal a backup's hour first.

## 6. Open a backup without sluis

```sh
aws s3 cp "s3://<bucket>/<prefix>backup/<instance>/<id>.tar.gz.age" .
age -d -i breakglass.txt <id>.tar.gz.age | tar xz
```

`manifest.json` lists the SHA-256 of every member. `<module>/secrets.jsonl` holds secrets in clear.

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

`sluisctl backup status` shows `lastCompleted` within a day.

## When the audit trail is down

With the trail unreachable, a restore is refused as `unverified`. `--allow-unverified` restores on the bucket's word alone and needs `backup.recipients`. `sluisctl` has no such flag.

```sh
sluis restore start <id> --confirm <instance> --allow-unverified   # Kubernetes, with the restore document
```

## Roll back

A failed restore leaves the maintenance flag on: run it again, or `sluis maintenance off --force` once abandoned. To rotate the installation's identity, run `pulumi up --replace` on `<name>-restore-identity-seed`; older backups then open with the offline identity only.

## Decided in

[ADR 0071](../../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md), [ADR 0072](../../../decisions/0072-storage-layout-v5-module-first.md), [ADR 0073](../../../decisions/0073-backup-format-2-age-encrypted-tar.md).
