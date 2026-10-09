package issuer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/truvity/sluis/internal/signer"
)

// libraryKey is the [op.SigningKey] the OpenID library signs an ID token or a
// JWT access token with. Its Key is not a private key but an opaque signer
// that calls [signer.Signer.Sign]: the library builds the header and the
// claims, the signer decides whether to sign them, and no key crosses over.
type libraryKey struct {
	alg    jose.SignatureAlgorithm
	kid    string
	opaque *libraryOpaque
}

var _ op.SigningKey = libraryKey{}

func (k libraryKey) SignatureAlgorithm() jose.SignatureAlgorithm { return k.alg }
func (k libraryKey) ID() string                                  { return k.kid }
func (k libraryKey) Key() any                                    { return k.opaque }

// libraryKey is the signing key for the active key id kid of alg: the opaque
// signer carries the public half, which go-jose needs to put the kid in the
// header and nothing else.
func (s *Storage) libraryKey(ctx context.Context, alg jose.SignatureAlgorithm, kid string) (op.SigningKey, error) {
	public, err := s.signer.PublicKeys(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range public {
		if p.KID == kid && p.Algorithm == alg {
			return libraryKey{alg: alg, kid: kid, opaque: &libraryOpaque{
				ctx: ctx, sg: s.signer,
				pub: jose.JSONWebKey{Key: p.Key, KeyID: kid, Algorithm: string(alg), Use: "sig"},
			}}, nil
		}
	}
	return nil, fmt.Errorf("issuer: the active %s key %s is not published", alg, kid)
}

// libraryOpaque is a [jose.OpaqueSigner] over [signer.Signer.Sign].
type libraryOpaque struct {
	// ctx is the request's: go-jose's SignPayload carries none.
	ctx context.Context //nolint:containedctx // see above
	sg  signer.Signer
	pub jose.JSONWebKey
}

var _ jose.OpaqueSigner = (*libraryOpaque)(nil)

// Public implements [jose.OpaqueSigner].
func (o *libraryOpaque) Public() *jose.JSONWebKey { p := o.pub; return &p }

// Algs implements [jose.OpaqueSigner].
func (o *libraryOpaque) Algs() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{jose.SignatureAlgorithm(o.pub.Algorithm)}
}

// SignPayload implements [jose.OpaqueSigner]. go-jose hands it the JWS signing
// input, base64url(header) "." base64url(claims). The claims go to the signer,
// which builds its own header from the purpose and the active key; the token it
// returns is used only if its signing input is byte for byte the library's,
// and then only its signature is taken. A header the library built differently
// (another typ, a key that rotated meanwhile) is refused, never re-signed.
func (o *libraryOpaque) SignPayload(input []byte, alg jose.SignatureAlgorithm) ([]byte, error) {
	header, claims, ok := strings.Cut(string(input), ".")
	if !ok {
		return nil, errors.New("issuer: the library's signing input is not header.claims")
	}
	payload, err := base64.RawURLEncoding.DecodeString(claims)
	if err != nil {
		return nil, fmt.Errorf("issuer: the library's claims are not base64url: %w", err)
	}
	signed, err := o.sg.Sign(o.ctx, signer.Request{Purpose: signer.PurposeIDToken, Algorithm: alg, Payload: payload})
	if err != nil {
		return nil, err
	}
	parts := strings.Split(signed.Token, ".")
	if len(parts) != 3 || parts[0] != header || parts[1] != claims {
		return nil, errors.New("issuer: the signer's header is not the one the library built (the key may have rotated); retry")
	}
	return base64.RawURLEncoding.DecodeString(parts[2])
}
