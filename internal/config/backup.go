//nolint:lll // messages are prose
package config

import (
	"errors"
	"fmt"
	"net/netip"
	neturl "net/url"
	"time"

	"filippo.io/age"
	policyconfig "github.com/truvity/policy/config"

	"github.com/truvity/sluis/storage/keys"
)

// SluisBackup is the backup module's service document (apiVersion
// sluis.truvity.github.io/sluis-backup/v1, schemas/config/sluis-backup.schema.json).
// It holds what the module reads and nothing of the issuer, the console or a
// policy: the storage it reads an installation from (`ports`, `secrets`), the
// audit trail, the age recipients a backup is encrypted to, and the bucket it
// is written to.
type SluisBackup struct {
	APIVersion string `json:"apiVersion"`
	// Instance names the installation: the path of its backups in the archive
	// bucket and the default encryption context of the archive key.
	Instance string `json:"instance,omitempty"`
	Log      *Log   `json:"log,omitempty"`
	// Keys is the key service block every product shares. Since format 2 the
	// backup module uses none of it: `keys.archive` is accepted for one release
	// and ignored (deprecated).
	Keys     *keys.Config             `json:"keys,omitempty"`
	Secrets  *Secrets                 `json:"secrets,omitempty"`
	Ports    *Ports                   `json:"ports,omitempty"`
	Platform *Platform                `json:"platform,omitempty"`
	Preset   string                   `json:"preset,omitempty"`
	Adapters map[string]AdapterChoice `json:"adapters,omitempty"`
	Audit    *Audit                   `json:"audit,omitempty"`
	Backup   Backup                   `json:"backup"`
}

// The roles of the backup module's zip. The binary is one; the document picks
// the function it is deployed as (docs/decisions/0072, point 10).
const (
	// BackupRoleBackup is the scheduled export, the default.
	BackupRoleBackup = "backup"
	// BackupRoleRestore is the restore function, which only an administrator or
	// the break-glass role may invoke.
	BackupRoleRestore = "restore"
)

// Backup is `backup`.
type Backup struct {
	// Role is [BackupRoleBackup] (the default) or [BackupRoleRestore].
	Role string `json:"role,omitempty"`
	// Recipients are the age X25519 public keys (age1...) every backup is
	// encrypted to. The backup role requires at least one.
	Recipients []string `json:"recipients,omitempty"`
	// Verify is how the restore role proves a backup is the one the backup
	// function made. Unset, a restore refuses every backup that is not started
	// with allowUnverified.
	Verify *BackupVerify `json:"verify,omitempty"`
	// Identity names the restore role's secret that holds the age identity
	// (AGE-SECRET-KEY-1...): internal/backup/<identity> in the secret store,
	// [BackupDefaultIdentity] when unset.
	Identity string `json:"identity,omitempty"`
	// Key and Region were the KMS archive key of format 1. Deprecated: they
	// are accepted for one release and ignored.
	Key    *keys.Entry `json:"key,omitempty"`
	Region string      `json:"region,omitempty"`
	// Target is the bucket the archive is written to.
	Target BackupTarget `json:"target"`
	// Retention is how many backups are kept and for how long.
	Retention *BackupRetention `json:"retention,omitempty"`
}

// BackupVerify is `backup.verify`: where the audit trail is and how to ask it.
type BackupVerify struct {
	// Query is the audit installation's query service: https, or http on a
	// loopback address.
	Query string `json:"query"`
	// TokenFile holds the bearer token the query service accepts.
	TokenFile string `json:"tokenFile"`
	// Profile is the audit profile that keeps the `activity` category;
	// "activity" when unset.
	Profile string `json:"profile,omitempty"`
	// Observer is the id the audit writer stamps on the backup function's
	// records. A record stamped by any other is refused. Required unless
	// AnyObserver is set.
	Observer string `json:"observer,omitempty"`
	// AnyObserver accepts a record stamped by anyone. Any principal that may
	// send to the audit queue, or write as the roster, can then plant a record
	// that vouches for a forged backup; a start logs a warning.
	AnyObserver bool `json:"anyObserver,omitempty"`
}

