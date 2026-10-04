package issuer

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
	jose "github.com/go-jose/go-jose/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// KMSAPI is the part of the AWS KMS client the issuer calls. It is narrow so
// that a test signs with a local key and a deployment passes the SDK client.
type KMSAPI interface {
	Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// kmsSignTimeout bounds one kms:Sign. A token request waits on it, so a KMS
// that hangs fails the request rather than holding it.
const kmsSignTimeout = 10 * time.Second

// es384Size is the byte length of each of r and s in a P-384 JWS signature.
const es384Size = 48

// KMSSigningKey reads one AWS KMS key as a [SigningKey]: the public half is
// fetched with kms:GetPublicKey and every signature is a kms:Sign, so the
// private key never reaches this process.
//
// ref is a key id, a key ARN or an alias. The key must be an ECC_NIST_P384
// SIGN_VERIFY key; anything else is refused, because a key of another shape
// would sign tokens no relying party could verify. The signer addresses the
// key by the ARN KMS reports, not by ref, so an alias that is re-pointed
// later cannot make an already-published kid sign with a different key: the
// next poll reads a different public key, hence a different kid, and the
// [KeyRing] schedules it like any other new key.
//
// The kid is the key's RFC 7638 thumbprint, as for a file, so the same key
// has the same id wherever it is read from.
//
// seed is the secret [SigningKey.Derive] works from. A KMS key has no
// private bytes to derive from, so the deployment supplies one; it must be
// the same in every replica.
func KMSSigningKey(ctx context.Context, api KMSAPI, ref string, seed []byte) (*SigningKey, error) {
	return KMSSigningKeyFor(ctx, api, ref, seed, jose.ES384)
}

// KMSSigningKeyFor is [KMSSigningKey] for the algorithm alg: ES384 over an
// ECC_NIST_P384 key, or RS256 over an RSA_2048, RSA_3072 or RSA_4096 key
// (RSASSA_PKCS1_V1_5_SHA_256 over a SHA-256 digest; the signature is raw
// PKCS#1 v1.5, so there is nothing to convert). A key of another shape than
// alg asks for is refused.
func KMSSigningKeyFor(ctx context.Context, api KMSAPI, ref string, seed []byte, alg jose.SignatureAlgorithm) (*SigningKey, error) {
	if api == nil || ref == "" {
		return nil, errors.New("issuer: a KMS signing key needs a client and a key")
	}
	if len(seed) < 32 {
		return nil, errors.New("issuer: the KMS signing key's state secret must be at least 32 bytes")
	}
	var wantSpecs []types.KeySpec
	var wantAlg types.SigningAlgorithmSpec
	switch alg {
	case jose.ES384:
		wantSpecs, wantAlg = []types.KeySpec{types.KeySpecEccNistP384}, types.SigningAlgorithmSpecEcdsaSha384
	case jose.RS256:
		wantSpecs = []types.KeySpec{types.KeySpecRsa2048, types.KeySpecRsa3072, types.KeySpecRsa4096}
		wantAlg = types.SigningAlgorithmSpecRsassaPkcs1V15Sha256
	default:
		return nil, fmt.Errorf("issuer: a KMS key signs ES384 or RS256, not %s", alg)
	}
	ctx, cancel := context.WithTimeout(ctx, kmsSignTimeout)
	defer cancel()
	out, err := api.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(ref)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "AccessDeniedException" || apiErr.ErrorCode() == "AccessDenied") {
			return nil, fmt.Errorf("issuer: this role may not call kms:GetPublicKey on %q: grant kms:GetPublicKey and kms:Sign "+
				"on the signing keys (docs/reference/configuration.md, signingKey.kms): %w", ref, err)
		}
		return nil, fmt.Errorf("issuer: kms:GetPublicKey on %q: %w", ref, err)
	}
	if !slices.Contains(wantSpecs, out.KeySpec) {
		return nil, fmt.Errorf("issuer: KMS key %q is %s; a %s signing key must be one of %v", ref, out.KeySpec, alg, wantSpecs)
	}
	if out.KeyUsage != types.KeyUsageTypeSignVerify {
		return nil, fmt.Errorf("issuer: KMS key %q has usage %s; a signing key must be SIGN_VERIFY", ref, out.KeyUsage)
	}
	if !slices.Contains(out.SigningAlgorithms, wantAlg) {
		return nil, fmt.Errorf("issuer: KMS key %q does not offer %s", ref, wantAlg)
	}
	parsed, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("issuer: KMS key %q: read the public key: %w", ref, err)
	}
	var pub crypto.PublicKey
	switch p := parsed.(type) {
	case *ecdsa.PublicKey:
		if alg != jose.ES384 || p.Curve != elliptic.P384() {
			return nil, fmt.Errorf("issuer: KMS key %q is not a P-384 public key", ref)
		}
		pub = p
	case *rsa.PublicKey:
		bits := map[types.KeySpec]int{types.KeySpecRsa2048: 2048, types.KeySpecRsa3072: 3072, types.KeySpecRsa4096: 4096}[out.KeySpec]
		if alg != jose.RS256 || p.N.BitLen() != bits {
			return nil, fmt.Errorf("issuer: KMS key %q is not a %s RSA public key", ref, out.KeySpec)
		}
		pub = p
	default:
		return nil, fmt.Errorf("issuer: KMS key %q has a %T public key", ref, parsed)
	}
	id, err := thumbprint(pub)
	if err != nil {
		return nil, err
	}
	// Never the ref in its place: an alias can move, and a signer addressed by
	// it could sign with a key other than the one whose public half was read.
	keyID := aws.ToString(out.KeyId)
	if keyID == "" {
		return nil, fmt.Errorf("issuer: KMS returned no key id for %q", ref)
	}
	signer := &kmsSigner{api: api, keyID: keyID, kid: id, pub: pub, alg: alg, metrics: kmsMetricsOnce()}
	return &SigningKey{id: id, key: signer, pub: pub, alg: alg, seed: append([]byte(nil), seed...)}, nil
}

