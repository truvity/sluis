//nolint:lll // messages are prose
package config

import (
	"errors"
	"fmt"
	"time"

	policyconfig "github.com/truvity/policy/config"

	"github.com/truvity/sluis/storage/keys"
)

// SluisBackup is the backup module's service document (apiVersion
// sluis.truvity.github.io/sluis-backup/v1, schemas/config/sluis-backup.schema.json).
// It holds what the module reads and nothing of the issuer, the console or a
// policy: the storage it reads an installation from (`ports`, `secrets`), the
// audit trail, the archive key, and the bucket a backup is written to.
type SluisBackup struct {
	APIVersion string `json:"apiVersion"`
	// Instance names the installation: the path of its backups in the archive
	// bucket and the default encryption context of the archive key.
	Instance string `json:"instance,omitempty"`
	Log      *Log   `json:"log,omitempty"`
	// Keys is the key service block every product shares. The backup module
	// uses its `archive` purpose only, and `backup.key` is the short way to
	// set it.
	Keys     *keys.Config             `json:"keys,omitempty"`
	Secrets  *Secrets                 `json:"secrets,omitempty"`
	Ports    *Ports                   `json:"ports,omitempty"`
	Platform *Platform                `json:"platform,omitempty"`
	Preset   string                   `json:"preset,omitempty"`
	Adapters map[string]AdapterChoice `json:"adapters,omitempty"`
	Audit    *Audit                   `json:"audit,omitempty"`
	Backup   Backup                   `json:"backup"`
}

// Backup is `backup`.
type Backup struct {
	// Key is the key that seals each backup's data key (purpose `archive`): an
	// alias, or {key, context}. It is the same as `keys.archive`; give one.
	Key *keys.Entry `json:"key,omitempty"`
	// Region is the region of the key service, for adapter kms.
	Region string `json:"region,omitempty"`
	// Target is the bucket the archive is written to.
	Target BackupTarget `json:"target"`
	// Retention is how many backups are kept and for how long.
	Retention *BackupRetention `json:"retention,omitempty"`
}

// BackupTarget is the S3 (or S3-compatible) bucket of the archive. Credentials
// are the platform's: the function's role.
type BackupTarget struct {
	Bucket string `json:"bucket"`
	// Prefix is put before `backup/<installation>/` in the bucket.
	Prefix string `json:"prefix,omitempty"`
	Region string `json:"region,omitempty"`
	// KMSKey is the SSE-KMS key of every write; unset leaves the bucket's own
	// default encryption.
	KMSKey    string `json:"kmsKey,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	PathStyle bool   `json:"pathStyle,omitempty"`
}

// BackupRetention is the retention rule of [Backup.Retention].
type BackupRetention struct {
	// Keep is how many of the newest completed backups are always kept.
	Keep *int `json:"keep,omitempty"`
	// MaxAge is how old a backup beyond Keep may be before it is deleted.
	MaxAge *Duration `json:"maxAge,omitempty"`
}

// The retention defaults.
const (
	BackupDefaultKeep   = 7
	BackupDefaultMaxAge = 30 * 24 * time.Hour
	// BackupMaxKeep bounds Keep: a rule of thousands is a typo.
	BackupMaxKeep = 1000
)

// Rule is the retention in force: the defaults fill what is unset.
func (r *BackupRetention) Rule() (keep int, maxAge time.Duration) {
	keep, maxAge = BackupDefaultKeep, BackupDefaultMaxAge
	if r == nil {
		return keep, maxAge
	}
	if r.Keep != nil {
		keep = *r.Keep
	}
	if r.MaxAge != nil {
		maxAge = r.MaxAge.D()
	}
	return keep, maxAge
}

// Name is the installation's name: `instance`, else "sluis".
func (s *SluisBackup) Name() string {
	if s.Instance != "" {
		return s.Instance
	}
	return "sluis"
}

// Validate refuses what the schema cannot say.
func (s *SluisBackup) Validate() error {
	if s.Backup.Target.Bucket == "" {
		return errors.New("backup.target.bucket: required")
	}
	if keep, maxAge := s.Backup.Retention.Rule(); keep < 1 || keep > BackupMaxKeep || maxAge < 0 {
		return fmt.Errorf("backup.retention: keep is 1 to %d and maxAge is not negative", BackupMaxKeep)
	}
	if _, err := s.ArchiveKeys(); err != nil {
		return err
	}
	return nil
}

// ArchiveKeys is the key configuration of the module: the `archive` purpose and
// no other, whether it came from `backup.key` or from `keys.archive`. The
// adapter is `keys.adapter`, or kms. A document that gives the key twice is
// refused: two places for one key is a place to drift.
func (s *SluisBackup) ArchiveKeys() (keys.Config, error) {
	kc := keys.Config{Adapter: "kms", Keys: map[keys.Purpose]keys.Entry{}}
	var given *keys.Entry
	if s.Keys != nil {
		if s.Keys.Adapter != "" {
			kc.Adapter = s.Keys.Adapter
		}
		if e, ok := s.Keys.Keys[keys.Archive]; ok {
			given = &e
		}
	}
	if s.Backup.Key != nil {
		if given != nil {
			return keys.Config{}, errors.New("backup.key and keys.archive both name the archive key: give one")
		}
		given = s.Backup.Key
	}
	if given == nil {
		return keys.Config{}, errors.New("backup.key: required (the key that seals each backup's data key, the same as keys.archive)")
	}
	kc.Keys[keys.Archive] = *given
	if err := kc.Validate(); err != nil {
		return keys.Config{}, err
	}
	return kc, nil
}

// Serve is the part of the document the storage ports and the audit trail are
// opened from, in the shape the shared builders (internal/store, internal/audit)
// read.
func (s *SluisBackup) Serve() *Serve {
	return &Serve{
		APIVersion: APIVersion("serve"), Instance: s.Instance, Log: s.Log, Keys: s.Keys,
		Secrets: s.Secrets, Ports: s.Ports, Platform: s.Platform, Preset: s.Preset, Adapters: s.Adapters, Audit: s.Audit,
	}
}

// BackupAPIVersion is the apiVersion of the backup module's document.
const BackupAPIVersion = Group + "/sluis-backup/v1"

// IsBackup reports whether the file is the backup module's document, by its
// apiVersion alone.
func IsBackup(file string) bool {
	doc, err := read(file)
	return err == nil && doc != nil && doc[policyconfig.APIVersionKey] == BackupAPIVersion
}

// LoadBackup reads the backup module's document, holds it to its schema and to
// [SluisBackup.Validate].
func LoadBackup(file string) (*SluisBackup, error) {
	var out SluisBackup
	kind := policyconfig.Kind{Name: Group + "/sluis-backup", Version: 1, Schema: schemaFor("sluis-backup")}
	if err := policyconfig.LoadKind(file, kind, &out); err != nil {
		return nil, err
	}
	if err := out.Validate(); err != nil {
		return nil, &policyconfig.Error{File: file, Err: err}
	}
	return &out, nil
}
