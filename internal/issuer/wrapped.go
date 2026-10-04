package issuer

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/smithy-go"
	jose "github.com/go-jose/go-jose/v4"
)

// Signing with KMS-wrapped keys.
//
// One symmetric "application" KMS key per estate, and a key pair per
// algorithm and per rotation period. KMS generates the pair
// (kms:GenerateDataKeyPairWithoutPlaintext) and returns the public key and the
// private key encrypted under the symmetric key. The wrapped private key and
// the public key are recorded in the issuer's key ring, in the shared state,
// beside the schedule every replica agrees on. A replica that has to sign with
// a key calls kms:Decrypt with the same encryption context, parses the key and
// keeps it in memory; tokens are signed locally. Nothing but the ciphertext is
// ever logged, persisted or returned.
//
// The trade against the `kms` adapter: a wrapped key is decrypted into the
// memory of every process that signs, so a leaked role (or a process dump) can
// forge tokens until the key rotates out; a `kms` key is non-extractable. In
// exchange there is no per-token KMS call, no key to provision, and rotation
// is automatic and free (a new data key every [WrappedConfig.RotateEvery]).

// WrapPurpose is the `purpose` of every encryption context this adapter uses.
// The key policy and the IAM grants pin it (deploy/pulumi).
const WrapPurpose = "sluis-signing"

const (
	// DefaultWrappedRotateEvery is how often a new key pair is generated.
	DefaultWrappedRotateEvery = 24 * time.Hour

	// MaxWrappedRotateEvery keeps a key's record inside [keyRingEntryTTL].
	MaxWrappedRotateEvery = 7 * 24 * time.Hour

	// wrapRSABits is the size of the RS256 data key (RSA_3072).
	wrapRSABits = 3072

	// wrapGenerateTimeout bounds generating and unwrapping one key: an RSA_3072
	// key pair takes KMS a couple of seconds.
	wrapGenerateTimeout = 20 * time.Second
)

