package issuer_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strings"
	"testing"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/internal/signer"
)

// mustSoftKey is a software signing key: an RSA one for RS256, else the P-384 default.
func mustSoftKey(t *testing.T, rsaKey bool) *signer.SigningKey {
	t.Helper()
	if !rsaKey {
		k, err := signer.NewSigningKey()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k, err := signer.ParseSigningKey(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(raw)}))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A client pinned to signing_alg RS256 gets its token from the RS256 ring,
// while the default audience is signed by the KMS ES384 key.
func TestAPinnedRS256ClientIsSignedByTheRSAKey(t *testing.T) {
	t.Parallel()
	es := mustSoftKey(t, false)
	rs := mustSoftKey(t, true)
	server := newMultiAlgServerWith(t, es, rs)

	b := newBrowser(t, server)
	b.signIn()
	pinned := redeem(t, b, b.authorize("&resource="+url.QueryEscape("https://resource.example/rs256")))
	header, _ := verifiedHeader(t, server, pinned["access_token"].(string))
	if header["alg"] != string(jose.RS256) || header["kid"] != rs.ID() {
		t.Fatalf("pinned access token header = %v, want RS256 by the RSA key %s", header, rs.ID())
	}
	plain, _ := verifiedHeader(t, server, pinned["id_token"].(string))
	if plain["alg"] != string(jose.ES384) || plain["kid"] != es.ID() {
		t.Fatalf("default id token header = %v, want ES384 by the EC key", plain)
	}
}

// M1: a client pinned to RS256 whose ring has no active signer (its only key
// is another replica's old one, and this replica's new key is not yet
// activated) gets an error, never an ES384 token.
func TestAPinnedRS256ClientNeverFallsBackToES384(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := issuer.NewMemoryState()
	// Another replica recorded an RSA key, immediately active there.
	other := signer.NewKeyRing(jose.RS256, state, signer.KeyRingConfig{}, nil)
	if err := other.Observe(ctx, mustSoftKey(t, true)); err != nil {
		t.Fatal(err)
	}
	es := mustSoftKey(t, false)
	fresh := mustSoftKey(t, true) // new here: waits its delay
	server, _ := newMultiAlgServerState(t, es, fresh, state)

	b := newBrowser(t, server)
	b.signIn()
	sentTo := b.authorize("&resource=" + url.QueryEscape("https://resource.example/rs256"))
	for strings.HasPrefix(sentTo, "/") {
		_, sentTo, _ = b.do(http.MethodGet, sentTo)
	}
	back, _ := url.Parse(sentTo)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")},
		"redirect_uri": {"http://localhost:8000/callback"}, "client_id": {"local-dev"}, "code_verifier": {pkceVerifier},
	}
	resp, err := http.Post(server.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())) //nolint:noctx // a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if tok, ok := body["access_token"].(string); ok {
		t.Fatalf("an RS256-pinned request got a token (%s...) while the RS256 ring had no signer", tok[:10])
	}
}

// rsaSigningKey is a software RSA signing key, for RS256.
func rsaSigningKey(t *testing.T) *signer.SigningKey { return mustSoftKey(t, true) }
