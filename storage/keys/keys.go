package keys

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"sort"
)

// Purpose is the short name a caller asks a key by. A deployment maps each
// purpose to one key in its configuration; the caller never sees the key's
// name.
type Purpose string

// The purposes the two products ask for. A Purpose outside this list is
// refused by the configuration, so a typo cannot silently run unkeyed.
const (
	// Sign signs: the token signing key of the identity issuer.
	Sign Purpose = "sign"
	// Seal signs and encrypts what the audit notary seals (asymmetric
	// signing, plus the symmetric key that wraps its secrets).
	Seal Purpose = "seal"
	// Pseudonym derives the per-tenant pseudonyms of the audit trail.
	Pseudonym Purpose = "pseudonym"
	// Conceal encrypts values that must be recoverable later.
	Conceal Purpose = "conceal"
	// Archive encrypts what is written to long-term storage.
	Archive Purpose = "archive"
)

// Purposes lists every purpose, in a stable order.
func Purposes() []Purpose {
	return []Purpose{Sign, Seal, Pseudonym, Conceal, Archive}
}

func (p Purpose) valid() bool {
	for _, q := range Purposes() {
		if p == q {
			return true
		}
	}
	return false
}

// Errors a backend or a caller can match with errors.Is.
var (
	// ErrUnsupported is returned when the key or backend cannot do what was
	// asked (Encrypt on an asymmetric key, MAC on a backend without one).
	ErrUnsupported = errors.New("keys: operation not supported by this key or backend")
	// ErrNotConfigured is returned for a purpose the configuration leaves out.
	ErrNotConfigured = errors.New("keys: purpose is not configured")
	// ErrDecrypt is returned when ciphertext does not open: wrong key, wrong
	// context, or damaged. The cause is deliberately not distinguished.
	ErrDecrypt = errors.New("keys: cannot decrypt (wrong key or context, or damaged ciphertext)")
	// ErrDestroyed is returned by MAC for a tenant whose material was
	// destroyed. It is a fault in the caller, not a reason to mint a new
	// secret: a second, unrelated pseudonym for the same person would split
	// their history in two.
	ErrDestroyed = errors.New("keys: the tenant's key material has been destroyed")
)

// Backend is a key service: it holds keys by name and does the work. A name
// is the string from the configuration (a KMS alias, a transit key name),
// already checked by ValidateName. Context is the encryption context to bind
// the operation to; nil means none.
//
// Asymmetric operations (Sign, PublicKey) take no context: KMS has no
// encryption context for them. A key that is an alias is not a permission;
// see the package documentation.
type Backend interface {
	// Name is the adapter name used in configuration ("kms", "local").
	Name() string
	// ValidateName refuses a key name this adapter does not accept (an ARN or
	// a key id where an alias is required), with a message that says what to
	// use instead.
	ValidateName(name string) error

	Encrypt(ctx context.Context, key string, plaintext []byte, ec map[string]string) ([]byte, error)
	Decrypt(ctx context.Context, key string, ciphertext []byte, ec map[string]string) ([]byte, error)
	// GenerateDataKey returns a fresh 32-byte data key and the same key
	// wrapped under the named key with ec.
	GenerateDataKey(ctx context.Context, key string, ec map[string]string) (plaintext, wrapped []byte, err error)

	// Sign signs a digest already computed with the key's hash: SHA-384 for
	// an ECC P-384 key (ES384), SHA-256 for an RSA key (RS256). The ECDSA
	// signature is ASN.1 DER, as KMS returns it; see ToJOSE.
	Sign(ctx context.Context, key string, digest []byte) ([]byte, error)
	// PublicKey returns the verifying half and the JOSE algorithm of the key.
	PublicKey(ctx context.Context, key string) (crypto.PublicKey, string, error)
}

// MACBackend is the optional capability behind Key.MAC, the pseudonym
// function. It is separate from Backend because only the backends that can
// hold or derive a per-(purpose, tenant) secret offer it, and a backend
// without it should say so rather than return something weak.
//
// MAC returns HMAC-SHA-256 of data under a secret that is unique to the
// (purpose, tenant) pair and stable across calls and processes. Two tenants
// get unrelated outputs for the same data.
type MACBackend interface {
	MAC(ctx context.Context, key string, purpose Purpose, tenant string, data []byte) ([]byte, error)
}

