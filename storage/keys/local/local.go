// Package local is a key backend for tests and for conformance: every key is
// derived from one root secret held in memory. Nothing is stored and no
// service is called. It is not a production backend: the root sits in the
// process, there is no rotation, and anyone with the root has every key.
// A name works as a symmetric key and as a P-384 signing key alike.
package local

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"sync"

	"github.com/truvity/sluis/storage/keys"
)

// MinRootLen is the shortest root accepted.
const MinRootLen = 32

// ciphertext layout: version(1) | nonce(12) | AES-256-GCM(ct||tag).
const version = 1

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Backend derives every key from a root secret.
type Backend struct {
	root []byte
	mu   sync.Mutex
	sign map[string]*ecdsa.PrivateKey
	gone map[string]bool // tenants destroyed, by (key, purpose, tenant)
}

var (
	_ keys.Backend        = (*Backend)(nil)
	_ keys.MACBackend     = (*Backend)(nil)
	_ keys.DestroyBackend = (*Backend)(nil)
)

// New returns a backend over root. It refuses a root that is short or has
// fewer than 16 distinct byte values (a repeated byte, a counter, a word):
// the root is the whole secret, and a weak one in a test is a weak one that
// gets copied into a deployment.
func New(root []byte) (*Backend, error) {
	if len(root) < MinRootLen {
		return nil, fmt.Errorf("local: root is %d bytes, need at least %d", len(root), MinRootLen)
	}
	seen := map[byte]bool{}
	for _, b := range root {
		seen[b] = true
	}
	if len(seen) < 16 {
		return nil, fmt.Errorf("local: root has %d distinct byte values, need at least 16 (use RandomRoot)", len(seen))
	}
	return &Backend{root: append([]byte(nil), root...), sign: map[string]*ecdsa.PrivateKey{}}, nil
}

// RandomRoot returns 32 random bytes.
func RandomRoot() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// Name is "local".
func (*Backend) Name() string { return "local" }

