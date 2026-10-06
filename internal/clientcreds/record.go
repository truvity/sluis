package clientcreds

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// Kind is the credential kind in the Secrets path.
const Kind = "oidc-client"

// RecordVersion is the version of the record's JSON.
const RecordVersion = 1

// Record is what is stored for one client.
type Record struct {
	V       int    `json:"v"`
	Current string `json:"current"`
	// Previous is the secret Current replaced, accepted until
	// PreviousValidUntil so a rotation does not cut a relying party off.
	Previous           string    `json:"previous,omitempty"`
	PreviousValidUntil time.Time `json:"previous_valid_until,omitzero"`
	Created            time.Time `json:"created"`
	Rotated            time.Time `json:"rotated,omitzero"`
}

// Encode returns the record's stored form.
func (r Record) Encode() ([]byte, error) {
	if r.V == 0 {
		r.V = RecordVersion
	}
	if r.Current == "" {
		return nil, errors.New("clientcreds: a record has a current secret")
	}
	return json.Marshal(r)
}

// DecodeRecord reads a stored record. The error never holds a value.
func DecodeRecord(b []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, errors.New("clientcreds: the record is not JSON of the expected shape")
	}
	if r.V != RecordVersion {
		return Record{}, fmt.Errorf("clientcreds: the record is version %d; this build reads version %d", r.V, RecordVersion)
	}
	if r.Current == "" {
		return Record{}, errors.New("clientcreds: the record has no current secret")
	}
	return r, nil
}

// Path is the Secrets path of a client's record. A client id that is not one
// path segment is spelled as `u-` and its bytes in hex, the way the credential
// layout does, so any declared id has a path and two ids never share one.
func Path(clientID string) string {
	seg := clientID
	if seg == "" || strings.HasPrefix(seg, "u-") || seg == "." || seg == ".." || port.CheckSecretPath(seg) != nil {
		seg = fmt.Sprintf("u-%x", clientID)
	}
	return port.CredentialsPrefix + Kind + "/" + seg + "/secret"
}

// Generate returns a new secret: 32 random bytes, base64url without padding.
func Generate() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("clientcreds: no randomness for a client secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
