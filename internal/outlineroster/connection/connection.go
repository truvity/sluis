// Package connection is what the console keeps for each Outline workspace and
// the Outline controller reads: the connection record (where the workspace is,
// who connected it and when), the credential beside it (the API key, which
// only the controller reads), an operator's confirmation of a held change, and
// an operator's request for a pass now.
//
// The policy names a workspace by a key and says nothing else of it
// (docs/decisions/0018): its URL and its key are this record.
package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Version is the version of every document here this build writes and reads.
const Version = 1

// ErrVersion is a document of another version.
var ErrVersion = errors.New("connection: a document of an unknown version")

// validWorkspace is the policy's rule for a workspace key.
var validWorkspace = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidWorkspace reports whether a string can be a workspace key.
func ValidWorkspace(key string) bool { return validWorkspace.MatchString(key) }

// Record is one connected workspace, as the console shows it. It holds no
// secret.
type Record struct {
	Version   int    `json:"version"`
	Workspace string `json:"workspace"`
	// URL is the workspace's origin, `https://wiki.example.com`, which the API
	// is under (`<url>/api/<method>`).
	URL string `json:"url"`
	// KeyUser is the address of the Outline user the API key belongs to, as
	// `auth.info` answered when the key was saved, and KeyUserID its id.
	KeyUser   string `json:"key_user,omitempty"`
	KeyUserID string `json:"key_user_id,omitempty"`
	// Team is the Outline team the key belongs to, by id: a later key must
	// belong to the same one, so a key can never quietly point the workspace
	// key at another Outline.
	Team        string    `json:"team,omitempty"`
	ConnectedBy string    `json:"connected_by,omitempty"`
	ConnectedAt time.Time `json:"connected_at,omitzero"`
}

// Credential is the API key a workspace is acted on with. Only the controller
// reads it.
type Credential struct {
	Version   int    `json:"version"`
	Workspace string `json:"workspace"`
	APIKey    string `json:"api_key"`
}

// NormaliseURL checks a workspace URL and returns its origin: https (http
// only for a loopback address, which is a test or a local run), a host, and no
// user, query or fragment. A path is refused: Outline is served at the root of
// its origin.
func NormaliseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("connection: %q is not a URL: %w", raw, err)
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	switch {
	case u.Scheme != "https" && (u.Scheme != "http" || !loopback):
		return "", fmt.Errorf("connection: %q is not an https URL", raw)
	case u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("connection: %q must be an origin: a scheme and a host, with no user, query or fragment", raw)
	case strings.Trim(u.Path, "/") != "":
		return "", fmt.Errorf("connection: %q has a path; give the origin Outline is served at", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// EncodeRecord writes a record.
func EncodeRecord(r Record) (string, error) {
	if !ValidWorkspace(r.Workspace) {
		return "", fmt.Errorf("connection: %q is not a workspace key", r.Workspace)
	}
	if _, err := NormaliseURL(r.URL); err != nil {
		return "", err
	}
	r.Version = Version
	raw, err := json.Marshal(r)
	return string(raw), err
}

// DecodeRecord reads a record.
func DecodeRecord(raw string) (Record, error) {
	var r Record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Record{}, fmt.Errorf("connection: decode a record: %w", err)
	}
	if r.Version != Version {
		return Record{}, fmt.Errorf("%w: %d", ErrVersion, r.Version)
	}
	if !ValidWorkspace(r.Workspace) {
		return Record{}, fmt.Errorf("connection: a record names no workspace")
	}
	return r, nil
}

// EncodeCredential writes a credential.
func EncodeCredential(c Credential) ([]byte, error) {
	if !ValidWorkspace(c.Workspace) || strings.TrimSpace(c.APIKey) == "" {
		return nil, errors.New("connection: a credential needs a workspace and an API key")
	}
	c.Version = Version
	return json.Marshal(c)
}

// DecodeCredential reads a credential.
func DecodeCredential(raw []byte) (Credential, error) {
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credential{}, errors.New("connection: the credential is not readable") // never the bytes
	}
	if c.Version != Version {
		return Credential{}, fmt.Errorf("%w: %d", ErrVersion, c.Version)
	}
	if c.APIKey == "" {
		return Credential{}, errors.New("connection: the credential holds no API key")
	}
	return c, nil
}

// ConfirmationTTL is how long an operator's confirmation of a held set
// stands: long enough to act on, short enough that a set confirmed and
// forgotten does not go through a week later.
const ConfirmationTTL = 24 * time.Hour

// The breakers a confirmation answers, by scope: a managed group's removals,
// a collection's revocations, the admin demotions and the suspensions.
const (
	ScopeAdmins      = "admins"
	ScopeSuspensions = "suspensions"
	scopeGroup       = "group:"
	scopeCollection  = "collection:"
)

// GroupScope is the scope of a managed group's removal breaker.
func GroupScope(group string) string { return scopeGroup + group }

// CollectionScope is the scope of a collection's revocation breaker.
func CollectionScope(collection string) string { return scopeCollection + collection }

// ValidScope reports whether a string is the scope of one breaker.
func ValidScope(scope string) bool {
	switch {
	case scope == ScopeAdmins, scope == ScopeSuspensions:
		return true
	case strings.HasPrefix(scope, scopeGroup):
		return len(scope) > len(scopeGroup)
	case strings.HasPrefix(scope, scopeCollection):
		return len(scope) > len(scopeCollection)
	}
	return false
}

// Confirmation is an operator's confirmation that one held set of changes, of
// one breaker of one workspace, may go ahead.
type Confirmation struct {
	Version     int       `json:"version"`
	Workspace   string    `json:"workspace"`
	Scope       string    `json:"scope"`
	Fingerprint string    `json:"fingerprint"`
	By          string    `json:"by"`
	At          time.Time `json:"at"`
}

// Current reports whether the confirmation still holds at now.
func (c Confirmation) Current(now time.Time) bool {
	return c.Fingerprint != "" && now.Sub(c.At) < ConfirmationTTL
}

// EncodeConfirmation writes a confirmation.
func EncodeConfirmation(c Confirmation) (string, error) {
	if !ValidWorkspace(c.Workspace) || !ValidScope(c.Scope) || c.Fingerprint == "" || c.By == "" {
		return "", errors.New("connection: a confirmation needs a workspace, a breaker, a fingerprint and who confirmed")
	}
	c.Version = Version
	raw, err := json.Marshal(c)
	return string(raw), err
}

// DecodeConfirmation reads a confirmation.
func DecodeConfirmation(raw string) (Confirmation, error) {
	var c Confirmation
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Confirmation{}, fmt.Errorf("connection: decode a confirmation: %w", err)
	}
	if c.Version != Version {
		return Confirmation{}, fmt.Errorf("%w: %d", ErrVersion, c.Version)
	}
	return c, nil
}

// PassRequest is an operator's request for a pass over one workspace now.
type PassRequest struct {
	Version   int       `json:"version"`
	Workspace string    `json:"workspace"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
}

// EncodePassRequest writes a request.
func EncodePassRequest(r PassRequest) (string, error) {
	if !ValidWorkspace(r.Workspace) {
		return "", errors.New("connection: a pass request names no workspace")
	}
	r.Version = Version
	raw, err := json.Marshal(r)
	return string(raw), err
}

// DecodePassRequest reads a request.
func DecodePassRequest(raw string) (PassRequest, error) {
	var r PassRequest
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return PassRequest{}, fmt.Errorf("connection: decode a pass request: %w", err)
	}
	if r.Version != Version {
		return PassRequest{}, fmt.Errorf("%w: %d", ErrVersion, r.Version)
	}
	return r, nil
}
