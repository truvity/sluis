package issuer_test

import (
	"context"
	"crypto"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
)

// spySigner records what went through [signer.Signer.Sign].
type spySigner struct {
	signer.Signer
	mu   sync.Mutex
	reqs []signer.Request
}

func (s *spySigner) Sign(ctx context.Context, req signer.Request) (signer.Signed, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return s.Signer.Sign(ctx, req)
}

func spiedStorage(t *testing.T) (*issuer.Storage, *signer.KeyRings, *spySigner) {
	t.Helper()
	iss := newIssuer(t, &fakeDirectory{standing: map[string]issuer.Standing{}})
	key, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := signer.NewKeyRings(key, nil, issuer.NewMemoryState(), signer.KeyRingConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spy := &spySigner{Signer: signer.New(keys, signer.LimitsFor(iss.Config().TokenLifetime))}
	storage, err := issuer.NewStorage(iss, fakeVerifier{}, nil, spy, keys, issuer.NewMemoryState())
	if err != nil {
		t.Fatal(err)
	}
	return storage, keys, spy
}

// What the library signs through Storage.SigningKey goes through Signer.Sign
// and verifies against the published JWKS: the issuer hands the library a key
// that is an opaque signer, not a private key.
func TestALibraryMintedTokenIsSignedByTheSignerAndVerifiesAgainstTheJWKS(t *testing.T) {
	t.Parallel()
	storage, _, spy := spiedStorage(t)
	ctx := context.Background()

	signing, err := storage.SigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, isSigner := signing.Key().(crypto.Signer); isSigner {
		t.Fatal("the library was handed a crypto key")
	}
	if _, opaque := signing.Key().(jose.OpaqueSigner); !opaque {
		t.Fatalf("the library's key is %T, want an opaque signer", signing.Key())
	}

	// What the library does with it (op.CreateIDToken and CreateJWT both).
	libSigner, err := op.SignerFromKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://issuer.example", "sub": "ada", "aud": []string{"cli"},
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "nonce": "n-0S6_WzA2Mj",
	})
	jws, err := libSigner.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jws.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	if len(spy.reqs) != 1 || spy.reqs[0].Purpose != signer.PurposeIDToken {
		t.Fatalf("Sign saw %+v, want one %q request", spy.reqs, signer.PurposeIDToken)
	}

	set, err := storage.KeySet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES384})
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Signatures[0].Header; got.KeyID != signing.ID() || got.Algorithm != string(jose.ES384) {
		t.Errorf("header kid %q alg %q, want %q ES384", got.KeyID, got.Algorithm, signing.ID())
	}
	verified := false
	for _, k := range set {
		if k.ID() != signing.ID() {
			continue
		}
		payload, err := parsed.Verify(k.Key())
		if err != nil {
			t.Fatalf("the token does not verify against the published key: %v", err)
		}
		if string(payload) != string(claims) {
			t.Errorf("payload = %s, want the library's claims %s", payload, claims)
		}
		verified = true
	}
	if !verified {
		t.Fatalf("the signing key %s is not in the JWKS", signing.ID())
	}
	if got := strings.Count(token, "."); got != 2 {
		t.Errorf("not a compact JWS: %q", token)
	}
}

// The signer's limits apply to library tokens too: a token that outlives the
// deployment's lifetime is refused, not signed.
func TestALibraryTokenOutlivingTheLifetimeIsRefused(t *testing.T) {
	t.Parallel()
	storage, _, spy := spiedStorage(t)
	signing, err := storage.SigningKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	libSigner, err := op.SignerFromKey(signing)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Unix(), "exp": now.Add(48 * time.Hour).Unix()})
	if _, err := libSigner.Sign(claims); err == nil {
		t.Fatal("a 48 hour library token was signed")
	}
	if len(spy.reqs) != 1 {
		t.Errorf("Sign saw %d requests, want the one that was refused", len(spy.reqs))
	}
}