// DestroyBackend is the optional capability behind Key.Destroy, the erasure
// of one tenant. It exists beside MACBackend because per-tenant material is
// the MAC secret and nothing else: Encrypt, Decrypt and Sign work under one
// key for the whole installation, so there is nothing of a tenant's in them
// to destroy.
//
// DestroyTenant removes the secret MAC uses for (purpose, tenant) and leaves
// a tombstone, so that afterwards MAC for that tenant returns ErrDestroyed
// instead of minting a new secret. It is idempotent: destroying a tenant
// that was never used, or already destroyed, succeeds and still leaves the
// tombstone. Other tenants are not affected. Destroyed reports whether the
// tombstone is there.
type DestroyBackend interface {
	DestroyTenant(ctx context.Context, key string, purpose Purpose, tenant string) error
	Destroyed(ctx context.Context, key string, purpose Purpose, tenant string) (bool, error)
}

// Options are what Open needs besides the configuration.
type Options struct {
	// Backend is the key service. Its Name must equal Config.Adapter.
	Backend Backend
	// Instance names this deployment in the default encryption context
	// ("instance"). It must be non-empty unless every purpose has context
	// "off" or an explicit map. Choose something stable and non-secret; it is
	// bound into ciphertexts, so renaming it needs an override on decrypt.
	Instance string
}

// Keys is the set of keys a process asked for, one per configured purpose.
type Keys struct {
	keys map[Purpose]*Key
}

// Open validates the configuration against the backend and returns the keys.
// It does no I/O: a bad alias fails on first use, not here.
func Open(cfg Config, opts Options) (*Keys, error) {
	if opts.Backend == nil {
		return nil, errors.New("keys: Options.Backend is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Adapter != opts.Backend.Name() {
		return nil, fmt.Errorf("keys: configuration names adapter %q but the backend is %q", cfg.Adapter, opts.Backend.Name())
	}
	set := &Keys{keys: map[Purpose]*Key{}}
	for p, e := range cfg.Keys {
		if err := opts.Backend.ValidateName(e.Key); err != nil {
			return nil, fmt.Errorf("keys.%s: %w", p, err)
		}
		ec, err := e.Context.resolve(p, opts.Instance)
		if err != nil {
			return nil, fmt.Errorf("keys.%s: %w", p, err)
		}
		set.keys[p] = &Key{purpose: p, name: e.Key, backend: opts.Backend, ec: ec}
	}
	return set, nil
}

// For returns the key for a purpose, or ErrNotConfigured.
func (s *Keys) For(p Purpose) (*Key, error) {
	k, ok := s.keys[p]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotConfigured, p)
	}
	return k, nil
}