// ValidateName accepts lower-case names such as "seal-key".
func (*Backend) ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("local key name %q: use lower-case letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

func (b *Backend) derive(label, name string, extra ...string) []byte {
	info := label + "\x00" + name
	for _, e := range extra {
		info += "\x00" + e
	}
	out, err := hkdf.Key(sha256.New, b.root, nil, info, 32)
	if err != nil {
		panic(err)
	}
	return out
}

// aad is the encryption context as additional data: sorted, length-prefixed,
// so ("a","bc") and ("ab","c") differ. No context is empty data, which is
// different from an empty-valued one.
func aad(ec map[string]string) []byte {
	if len(ec) == 0 {
		return nil
	}
	names := make([]string, 0, len(ec))
	for k := range ec {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []byte
	for _, k := range names {
		for _, s := range []string{k, ec[k]} {
			out = binary.BigEndian.AppendUint32(out, uint32(len(s)))
			out = append(out, s...)
		}
	}
	return out
}

func (b *Backend) aead(key string) cipher.AEAD {
	blk, err := aes.NewCipher(b.derive("enc", key))
	if err != nil {
		panic(err)
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		panic(err)
	}
	return g
}

// Encrypt seals plaintext with AES-256-GCM, the context as additional data.
func (b *Backend) Encrypt(_ context.Context, key string, plaintext []byte, ec map[string]string) ([]byte, error) {
	g := b.aead(key)
	out := make([]byte, 1+g.NonceSize())
	out[0] = version
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return g.Seal(out, out[1:], plaintext, aad(ec)), nil
}

// Decrypt opens a ciphertext; any failure is keys.ErrDecrypt.
func (b *Backend) Decrypt(_ context.Context, key string, ct []byte, ec map[string]string) ([]byte, error) {
	g := b.aead(key)
	if len(ct) < 1+g.NonceSize()+g.Overhead() || ct[0] != version {
		return nil, keys.ErrDecrypt
	}
	n := ct[1 : 1+g.NonceSize()]
	pt, err := g.Open(nil, n, ct[1+g.NonceSize():], aad(ec))
	if err != nil {
		return nil, keys.ErrDecrypt
	}
	return pt, nil
}

// GenerateDataKey returns 32 random bytes and their wrapped form.
func (b *Backend) GenerateDataKey(ctx context.Context, key string, ec map[string]string) ([]byte, []byte, error) {
	p := make([]byte, 32)
	if _, err := rand.Read(p); err != nil {
		return nil, nil, err
	}
	w, err := b.Encrypt(ctx, key, p, ec)
	if err != nil {
		return nil, nil, err
	}
	return p, w, nil
}

// signKey derives an ECC P-384 key from the root and the name.
func (b *Backend) signKey(name string) (*ecdsa.PrivateKey, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if k, ok := b.sign[name]; ok {
		return k, nil
	}
	curve := elliptic.P384()
	seed, err := hkdf.Key(sha256.New, b.root, nil, "sign\x00"+name, 48+16)
	if err != nil {
		return nil, err
	}
	// scalar in [1, N-1], from 16 spare bytes so the bias is negligible.
	n1 := new(big.Int).Sub(curve.Params().N, big.NewInt(1))
	d := new(big.Int).SetBytes(seed)
	d.Mod(d, n1).Add(d, big.NewInt(1))
	raw := make([]byte, 48)
	d.FillBytes(raw)
	k, err := ecdsa.ParseRawPrivateKey(curve, raw)
	if err != nil {
		return nil, err
	}
	b.sign[name] = k
	return k, nil
}

// Sign signs a SHA-384 digest with the name's P-384 key (ES384).
func (b *Backend) Sign(_ context.Context, key string, digest []byte) ([]byte, error) {
	if len(digest) != 48 {
		return nil, fmt.Errorf("local: ES384 signs a 48-byte SHA-384 digest, got %d bytes", len(digest))
	}
	k, err := b.signKey(key)
	if err != nil {
		return nil, err
	}
	return ecdsa.SignASN1(rand.Reader, k, digest)
}

// PublicKey returns the P-384 public key and "ES384".
func (b *Backend) PublicKey(_ context.Context, key string) (crypto.PublicKey, string, error) {
	k, err := b.signKey(key)
	if err != nil {
		return nil, "", err
	}
	return &k.PublicKey, "ES384", nil
}

// MAC is HMAC-SHA-256 under a key derived from (key, purpose, tenant).
func (b *Backend) MAC(_ context.Context, key string, purpose keys.Purpose, tenant string, data []byte) ([]byte, error) {
	if tenant == "" {
		return nil, errors.New("local: MAC needs a tenant")
	}
	if b.isGone(key, purpose, tenant) {
		return nil, fmt.Errorf("%w: %s/%s", keys.ErrDestroyed, purpose, tenant)
	}
	m := hmac.New(sha256.New, b.derive("mac", key, string(purpose), tenant))
	m.Write(data)
	return m.Sum(nil), nil
}

func goneID(key string, purpose keys.Purpose, tenant string) string {
	return key + "\x00" + string(purpose) + "\x00" + tenant
}

func (b *Backend) isGone(key string, purpose keys.Purpose, tenant string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gone[goneID(key, purpose, tenant)]
}

// DestroyTenant marks the tenant destroyed. Every key here derives from the
// root, so there is no stored secret to delete: the mark is in this
// backend's memory only, and a new Backend over the same root derives the
// tenant's key again. That is enough to test the semantics, and it is one
// more reason local is not a production backend.
func (b *Backend) DestroyTenant(_ context.Context, key string, purpose keys.Purpose, tenant string) error {
	if tenant == "" {
		return errors.New("local: Destroy needs a tenant")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gone == nil {
		b.gone = map[string]bool{}
	}
	b.gone[goneID(key, purpose, tenant)] = true
	return nil
}

// Destroyed reports whether DestroyTenant was called on this backend.
func (b *Backend) Destroyed(_ context.Context, key string, purpose keys.Purpose, tenant string) (bool, error) {
	return b.isGone(key, purpose, tenant), nil
}
