package issuer

import (
	"fmt"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/signer"
)

// NewTestStorage is [NewStorage] over key rings built here from key (nil: a
// generated one) and additional, the way the deployment's wiring builds them.
func NewTestStorage(
	iss *Issuer, verify Verifier, secrets clientcreds.Lookup,
	key *signer.SigningKey, additional []*signer.SigningKey, state State,
) (*Storage, error) {
	storage, _, err := NewTestStorageRings(iss, verify, secrets, key, additional, state)
	return storage, err
}

// NewTestStorageRings is [NewTestStorage] that also returns the rings, which
// are what rotation is fed to.
func NewTestStorageRings(
	iss *Issuer, verify Verifier, secrets clientcreds.Lookup,
	key *signer.SigningKey, additional []*signer.SigningKey, state State,
) (*Storage, *signer.KeyRings, error) {
	if key == nil {
		generated, err := signer.NewSigningKey()
		if err != nil {
			return nil, nil, err
		}
		key = generated
	}
	if state == nil {
		state = NewMemoryState()
	}
	keys, err := signer.NewKeyRings(key, additional, state, signer.KeyRingConfig{}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("issuer: adopt the signing keys: %w", err)
	}
	storage, err := NewStorage(iss, verify, secrets, signer.New(keys, signer.LimitsFor(iss.Config().TokenLifetime)), keys, state)
	return storage, keys, err
}