// VerifyQueryURL refuses a query-service address that is not https, or http
// on a loopback host: the token and the answer cross it.
func VerifyQueryURL(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("the audit installation's query service, an https address")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return nil
		}
		if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("http is for a loopback address only: use https")
	}
	return errors.New("the audit installation's query service, an https address")
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
	// Checksum is the integrity header of every upload: sha256 (the default),
	// md5 (Content-MD5) or none, for a store that refuses SHA-256.
	Checksum string `json:"checksum,omitempty"`
}

// BackupRetention is the retention rule of [Backup.Retention].
type BackupRetention struct {
	// Keep is how many of the newest completed backups are always kept.
	Keep *int `json:"keep,omitempty"`
	// MaxAge is how old a backup beyond Keep may be before it is deleted.
	MaxAge *Duration `json:"maxAge,omitempty"`
}

// BackupDefaultIdentity is the restore identity's secret when backup.identity
// names none.
const BackupDefaultIdentity = "restore-identity"

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

// RoleOf is the role the document is deployed as: `backup.role`, else backup.
func (s *SluisBackup) RoleOf() string {
	if s.Backup.Role == "" {
		return BackupRoleBackup
	}
	return s.Backup.Role
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
	switch s.Backup.Role {
	case "", BackupRoleBackup, BackupRoleRestore:
	default:
		return fmt.Errorf("backup.role: %q is not %s or %s", s.Backup.Role, BackupRoleBackup, BackupRoleRestore)
	}
	if s.Backup.Target.Bucket == "" {
		return errors.New("backup.target.bucket: required")
	}
	if keep, maxAge := s.Backup.Retention.Rule(); keep < 1 || keep > BackupMaxKeep || maxAge < 0 {
		return fmt.Errorf("backup.retention: keep is 1 to %d and maxAge is not negative", BackupMaxKeep)
	}
	if v := s.Backup.Verify; v != nil {
		if err := VerifyQueryURL(v.Query); err != nil {
			return fmt.Errorf("backup.verify.query: required: %w", err)
		}
		if v.Observer == "" && !v.AnyObserver {
			return errors.New("backup.verify.observer: required: the id the audit writer stamps on the backup function's records " +
				"(or set anyObserver: true, which lets any sender of audit records vouch for a backup)")
		}
		if v.Observer != "" && v.AnyObserver {
			return errors.New("backup.verify: observer and anyObserver are one or the other")
		}
		if v.TokenFile == "" {
			return errors.New("backup.verify.tokenFile: required: the query service answers nobody it cannot name")
		}
	}
	// The backup role encrypts to the recipients; the restore role needs
	// none, but a list it is given is still held to the form.
	if s.RoleOf() == BackupRoleBackup || len(s.Backup.Recipients) > 0 {
		if err := checkRecipients(s.Backup.Recipients); err != nil {
			return err
		}
	}
	return nil
}

// checkRecipients refuses an empty list and anything that is not an age
// X25519 public key.
func checkRecipients(rs []string) error {
	if len(rs) == 0 {
		return errors.New("backup.recipients: required: at least one age X25519 public key (age1...) every backup is encrypted to")
	}
	for i, r := range rs {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return fmt.Errorf("backup.recipients[%d]: not an age X25519 public key (age1...)", i)
		}
	}
	return nil
}

// Deprecated reports the keys the document sets that format 2 ignores
// (backup.key, backup.region, keys.archive), for a warning at start.
func (s *SluisBackup) Deprecated() []string {
	var out []string
	if s.Backup.Key != nil {
		out = append(out, "backup.key")
	}
	if s.Backup.Region != "" {
		out = append(out, "backup.region")
	}
	if s.Keys != nil {
		if _, ok := s.Keys.Keys[keys.Archive]; ok {
			out = append(out, "keys.archive")
		}
	}
	return out
}

// IdentityRef is the name of the restore identity's secret.
func (s *SluisBackup) IdentityRef() string {
	if s.Backup.Identity != "" {
		return s.Backup.Identity
	}
	return BackupDefaultIdentity
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
