package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"filippo.io/age"

	"github.com/truvity/sluis/internal/port"
)

// Version is the format version this release writes and reads.
const Version = 2

// Errors a caller can match with errors.Is.
var (
	// ErrFormat is an archive this release cannot read: no sidecar, not a
	// manifest, an unknown version, or a field out of range.
	ErrFormat = errors.New("backup: unreadable archive")
	// ErrIntegrity is an archive that was changed or truncated: the object's
	// size or SHA-256, its decryption, a member's hash or a count did not
	// match.
	ErrIntegrity = errors.New("backup: archive failed verification")
	// ErrKey is an archive the identity does not open: it is not one of the
	// recipients, or the identity could not be read. Nothing was read.
	ErrKey = errors.New("backup: the identity does not open the archive")
)

// Section is one of the kinds of content a module contributes.
type Section string

// The sections, in archive order.
const (
	State   Section = "state"
	Secrets Section = "secrets"
	Blobs   Section = "blobs"
)

// Sections lists every section in archive order.
func Sections() []Section { return []Section{State, Secrets, Blobs} }

// Valid reports whether s is one of [Sections].
func (s Section) Valid() bool { return s == State || s == Secrets || s == Blobs }

// Layout names the storage layout of the installation that was backed up.
type Layout string

// The layouts a backup can describe.
const (
	LayoutV4 Layout = "v4"
	LayoutV5 Layout = "v5"
)

// Valid reports whether l is a known layout.
func (l Layout) Valid() bool { return l == LayoutV4 || l == LayoutV5 }

// ManifestFile is the name of the tar's first member.
const ManifestFile = "manifest.json"

// Manifest is manifest.json, the first member of the tar.
type Manifest struct {
	// Format is the archive format version (see [Version]).
	Format int `json:"format"`
	// Instance and ID name the backup; they are also in its object key.
	Instance string `json:"instance"`
	ID       string `json:"id"`
	// Layout is the storage layout of the source installation.
	Layout Layout `json:"layout"`
	// Created is the start of the backup, UTC, RFC 3339.
	Created string `json:"created"`
	// Creator names what wrote it, for example "sluis-backup 1.76.1".
	Creator string `json:"creator"`
	// Modules lists the modules in the backup in [port.Modules] order, each
	// once.
	Modules []ModuleEntry `json:"modules"`
	// Files is the SHA-256 (hex) of every other member, by member name.
	Files map[string]string `json:"files"`
}

// ModuleEntry is one module's members.
type ModuleEntry struct {
	Module  port.Module `json:"module"`
	State   Lines       `json:"state"`
	Secrets Lines       `json:"secrets"`
	Blobs   BlobsEntry  `json:"blobs"`
}

// Lines is one JSONL member: its record count and SHA-256.
type Lines struct {
	Records int64  `json:"records"`
	SHA256  string `json:"sha256"`
}

// BlobsEntry counts a module's blob members.
type BlobsEntry struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// Module returns the entry of a module, or nil when the backup does not hold
// it.
func (m *Manifest) Module(mod port.Module) *ModuleEntry {
	for i := range m.Modules {
		if m.Modules[i].Module == mod {
			return &m.Modules[i]
		}
	}
	return nil
}

// Records is the number of records (lines, or blobs) of a section across all
// modules.
func (m *Manifest) Records(s Section) int64 {
	var n int64
	for _, e := range m.Modules {
		switch s {
		case State:
			n += e.State.Records
		case Secrets:
			n += e.Secrets.Records
		case Blobs:
			n += e.Blobs.Count
		}
	}
	return n
}

// Sidecar is <id>.json, written after the object: what a listing shows and
// what a reader checks the object against. It names no secret.
type Sidecar struct {
	Format     int             `json:"format"`
	Instance   string          `json:"instance"`
	ID         string          `json:"id"`
	Created    string          `json:"created"`
	Creator    string          `json:"creator"`
	Layout     Layout          `json:"layout"`
	Object     ObjectInfo      `json:"object"`
	Recipients []string        `json:"recipients"`
	Modules    []SidecarModule `json:"modules"`
}

// ObjectInfo is the encrypted object as stored.
type ObjectInfo struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// SidecarModule is one module's counts.
type SidecarModule struct {
	Module        port.Module `json:"module"`
	StateRecords  int64       `json:"stateRecords"`
	SecretRecords int64       `json:"secretRecords"`
	Blobs         int64       `json:"blobs"`
	BlobBytes     int64       `json:"blobBytes"`
}

