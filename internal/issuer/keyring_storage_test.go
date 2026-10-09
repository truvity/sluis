package issuer_test

import (
	"context"
	"crypto"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
)

// The plumbing end to end: a [issuer.Storage] built with one key adopts a
// rotated one through [issuer.Storage.Rotate], and everything that signs
// through it — the OpenID surface's own key, the JWKS, and [issuer.Storage.MintFor]
// — follows the same schedule rather than each holding its own idea of
// which key is current.
func TestStorageRotateAdoptsANewSigningKey(t *testing.T) {
	t.Parallel()
	dir := &fakeDirectory{standing: map[string]issuer.Standing{
		"ada@north.example": live("directory-admins@north.example"),
	}}
	iss := newIssuer(t, dir)

	key1, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, key1, nil, issuer.NewMemoryState())
	if err != nil {
		t.Fatal(err)
	}
	// A real, tiny delay rather than zero: [signer.KeyRingConfig] treats
	// zero as "use the default", and this test wants the schedule to run
	// its course in real time instead of asserting on its own arithmetic —
	// that is what TestKeyRingRotationSchedule already does with an
	// injected clock.
	storage.ConfigureKeyRotation(signer.KeyRingConfig{ActivationDelay: time.Nanosecond, Overlap: time.Hour})

	ctx := context.Background()
	signing, err := storage.SigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if signing.ID() != key1.ID() {
		t.Fatalf("signs with %s, want the seed key %s", signing.ID(), key1.ID())
	}

	key2, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Rotate(ctx, key2); err != nil {
		t.Fatal(err)
	}

	keys, err := storage.KeySet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("published %d keys right after rotation, want 2: publish before sign", len(keys))
	}
	if signing, err = storage.SigningKey(ctx); err != nil {
		t.Fatal(err)
	} else if signing.ID() != key1.ID() {
		t.Fatalf("signs with %s immediately after rotation, want it to keep signing with key1 until activation", signing.ID())
	}

	time.Sleep(time.Millisecond)                      // past the one-nanosecond activation delay
	if err := storage.Rotate(ctx, key2); err != nil { // the next poll tick, same key
		t.Fatal(err)
	}

	if signing, err = storage.SigningKey(ctx); err != nil {
		t.Fatal(err)
	} else if signing.ID() != key2.ID() {
		t.Fatalf("signs with %s, want key2 once its activation delay elapsed", signing.ID())
	}

	// MintFor signs with whatever is active NOW, not with the key the
	// storage was built with.
	token, _, err := storage.MintFor(ctx, "ada@north.example", "aws:1111:power", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256, jose.ES384, jose.ES512})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signed.Verify(key2.Key().(crypto.Signer).Public()); err != nil {
		t.Errorf("MintFor did not sign with the newly active key: %v", err)
	}
}
