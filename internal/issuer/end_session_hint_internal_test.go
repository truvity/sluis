package issuer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/signer"
	"github.com/truvity/sluis/policy"
)

// A hint's `aud` and `azp` are names a request can make up until the hint
// verifies. One that does not verify reaches no client lookup, so a URL in its
// `aud` is never fetched as a client document: the client-document egress is
// not reachable from a hint somebody wrote.
func TestAHintThatDoesNotVerifyReachesNoClientLookup(t *testing.T) {
	t.Parallel()

	declared, err := policy.Parse([]byte("version: 1\n" +
		"groups: { viewers: { members: [eng@north.example] } }\n" +
		"clients:\n" +
		"  app: { kind: public, redirects: ['http://127.0.0.1/callback'], requires: [viewers] }\n"))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := New(Config{URL: "http://issuer.example", AllowInsecure: true}, set, aDirectory{}, NewMemoryState())

	key, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}

	storage, err := NewTestStorage(iss, nil, nil, key, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Client documents on, for the origin the hints name, and any fetch fails
	// the test.
	storage.documents = newDocumentClients(policy.ClientDocuments{
		Origins: []string{"clients.example"}, Requires: []string{"viewers"},
	})
	storage.documents.fetch.Transport = refuseToDial{t}

	provider, err := Provider(iss, storage)
	if err != nil {
		t.Fatal(err)
	}

	var asked []string

	known := knownClient(iss, storage)
	check := endSessionHints(iss.Config().URL, provider.IDTokenHintVerifier, func(ctx context.Context, id string) bool {
		asked = append(asked, id)
		return known(ctx, id)
	})

	const document = "https://clients.example/cimd.json"

	now := time.Now()
	claims := func(issuer, aud string) map[string]any {
		return map[string]any{
			"iss": issuer, "sub": "ada@north.example", "aud": aud,
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		}
	}

	other, err := signer.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}

	for name, hint := range map[string]string{
		"HS256 with a document audience": signedTestHint(t, jose.HS256, []byte("a-client-secret-of-at-least-32-bytes!"), "",
			claims("app", document)),
		"HS256 with the issuer as its audience": signedTestHint(t, jose.HS256, []byte("a-client-secret-of-at-least-32-bytes!"), "",
			claims("app", "http://issuer.example")),
		"another key":    signedTestHint(t, other.SignatureAlgorithm(), other.Key(), other.ID(), claims("http://issuer.example", document)),
		"another issuer": signedTestHint(t, key.SignatureAlgorithm(), key.Key(), key.ID(), claims("https://elsewhere.example", document)),
		"alg none": base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
			base64.RawURLEncoding.EncodeToString(mustJSON(t, claims("http://issuer.example", document))) + ".",
	} {
		asked = nil

		if err := check(t.Context(), hint); err == nil {
			t.Errorf("%s: accepted", name)
		}

		if len(asked) != 0 {
			t.Errorf("%s: looked up %v before the hint verified", name, asked)
		}
	}

	// A hint the issuer signed is looked up by the client it names.
	asked = nil
	if err := check(t.Context(), signedTestHint(t, key.SignatureAlgorithm(), key.Key(), key.ID(), claims("http://issuer.example", "app"))); err != nil {
		t.Errorf("the issuer's own hint for a declared client: %v", err)
	}

	if !slices.Equal(asked, []string{"app"}) {
		t.Errorf("looked up %v, want [app]", asked)
	}
}

func signedTestHint(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string, claims map[string]any) string {
	t.Helper()

	options := (&jose.SignerOptions{}).WithType("JWT")
	if kid != "" {
		options = options.WithHeader("kid", kid)
	}

	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, options)
	if err != nil {
		t.Fatal(err)
	}

	signed, err := s.Sign(mustJSON(t, claims))
	if err != nil {
		t.Fatal(err)
	}

	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}
