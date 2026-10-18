package sluispulumi

import (
	"errors"
	"fmt"
	"strings"
)

// The Sids of the archive grants of the backup and restore roles.
const (
	sidArchiveObjects = "SluisArchiveObjects"
	sidArchiveList    = "SluisArchiveList"
	sidArchiveSSEKey  = "SluisArchiveSSEKey"
	sidRestoreSelf    = "SluisRestoreSelf"
	// ArchivePurpose is the `purpose` of the encryption context of the archive
	// key: the runtime's default context is {instance, purpose}.
	ArchivePurpose = "archive"
)

// ArchiveGrant is what the roles need to name the archive: the bucket, the
// path of the installation's backups in it and the keys.
type ArchiveGrant struct {
	// BucketArn is the archive bucket; it may be in another account.
	BucketArn string
	// Prefix is `backup.target.prefix` with a trailing slash, or "".
	Prefix string
	// Instance names the archive path `<Prefix>backup/<Instance>/` and the
	// archive key's encryption context.
	Instance string
	// KeyArn is the former archive key.
	//
	// Deprecated: backups are encrypted with age (format v2), not with a KMS
	// key. The field is accepted and ignored: no statement names it.
	KeyArn string
	// SSEKeyArn is the key of server-side encryption of the bucket's objects
	// (`backup.target.kmsKey`), if the bucket does not use S3's own.
	SSEKeyArn string
	// Versioned adds the version actions: a bucket under Object Lock is.
	Versioned bool
}

// ArchivePath is the key prefix of the installation's backups, with the
// trailing slash: `<prefix>backup/<instance>/`.
func (g ArchiveGrant) ArchivePath() string { return g.Prefix + "backup/" + g.Instance + "/" }

func (g ArchiveGrant) validate() error {
	var errs []error
	if g.BucketArn == "" {
		errs = append(errs, errors.New("the BucketArn is required"))
	}
	if g.Instance == "" {
		errs = append(errs, errors.New("the Instance is required"))
	}
	if g.Prefix != "" && (strings.HasPrefix(g.Prefix, "/") || !strings.HasSuffix(g.Prefix, "/")) {
		errs = append(errs, fmt.Errorf("the Prefix %q ends in a slash and does not start with one", g.Prefix))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sluispulumi: ArchiveGrant: %w", err)
	}
	return nil
}

// sseKeyStatement is the bucket's own key, through S3 only.
func (g ArchiveGrant) sseKeyStatement(actions []string) []statement {
	if g.SSEKeyArn == "" {
		return nil
	}
	return []statement{{
		"Sid": sidArchiveSSEKey, "Effect": "Allow", "Action": actions, "Resource": g.SSEKeyArn,
		"Condition": map[string]any{"StringLike": map[string]any{"kms:ViaService": "s3.*.amazonaws.com"}},
	}}
}

func (g ArchiveGrant) listStatement(versions bool) statement {
	acts := []string{s3ListBucket}
	if versions {
		acts = append(acts, "s3:ListBucketVersions")
	}
	return statement{
		"Sid": sidArchiveList, "Effect": "Allow", "Action": acts, "Resource": g.BucketArn,
		"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{g.ArchivePath() + "*"}}},
	}
}

// backupArchiveStatements is the backup role's use of the archive: put, list,
// get and delete under the installation's path, the versions of those objects
// when the bucket is versioned, and the bucket's own SSE key. Nothing outside
// the path, and no key that opens a backup: they are age-encrypted to the
// installation's recipients.
func backupArchiveStatements(g ArchiveGrant) []statement {
	acts := []string{s3GetObject, s3PutObject, s3DeleteObject}
	if g.Versioned {
		acts = append(acts, "s3:GetObjectVersion")
	}
	st := []statement{
		{"Sid": sidArchiveObjects, "Effect": "Allow", "Action": acts, "Resource": g.BucketArn + "/" + g.ArchivePath() + "*"},
		g.listStatement(g.Versioned),
	}
	return append(st, g.sseKeyStatement([]string{kmsDecrypt, kmsGenerateDK})...)
}

// restoreArchiveStatements is the restore role's read of the archive: get and
// list under the installation's path (the age identity that opens a backup is
// [restoreIdentityAllow]'s). No put, no delete.
func restoreArchiveStatements(g ArchiveGrant) []statement {
	acts := []string{s3GetObject}
	if g.Versioned {
		acts = append(acts, "s3:GetObjectVersion")
	}
	st := []statement{
		{"Sid": sidArchiveObjects, "Effect": "Allow", "Action": acts, "Resource": g.BucketArn + "/" + g.ArchivePath() + "*"},
		g.listStatement(g.Versioned),
	}
	return append(st, g.sseKeyStatement([]string{kmsDecrypt})...)
}

