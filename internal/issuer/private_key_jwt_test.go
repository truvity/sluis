package issuer_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/demo"
	"github.com/truvity/sluis/internal/issuer"
	"github.com/truvity/sluis/policy"
)

const assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// keyClient is a client that authenticates with private_key_jwt: its key, and
// the issuer it signs assertions for.
type keyClient struct {
	id     string
	kid    string
	key    *ecdsa.PrivateKey
	issuer string
}

// assertion signs a client assertion (RFC 7523 2.2) with claims over the
// defaults: iss and sub the client, aud the issuer, a fresh jti, a minute of
// life. A nil value removes a claim.
func (c keyClient) assertion(t *testing.T, over map[string]any) string {
	t.Helper()

	now := time.Now()
	claims := map[string]any{
		"iss": c.id, "sub": c.id, "aud": c.issuer, "jti": strings.ReplaceAll(rand.Text(), "-", ""),
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	}
	for name, value := range over {
		if value == nil {
			delete(claims, name)
			continue
		}
		claims[name] = value
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: c.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", c.kid))
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}

	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func (c keyClient) form(assertion string, form url.Values) url.Values {
	out := url.Values{"client_assertion_type": {assertionType}, "client_assertion": {assertion}}
	for name, values := range form {
		out[name] = values
	}

	return out
}

// publicJWK is the JSON of key's public half, as a policy's `jwks` holds it.
func publicJWK(t *testing.T, key *ecdsa.PrivateKey, kid string) string {
	t.Helper()

	raw, err := json.Marshal(jose.JSONWebKey{Key: &key.PublicKey, KeyID: kid, Algorithm: "ES256", Use: "sig"})
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
}

// privateKeyJWTServer serves an issuer whose policy has one private_key_jwt
// client, `portal`, and one secret client, `argocd`.
func privateKeyJWTServer(t *testing.T) (*httptest.Server, *issuer.Issuer, keyClient) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	const issuerURL = "http://issuer.example"

	doc := `
version: 1
groups:
  devel:k8s:viewer: { members: [engineering@north.example] }
lifetimes: { default: 12h }
clients:
  portal:
    kind: confidential
    token_endpoint_auth_method: private_key_jwt
    jwks: { keys: [` + publicJWK(t, key, "portal-1") + `] }
    pkce: required
    redirects: [https://portal.example/_session/callback]
    signed_out: [https://portal.example/]
    requires: [devel:k8s:viewer]
  argocd:
    kind: confidential
    secret: argocd-oidc-client
    redirects: [https://argocd.example/auth/callback]
    requires: [devel:k8s:viewer]
`

	declared, err := policy.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := issuer.New(issuer.Config{URL: issuerURL, AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, lookup{"argocd": clientcreds.Secrets{Current: "right"}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	return server, iss, keyClient{id: "portal", kid: "portal-1", key: key, issuer: issuerURL}
}

func openKeySession(t *testing.T, iss *issuer.Issuer, client, refresh string) {
	t.Helper()

	if _, err := iss.Sessions().Record(t.Context(), issuer.Opened{
		Identity: "ada@north.example", ClientID: client, How: issuer.HowCode,
		Token: refresh, Scopes: []string{"openid", "email", "offline_access"},
	}); err != nil {
		t.Fatalf("record the session: %v", err)
	}
}

func refreshOf(token string) url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "scope": {"openid email offline_access"}}
}

// A client that declares `token_endpoint_auth_method: private_key_jwt`
// authenticates at the token endpoint with an assertion signed by a key of its
// `jwks`, and with nothing else; and each assertion is spent once.
func TestAPrivateKeyJWTClientAuthenticatesWithItsAssertionOnce(t *testing.T) {
	t.Parallel()

	server, iss, portal := privateKeyJWTServer(t)

	openKeySession(t, iss, "portal", "rt-ok")

	once := portal.assertion(t, nil)

	got := postForm(t, server, "/token", portal.form(once, refreshOf("rt-ok")), "", "", nil)
	if got.status != http.StatusOK || got.body["access_token"] == nil {
		t.Fatalf("a refresh with a good assertion: %d %s", got.status, got.raw)
	}

	// The same assertion again, on another refresh token of the client.
	openKeySession(t, iss, "portal", "rt-replay")

	got = postForm(t, server, "/token", portal.form(once, refreshOf("rt-replay")), "", "", nil)
	if got.status < 400 || got.body["error"] != "invalid_client" || got.body["access_token"] != nil {
		t.Errorf("a replayed assertion: %d %s, want invalid_client", got.status, got.raw)
	}

	stranger, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		assertion string
		form      url.Values
		user      string
		pass      string
	}{
		"no jti":            {assertion: portal.assertion(t, map[string]any{"jti": nil})},
		"valid for an hour": {assertion: portal.assertion(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})},
		"expired": {assertion: portal.assertion(t, map[string]any{
			"iat": time.Now().Add(-3 * time.Minute).Unix(), "exp": time.Now().Add(-time.Minute).Unix(),
		})},
		"another audience":       {assertion: portal.assertion(t, map[string]any{"aud": "https://elsewhere.example"})},
		"sub is not the client":  {assertion: portal.assertion(t, map[string]any{"sub": "argocd"})},
		"signed by another key":  {assertion: keyClient{id: "portal", kid: "portal-1", key: stranger, issuer: portal.issuer}.assertion(t, nil)},
		"a kid it does not hold": {assertion: keyClient{id: "portal", kid: "portal-2", key: portal.key, issuer: portal.issuer}.assertion(t, nil)},
		"as another client":      {assertion: keyClient{id: "argocd", kid: "portal-1", key: portal.key, issuer: portal.issuer}.assertion(t, nil)},
		"a secret, no assertion": {form: url.Values{"client_id": {"portal"}, "client_secret": {"anything"}}},
		"nothing at all":         {form: url.Values{"client_id": {"portal"}}},
		"a secret in Basic":      {user: "portal", pass: "anything"},
	} {
		refresh := "rt-" + strings.ReplaceAll(name, " ", "-")
		openKeySession(t, iss, "portal", refresh)

		form := refreshOf(refresh)
		if tc.assertion != "" {
			form = portal.form(tc.assertion, form)
		}
		for k, v := range tc.form {
			form[k] = v
		}

		got := postForm(t, server, "/token", form, tc.user, tc.pass, nil)
		if got.status < 400 || got.body["access_token"] != nil {
			t.Errorf("%s: %d %s, want a refusal", name, got.status, got.raw)
		}
	}

	// A client with a secret still authenticates with it, and not with an
	// assertion it has no key for.
	openKeySession(t, iss, "argocd", "rt-argocd")

	if got := postForm(t, server, "/token", refreshOf("rt-argocd"), "argocd", "right", nil); got.status != http.StatusOK {
		t.Errorf("the secret client with its secret: %d %s", got.status, got.raw)
	}
}

