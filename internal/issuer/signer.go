package issuer

import (
	"context"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/signer"
)

// ringSigner adapts the [KeyRings] to the signer's [signer.Ring]: the ring
// decides which key and when, the signer decides what may be signed with it.
type ringSigner struct{ rings *KeyRings }

// newRingSigner is the in-process signer over rings. An access token may live
// as long as the deployment's token lifetime; a logout token carries no exp.
func newRingSigner(rings *KeyRings, tokenLifetime time.Duration) signer.Signer {
	return signer.New(ringSigner{rings}, signer.Limits{MaxLifetime: map[signer.Purpose]time.Duration{
		signer.PurposeAccess: tokenLifetime,
		signer.PurposeLogout: 0,
	}})
}

func (r ringSigner) Maintain(ctx context.Context) { r.rings.Maintain(ctx) }

func (r ringSigner) Default() jose.SignatureAlgorithm { return r.rings.Default() }

func (r ringSigner) Active(alg jose.SignatureAlgorithm) (signer.ActiveKey, bool) {
	k := r.rings.Active(alg)
	if k == nil {
		return signer.ActiveKey{}, false
	}
	return signer.ActiveKey{KID: k.ID(), Algorithm: k.SignatureAlgorithm(), Key: k.Key()}, true
}

func (r ringSigner) Published() []signer.PublicKey {
	var out []signer.PublicKey
	for _, k := range r.rings.Published() {
		out = append(out, signer.PublicKey{KID: k.ID(), Algorithm: k.Algorithm(), Key: k.Key()})
	}
	return out
}