// KMSWrapAPI is the part of the AWS KMS client the wrapped signing adapter
// calls. It is narrow so that a test supplies a fake that generates real key
// pairs locally.
type KMSWrapAPI interface {
	GenerateDataKeyPairWithoutPlaintext(ctx context.Context, in *kms.GenerateDataKeyPairWithoutPlaintextInput,
		opts ...func(*kms.Options)) (*kms.GenerateDataKeyPairWithoutPlaintextOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// WrappedConfig is `signingKey.kmsWrapped`, resolved.
type WrappedConfig struct {
	// KeyID is the symmetric key: an id, an ARN or an alias.
	KeyID string
	// Algorithms are the algorithms to sign with; the first is the installation
	// default. ES384 and RS256 are supported.
	Algorithms []jose.SignatureAlgorithm
	// RotateEvery is how often a new key pair is generated per algorithm.
	RotateEvery time.Duration
	// Prepublish is how long a new key is published before anything signs with
	// it: at least as long as a verifier may cache the JWKS.
	Prepublish time.Duration
	// Retain is how long a superseded key stays published: at least the longest
	// token lifetime plus a margin for clock skew.
	Retain time.Duration
	// Interval is how often a process looks for work (a new key to learn, one
	// to generate). Zero is [DefaultKeyPollInterval].
	Interval time.Duration
}

// Validate refuses a configuration that cannot rotate safely.
func (c WrappedConfig) Validate(tokenLifetime time.Duration) error {
	if c.KeyID == "" {
		return errors.New("signingKey.kmsWrapped.keyId is required")
	}
	if len(c.Algorithms) == 0 {
		return errors.New("signingKey.kmsWrapped.algorithms needs at least one algorithm")
	}
	seen := map[jose.SignatureAlgorithm]bool{}
	for _, a := range c.Algorithms {
		switch a {
		case jose.ES384, jose.RS256:
		case jose.EdDSA:
			return errors.New("signingKey.kmsWrapped.algorithms: EdDSA is not supported yet " +
				"(the policy's signing_alg and the issuer's verifiers do not know it); use ES384 or RS256")
		default:
			return fmt.Errorf("signingKey.kmsWrapped.algorithms: %q is not supported (ES384 or RS256)", a)
		}
		if seen[a] {
			return fmt.Errorf("signingKey.kmsWrapped.algorithms: %s is listed twice", a)
		}
		seen[a] = true
	}
	if c.Prepublish <= 0 || c.Retain <= 0 || c.RotateEvery <= 0 {
		return errors.New("signingKey.kmsWrapped: rotateEvery, prepublish and retain must be positive")
	}
	if c.RotateEvery > MaxWrappedRotateEvery {
		return fmt.Errorf("signingKey.kmsWrapped.rotateEvery (%s) must be at most %s", c.RotateEvery, MaxWrappedRotateEvery)
	}
	if c.RotateEvery <= c.Prepublish {
		return fmt.Errorf("signingKey.kmsWrapped.rotateEvery (%s) must be longer than prepublish (%s): "+
			"a key must activate before the next one is generated", c.RotateEvery, c.Prepublish)
	}
	if least := tokenLifetime + KeyOverlapSkew; c.Retain < least {
		return fmt.Errorf("signingKey.kmsWrapped.retain (%s) must be at least lifetimes.token plus a skew margin (%s): "+
			"a key that leaves the JWKS earlier makes valid tokens unverifiable", c.Retain, least)
	}
	return nil
}

// WrappedLease runs fn under the lease that serialises key generation for one
// algorithm; ran is false, with no error, when another runner holds it.
type WrappedLease func(ctx context.Context, alg jose.SignatureAlgorithm, fn func(context.Context) error) (ran bool, err error)

// WrappedSigning generates, wraps and unwraps the keys. It holds no key
// material itself: a key lives in the [KeyRing] that schedules it.
type WrappedSigning struct {
	cfg   WrappedConfig
	api   KMSWrapAPI
	seed  []byte
	lease WrappedLease
	log   *slog.Logger
	now   func() time.Time
}

// NewWrappedSigning returns the adapter. seed is the state secret
// [SigningKey.Derive] works from: a wrapped key is replaced daily, and the
// sign-in state must outlive that.
func NewWrappedSigning(cfg WrappedConfig, api KMSWrapAPI, seed []byte, lease WrappedLease, log *slog.Logger) (*WrappedSigning, error) {
	if api == nil {
		return nil, errors.New("issuer: wrapped signing needs a KMS client")
	}
	if len(seed) < 32 {
		return nil, errors.New("issuer: the wrapped signing key's state secret must be at least 32 bytes")
	}
	if lease == nil {
		return nil, errors.New("issuer: wrapped signing needs a lease to serialise key generation")
	}
	if log == nil {
		log = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultKeyPollInterval
	}
	return &WrappedSigning{cfg: cfg, api: api, seed: append([]byte(nil), seed...), lease: lease, log: log, now: time.Now}, nil
}

// EncryptionContext is the context a key is wrapped under, and unwrapped with:
// the same three pairs, exactly. KMS generates the pair, so the kid cannot be
// the public key's thumbprint; it is a random 128-bit id chosen first, which
// binds a ciphertext to the entry that records it (a wrapped key moved under
// another kid, or another algorithm, does not decrypt).
func EncryptionContext(alg jose.SignatureAlgorithm, kid string) map[string]string {
	return map[string]string{"purpose": WrapPurpose, "alg": string(alg), "kid": kid}
}

func keyPairSpec(alg jose.SignatureAlgorithm) (types.DataKeyPairSpec, error) {
	switch alg {
	case jose.ES384:
		return types.DataKeyPairSpecEccNistP384, nil
	case jose.RS256:
		return types.DataKeyPairSpecRsa3072, nil
	}
	return "", fmt.Errorf("issuer: a wrapped key signs ES384 or RS256, not %s", alg)
}

func newKid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("issuer: generate a key id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// wrapError names the permission a denied call needs.
func wrapError(call string, err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "AccessDeniedException" || apiErr.ErrorCode() == "AccessDenied") {
		return fmt.Errorf("issuer: this role may not call kms:%s: grant kms:GenerateDataKeyPairWithoutPlaintext and kms:Decrypt on the "+
			"application key with the encryption context purpose=%s (docs/deployment/aws.md, Signing on AWS): %w", call, WrapPurpose, err)
	}
	return fmt.Errorf("issuer: kms:%s: %w", call, err)
}

// generate asks KMS for a key pair for alg and returns the key, already
// unwrapped: the immediate kms:Decrypt proves this role can open what it just
// made, so a missing permission fails here and not a pre-publish period later.
func (w *WrappedSigning) generate(ctx context.Context, alg jose.SignatureAlgorithm) (*SigningKey, error) {
	spec, err := keyPairSpec(alg)
	if err != nil {
		return nil, err
	}
	kid, err := newKid()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, wrapGenerateTimeout)
	defer cancel()
	out, err := w.api.GenerateDataKeyPairWithoutPlaintext(ctx, &kms.GenerateDataKeyPairWithoutPlaintextInput{
		KeyId:             aws.String(w.cfg.KeyID),
		KeyPairSpec:       spec,
		EncryptionContext: EncryptionContext(alg, kid),
	})
	if err != nil {
		return nil, wrapError("GenerateDataKeyPairWithoutPlaintext", err)
	}
	if len(out.PrivateKeyCiphertextBlob) == 0 || len(out.PublicKey) == 0 {
		return nil, errors.New("issuer: KMS returned no wrapped key or no public key")
	}
	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("issuer: read the generated public key: %w", err)
	}
	key, err := w.unwrap(ctx, alg, kid, pub, out.PrivateKeyCiphertextBlob)
	if err != nil {
		return nil, fmt.Errorf("issuer: the key just generated cannot be unwrapped: %w", err)
	}
	return key, nil
}