// kmsSigner is a [jose.OpaqueSigner] over kms:Sign.
type kmsSigner struct {
	api     KMSAPI
	keyID   string
	kid     string
	pub     crypto.PublicKey
	alg     jose.SignatureAlgorithm
	metrics kmsInstruments
}

// Public implements [jose.OpaqueSigner].
func (s *kmsSigner) Public() *jose.JSONWebKey {
	return &jose.JSONWebKey{Key: s.pub, KeyID: s.kid, Algorithm: string(s.alg), Use: "sig"}
}

// Algs implements [jose.OpaqueSigner].
func (s *kmsSigner) Algs() []jose.SignatureAlgorithm { return []jose.SignatureAlgorithm{s.alg} }

// SignPayload implements [jose.OpaqueSigner]: the digest of the JWS signing
// input goes to KMS (SHA-384 and ECDSA for ES384, whose DER signature is
// converted to raw r||s; SHA-256 and PKCS#1 v1.5 for RS256), and the
// signature is verified against the public half before it is returned.
func (s *kmsSigner) SignPayload(payload []byte, alg jose.SignatureAlgorithm) ([]byte, error) {
	if alg != s.alg {
		return nil, fmt.Errorf("issuer: this KMS key signs %s, not %s", s.alg, alg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), kmsSignTimeout)
	defer cancel()
	var digest []byte
	var spec types.SigningAlgorithmSpec
	if s.alg == jose.RS256 {
		sum := sha256.Sum256(payload)
		digest, spec = sum[:], types.SigningAlgorithmSpecRsassaPkcs1V15Sha256
	} else {
		sum := sha512.Sum384(payload)
		digest, spec = sum[:], types.SigningAlgorithmSpecEcdsaSha384
	}
	out, err := s.api.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(s.keyID),
		Message:          digest,
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: spec,
	})
	if err != nil {
		s.metrics.record(ctx, s.kid, kmsResult(err))
		return nil, fmt.Errorf("issuer: kms:Sign with %s: %w", s.keyID, err)
	}
	raw := out.Signature
	switch pub := s.pub.(type) {
	case *rsa.PublicKey:
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, raw)
		if err != nil {
			err = errors.New("the signature does not verify against the key's public half")
		}
	case *ecdsa.PublicKey:
		raw, err = derToRaw(out.Signature, es384Size)
		if err == nil && !ecdsa.Verify(pub, digest,
			new(big.Int).SetBytes(raw[:es384Size]), new(big.Int).SetBytes(raw[es384Size:])) {
			err = errors.New("the signature does not verify against the key's public half")
		}
	}
	if err != nil {
		s.metrics.record(ctx, s.kid, "error")
		return nil, fmt.Errorf("issuer: kms:Sign with %s: %w", s.keyID, err)
	}
	s.metrics.record(ctx, s.kid, "ok")
	return raw, nil
}

