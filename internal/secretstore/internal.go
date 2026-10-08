package secretstore

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/truvity/sluis/storage/state"
)

// PersonToken is the token pair a person's authorization of a linked system
// left. A system that rotates its refresh token on every use makes this the
// one copy: a pair issued and not kept is a link that can never be checked
// again.
type PersonToken struct {
	AccessToken    string    `json:"access_token"`
	AccessExpires  time.Time `json:"access_expires,omitzero"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	RefreshExpires time.Time `json:"refresh_expires,omitzero"`
}

// Internal is what only sluis reads: the store rooted at <root>/internal. The
// addresses below it are sluis's own and not a contract; they keep v3's names
// so the migration is a prefix move.
//
// internal/config/<name> is not here: those names are plain text, as Pulumi and
// an operator write them, and are read through the secrets source
// (internal/secrets), so that directory has one shape.
type Internal struct{ s state.Store }

// NewInternal returns the view of s, which is rooted at <root>/internal.
func NewInternal(s state.Store) Internal { return Internal{s: s} }

// Store is the store the view is over.
func (i Internal) Store() state.Store { return i.s }

// ConsoleSessionKey is the key the console signs its sessions with (v3:
// credentials/console/session-key).
func (i Internal) ConsoleSessionKey() state.Value[[]byte] {
	return state.NewValue(i.s, "credentials/console/session-key", state.Raw())
}

// PersonToken is a person's token pair for a linked system ("github"): v3's
// credentials/<system>-link/<person>/<ref>, replaced in place now that a
// version history replaces the random <ref>.
func (i Internal) PersonToken(person, system string) state.Value[PersonToken] {
	return state.NewValue(i.s, "credentials/"+segment(system)+"-link/"+segment(person), state.JSON[PersonToken]())
}

// Directory is an identity directory's credential, for the Google Workspace
// domain or id (v3: credentials/directory/google/<workspace-id>/<ref>).
func (i Internal) Directory(domain string) state.Value[[]byte] {
	return state.NewValue(i.s, "credentials/directory/google/"+segment(domain), state.Raw())
}

// refPattern is an `internal/<kind>/<id>` address: two lower-case segments.
var refPattern = regexp.MustCompile(`^internal/[a-z0-9][a-z0-9-]{0,30}/[a-z0-9][a-z0-9._-]{0,62}$`)

// ErrRef is an address that is not `internal/<kind>/<id>`.
var ErrRef = fmt.Errorf("secretstore: want an internal/<kind>/<id> address")

// CheckInternalRef refuses an address that is not `internal/<kind>/<id>`: an
// external address, a path that climbs, or any other shape. It returns the
// address below the internal namespace, `<kind>/<id>`.
func CheckInternalRef(ref string) (string, error) {
	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") {
		return "", fmt.Errorf("%w: %q", ErrRef, ref)
	}
	return strings.TrimPrefix(ref, "internal/"), nil
}

// S3Credentials is the static credential document of an S3-compatible store,
// at the internal address ref (`internal/<kind>/<id>`, the setting
// `ports.blob.s3.credentialsRef`).
func (i Internal) S3Credentials(ref string) (state.Value[S3Credentialsv1], error) {
	addr, err := CheckInternalRef(ref)
	if err != nil {
		return state.Value[S3Credentialsv1]{}, err
	}
	return state.NewValue(i.s, addr, state.Codec[S3Credentialsv1](s3CredentialsCodec)), nil
}
