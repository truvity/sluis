package issuer_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/signer"
)

// signedHint is a JWT with the claims given, signed by key under alg.
func signedHint(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string, claims map[string]any) string {
	t.Helper()

	options := (&jose.SignerOptions{}).WithType("JWT")
	if kid != "" {
		options = options.WithHeader("kid", kid)
	}

	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, options)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	signed, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}

	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	return token
}

// unsignedHint is a JWT with `alg: none` and no signature.
func unsignedHint(t *testing.T, claims map[string]any) string {
	t.Helper()

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
}

// hintClaims are an ID token's claims for ada at the demonstration issuer,
// valid for an hour, with the changes a case makes.
func hintClaims(changes map[string]any) map[string]any {
	now := time.Now()
	claims := map[string]any{
		"iss": "http://issuer.example", "sub": "ada@north.example",
		"aud": []string{"local-dev"}, "azp": "local-dev",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "auth_time": now.Unix(),
	}

	for name, value := range changes {
		if value == nil {
			delete(claims, name)
			continue
		}

		claims[name] = value
	}

	return claims
}

// ownKeyHint is a hint the issuer's own key signed, with the changes given.
func ownKeyHint(t *testing.T, key *signer.SigningKey, changes map[string]any) string {
	t.Helper()

	return signedHint(t, key.SignatureAlgorithm(), key.Key(), key.ID(), hintClaims(changes))
}

// OpenID Connect RP-Initiated Logout 1.0 2: an `id_token_hint` the issuer
// cannot accept is an error, shown as one. It is never the page that asks the
// person to sign out, and it ends nothing.
//
// The conformance suite's `oidcc-rp-initiated-logout-bad-id-token-hint` sends
// the first shape: HS256 under the client's secret, the client as `iss`, and
// the issuer's own URL as `aud`. The issuer looked that `aud` up as a client
// document, refused it in a log line without a status, and then showed the
// page that asks.
func TestAnInvalidIDTokenHintIsRefusedBeforeThePageThatAsks(t *testing.T) {
	t.Parallel()
	key := mustSoftKey(t, false)
	secret := []byte("a-client-secret-of-at-least-32-bytes!")

	valid := ownKeyHint(t, key, nil)
	dot := strings.LastIndex(valid, ".")
	signature := []byte(valid[dot+1:])
	signature[len(signature)/2] ^= 0x01
	modified := valid[:dot+1] + string(signature)

	cases := map[string]string{
		"HS256 with the issuer as its audience": signedHint(t, jose.HS256, secret, "", map[string]any{
			"iss": "local-dev", "sub": "SubjectID", "aud": "http://issuer.example", "nonce": "flibble",
			"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		}),
		"HS256 for a declared client": signedHint(t, jose.HS256, secret, "", hintClaims(nil)),
		"alg none":                    unsignedHint(t, hintClaims(nil)),
		"another key":                 ownKeyHint(t, mustSoftKey(t, false), nil),
		"a modified signature":        modified,
		"another issuer":              ownKeyHint(t, key, map[string]any{"iss": "https://elsewhere.example"}),
		"another issuer, expired": ownKeyHint(t, key, map[string]any{
			"iss": "https://elsewhere.example", "iat": time.Now().Add(-2 * time.Hour).Unix(), "exp": time.Now().Add(-time.Hour).Unix(),
		}),
		"the issuer as its audience": ownKeyHint(t, key, map[string]any{"aud": "http://issuer.example", "azp": nil}),
		"an undeclared audience":     ownKeyHint(t, key, map[string]any{"aud": "nobody", "azp": nil}),
		"a declared and an undeclared audience": ownKeyHint(t, key,
			map[string]any{"aud": []string{"local-dev", "nobody"}}),
		"an undeclared authorized party": ownKeyHint(t, key, map[string]any{"azp": "nobody"}),
		"a client document audience": ownKeyHint(t, key,
			map[string]any{"aud": "https://clients.example/cimd.json", "azp": nil}),
		"no audience": ownKeyHint(t, key, map[string]any{"aud": nil, "azp": nil}),
		"expired, with an undeclared audience": ownKeyHint(t, key, map[string]any{
			"aud": "nobody", "azp": nil, "iat": time.Now().Add(-2 * time.Hour).Unix(), "exp": time.Now().Add(-time.Hour).Unix(),
		}),
		"not a token": "not.a.token",
	}

	for name, hint := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A server each: the fake directory behind one is not safe for
			// parallel sign-ins. The key is shared, so the hints verify alike.
			b := newBrowser(t, hintServer(t, "ada@north.example", key))
			b.signIn()

			query := "?id_token_hint=" + url.QueryEscape(hint) + "&state=xyz&post_logout_redirect_uri=" +
				url.QueryEscape("http://localhost:8000/callback")

			status, location, page := endSessionOnce(t, b, http.MethodGet, query, "text/html", nil)
			if status != http.StatusBadRequest || location != "" {
				t.Fatalf("GET = %d to %q, want 400 and no redirect", status, location)
			}

			if strings.Contains(page, `action="/end_session"`) || strings.Contains(page, "Sign out?") {
				t.Fatalf("an invalid hint was answered with the page that asks:\n%s", page)
			}

			wantRefusedPage(t, page, "invalid request", "id_token_hint invalid", "status=400")

			if signedOut(b) {
				t.Error("an invalid hint ended the sign-in")
			}

			if where := b.authorize(""); !strings.Contains(where, "/authorize/callback") {
				t.Errorf("an invalid hint ended the sign-in (%q)", where)
			}

			// A program reads the OAuth error, with the same status.
			status, _, raw := endSessionOnce(t, b, http.MethodGet, query, "application/json", nil)
			if status != http.StatusBadRequest || !strings.Contains(raw, `"error":"invalid_request"`) {
				t.Errorf("a client got %d %s, want 400 invalid_request", status, raw)
			}
		})
	}
}

// A browser with no sign-in has nothing to ask about, and an invalid hint is
// refused there the same way.
func TestAnInvalidIDTokenHintIsRefusedWithoutASignIn(t *testing.T) {
	t.Parallel()
	key := mustSoftKey(t, false)
	b := newBrowser(t, hintServer(t, "ada@north.example", key))

	hint := ownKeyHint(t, key, map[string]any{"aud": "http://issuer.example", "azp": nil})
	status, location, page := endSessionOnce(t, b, http.MethodGet, "?id_token_hint="+url.QueryEscape(hint), "text/html", nil)
	if status != http.StatusBadRequest || location != "" {
		t.Fatalf("GET = %d to %q %s, want 400", status, location, page)
	}
}
