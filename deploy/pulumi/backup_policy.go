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
	sidArchiveKey     = "SluisArchiveKey"
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
	// KeyArn is the archive key (`backup.key`, purpose `archive`).
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
	if g.KeyArn == "" {
		errs = append(errs, errors.New("the KeyArn is required"))
	}
	if g.Prefix != "" && (strings.HasPrefix(g.Prefix, "/") || !strings.HasSuffix(g.Prefix, "/")) {
		errs = append(errs, fmt.Errorf("the Prefix %q ends in a slash and does not start with one", g.Prefix))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("sluispulumi: ArchiveGrant: %w", err)
	}
	return nil
}

// archiveKeyStatement is the use of the archive key under the runtime's default
// context {instance, purpose: archive} and no other context key.
func (g ArchiveGrant) archiveKeyStatement(actions []string) statement {
	return statement{
		"Sid": sidArchiveKey, "Effect": "Allow", "Action": actions, "Resource": g.KeyArn,
		"Condition": map[string]any{
			"StringEquals": map[string]any{
				"kms:EncryptionContext:instance": g.Instance,
				"kms:EncryptionContext:purpose":  ArchivePurpose,
			},
			"ForAllValues:StringEquals": map[string]any{"kms:EncryptionContextKeys": contextKeys},
		},
	}
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
// when the bucket is versioned, and the archive key to seal and open a data
// key. Nothing outside the path.
func backupArchiveStatements(g ArchiveGrant) []statement {
	acts := []string{s3GetObject, s3PutObject, s3DeleteObject}
	if g.Versioned {
		acts = append(acts, "s3:GetObjectVersion")
	}
	st := []statement{
		{"Sid": sidArchiveObjects, "Effect": "Allow", "Action": acts, "Resource": g.BucketArn + "/" + g.ArchivePath() + "*"},
		g.listStatement(g.Versioned),
		g.archiveKeyStatement([]string{kmsDecrypt, kmsGenerateDK}),
	}
	return append(st, g.sseKeyStatement([]string{kmsDecrypt, kmsGenerateDK})...)
}

// restoreArchiveStatements is the restore role's read of the archive: get and
// list under the installation's path and the archive key to open a data key.
// No put, no delete, no key to seal.
func restoreArchiveStatements(g ArchiveGrant) []statement {
	acts := []string{s3GetObject}
	if g.Versioned {
		acts = append(acts, "s3:GetObjectVersion")
	}
	st := []statement{
		{"Sid": sidArchiveObjects, "Effect": "Allow", "Action": acts, "Resource": g.BucketArn + "/" + g.ArchivePath() + "*"},
		g.listStatement(g.Versioned),
		g.archiveKeyStatement([]string{kmsDecrypt}),
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
