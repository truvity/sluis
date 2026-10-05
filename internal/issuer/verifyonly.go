package issuer

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// VerifyOnlyKey is a PUBLIC key the issuer publishes in its JWKS and never signs
// with: the key an earlier signer used, kept for a bounded overlap so tokens it
// signed keep verifying until they expire (the cutover from cert-manager file
// keys to kms-wrapped ones). Until, when set, is the instant after which the key
// is no longer published.
type VerifyOnlyKey struct {
	// ID is the `kid` the old tokens carry.
	ID    string
	Alg   jose.SignatureAlgorithm
	Pub   crypto.PublicKey
	Until time.Time
}

// ParseVerifyOnlyKey reads a public key, as a PEM (`PUBLIC KEY`, `RSA PUBLIC
// KEY` or a `CERTIFICATE`'s key) or a JWK. The `kid` is id when given, the JWK's
// own when it has one, and otherwise the RFC 7638 thumbprint of the key: the
// derivation a file signing key uses, so the kid an old token carries is the
// one published for the same key. The algorithm is alg when given (it must suit
// the key), else the one the key type signs with.
//
// A private key is refused, whatever the encoding: nothing here may hold the
// means to sign. The error names no key material.
func ParseVerifyOnlyKey(raw []byte, id, alg string, until time.Time) (VerifyOnlyKey, error) {
	pub, jwkID, err := parsePublicKey(raw)
	if err != nil {
		return VerifyOnlyKey{}, err
	}
	want, err := publicSignatureAlgorithm(pub)
	if err != nil {
		return VerifyOnlyKey{}, err
	}
	if alg != "" && jose.SignatureAlgorithm(alg) != want {
		return VerifyOnlyKey{}, fmt.Errorf("issuer: the key is for %s and alg says %s", want, alg)
	}
	if id == "" {
		id = jwkID
	}
	if id == "" {
		if id, err = thumbprint(pub); err != nil {
			return VerifyOnlyKey{}, err
		}
	}
	return VerifyOnlyKey{ID: id, Alg: want, Pub: pub, Until: until}, nil
}

var errPrivateKey = errors.New("issuer: a verify-only key must be a PUBLIC key, and this one holds a private key")

func parsePublicKey(raw []byte) (crypto.PublicKey, string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var jwk jose.JSONWebKey
		if err := json.Unmarshal([]byte(trimmed), &jwk); err != nil {
			return nil, "", errors.New("issuer: the key is not a JWK or a PEM")
		}
		if !jwk.Valid() {
			return nil, "", errors.New("issuer: the JWK is not valid")
		}
		if !jwk.IsPublic() {
			return nil, "", errPrivateKey
		}
		return jwk.Key, jwk.KeyID, nil
	}
	var found crypto.PublicKey
	rest := []byte(trimmed)
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return nil, "", errPrivateKey
		}
		var pub any
		var err error
		switch block.Type {
		case "PUBLIC KEY":
			pub, err = x509.ParsePKIXPublicKey(block.Bytes)
		case "RSA PUBLIC KEY":
			pub, err = x509.ParsePKCS1PublicKey(block.Bytes)
		case "CERTIFICATE":
			var cert *x509.Certificate
			if cert, err = x509.ParseCertificate(block.Bytes); err == nil {
				pub = cert.PublicKey
			}
		default:
			return nil, "", fmt.Errorf("issuer: a PEM block of type %q is not a public key", block.Type)
		}
		if err != nil {
			return nil, "", fmt.Errorf("issuer: a %s block is not a public key: %w", block.Type, err)
		}
		if found != nil {
			return nil, "", errors.New("issuer: a verify-only key file holds one key, and this holds several")
		}
		found = pub
	}
	if found == nil {
		return nil, "", errors.New("issuer: the key is not a JWK or a PEM")
	}
	return found, "", nil
}

func publicSignatureAlgorithm(pub crypto.PublicKey) (jose.SignatureAlgorithm, error) {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		return jose.RS256, nil
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return jose.ES256, nil
		case elliptic.P384():
			return jose.ES384, nil
		case elliptic.P521():
			return jose.ES512, nil
		}
	}
	return "", fmt.Errorf("issuer: a verify-only key is an RSA or an ECDSA key on P-256, P-384 or P-521, not %T", pub)
}

// UseVerifyOnly sets the keys published beside the rings' own. They are never
// signed with: [Storage.SigningKey] knows only the rings. A key whose id is one
// a ring publishes is left to the ring.
func (s *Storage) UseVerifyOnly(keys []VerifyOnlyKey, now func() time.Time) {
	s.verifyOnly = keys
	if now == nil {
		now = time.Now
	}
	s.now = now
}

// verifyOnlyKeys are the verify-only keys still inside their lifetime.
func (s *Storage) verifyOnlyKeys(published []op.Key) []op.Key {
	have := map[string]bool{}
	for _, k := range published {
		have[k.ID()] = true
	}
	var out []op.Key
	for _, k := range s.verifyOnly {
		if (!k.Until.IsZero() && !s.now().Before(k.Until)) || have[k.ID] {
			continue
		}
		out = append(out, publishedKey{id: k.ID, alg: k.Alg, pub: k.Pub})
	}
	return out
}
