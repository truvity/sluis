package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/backup"
)

// RecordVersion is the version of the record encoding this release writes.
const RecordVersion = 1

// Record types.
const (
	TypeState  = "state"
	TypeIndex  = "index"
	TypeSecret = "secret"
)

// The secret namespaces.
const (
	Internal = "internal"
	External = "external"
)

// ErrRecord is a record that cannot be decoded: not JSON, an unknown version
// or type, a type that does not belong in the section, or a field out of range.
var ErrRecord = errors.New("export: unreadable record")

// Record is a decoded record: [State], [Index] or [Secret]. A blob is not a
// record: it is a file of its own in the archive.
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

func (State) recordType() string  { return TypeState }
func (Index) recordType() string  { return TypeIndex }
func (Secret) recordType() string { return TypeSecret }

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
// records; secrets, secret records.
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
	}
	return nil, fmt.Errorf("%w: unknown type %q", ErrRecord, h.T)
}
