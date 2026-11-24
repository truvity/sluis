package mimic

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/go-jose/go-jose/v4"
)

// keys are mimic's own: one signing key for every issuer, and the keys derived from the secret, one per purpose.
type keys struct {
	signer  crypto.Signer
	alg     jose.SignatureAlgorithm
	kid     string
	public  jose.JSONWebKey
	subject []byte // the subject hash and the stable UUIDs
	session []byte // the MAC of the session and sign-in cookies
	choice  []byte // the token of the identity chooser's form
}

// The labels of the keys derived from the secret (RFC 5869 info).
const (
	infoSubject = "sluis-mimic subject v1"
	infoSession = "sluis-mimic session v1"
	infoChoice  = "sluis-mimic choice v1"
)

func loadKeys(k Keys) (*keys, error) {
	pemBytes, err := readSource(k.Signing)
	if err != nil {
		return nil, fmt.Errorf("keys.signing: %w", err)
	}
	signer, alg, err := parseSigningKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("keys.signing: %w", err)
	}
	secret, err := readSource(k.Secret)
	if err != nil {
		return nil, fmt.Errorf("keys.secret: %w", err)
	}
	if len(secret) < 32 {
		return nil, errors.New("keys.secret: at least 32 bytes")
	}
	return newKeys(signer, alg, secret)
}

func newKeys(signer crypto.Signer, alg jose.SignatureAlgorithm, secret []byte) (*keys, error) {
	pub := jose.JSONWebKey{Key: signer.Public(), Algorithm: string(alg), Use: "sig"}
	thumb, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	pub.KeyID = base64.RawURLEncoding.EncodeToString(thumb)
	sub, err := hkdf.Key(sha256.New, secret, nil, infoSubject, 32)
	if err != nil {
		return nil, err
	}
	ses, err := hkdf.Key(sha256.New, secret, nil, infoSession, 32)
	if err != nil {
		return nil, err
	}
	choice, err := hkdf.Key(sha256.New, secret, nil, infoChoice, 32)
	if err != nil {
		return nil, err
	}
	return &keys{signer: signer, alg: alg, kid: pub.KeyID, public: pub, subject: sub, session: ses, choice: choice}, nil
}

// parseSigningKey reads a PKCS#8 PEM private key: ECDSA P-256 signs ES256, RSA of 2048 bits or more signs RS256.
func parseSigningKey(b []byte) (crypto.Signer, jose.SignatureAlgorithm, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, "", errors.New("not a PKCS#8 PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", errors.New("not a PKCS#8 PEM private key")
	}
	switch k := k.(type) {
	case *ecdsa.PrivateKey:
		if k.Curve != elliptic.P256() {
			return nil, "", errors.New("an ECDSA key must be on P-256")
		}
		return k, jose.ES256, nil
	case *rsa.PrivateKey:
		if k.N.BitLen() < 2048 {
			return nil, "", errors.New("an RSA key must have 2048 bits or more")
		}
		return k, jose.RS256, nil
	default:
		return nil, "", errors.New("the key must be ECDSA P-256 or RSA")
	}
}

// sign makes a compact JWS of the claims with the type header typ.
func (k *keys) sign(typ string, claims map[string]any) (string, error) {
	opts := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader(jose.HeaderKey("kid"), k.kid)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: k.alg, Key: k.signer}, opts)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	obj, err := s.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

// mac is HMAC-SHA256 under key over the JSON array of parts, so no two lists of parts share an input.
func mac(key []byte, parts ...string) []byte {
	in, _ := json.Marshal(parts)
	h := hmac.New(sha256.New, key)
	h.Write(in)
	return h.Sum(nil)
}

// subjectOf is the `sub` of an identity for a subject of the stable sluis: a keyed hash of the sluis subject, the
// tenant and the identity, the same on every run while the secret stays.
func (k *keys) subjectOf(sluisSub, tenant, username string) string {
	return base64.RawURLEncoding.EncodeToString(mac(k.subject, "sub", sluisSub, tenant, username))
}

// uuidOf is a stable UUID (RFC 9562 version 8) of the parts, for Entra's `tid` and `oid`.
func (k *keys) uuidOf(parts ...string) string {
	b := mac(k.subject, append([]string{"uuid"}, parts...)...)[:16]
	b[6] = b[6]&0x0f | 0x80 // version 8
	b[8] = b[8]&0x3f | 0x80 // variant 10
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