// Records is every record and blob the sidecar counts.
func (s *Sidecar) Records() int64 {
	var n int64
	for _, m := range s.Modules {
		n += m.StateRecords + m.SecretRecords + m.Blobs
	}
	return n
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether s is acceptable as an instance name or a backup
// id: one path segment of letters, digits, dot, dash and underscore.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Root is the prefix of every backup of an instance.
func Root(instance string) string { return "backup/" + instance + "/" }

// The suffixes of a backup's two objects.
const (
	ObjectSuffix  = ".tar.gz.age"
	SidecarSuffix = ".json"
)

// ObjectName is the key of a backup's encrypted object.
func ObjectName(instance, id string) string { return Root(instance) + id + ObjectSuffix }

// SidecarName is the key of a backup's sidecar.
func SidecarName(instance, id string) string { return Root(instance) + id + SidecarSuffix }

// StateFile is the member name of a module's State records.
func StateFile(m port.Module) string { return string(m) + "/state.jsonl" }

// SecretsFile is the member name of a module's secrets.
func SecretsFile(m port.Module) string { return string(m) + "/secrets.jsonl" }

// BlobFile is the member name of one blob of a module.
func BlobFile(m port.Module, key string) string {
	return string(m) + "/blobs/" + EscapeKey(key)
}

// ParseRecipients parses age X25519 recipients (age1...). At least one is
// required.
func ParseRecipients(in []string) ([]age.Recipient, error) {
	if len(in) == 0 {
		return nil, errors.New("backup: no recipients: backup.recipients needs at least one age1... public key")
	}
	out := make([]age.Recipient, 0, len(in))
	for i, s := range in {
		r, err := age.ParseX25519Recipient(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("backup: recipient %d is not an age X25519 public key (age1...)", i)
		}
		out = append(out, r)
	}
	return out, nil
}

// Fingerprint is the lower-case hex SHA-256 of an age recipient (the age1...
// string, trimmed). It is what the audit record of a backup names the
// recipients by.
func Fingerprint(recipient string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(recipient)))
	return hex.EncodeToString(sum[:])
}

// Fingerprints are the [Fingerprint] of each recipient, sorted and without
// repeats: two lists name the same recipients when their fingerprints are
// equal.
func Fingerprints(recipients []string) []string {
	out := make([]string, 0, len(recipients))
	for _, r := range recipients {
		out = append(out, Fingerprint(r))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ParseIdentities parses an age identity file: one or more AGE-SECRET-KEY-1...
// lines, with # comments and blank lines allowed (age-keygen's output).
func ParseIdentities(text string) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil {
		// The parser's message may quote the line; the line is the secret.
		return nil, fmt.Errorf("%w: the identity is not an age identity file (AGE-SECRET-KEY-1...)", ErrKey)
	}
	return ids, nil
}

func hex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validate checks a sidecar against the place it was read from.
func (s *Sidecar) validate(instance, id string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: sidecar: %s", ErrFormat, fmt.Sprintf(format, a...))
	}
	if s.Format != Version {
		return bad("format %d is not readable by this release (reads %d)", s.Format, Version)
	}
	if s.Instance != instance || s.ID != id {
		return fmt.Errorf("%w: the sidecar is for %s/%s, read as %s/%s", ErrIntegrity, s.Instance, s.ID, instance, id)
	}
	if !s.Layout.Valid() {
		return bad("layout %q is unknown", s.Layout)
	}
	if s.Object.Key != ObjectName(instance, id) || s.Object.Size <= 0 || !hex64(s.Object.SHA256) {
		return bad("the object is not described")
	}
	seen := map[port.Module]bool{}
	for _, e := range s.Modules {
		if !e.Module.Valid() || seen[e.Module] {
			return bad("module %q is unknown or listed twice", e.Module)
		}
		seen[e.Module] = true
	}
	return nil
}

// validate checks a decoded manifest against its sidecar.
func (m *Manifest) validate(sc *Sidecar) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: manifest: %s", ErrFormat, fmt.Sprintf(format, a...))
	}
	if m.Format != Version {
		return bad("format %d is not readable by this release (reads %d)", m.Format, Version)
	}
	if m.Instance != sc.Instance || m.ID != sc.ID || m.Layout != sc.Layout || m.Created != sc.Created {
		return fmt.Errorf("%w: the manifest is for %s/%s and differs from its sidecar", ErrIntegrity, m.Instance, m.ID)
	}
	seen := map[port.Module]bool{}
	last := -1
	order := map[port.Module]int{}
	for i, mod := range port.Modules() {
		order[mod] = i
	}
	for _, e := range m.Modules {
		if !e.Module.Valid() || seen[e.Module] {
			return bad("module %q is unknown or listed twice", e.Module)
		}
		if order[e.Module] < last {
			return bad("module %q is out of order", e.Module)
		}
		last = order[e.Module]
		seen[e.Module] = true
		if e.State.Records < 0 || e.Secrets.Records < 0 || e.Blobs.Count < 0 || e.Blobs.Bytes < 0 {
			return bad("module %q has a negative count", e.Module)
		}
		if m.Files[StateFile(e.Module)] != e.State.SHA256 || m.Files[SecretsFile(e.Module)] != e.Secrets.SHA256 {
			return bad("module %q: the hashes of its record files differ from the file list", e.Module)
		}
	}
	for name, sum := range m.Files {
		if name == ManifestFile || !hex64(sum) {
			return bad("file %q is malformed", name)
		}
	}
	// The counts a listing showed must be the counts restored.
	if len(sc.Modules) != len(m.Modules) {
		return fmt.Errorf("%w: the sidecar lists %d modules, the manifest %d", ErrIntegrity, len(sc.Modules), len(m.Modules))
	}
	for i, e := range m.Modules {
		s := sc.Modules[i]
		if s.Module != e.Module || s.StateRecords != e.State.Records || s.SecretRecords != e.Secrets.Records ||
			s.Blobs != e.Blobs.Count || s.BlobBytes != e.Blobs.Bytes {
			return fmt.Errorf("%w: the sidecar's counts of %s differ from the manifest's", ErrIntegrity, e.Module)
		}
	}
	return nil
}