// Revocation takes the client's assertion too (RFC 7009 2.1).
func TestAPrivateKeyJWTClientRevokesWithAnAssertion(t *testing.T) {
	t.Parallel()

	server, iss, portal := privateKeyJWTServer(t)

	openKeySession(t, iss, "portal", "rt-revoke")

	got := postForm(t, server, "/revoke", portal.form(portal.assertion(t, nil),
		url.Values{"token": {"rt-revoke"}, "token_type_hint": {"refresh_token"}}), "", "", nil)
	if got.status != http.StatusOK {
		t.Fatalf("revoke with an assertion: %d %s", got.status, got.raw)
	}

	got = postForm(t, server, "/token", portal.form(portal.assertion(t, nil), refreshOf("rt-revoke")), "", "", nil)
	if got.status < 400 || got.body["access_token"] != nil {
		t.Errorf("a refresh after the revocation: %d %s, want a refusal", got.status, got.raw)
	}
}

// Discovery advertises `private_key_jwt` and the algorithms of an assertion
// while a client declares it, and neither while none does.
func TestDiscoveryAdvertisesPrivateKeyJWTOnlyWhileAClientDeclaresIt(t *testing.T) {
	t.Parallel()

	server, _, _ := privateKeyJWTServer(t)
	doc := discoveryOf(t, server)

	for _, field := range []string{"token_endpoint_auth_methods_supported", "revocation_endpoint_auth_methods_supported"} {
		if !slices.Contains(stringsOf(doc[field]), "private_key_jwt") {
			t.Errorf("%s = %v, want private_key_jwt among them", field, doc[field])
		}
	}

	for _, field := range []string{"token_endpoint_auth_signing_alg_values_supported", "revocation_endpoint_auth_signing_alg_values_supported"} {
		if got := stringsOf(doc[field]); !slices.Equal(got, policy.ClientAssertionAlgs) {
			t.Errorf("%s = %v, want %v", field, got, policy.ClientAssertionAlgs)
		}
	}

	declared, err := policy.Parse([]byte(demo.Policy))
	if err != nil {
		t.Fatal(err)
	}

	set, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	iss := issuer.New(issuer.Config{URL: "http://issuer.example", AllowInsecure: true}, set, adaDirectory(), issuer.NewMemoryState())

	storage, err := issuer.NewTestStorage(iss, fakeVerifier{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	h, err := handler(iss, storage)
	if err != nil {
		t.Fatal(err)
	}

	plain := httptest.NewServer(h)
	t.Cleanup(plain.Close)

	doc = discoveryOf(t, plain)
	if slices.Contains(stringsOf(doc["token_endpoint_auth_methods_supported"]), "private_key_jwt") {
		t.Errorf("token_endpoint_auth_methods_supported = %v with no private_key_jwt client", doc["token_endpoint_auth_methods_supported"])
	}

	for _, field := range []string{"token_endpoint_auth_signing_alg_values_supported", "revocation_endpoint_auth_signing_alg_values_supported"} {
		if _, ok := doc[field]; ok {
			t.Errorf("%s is advertised with no private_key_jwt client", field)
		}
	}
}

func discoveryOf(t *testing.T, server *httptest.Server) map[string]any {
	t.Helper()

	resp, err := server.Client().Get(server.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var doc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}

	return doc
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}

	return out
}