// derToRaw converts a DER ECDSA-Sig-Value to the fixed-width r||s of RFC 7518
// section 3.4, each half left-padded to size bytes.
func derToRaw(der []byte, size int) ([]byte, error) {
	var sig struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil {
		return nil, fmt.Errorf("the signature is not DER: %w", err)
	}
	if len(rest) != 0 {
		return nil, errors.New("the signature has trailing bytes after its DER value")
	}
	if sig.R == nil || sig.S == nil || sig.R.Sign() <= 0 || sig.S.Sign() <= 0 ||
		sig.R.BitLen() > size*8 || sig.S.BitLen() > size*8 {
		return nil, errors.New("the signature's r or s is out of range for the curve")
	}
	raw := make([]byte, 2*size)
	sig.R.FillBytes(raw[:size])
	sig.S.FillBytes(raw[size:])
	return raw, nil
}

// kmsInstruments counts signatures by key. The kid is an attribute here,
// unlike the ring's metrics: the keys are the handful listed in the
// configuration, so the set is small, and "which key is signing, and which is
// failing" is the question this metric exists to answer.
type kmsInstruments struct {
	signatures metric.Int64Counter
}

var kmsMetricsOnce = sync.OnceValue(kmsMetrics)

// kmsResult is the metric's result for a failed call: throttled is told apart
// because the KMS request quota is the ceiling on token throughput.
func kmsResult(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "Throttling", "TooManyRequestsException", "RequestLimitExceeded", "LimitExceededException":
			return "throttled"
		}
	}
	return "error"
}

func kmsMetrics() kmsInstruments {
	signatures, _ := otel.Meter(keyRingMeterName).Int64Counter("access_issuer.kms_signatures",
		metric.WithDescription("kms:Sign calls made to sign a token, by the signing key's kid and their result: ok, throttled or error."))
	return kmsInstruments{signatures: signatures}
}

func (m kmsInstruments) record(ctx context.Context, kid, result string) {
	m.signatures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kid", kid), attribute.String("result", result)))
}

// KMSKeyRefs is the configured KMS keys, in order: the last signs, the earlier
// ones are published until their overlap ends. Adding a key to the end of the
// list is the rotation.
type KMSKeyRefs struct {
	// Alg is the algorithm of every key in Refs; empty is ES384.
	Alg  jose.SignatureAlgorithm
	API  KMSAPI
	Refs []string
	Seed []byte
}

// Load reads every key, in order. It fails on the first one it cannot read,
// which is what startup wants.
func (k KMSKeyRefs) Load(ctx context.Context) ([]*SigningKey, error) {
	out := make([]*SigningKey, 0, len(k.Refs))
	for _, ref := range k.Refs {
		key, err := KMSSigningKeyFor(ctx, k.API, strings.TrimSpace(ref), k.Seed, k.alg())
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, nil
}

func (k KMSKeyRefs) alg() jose.SignatureAlgorithm {
	if k.Alg == "" {
		return jose.ES384
	}
	return k.Alg
}