// unwrap decrypts one wrapped key with the context it was made under and
// returns it as a [SigningKey] that signs locally. pub is the public half the
// key ring published for kid: the decrypted key must be its pair, and of the
// shape alg needs.
func (w *WrappedSigning) unwrap(ctx context.Context, alg jose.SignatureAlgorithm, kid string, pub crypto.PublicKey, wrapped []byte) (*SigningKey, error) {
	if len(wrapped) == 0 {
		return nil, errors.New("issuer: no wrapped key")
	}
	ctx, cancel := context.WithTimeout(ctx, wrapGenerateTimeout)
	defer cancel()
	out, err := w.api.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:      wrapped,
		KeyId:               aws.String(w.cfg.KeyID),
		EncryptionAlgorithm: types.EncryptionAlgorithmSpecSymmetricDefault,
		EncryptionContext:   EncryptionContext(alg, kid),
	})
	if err != nil {
		return nil, wrapError("Decrypt", err)
	}
	// The plaintext is the one copy outside the parsed key: wipe it.
	defer clear(out.Plaintext)
	parsed, err := x509.ParsePKCS8PrivateKey(out.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("issuer: read the unwrapped key %s: %w", kid, err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("issuer: the unwrapped key %s is %T, which cannot sign", kid, parsed)
	}
	switch k := parsed.(type) {
	case *ecdsa.PrivateKey:
		if alg != jose.ES384 || k.Curve != elliptic.P384() {
			return nil, fmt.Errorf("issuer: the unwrapped key %s is not a P-384 key for %s", kid, alg)
		}
	case *rsa.PrivateKey:
		if alg != jose.RS256 || k.N.BitLen() != wrapRSABits {
			return nil, fmt.Errorf("issuer: the unwrapped key %s is not an RSA-%d key for %s", kid, wrapRSABits, alg)
		}
	default:
		return nil, fmt.Errorf("issuer: the unwrapped key %s is %T; %s needs an EC P-384 or RSA key", kid, parsed, alg)
	}
	want, err1 := x509.MarshalPKIXPublicKey(pub)
	have, err2 := x509.MarshalPKIXPublicKey(signer.Public())
	if err1 != nil || err2 != nil || !bytes.Equal(want, have) {
		return nil, fmt.Errorf("issuer: the unwrapped key %s is not the pair of the public key published for it", kid)
	}
	return &SigningKey{
		id: kid, key: signer, pub: signer.Public(), alg: alg,
		seed: append([]byte(nil), w.seed...), wrapped: append([]byte(nil), wrapped...),
	}, nil
}

// SetClock replaces the clock. For tests, matching [KeyRing.SetClock].
func (w *WrappedSigning) SetClock(now func() time.Time) { w.now = now }

// ringConfig is the schedule every ring of this adapter runs.
func (w *WrappedSigning) ringConfig() KeyRingConfig {
	return KeyRingConfig{ActivationDelay: w.cfg.Prepublish, Overlap: w.cfg.Retain}
}

