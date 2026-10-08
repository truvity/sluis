// Package kms is the AWS KMS backend of package keys.
//
// Keys are named by alias (alias/<name>) and addressed by that alias on every
// call, so re-pointing an alias moves the caller to the new key. Encrypt,
// Decrypt and GenerateDataKey carry the encryption context; Sign sends a
// DIGEST and takes none. Encrypt is KMS's direct Encrypt, so it takes at most
// 4096 bytes: wrap a data key (keys.Key.GenerateDataKey) for anything larger.
package kms

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"

	"github.com/truvity/sluis/storage/keys"
)

// API is the part of the AWS KMS client this backend calls; *kms.Client
// satisfies it.
type API interface {
	Encrypt(ctx context.Context, in *kms.EncryptInput, opts ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
	GenerateDataKey(ctx context.Context, in *kms.GenerateDataKeyInput, opts ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// DefaultInfoTTL is how long the algorithm and public key of a signing key
// are cached. An alias can be re-pointed; the cache lets Sign skip a
// GetPublicKey per signature without pinning the old key for ever.
const DefaultInfoTTL = time.Minute

var aliasRe = regexp.MustCompile(`^alias/[a-zA-Z0-9/_-]+$`)

// Backend implements keys.Backend and keys.MACBackend over KMS.
type Backend struct {
	api     API
	store   WrappedStore
	infoTTL time.Duration
	now     func() time.Time

	mu    sync.Mutex
	info  map[string]keyInfo
	macKs map[string]macEntry // wrapped-key id -> plaintext HMAC key and when it was last checked
}

var (
	_ keys.Backend        = (*Backend)(nil)
	_ keys.MACBackend     = (*Backend)(nil)
	_ keys.DestroyBackend = (*Backend)(nil)
)

// Option configures New.
type Option func(*Backend)

// WithWrappedStore supplies the store for the wrapped HMAC keys behind MAC.
// Without it, MAC returns keys.ErrUnsupported.
func WithWrappedStore(s WrappedStore) Option { return func(b *Backend) { b.store = s } }

// WithInfoTTL changes DefaultInfoTTL.
func WithInfoTTL(d time.Duration) Option { return func(b *Backend) { b.infoTTL = d } }

// New returns a backend over api.
func New(api API, opts ...Option) *Backend {
	b := &Backend{api: api, infoTTL: DefaultInfoTTL, now: time.Now,
		info: map[string]keyInfo{}, macKs: map[string]macEntry{}}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Name is "kms".
func (*Backend) Name() string { return "kms" }

// ValidateName accepts an alias, and says what to use when given anything
// else.
func (*Backend) ValidateName(name string) error {
	switch {
	case strings.HasPrefix(name, "arn:"):
		return fmt.Errorf("kms key %q: an ARN is not accepted; use the key's alias (alias/<name>)", name)
	case strings.HasPrefix(name, "alias/aws/"):
		return fmt.Errorf("kms key %q: an AWS managed alias cannot carry a key policy of your own; create a customer managed key and alias it", name)
	case !strings.HasPrefix(name, "alias/"):
		return fmt.Errorf("kms key %q: a key is named by alias (alias/<name>), not by key id", name)
	case !aliasRe.MatchString(name):
		return fmt.Errorf("kms key %q: an alias is alias/ followed by letters, digits, '/', '_' or '-'", name)
	}
	return nil
}

func mapErr(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "InvalidCiphertextException", "IncorrectKeyException":
			return fmt.Errorf("%w: %s", keys.ErrDecrypt, ae.ErrorCode())
		}
	}
	return err
}

// Encrypt calls kms:Encrypt (at most 4096 bytes of plaintext).
func (b *Backend) Encrypt(ctx context.Context, key string, pt []byte, ec map[string]string) ([]byte, error) {
	out, err := b.api.Encrypt(ctx, &kms.EncryptInput{KeyId: &key, Plaintext: pt, EncryptionContext: ec})
	if err != nil {
		return nil, fmt.Errorf("kms: encrypt under %s: %w", key, err)
	}
	return out.CiphertextBlob, nil
}

// Decrypt calls kms:Decrypt, pinned to the key named, so a ciphertext made
// under another key does not open.
func (b *Backend) Decrypt(ctx context.Context, key string, ct []byte, ec map[string]string) ([]byte, error) {
	if len(ct) == 0 {
		return nil, keys.ErrDecrypt
	}
	out, err := b.api.Decrypt(ctx, &kms.DecryptInput{KeyId: &key, CiphertextBlob: ct, EncryptionContext: ec})
	if err != nil {
		return nil, mapErr(fmt.Errorf("kms: decrypt under %s: %w", key, err))
	}
	return out.Plaintext, nil
}

// GenerateDataKey calls kms:GenerateDataKey for an AES-256 key.
func (b *Backend) GenerateDataKey(ctx context.Context, key string, ec map[string]string) ([]byte, []byte, error) {
	out, err := b.api.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{KeyId: &key, KeySpec: types.DataKeySpecAes256, EncryptionContext: ec})
	if err != nil {
		return nil, nil, fmt.Errorf("kms: generate data key under %s: %w", key, err)
	}
	return out.Plaintext, out.CiphertextBlob, nil
}

type keyInfo struct {
	pub     crypto.PublicKey
	alg     string
	signAlg types.SigningAlgorithmSpec
	hashLen int
	expires time.Time
}

func (b *Backend) keyInfo(ctx context.Context, key string) (keyInfo, error) {
	b.mu.Lock()
	ki, ok := b.info[key]
	b.mu.Unlock()
	if ok && b.now().Before(ki.expires) {
		return ki, nil
	}
	out, err := b.api.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &key})
	if err != nil {
		return keyInfo{}, fmt.Errorf("kms: public key of %s: %w", key, err)
	}
	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return keyInfo{}, fmt.Errorf("kms: public key of %s does not parse: %w", key, err)
	}
	ki = keyInfo{pub: pub, expires: b.now().Add(b.infoTTL)}
	switch out.KeySpec {
	case types.KeySpecEccNistP384:
		ki.alg, ki.signAlg, ki.hashLen = "ES384", types.SigningAlgorithmSpecEcdsaSha384, 48
	case types.KeySpecRsa2048, types.KeySpecRsa3072, types.KeySpecRsa4096:
		ki.alg, ki.signAlg, ki.hashLen = "RS256", types.SigningAlgorithmSpecRsassaPkcs1V15Sha256, 32
	default:
		return keyInfo{}, fmt.Errorf("kms: %s is a %s key; signing keys are ECC_NIST_P384 (ES384) or RSA (RS256): %w",
			key, out.KeySpec, keys.ErrUnsupported)
	}
	if out.KeyUsage != types.KeyUsageTypeSignVerify {
		return keyInfo{}, fmt.Errorf("kms: %s is a %s key, not SIGN_VERIFY: %w", key, out.KeyUsage, keys.ErrUnsupported)
	}
	b.mu.Lock()
	b.info[key] = ki
	b.mu.Unlock()
	return ki, nil
}

// Sign signs a digest: SHA-384 for an ECC P-384 key, SHA-256 for an RSA key.
// An ECDSA signature is ASN.1 DER as KMS returns it.
func (b *Backend) Sign(ctx context.Context, key string, digest []byte) ([]byte, error) {
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(digest) != ki.hashLen {
		return nil, fmt.Errorf("kms: %s signs a %d-byte digest (%s), got %d bytes", key, ki.hashLen, ki.alg, len(digest))
	}
	out, err := b.api.Sign(ctx, &kms.SignInput{KeyId: &key, Message: digest,
		MessageType: types.MessageTypeDigest, SigningAlgorithm: ki.signAlg})
	if err != nil {
		return nil, fmt.Errorf("kms: sign with %s: %w", key, err)
	}
	return out.Signature, nil
}

// PublicKey returns the key's public half and its JOSE algorithm.
func (b *Backend) PublicKey(ctx context.Context, key string) (crypto.PublicKey, string, error) {
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return nil, "", err
	}
	return ki.pub, ki.alg, nil
}
