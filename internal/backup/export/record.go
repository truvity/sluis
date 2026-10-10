package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/backup"
)

// RecordVersion is the version of the record encoding this release writes.
const RecordVersion = 1

// BlobPart is the size of the pieces a blob is split into.
const BlobPart = 8 << 20

// Record types.
const (
	TypeState  = "state"
	TypeIndex  = "index"
	TypeSecret = "secret"
	TypeBlob   = "blob"
)

// The secret namespaces.
const (
	Internal = "internal"
	External = "external"
)

// ErrRecord is a record that cannot be decoded: not JSON, an unknown version
// or type, a type that does not belong in the section, or a field out of range.
var ErrRecord = errors.New("export: unreadable record")

// Record is a decoded record: [State], [Index], [Secret] or [Blob].
type Record interface{ recordType() string }

type head struct {
	V int    `json:"v"`
	T string `json:"t"`
}

// State is one State record.
type State struct {
	V     int    `json:"v"`
	T     string `json:"t"`
	Key   string `json:"key"`
	Value []byte `json:"value"`
	// Expires is the instant the record expires, or empty for none.
	Expires string `json:"expires,omitempty"`
}

// Index is one Index set.
type Index struct {
	V       int      `json:"v"`
	T       string   `json:"t"`
	Key     string   `json:"key"`
	Members []string `json:"members"`
	Expires string   `json:"expires,omitempty"`
}

// Secret is one secret as the store holds it.
type Secret struct {
	V int    `json:"v"`
	T string `json:"t"`
	// NS is [Internal] or [External]; Name is the path below
	// <ns>/<module>.
	NS   string `json:"ns"`
	Name string `json:"name"`
	// Doc is the stored JSON object, the latest version.
	Doc json.RawMessage `json:"doc"`
}

// Blob is one part of one object.
type Blob struct {
	V    int    `json:"v"`
	T    string `json:"t"`
	Name string `json:"name"`
	// Size and SHA256 are the whole object's.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Part counts from 0 of Parts.
	Part  int    `json:"part"`
	Parts int    `json:"parts"`
	Data  []byte `json:"data"`
}

func (State) recordType() string  { return TypeState }
func (Index) recordType() string  { return TypeIndex }
func (Secret) recordType() string { return TypeSecret }
func (Blob) recordType() string   { return TypeBlob }

// ExpiresAt is the instant a record expires, or the zero time for none.
func ExpiresAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: expires %q", ErrRecord, s)
	}
	return t, nil
}

func expires(now time.Time, ttl time.Duration) string {
	if ttl <= 0 {
		return ""
	}
	return now.Add(ttl).UTC().Format(time.RFC3339Nano)
}

// Decode reads a record of a section. The state section holds state and index
// records; secrets, secret records; blobs, blob records.
func Decode(s backup.Section, rec []byte) (Record, error) {
	var h head
	if err := json.Unmarshal(rec, &h); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRecord, err)
	}
	if h.V != RecordVersion {
		return nil, fmt.Errorf("%w: record version %d, this release reads %d", ErrRecord, h.V, RecordVersion)
	}
	strict := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(rec))
		d.DisallowUnknownFields()
		if err := d.Decode(v); err != nil {
			return fmt.Errorf("%w: %v", ErrRecord, err)
		}
		return nil
	}
	bad := func() (Record, error) {
		return nil, fmt.Errorf("%w: a %q record in the %s section", ErrRecord, h.T, s)
	}
	switch h.T {
	case TypeState, TypeIndex:
		if s != backup.State {
			return bad()
		}
		if h.T == TypeState {
			var r State
			if err := strict(&r); err != nil {
				return nil, err
			}
			if r.Key == "" {
				return nil, fmt.Errorf("%w: a state record has no key", ErrRecord)
			}
			_, err := ExpiresAt(r.Expires)
			return r, err
		}
		var r Index
		if err := strict(&r); err != nil {
			return nil, err
		}
		if r.Key == "" {
			return nil, fmt.Errorf("%w: an index record has no key", ErrRecord)
		}
		_, err := ExpiresAt(r.Expires)
		return r, err
	case TypeSecret:
		if s != backup.Secrets {
			return bad()
		}
		var r Secret
		if err := strict(&r); err != nil {
			return nil, err
		}
		if (r.NS != Internal && r.NS != External) || r.Name == "" || len(r.Doc) == 0 {
			return nil, fmt.Errorf("%w: a secret record is incomplete", ErrRecord)
		}
		return r, nil
	case TypeBlob:
		if s != backup.Blobs {
			return bad()
		}
		var r Blob
		if err := strict(&r); err != nil {
			return nil, err
		}
		if r.Name == "" || r.Parts < 1 || r.Part < 0 || r.Part >= r.Parts || len(r.SHA256) != 64 || r.Size < 0 {
			return nil, fmt.Errorf("%w: a blob record is out of range", ErrRecord)
		}
		return r, nil
	}
	return nil, fmt.Errorf("%w: unknown type %q", ErrRecord, h.T)
}

// Assembler puts the parts of blobs back together. Parts of one object arrive
// in order, one object after another, as they were written; memory is one
// object.
type Assembler struct {
	cur  *Blob
	body []byte
}

// Add takes the next part. When it is the last of an object, done is true and
// name and body are the object, verified against its size and hash; body is
// valid until the next Add.
func (a *Assembler) Add(b Blob) (name string, body []byte, done bool, err error) {
	switch {
	case b.Part == 0:
		if a.cur != nil {
			return "", nil, false, fmt.Errorf("%w: blob %q ended before its last part", ErrRecord, a.cur.Name)
		}
		a.cur, a.body = &b, a.body[:0]
	case a.cur == nil || a.cur.Name != b.Name || a.cur.Parts != b.Parts || a.cur.SHA256 != b.SHA256 || a.cur.Size != b.Size ||
		b.Part != a.next():
		a.cur = nil
		return "", nil, false, fmt.Errorf("%w: blob %q part %d is out of order", ErrRecord, b.Name, b.Part)
	}
	a.body = append(a.body, b.Data...)
	a.cur.Part = b.Part
	if b.Part < b.Parts-1 {
		return "", nil, false, nil
	}
	name, size, want := a.cur.Name, a.cur.Size, a.cur.SHA256
	a.cur = nil
	sum := sha256.Sum256(a.body)
	if int64(len(a.body)) != size || hex.EncodeToString(sum[:]) != want {
		return "", nil, false, fmt.Errorf("%w: blob %q does not match its size and hash", ErrRecord, name)
	}
	return name, a.body, true, nil
}

func (a *Assembler) next() int { return a.cur.Part + 1 }

// Pending reports whether an object is only partly given.
func (a *Assembler) Pending() bool { return a.cur != nil }
