package secretstore

import (
	"fmt"
	"regexp"
	"strings"
	"time"
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

// CloudflareMintedToken is one Cloudflare token sluis minted and has not yet
// deleted.
type CloudflareMintedToken struct {
	ID        string    `json:"id"`
	ExpiresOn time.Time `json:"expires_on"`
}

// CloudflareMinted is the record of the tokens sluis minted for a preset, which
// the sweep deletes by id. Cloudflare hides an expired token from its list while
// still counting it toward the account's 500, so a sweep that listed would never
// find what it must delete. The ids are not secrets.
type CloudflareMinted struct {
	Tokens []CloudflareMintedToken `json:"tokens"`
}
