package issuer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// An address is the identity here: every link, grant and group is keyed by it.
// A provider's address can change hands, though: a Google Workspace account
// deleted and an account made again under the same address is a different
// account with the same address, and would inherit everything the old one had.
// So the first sign-in with an address records the provider's stable id of the
// account (Google's `sub`), and a later sign-in with the same address from
// another account is refused (audit 2026-10-18, C).
//
// The pin is one value in the shared State per provider and address, written
// once, the way the state secret's fingerprint is: trust on first use, and no
// migration, since an address signed in with before the pin existed is pinned
// by its next sign-in.

// subjectPinTTL keeps a pin for as long as the store keeps anything: the State
// interface wants a lifetime, and a pin that lapsed would let the next account
// with the address be taken for the old one.
const subjectPinTTL = 10 * 365 * 24 * time.Hour

// ErrSubjectChanged is a sign-in whose address is pinned to another account at
// the provider.
var ErrSubjectChanged = errors.New("issuer: this address first signed in here from another account at the provider")

// SubjectPinKey is the State key of the pin of an address at a provider: the
// address is hashed, so the key names nobody. An operator who confirms that an
// address has rightly moved to a new account deletes this key, and the next
// sign-in pins the new account.
func SubjectPinKey(provider, email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "issuer:signin-subject:" + provider + ":" + hex.EncodeToString(sum[:])
}

// pinSubject holds email to the account subject at provider: recorded at the
// first sign-in, compared at every later one. An empty subject (a provider that
// names none) pins nothing. A store that cannot answer is a refusal: a pin that
// passes when it could not look is not one.
func (i *Issuer) pinSubject(ctx context.Context, provider, email, subject string) error {
	if i == nil || i.pins == nil || subject == "" {
		return nil
	}
	key := SubjectPinKey(provider, email)
	for range 2 {
		won, err := i.pins.SetIfAbsent(ctx, key, []byte(subject), subjectPinTTL)
		if err != nil {
			return fmt.Errorf("record the account the address signs in from: %w", err)
		}
		if won {
			return nil
		}
		stored, ok, err := i.pins.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read the account the address signs in from: %w", err)
		}
		if !ok {
			continue // deleted between the two calls: record it again
		}
		if subtle.ConstantTimeCompare(stored, []byte(subject)) != 1 {
			return ErrSubjectChanged
		}
		return nil
	}
	return errors.New("the account the address signs in from could not be recorded")
}