// Configured lists the purposes that have a key, sorted.
func (s *Keys) Configured() []Purpose {
	out := make([]Purpose, 0, len(s.keys))
	for p := range s.keys {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Key is one purpose's key with its encryption context decided.
type Key struct {
	purpose Purpose
	name    string
	backend Backend
	ec      map[string]string
}

// Purpose is the purpose the key was asked for.
func (k *Key) Purpose() Purpose { return k.purpose }

// Context returns a copy of the encryption context sent with Encrypt and
// GenerateDataKey (nil when none).
func (k *Key) Context() map[string]string { return cloneMap(k.ec) }

// DecryptOption changes how one Decrypt or Unwrap call chooses its context.
type DecryptOption func(*decryptOpts)

type decryptOpts struct {
	set bool
	ec  map[string]string
}

// WithContext decrypts with this context instead of the configured one, so
// an old ciphertext (the issuer's ring entries bound to
// {purpose: sluis-signing, alg, kid}) opens without being re-wrapped.
func WithContext(ec map[string]string) DecryptOption {
	return func(o *decryptOpts) { o.set, o.ec = true, cloneMap(ec) }
}

// WithoutContext decrypts with no context, for ciphertexts written before
// contexts were used.
func WithoutContext() DecryptOption {
	return func(o *decryptOpts) { o.set, o.ec = true, nil }
}

func (k *Key) decryptContext(opts []DecryptOption) map[string]string {
	var o decryptOpts
	for _, f := range opts {
		f(&o)
	}
	if o.set {
		return o.ec
	}
	return k.ec
}

// Encrypt encrypts under the key with the configured context.
func (k *Key) Encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	return k.backend.Encrypt(ctx, k.name, plaintext, k.ec)
}

// Decrypt opens a ciphertext with the configured context, or the one an
// option names.
func (k *Key) Decrypt(ctx context.Context, ciphertext []byte, opts ...DecryptOption) ([]byte, error) {
	return k.backend.Decrypt(ctx, k.name, ciphertext, k.decryptContext(opts))
}

// DataKey is a fresh symmetric key and its wrapped form. Store Wrapped, use
// Plaintext, and zero Plaintext when done.
type DataKey struct {
	Plaintext []byte
	Wrapped   []byte
}

// GenerateDataKey makes a data key wrapped under this key (envelope
// encryption). The plaintext is 32 bytes.
func (k *Key) GenerateDataKey(ctx context.Context) (DataKey, error) {
	p, w, err := k.backend.GenerateDataKey(ctx, k.name, k.ec)
	if err != nil {
		return DataKey{}, err
	}
	return DataKey{Plaintext: p, Wrapped: w}, nil
}

// UnwrapDataKey recovers the plaintext of a wrapped data key. Options choose
// the context as for Decrypt.
func (k *Key) UnwrapDataKey(ctx context.Context, wrapped []byte, opts ...DecryptOption) ([]byte, error) {
	return k.Decrypt(ctx, wrapped, opts...)
}

// Signature is a signature and the JOSE algorithm that produced it.
type Signature struct {
	Algorithm string
	// Value is ASN.1 DER for ES384, raw for RS256. Use ToJOSE for JWS.
	Value []byte
}

// Sign signs a digest (see Backend.Sign for the hash). No encryption context
// applies to a signature.
func (k *Key) Sign(ctx context.Context, digest []byte) (Signature, error) {
	_, alg, err := k.backend.PublicKey(ctx, k.name)
	if err != nil {
		return Signature{}, err
	}
	v, err := k.backend.Sign(ctx, k.name, digest)
	if err != nil {
		return Signature{}, err
	}
	return Signature{Algorithm: alg, Value: v}, nil
}

// Public is a verifying key and its JOSE algorithm.
type Public struct {
	Algorithm string
	Key       crypto.PublicKey
}

// PublicKey returns the verifying half of an asymmetric key.
func (k *Key) PublicKey(ctx context.Context) (Public, error) {
	pub, alg, err := k.backend.PublicKey(ctx, k.name)
	if err != nil {
		return Public{}, err
	}
	return Public{Algorithm: alg, Key: pub}, nil
}

// MAC returns the pseudonym of data for a tenant: HMAC-SHA-256 under a secret
// unique to (purpose, tenant). See MACBackend.
func (k *Key) MAC(ctx context.Context, tenant string, data []byte) ([]byte, error) {
	if tenant == "" {
		return nil, errors.New("keys: MAC needs a tenant")
	}
	m, ok := k.backend.(MACBackend)
	if !ok {
		return nil, fmt.Errorf("%w: backend %q has no MAC", ErrUnsupported, k.backend.Name())
	}
	return m.MAC(ctx, k.name, k.purpose, tenant, data)
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Destroy erases a tenant: the secret behind MAC for this purpose is removed
// and MAC for the tenant fails with ErrDestroyed from then on. Pseudonyms
// already computed stay where they were written and can never be recomputed,
// which is what erasure means here. It cannot be undone and it is idempotent.
//
// A backend without the capability returns ErrUnsupported (see
// DestroyBackend); a caller must not read that as "destroyed".
func (k *Key) Destroy(ctx context.Context, tenant string) error {
	if tenant == "" {
		return errors.New("keys: Destroy needs a tenant")
	}
	d, ok := k.backend.(DestroyBackend)
	if !ok {
		return fmt.Errorf("%w: backend %q cannot destroy a tenant's key material", ErrUnsupported, k.backend.Name())
	}
	return d.DestroyTenant(ctx, k.name, k.purpose, tenant)
}

// Destroyed reports whether Destroy has been done for the tenant. A backend
// without the capability returns ErrUnsupported.
func (k *Key) Destroyed(ctx context.Context, tenant string) (bool, error) {
	if tenant == "" {
		return false, errors.New("keys: Destroyed needs a tenant")
	}
	d, ok := k.backend.(DestroyBackend)
	if !ok {
		return false, fmt.Errorf("%w: backend %q cannot destroy a tenant's key material", ErrUnsupported, k.backend.Name())
	}
	return d.Destroyed(ctx, k.name, k.purpose, tenant)
}
