// Package signer is the only place a signing key is used or opened
// (docs/decisions/0071). The issuer builds the claims; the signer checks the
// request against its limits, signs and publishes the public keys.
//
// Today the signer runs inside the issuer's process, behind the [Signer]
// interface, so that the move to a process of its own (cmd/sluis-signer)
// changes the transport and nothing the issuer calls. It imports no HTTP
// server, cookie, session or OIDC protocol package: the import-boundary test
// holds it to that.
//
// # Limits
//
// A request is checked on every call (ADR 0071, D102 a): the purpose must be
// one the signer was configured with, and the lifetime the payload claims
// (exp minus iat) must not exceed that purpose's maximum. These are the seams
// for the rest of the mitigations (a circuit breaker, per-caller rate limits
// and the signer's own audit trail), which are not here yet.
package signer

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Purpose says what a token is for. The signer signs only the purposes it is
// configured with, and a purpose fixes the JWT type header, so a caller cannot
// ask for a logout token to be typed as an access token.
type Purpose string

const (
	// PurposeAccess is an access or ID token: typ JWT.
	PurposeAccess Purpose = "access"
	// PurposeLogout is an OpenID back-channel logout token: typ logout+jwt.
	PurposeLogout Purpose = "logout"
)

// typ is the JWT type header of a purpose.
var typ = map[Purpose]string{PurposeAccess: "JWT", PurposeLogout: "logout+jwt"}

var (
	// ErrUnknownPurpose is a request for a purpose the signer does not sign.
	ErrUnknownPurpose = errors.New("signer: unknown purpose")
	// ErrLifetime is a payload that lives longer than its purpose allows, or
	// that carries an exp without the iat to measure it from.
	ErrLifetime = errors.New("signer: lifetime above the maximum")
	// ErrNoKey is an algorithm with no key that can sign yet.
	ErrNoKey = errors.New("no signing key")
)

// Request is one thing to sign.
type Request struct {
	Purpose Purpose
	// Algorithm selects the ring; empty is the installation default.
	Algorithm jose.SignatureAlgorithm
	// Payload is the claims, already encoded as JSON by the issuer.
	Payload []byte
}

// Signed is a compact JWS and the key that made it.
type Signed struct {
	Token     string
	KID       string
	Algorithm jose.SignatureAlgorithm
}

// PublicKey is one entry of the published key set.
type PublicKey struct {
	KID       string
	Algorithm jose.SignatureAlgorithm
	Key       crypto.PublicKey
}

// Signer is what the issuer calls. It is the whole surface: the private key
// never crosses it.
type Signer interface {
	// Sign signs one payload for one purpose.
	Sign(ctx context.Context, req Request) (Signed, error)
	// PublicKeys is every key currently published: the signing one, those
	// waiting out their activation delay and the retiring ones.
	PublicKeys(ctx context.Context) ([]PublicKey, error)
}

// ActiveKey is the key a ring signs with now.
type ActiveKey struct {
	KID       string
	Algorithm jose.SignatureAlgorithm
	// Key is what the JOSE library signs with: a private key, or an opaque
	// signer for a key that never leaves its KMS.
	Key any
}

// Ring is the key ring the signer signs from. The signer owns what is done
// with a key; the ring owns which key and when (rotation, overlap, the KMS
// wrapping).
type Ring interface {
	// Maintain gives the ring its chance to rotate and to absorb keys other
	// replicas recorded. Called before every Sign and PublicKeys.
	Maintain(ctx context.Context)
	// Active is the key that signs for an algorithm, or false.
	Active(alg jose.SignatureAlgorithm) (ActiveKey, bool)
	// Default is the algorithm used when a request names none.
	Default() jose.SignatureAlgorithm
	// Published is every published key.
	Published() []PublicKey
}

// Limits are the per-call checks. A purpose absent from MaxLifetime is
// unknown and refused. A maximum of zero means the payload may carry no exp.
type Limits struct {
	MaxLifetime map[Purpose]time.Duration
}

type local struct {
	ring   Ring
	limits Limits
}

// New is the in-process signer over a ring.
func New(ring Ring, limits Limits) Signer { return &local{ring: ring, limits: limits} }

// check applies the limits to a request.
func (s *local) check(req Request) error {
	max, ok := s.limits.MaxLifetime[req.Purpose]
	if _, known := typ[req.Purpose]; !ok || !known {
		return fmt.Errorf("%w %q", ErrUnknownPurpose, req.Purpose)
	}
	var c struct {
		IAT *int64 `json:"iat"`
		EXP *int64 `json:"exp"`
	}
	if err := json.Unmarshal(req.Payload, &c); err != nil {
		return fmt.Errorf("signer: the payload is not a JSON object: %w", err)
	}
	if c.EXP == nil {
		return nil
	}
	if c.IAT == nil {
		return fmt.Errorf("%w: exp without iat", ErrLifetime)
	}
	// Whole seconds: iat and exp are each truncated, so a lifetime of 90.5s
	// can read as 91s. The maximum is rounded up to match.
	allowed := (max + time.Second - 1).Truncate(time.Second)
	if got := time.Duration(*c.EXP-*c.IAT) * time.Second; got > allowed {
		return fmt.Errorf("%w: %s for %s, at most %s", ErrLifetime, got, req.Purpose, max)
	}
	return nil
}

// Sign implements [Signer].
func (s *local) Sign(ctx context.Context, req Request) (Signed, error) {
	if err := s.check(req); err != nil {
		return Signed{}, err
	}
	s.ring.Maintain(ctx)
	alg := req.Algorithm
	if alg == "" {
		alg = s.ring.Default()
	}
	active, ok := s.ring.Active(alg)
	if !ok {
		return Signed{}, ErrNoKey
	}
	js, err := jose.NewSigner(
		jose.SigningKey{Algorithm: active.Algorithm, Key: active.Key},
		(&jose.SignerOptions{}).WithType(jose.ContentType(typ[req.Purpose])).WithHeader("kid", active.KID),
	)
	if err != nil {
		return Signed{}, err
	}
	signed, err := js.Sign(req.Payload)
	if err != nil {
		return Signed{}, err
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		return Signed{}, err
	}
	return Signed{Token: token, KID: active.KID, Algorithm: active.Algorithm}, nil
}

// PublicKeys implements [Signer].
func (s *local) PublicKeys(ctx context.Context) ([]PublicKey, error) {
	s.ring.Maintain(ctx)
	return s.ring.Published(), nil
}
