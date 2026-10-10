package backup

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/truvity/sluis/internal/port"
)

// Version is the format version this release writes.
const Version = 1

// Readable reports whether this release opens an archive of format version v:
// the current version and the one before it.
func Readable(v int) bool {
	return v == Version || (Version > 1 && v == Version-1)
}

// Errors a caller can match with errors.Is.
var (
	// ErrFormat is an archive this release cannot read: not a manifest, an
	// unknown or too old version, or a field out of range.
	ErrFormat = errors.New("backup: unreadable archive")
	// ErrIntegrity is an archive that was changed, truncated or reordered, or
	// whose chunk does not open: the manifest's MAC, a chunk's hash, its
	// authentication, or a count did not match.
	ErrIntegrity = errors.New("backup: archive failed verification")
	// ErrKey is a data key that could not be recovered: the key refused to
	// decrypt, or the context differs. Nothing was read.
	ErrKey = errors.New("backup: cannot recover the data key")
)

// Section is one of the three kinds of content a module contributes.
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

// Manifest describes one backup. It is the authenticated body of
// manifest.json.
type Manifest struct {
	// Format is the archive format version (see [Version]).
	Format int `json:"format"`
	// Installation and ID name the backup; they are also in its path.
	Installation string `json:"installation"`
	ID           string `json:"id"`
	// Layout is the storage layout of the source installation.
	Layout Layout `json:"layout"`
	// Created is the start of the backup, UTC, RFC 3339.
	Created string `json:"created"`
	// Creator names what wrote it, for example "sluis-backup 1.75.0".
	Creator string `json:"creator"`
	// ChunkBytes is the plaintext size a chunk is cut at.
	ChunkBytes int `json:"chunkBytes"`
	// Modules lists the modules in the backup in [port.Modules] order, each
	// once.
	Modules []ModuleEntry `json:"modules"`
}

// ModuleEntry is one module's three sections.
type ModuleEntry struct {
	Module  port.Module `json:"module"`
	State   Part        `json:"state"`
	Secrets Part        `json:"secrets"`
	Blobs   Part        `json:"blobs"`
}

// Part is one section of one module.
type Part struct {
	// Records is the number of records in all chunks.
	Records int64 `json:"records"`
	// Chunks lists the chunks in order; their numbers run from 0.
	Chunks []Chunk `json:"chunks,omitempty"`
}

// Chunk is one sealed object.
type Chunk struct {
	N       int    `json:"n"`
	Records int    `json:"records"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// Part returns the module's part for a section.
func (e *ModuleEntry) part(s Section) *Part {
	switch s {
	case State:
		return &e.State
	case Secrets:
		return &e.Secrets
	default:
		return &e.Blobs
	}
}

// Part returns the part of a module's section, or the zero Part when the
// module is not in the backup.
func (m *Manifest) Part(mod port.Module, s Section) Part {
	for i := range m.Modules {
		if m.Modules[i].Module == mod {
			return *m.Modules[i].part(s)
		}
	}
	return Part{}
}

// Records is the number of records of a section across all modules.
func (m *Manifest) Records(s Section) int64 {
	var n int64
	for i := range m.Modules {
		n += m.Modules[i].part(s).Records
	}
	return n
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether s is acceptable as an installation name or a
// backup id: one path segment of letters, digits, dot, dash and underscore.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Prefix is the object-name prefix of a backup.
func Prefix(installation, id string) string { return "backup/" + installation + "/" + id + "/" }

// ManifestName is the object name of a backup's manifest.
func ManifestName(installation, id string) string { return Prefix(installation, id) + "manifest.json" }

// ChunkName is the object name of chunk n of a section of a module.
func ChunkName(installation, id string, s Section, m port.Module, n int) string {
	return fmt.Sprintf("%s%s/%s/%d", Prefix(installation, id), s, m, n)
}

// validate checks a decoded manifest against the place it was read from.
func (m *Manifest) validate(installation, id string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: manifest: %s", ErrFormat, fmt.Sprintf(format, a...))
	}
	if !Readable(m.Format) {
		return bad("format %d is not readable by this release (reads %d)", m.Format, Version)
	}
	if m.Installation != installation || m.ID != id {
		return fmt.Errorf("%w: manifest is for %s/%s, read as %s/%s", ErrIntegrity, m.Installation, m.ID, installation, id)
	}
	if !ValidName(m.Installation) || !ValidName(m.ID) {
		return bad("installation or id is not a valid name")
	}
	if !m.Layout.Valid() {
		return bad("layout %q is unknown", m.Layout)
	}
	if m.ChunkBytes <= 0 {
		return bad("chunkBytes %d", m.ChunkBytes)
	}
	seen := map[port.Module]bool{}
	for i := range m.Modules {
		e := &m.Modules[i]
		if !e.Module.Valid() {
			return bad("module %q is unknown", e.Module)
		}
		if seen[e.Module] {
			return bad("module %q is listed twice", e.Module)
		}
		seen[e.Module] = true
		for _, s := range Sections() {
			p := e.part(s)
			var sum int64
			for n, c := range p.Chunks {
				if c.N != n {
					return bad("%s/%s: chunk %d is numbered %d", s, e.Module, n, c.N)
				}
				if c.Records < 0 || c.Size <= 0 || len(c.SHA256) != 64 {
					return bad("%s/%s: chunk %d is malformed", s, e.Module, n)
				}
				sum += int64(c.Records)
			}
			if sum != p.Records {
				return bad("%s/%s: %d records listed, chunks hold %d", s, e.Module, p.Records, sum)
			}
		}
	}
	return nil
}
