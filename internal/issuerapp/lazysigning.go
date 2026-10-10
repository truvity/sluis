package issuerapp

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/signer"
)

// signInStateLabel separates the key that signs a login in progress from every
// other derivation of the installation's secret.
const signInStateLabel = "sluis/sign-in-state"

// lazyOpenTimeout bounds an open that no request context bounds: the key a
// sign-in state is signed with is asked for where no context is passed.
const lazyOpenTimeout = 30 * time.Second

// signingSet is what the KMS signing modes build over the state secret: the
// rings, and the secret-derived values the rest of the service uses.
type signingSet struct {
	rings *signer.KeyRings
	// seed is the state secret (the primary key's), for its fingerprint check.
	seed []byte
	// stateKey signs the state a login in progress carries.
	stateKey []byte
	kmsRefs  []*signer.KMSKeyRefs
	wrapped  *signer.WrappedSigning
}

// newSigningSet builds the set from the primary key and the keys beside it.
func newSigningSet(key *signer.SigningKey, rings *signer.KeyRings, kmsRefs []*signer.KMSKeyRefs, wrapped *signer.WrappedSigning) *signingSet {
	return &signingSet{rings: rings, seed: key.Seed(), stateKey: key.Derive(signInStateLabel), kmsRefs: kmsRefs, wrapped: wrapped}
}

// deferredSigning is the signing rings of the KMS modes, opened by the first
// thing that needs a key and not as the process starts.
//
// Opening reads the state secret (the signing keys are built over it as their
// seed) and calls KMS (the public key of each key, or the unwrapping of the
// recorded ones), so a start that did it would make those calls in every new
// environment of a herd. What is known without opening is answered without it:
// the algorithms (they are configuration), which one is the default, and
// whether one is configured. What needs a key opens the set:
//
//   - the first token signed;
//   - the first request for the key set (/keys): it lists the public keys, which
//     are the rings' and exist only once they are open, so this request pays;
//   - the first sign-in state signed or checked, and the first check of the
//     state secret's fingerprint (they need the secret itself).
//
// The discovery document does not open the set: it lists the configured
// algorithms while the set is closed, and the published ones once it is open.
//
// The open is single-flight: requests that arrive together share one read of
// the secret and one set of KMS calls, and a failed open is not remembered, so
// the next request tries again. A set is opened once for the life of the
// process, as it is built at start elsewhere; its secret is not read again, and
// rebuilding the rings would drop what rotation holds in memory.
type deferredSigning struct {
	algs   []jose.SignatureAlgorithm
	set    *lazy.Value[*signingSet]
	opened atomic.Pointer[signingSet]
	log    *slog.Logger
}

var (
	_ signer.Ring      = (*deferredSigning)(nil)
	_ signer.Directory = (*deferredSigning)(nil)
	_ signer.Opener    = (*deferredSigning)(nil)
)

// foreverTTL keeps an opened set for the life of the process.
const foreverTTL = time.Duration(1<<63 - 1)

// newDeferredSigning returns the rings, opened by build on first use. algs are
// the configured algorithms, the first the installation's default.
func newDeferredSigning(algs []jose.SignatureAlgorithm, build func(context.Context) (*signingSet, error), log *slog.Logger) *deferredSigning {
	d := &deferredSigning{algs: slices.Clone(algs), log: log}
	d.set = lazy.New(foreverTTL, func(ctx context.Context) (*signingSet, error) {
		set, err := build(ctx)
		if err != nil {
			return nil, err
		}
		d.opened.Store(set)
		return set, nil
	})
	return d
}

// Open implements [signer.Opener].
func (d *deferredSigning) Open(ctx context.Context) error {
	_, err := d.get(ctx)
	return err
}

// Opened implements [signer.Opener].
func (d *deferredSigning) Opened() bool { return d.opened.Load() != nil }

func (d *deferredSigning) get(ctx context.Context) (*signingSet, error) {
	if set := d.opened.Load(); set != nil {
		return set, nil
	}
	set, err := d.set.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("open the signing keys: %w", err)
	}
	return set, nil
}

// getBounded is [deferredSigning.get] for a caller with no request context.
func (d *deferredSigning) getBounded() (*signingSet, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lazyOpenTimeout)
	defer cancel()
	return d.get(ctx)
}

// stateKey is the key a login in progress is signed with; it opens the set.
func (d *deferredSigning) stateKey() ([]byte, error) {
	set, err := d.getBounded()
	if err != nil {
		return nil, err
	}
	return set.stateKey, nil
}

// stateSeed is the state secret; it opens the set.
func (d *deferredSigning) stateSeed(ctx context.Context) ([]byte, error) {
	set, err := d.get(ctx)
	if err != nil {
		return nil, err
	}
	return set.seed, nil
}

// Maintain implements [signer.Ring] and [signer.Directory]. A set that cannot
// be opened is reported by [signer.Opener.Open], which every caller that can
// fail calls first.
func (d *deferredSigning) Maintain(ctx context.Context) {
	if set := d.opened.Load(); set != nil {
		set.rings.Maintain(ctx)
	}
}

// Default implements [signer.Ring] and [signer.Directory]: the first
// configured algorithm.
func (d *deferredSigning) Default() jose.SignatureAlgorithm { return d.algs[0] }

// Has implements [signer.Directory].
func (d *deferredSigning) Has(alg jose.SignatureAlgorithm) bool { return slices.Contains(d.algs, alg) }

// Configured implements [signer.Directory].
func (d *deferredSigning) Configured() []jose.SignatureAlgorithm {
	out := slices.Clone(d.algs)
	slices.Sort(out)
	return out
}

// Algorithms implements [signer.Directory]: the configured algorithms until the
// set is open, then those with a published key.
func (d *deferredSigning) Algorithms() []jose.SignatureAlgorithm {
	if set := d.opened.Load(); set != nil {
		return set.rings.Algorithms()
	}
	return d.Configured()
}

// ActiveKID implements [signer.Directory].
func (d *deferredSigning) ActiveKID(alg jose.SignatureAlgorithm) (string, bool) {
	if set := d.opened.Load(); set != nil {
		return set.rings.ActiveKID(alg)
	}
	return "", false
}

// Secret implements [signer.Directory]: nil until the set is open.
func (d *deferredSigning) Secret(label string) []byte {
	if set := d.opened.Load(); set != nil {
		return set.rings.Secret(label)
	}
	return nil
}

// Signing implements [signer.Ring].
func (d *deferredSigning) Signing(alg jose.SignatureAlgorithm) (signer.ActiveKey, bool) {
	if set := d.opened.Load(); set != nil {
		return set.rings.Signing(alg)
	}
	return signer.ActiveKey{}, false
}

// Published implements [signer.Ring].
func (d *deferredSigning) Published() []signer.PublicKey {
	if set := d.opened.Load(); set != nil {
		return set.rings.Published()
	}
	return nil
}
