package policy_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"github.com/truvity/sluis/policy"
)

// jwk is the JSON of a key as a policy's `jwks` holds it, with the fields over
// set (an empty value removes one).
func jwk(t *testing.T, key any, kid, alg string, over map[string]any) string {
	t.Helper()

	raw, err := json.Marshal(jose.JSONWebKey{Key: key, KeyID: kid, Algorithm: alg, Use: "sig"})
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}

	for name, value := range over {
		if value == "" {
			delete(fields, name)
			continue
		}
		fields[name] = value
	}

	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}

func keyClientPolicy(fields string) string {
	return "clients:\n  portal: { kind: confidential, redirects: ['https://portal.example/cb'], requires: [a]" + fields + " }\n"
}

// A confidential client may authenticate with private_key_jwt: it holds keys,
// not a secret, and the set and the access document carry it.
func TestAConfidentialClientMayAuthenticateWithPrivateKeyJWT(t *testing.T) {
	t.Parallel()

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	keys := jwk(t, &ec.PublicKey, "k1", "ES256", nil) + ", " + jwk(t, &rs.PublicKey, "k2", "PS256", nil)

	declared, err := parseSessionPolicy(t, keyClientPolicy(", token_endpoint_auth_method: private_key_jwt, jwks: { keys: ["+keys+"] }"))
	if err != nil {
		t.Fatalf("a private_key_jwt client: %v, want accepted", err)
	}

	portal := declared.Clients["portal"]
	if !portal.PrivateKeyJWT() || portal.HoldsSecret() {
		t.Errorf("PrivateKeyJWT = %v, HoldsSecret = %v, want true and false", portal.PrivateKeyJWT(), portal.HoldsSecret())
	}

	set, err := portal.JWKS.KeySet()
	if err != nil || len(set) != 2 || set[0].KeyID != "k1" || set[1].KeyID != "k2" {
		t.Fatalf("KeySet = %v, %v", set, err)
	}

	built, err := policy.NewSet(declared)
	if err != nil {
		t.Fatal(err)
	}

	if got := built.PrivateKeyJWTClients(); !slices.Equal(got, []string{"portal"}) {
		t.Errorf("PrivateKeyJWTClients = %v, want [portal]", got)
	}

	access, err := policy.ParseAccess([]byte("version: 1\n" +
		"access:\n" +
		"  groups: [{ name: a, members: [g@h.example] }]\n" +
		"  clients:\n" +
		"    - { name: portal, kind: confidential, redirects: ['https://portal.example/cb'], requires: [a], " +
		"tokenEndpointAuthMethod: private_key_jwt, jwks: { keys: [" + jwk(t, &ec.PublicKey, "k1", "ES256", nil) + "] } }\n"))
	if err != nil {
		t.Fatalf("the access document: %v", err)
	}

	if !access.Clients["portal"].PrivateKeyJWT() || access.Clients["portal"].JWKS == nil {
		t.Error("tokenEndpointAuthMethod and jwks did not reach the layer")
	}
}

func TestPrivateKeyJWTRefusals(t *testing.T) {
	t.Parallel()

	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	small, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // the refusal of a small key is what is tested
	if err != nil {
		t.Fatal(err)
	}

	good := jwk(t, &ec.PublicKey, "k1", "ES256", nil)
	with := func(keys ...string) string {
		return ", token_endpoint_auth_method: private_key_jwt, jwks: { keys: [" + strings.Join(keys, ", ") + "] }"
	}

	for name, tc := range map[string]struct{ rest, want string }{
		"another method": {
			keyClientPolicy(", secret: s, token_endpoint_auth_method: client_secret_jwt"),
			`token_endpoint_auth_method "client_secret_jwt" is not "private_key_jwt"`,
		},
		"on a public client": {
			"clients:\n  cli: { kind: public, redirects: ['http://127.0.0.1/cb'], requires: [a]" + with(good) + " }\n",
			"is public and declares token_endpoint_auth_method: private_key_jwt",
		},
		"beside a secret": {
			keyClientPolicy(", secret: s" + with(good)),
			"also names a secret",
		},
		"without keys": {
			keyClientPolicy(", token_endpoint_auth_method: private_key_jwt"),
			"needs at least one public key",
		},
		"jwks without the method": {
			keyClientPolicy(", secret: s, jwks: { keys: [" + good + "] }"),
			"declares jwks without token_endpoint_auth_method",
		},
		"a private key": {
			keyClientPolicy(with(jwk(t, ec, "k1", "ES256", nil))),
			"holds private material",
		},
		"no kid": {
			keyClientPolicy(with(jwk(t, &ec.PublicKey, "", "ES256", nil))),
			"has no kid",
		},
		"a kid twice": {
			keyClientPolicy(with(good, good)),
			`kid "k1" is named twice`,
		},
		"no alg": {
			keyClientPolicy(with(jwk(t, &ec.PublicKey, "k1", "", nil))),
			`alg "" is not one of`,
		},
		"an algorithm the parser refuses": {
			keyClientPolicy(with(jwk(t, &p384.PublicKey, "k1", "ES384", nil))),
			`alg "ES384" is not one of`,
		},
		"an alg the key cannot sign": {
			keyClientPolicy(with(jwk(t, &p384.PublicKey, "k1", "ES256", nil))),
			"ES256 needs an EC key on P-256",
		},
		"a small RSA key": {
			keyClientPolicy(with(jwk(t, &small.PublicKey, "k1", "RS256", nil))),
			"at least 2048",
		},
		"an encryption key": {
			keyClientPolicy(with(jwk(t, &ec.PublicKey, "k1", "ES256", map[string]any{"use": "enc"}))),
			`use "enc"`,
		},
		"too many keys": {
			keyClientPolicy(with(jwk(t, &ec.PublicKey, "a", "ES256", nil), jwk(t, &ec.PublicKey, "b", "ES256", nil),
				jwk(t, &ec.PublicKey, "c", "ES256", nil), jwk(t, &ec.PublicKey, "d", "ES256", nil), jwk(t, &ec.PublicKey, "e", "ES256", nil))),
			"5 keys, at most 4",
		},
		"no secret and no method": {
			keyClientPolicy(""),
			"is confidential and names no secret",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := parseSessionPolicy(t, tc.rest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
