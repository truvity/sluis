package policy

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/go-jose/go-jose/v4"
)

// MaxClientKeys bounds a client's `jwks`: room for a key and its successor
// during a change, with some to spare, and no more.
const MaxClientKeys = 4

// ClientAssertionAlgs are the algorithms a client's assertion may be signed
// with, and so the `alg` a key of its `jwks` may name. They are the ones the
// issuer's assertion parser accepts; a key for another algorithm could never
// verify an assertion, so it is refused at load rather than at sign-in.
var ClientAssertionAlgs = []string{string(jose.ES256), string(jose.PS256), string(jose.RS256)}

// ClientJWKS is a client's public key set, as a JSON Web Key Set (RFC 7517
// 5) spelled in the policy's YAML: `jwks: {keys: [{kty: EC, crv: P-256, x:
// ..., y: ..., kid: ..., alg: ES256}]}`.
type ClientJWKS struct {
	Keys []map[string]any `yaml:"keys" json:"keys"`
}

// KeySet reads the keys as JSON Web Keys. It is what [Client.validate] checks
// and what the issuer verifies an assertion with.
func (j *ClientJWKS) KeySet() ([]jose.JSONWebKey, error) {
	if j == nil {
		return nil, nil
	}
	out := make([]jose.JSONWebKey, 0, len(j.Keys))
	for i, raw := range j.Keys {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		var key jose.JSONWebKey
		if err := key.UnmarshalJSON(encoded); err != nil {
			return nil, fmt.Errorf("key %d is not a JSON Web Key: %w", i, err)
		}
		out = append(out, key)
	}
	return out, nil
}

// validateAuthMethod holds `token_endpoint_auth_method` and `jwks` to their
// rules: the method is `private_key_jwt` or absent, on a confidential client
// only, with no secret beside it, and with a key set of public signing keys,
// each named by a distinct `kid` and pinned to an algorithm of
// [ClientAssertionAlgs].
func (c Client) validateAuthMethod(id string) error {
	switch c.TokenEndpointAuthMethod {
	case "":
		if c.JWKS != nil {
			return fmt.Errorf("client %q declares jwks without token_endpoint_auth_method: private_key_jwt: "+
				"the keys would check nothing", id)
		}
		return nil
	case AuthPrivateKeyJWT:
	default:
		return fmt.Errorf("client %q: token_endpoint_auth_method %q is not %q (absent, a confidential client presents its secret)",
			id, c.TokenEndpointAuthMethod, AuthPrivateKeyJWT)
	}
	if c.Kind != KindConfidential {
		return fmt.Errorf("client %q is %s and declares token_endpoint_auth_method: private_key_jwt: only a confidential client "+
			"authenticates at the token endpoint", id, c.Kind)
	}
	if !c.Secret.IsZero() {
		return fmt.Errorf("client %q authenticates with private_key_jwt and also names a secret: it holds a key instead of a secret, "+
			"so remove the secret", id)
	}
	if err := c.JWKS.validate(); err != nil {
		return fmt.Errorf("client %q: jwks: %w", id, err)
	}
	return nil
}

func (j *ClientJWKS) validate() error {
	if j == nil || len(j.Keys) == 0 {
		return errors.New("a private_key_jwt client needs at least one public key")
	}
	if len(j.Keys) > MaxClientKeys {
		return fmt.Errorf("%d keys, at most %d", len(j.Keys), MaxClientKeys)
	}
	keys, err := j.KeySet()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range keys {
		key := &keys[i]
		switch {
		case !key.Valid():
			return fmt.Errorf("key %d is not a valid key", i)
		case !key.IsPublic():
			return fmt.Errorf("key %d holds private material: a client's jwks carries its public keys only", i)
		case key.KeyID == "":
			return fmt.Errorf("key %d has no kid: an assertion names the key it is signed with", i)
		case seen[key.KeyID]:
			return fmt.Errorf("kid %q is named twice", key.KeyID)
		case !slices.Contains(ClientAssertionAlgs, key.Algorithm):
			return fmt.Errorf("key %q: alg %q is not one of %v", key.KeyID, key.Algorithm, ClientAssertionAlgs)
		case key.Use != "" && key.Use != "sig":
			return fmt.Errorf("key %q: use %q, a client assertion key is for signatures (sig)", key.KeyID, key.Use)
		}
		if err := algFitsKey(key); err != nil {
			return fmt.Errorf("key %q: %w", key.KeyID, err)
		}
		seen[key.KeyID] = true
	}
	return nil
}

// algFitsKey refuses an `alg` the key cannot sign: ES256 is a P-256 key, PS256
// and RS256 an RSA key of at least 2048 bits.
func algFitsKey(key *jose.JSONWebKey) error {
	switch key.Algorithm {
	case string(jose.ES256):
		if ec, ok := ecdsaPublic(key.Key); !ok || ec != "P-256" {
			return errors.New("alg ES256 needs an EC key on P-256")
		}
	case string(jose.PS256), string(jose.RS256):
		bits, ok := rsaBits(key.Key)
		if !ok {
			return fmt.Errorf("alg %s needs an RSA key", key.Algorithm)
		}
		if bits < 2048 {
			return fmt.Errorf("an RSA key of %d bits, at least 2048", bits)
		}
	}
	return nil
}

// ecdsaPublic names the curve of an ECDSA public key.
func ecdsaPublic(key any) (string, bool) {
	k, ok := key.(*ecdsa.PublicKey)
	if !ok || k.Curve == nil {
		return "", false
	}
	return k.Curve.Params().Name, true
}

// rsaBits is the size of an RSA public key.
func rsaBits(key any) (int, bool) {
	k, ok := key.(*rsa.PublicKey)
	if !ok || k.N == nil {
		return 0, false
	}
	return k.N.BitLen(), true
}