// Algorithms are the configured algorithms, the first the default.
func (w *WrappedSigning) Algorithms() []jose.SignatureAlgorithm {
	return slices.Clone(w.cfg.Algorithms)
}

// Bootstrap returns the key each algorithm signs with at start: the newest
// activated key the shared state already holds (unwrapped here), or, for an
// algorithm with none, a key generated now under the lease, recorded and
// active at once. The first algorithm's key is the primary.
func (w *WrappedSigning) Bootstrap(ctx context.Context, state State) (primary *SigningKey, more []*SigningKey, err error) {
	for i, alg := range w.cfg.Algorithms {
		key, err := w.bootstrapAlg(ctx, state, alg)
		if err != nil {
			return nil, nil, fmt.Errorf("signingKey.kmsWrapped: %s: %w", alg, err)
		}
		if i == 0 {
			primary = key
		} else {
			more = append(more, key)
		}
	}
	return primary, more, nil
}

func (w *WrappedSigning) bootstrapAlg(ctx context.Context, state State, alg jose.SignatureAlgorithm) (*SigningKey, error) {
	key, err := w.loadExisting(ctx, state, alg)
	if err != nil || key != nil {
		return key, err
	}
	var made *SigningKey
	ran, err := w.lease(ctx, alg, func(lctx context.Context) error {
		// Another replica may have finished while this one waited for the lease.
		existing, err := w.loadExisting(lctx, state, alg)
		if err != nil || existing != nil {
			made = existing
			return err
		}
		fresh, err := w.generate(lctx, alg)
		if err != nil {
			return err
		}
		// Nothing wrapped exists to sign with, so this key is active at once.
		fresh.activateNow = true
		// Recorded before the lease is released: a replica that starts next finds it.
		ring := NewKeyRing(alg, state, w.ringConfig(), w.log)
		ring.SetClock(w.now)
		if err := ring.Observe(lctx, fresh); err != nil {
			return err
		}
		made = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	if made != nil {
		return made, nil
	}
	if ran {
		return nil, errors.New("no key was generated")
	}
	// Another runner holds the lease and is generating: wait for its key.
	deadline := time.Now().Add(wrapGenerateTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if key, err = w.loadExisting(ctx, state, alg); err != nil || key != nil {
			return key, err
		}
	}
	return nil, errors.New("another replica holds the key-generation lease and produced no key in time")
}

// loadExisting unwraps the newest activated wrapped key the state records for
// alg, nil when it records none. A key that cannot be unwrapped stops the start
// with its error rather than being replaced: a role that cannot decrypt is a
// fault to fix, and a second key would hide it.
func (w *WrappedSigning) loadExisting(ctx context.Context, state State, alg jose.SignatureAlgorithm) (*SigningKey, error) {
	entries, err := readRingEntries(ctx, state, alg)
	if err != nil {
		return nil, err
	}
	var wrapped []*ringEntry
	for _, e := range entries {
		if len(e.Wrapped) > 0 {
			wrapped = append(wrapped, e)
		}
	}
	if len(wrapped) == 0 {
		return nil, nil
	}
	slices.SortFunc(wrapped, func(a, b *ringEntry) int { return b.ActivateAt.Compare(a.ActivateAt) })
	now := w.now()
	var first error
	for _, e := range wrapped {
		if e.ActivateAt.After(now) && len(wrapped) > 1 {
			continue
		}
		key, err := w.unwrap(ctx, alg, e.ID, e.JWK.Key, e.Wrapped)
		if err == nil {
			return key, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}

// readRingEntries reads every live, unretired entry a ring of alg has recorded.
func readRingEntries(ctx context.Context, state State, alg jose.SignatureAlgorithm) ([]*ringEntry, error) {
	ids, err := state.Members(ctx, keyRingIndexKey(alg))
	if err != nil {
		return nil, fmt.Errorf("read the key index: %w", err)
	}
	var out []*ringEntry
	for _, id := range ids {
		if _, retired, err := state.Get(ctx, keyRingRetiredKey(alg, id)); err != nil {
			return nil, fmt.Errorf("read the retirement of %s: %w", id, err)
		} else if retired {
			continue
		}
		e, err := getJSON[ringEntry](ctx, state, keyRingEntryKey(alg, id))
		if err != nil {
			return nil, err
		}
		if e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}