// BackupRoleStatements is the statement list of the backup function's role: the
// module pattern of [ModuleRoleStatements] for the role [RoleBackup] (its own
// table, the read-all cross-grant on the other five modules, the explicit deny
// of the maintenance flag's writes on every table, so that it reads the flag in
// its own table and never writes one) and the archive.
//
// env.Roles and env.FunctionArns are not needed: the role invokes nothing.
func BackupRoleStatements(env ModuleEnv, g ArchiveGrant) ([]map[string]any, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	st, err := ModuleRoleStatements(env, Role{Name: RoleBackup, Hosts: []Module{ModuleBackup}})
	if err != nil {
		return nil, err
	}
	st = append(st, backupArchiveStatements(g)...)
	st = append(st, restoreIdentityDeny(env)...)
	if err := uniqueSids(st); err != nil {
		return nil, err
	}
	return st, nil
}

// ArchiveBucketPolicyStatements are the statements of the archive bucket's own
// policy that let the roles use it. A bucket in the stack's account needs none,
// because the roles' policies allow; a bucket in another account needs these
// (and, with a customer-managed key for its objects, the key's policy naming
// the roles) on the bucket's side, which the owner of that account applies. The
// backup role may put, list, get and delete under the installation's path, the
// restore role (when its ARN is given) get and list; nothing else is named.
func ArchiveBucketPolicyStatements(g ArchiveGrant, backupRoleArn, restoreRoleArn string) ([]map[string]any, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	if backupRoleArn == "" {
		return nil, errors.New("sluispulumi: ArchiveBucketPolicyStatements: the backup role's ARN is required")
	}
	var out []map[string]any
	named := func(prefix, role string, src []statement) {
		for _, s := range src {
			c := statement{}
			for k, v := range s {
				c[k] = v
			}
			c["Sid"] = prefix + strings.TrimPrefix(s["Sid"].(string), "Sluis")
			c["Principal"] = map[string]any{"AWS": role}
			out = append(out, c)
		}
	}
	// The first two statements of each list are the bucket's (objects and
	// listing); the key statements are the key policy's business.
	named("SluisBackup", backupRoleArn, backupArchiveStatements(g)[:2])
	if restoreRoleArn != "" {
		named("SluisRestore", restoreRoleArn, restoreArchiveStatements(g)[:2])
	}
	return out, nil
}

// ArchiveGuardActions are what only the break-glass roles may do to the archive
// bucket's lock: shorten a governance retention, change or remove the lock
// configuration, and delete a version (a locked version is not deleted until its
// retention ends, and an unlocked one is the backup, gone for good).
var ArchiveGuardActions = []string{"s3:BypassGovernanceRetention", "s3:DeleteObjectVersion", "s3:PutBucketObjectLockConfiguration"}

// ArchiveBucketGuardStatement is the bucket-policy statement that keeps the
// archive's lock to the break-glass roles: a Deny of ArchiveGuardActions on the
// bucket and its objects to every principal not matched by breakglass. A role
// with a broad S3 allow (a power user, a StackSet or automation role) can then
// neither shorten the lock nor delete a backup's version. The backup and restore
// roles do not use these actions: the prune deletes the object (a delete marker)
// and the lifecycle expires the versions, which no principal's policy governs.
// breakglass are role ARNs (CheckKeyAdmin), at least one.
func ArchiveBucketGuardStatement(bucketArn string, breakglass []string) (map[string]any, error) {
	if bucketArn == "" {
		return nil, errors.New("sluispulumi: ArchiveBucketGuardStatement: the bucket's ARN is required")
	}
	exempt, err := keyAdminSet(breakglass)
	if err != nil {
		return nil, fmt.Errorf("sluispulumi: the archive bucket's guard: %w", err)
	}
	return statement{
		"Sid": "SluisArchiveLockBreakglassOnly", "Effect": "Deny", "Principal": "*",
		"Action": append([]string(nil), ArchiveGuardActions...), "Resource": []string{bucketArn, bucketArn + "/*"},
		"Condition": map[string]any{"ArnNotLike": map[string]any{condPrincipalArn: exempt}},
	}, nil
}

// RestoreRoleStatements is the statement list of the restore function's role:
// the module pattern for the role [RoleRestore] (its own table, which holds
// `rec.backup.restore.*` and `lease.restore:*`, and the write-all cross-grant on
// the other five modules' tables, parameters and blob prefixes; no deny of the
// maintenance flag: this role alone writes it), the archive for reading, and
// lambda:InvokeFunction on the ARNs it continues itself through.
//
// A restore that pauses invokes the very ARN (alias included) it was invoked
// through, so selfArns are the aliases of the callers allowed to start one.
func RestoreRoleStatements(env ModuleEnv, g ArchiveGrant, selfArns []string) ([]map[string]any, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	if len(selfArns) == 0 {
		return nil, errors.New("sluispulumi: RestoreRoleStatements: the role continues itself through its own aliases: none given")
	}
	st, err := ModuleRoleStatements(env, Role{Name: RoleRestore, Hosts: []Module{ModuleBackup}})
	if err != nil {
		return nil, err
	}
	st = append(st, restoreArchiveStatements(g)...)
	st = append(st, restoreIdentityAllow(env)...)
	st = append(st, statement{
		"Sid": sidRestoreSelf, "Effect": "Allow", "Action": lambdaInvokeFunction, "Resource": sortedStrings(selfArns),
	})
	if err := uniqueSids(st); err != nil {
		return nil, err
	}
	return st, nil
}
