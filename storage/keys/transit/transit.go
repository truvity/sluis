package transit

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/openbao"
)

// DefaultMount is the transit mount used without [WithMount].
const DefaultMount = "transit"

// DefaultInfoTTL is how long what the backend read about a key (its type,
// whether it is derived, its latest version and public key) is kept. Sign
// pins to the version read, so this is also how long after a rotation the
// old version keeps signing.
const DefaultInfoTTL = time.Minute

// Binding says how the encryption context reaches transit; see the package
// documentation.
type Binding int

const (
	// BindAuto binds associated_data for a key that is not derived, context for a
	// derived one.
	BindAuto Binding = iota
	// BindAssociatedData always sends associated_data, and refuses a derived
	// key or a key type with no AEAD.
	BindAssociatedData
	// BindDerivation always sends context, and refuses a key that is not
	// derived.
	BindDerivation
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Backend implements keys.Backend and keys.MACBackend over transit.
type Backend struct {
	c          *openbao.Client
	mount      string
	binding    Binding
	infoTTL    time.Duration
	macVersion int
	now        func() time.Time

	mu   sync.Mutex
	info map[string]keyInfo
}

var (
	_ keys.Backend    = (*Backend)(nil)
	_ keys.MACBackend = (*Backend)(nil)
)

// Option configures New.
type Option func(*Backend)

// WithMount sets the transit mount (default [DefaultMount]).
func WithMount(mount string) Option {
	return func(b *Backend) { b.mount = strings.Trim(mount, "/") }
}

// WithBinding sets how the encryption context is sent (default [BindAuto]).
func WithBinding(m Binding) Option { return func(b *Backend) { b.binding = m } }

// WithInfoTTL changes [DefaultInfoTTL].
func WithInfoTTL(d time.Duration) Option { return func(b *Backend) { b.infoTTL = d } }

// WithMACKeyVersion sets the key version MAC uses (default 1). It must stay
// readable on the server for as long as pseudonyms made with it are compared.
func WithMACKeyVersion(v int) Option { return func(b *Backend) { b.macVersion = v } }

// New returns a backend over c. It does no I/O.
func New(c *openbao.Client, opts ...Option) *Backend {
	b := &Backend{c: c, mount: DefaultMount, infoTTL: DefaultInfoTTL, macVersion: 1,
		now: time.Now, info: map[string]keyInfo{}}
	for _, o := range opts {
		o(b)
	}
	if b.mount == "" {
		b.mount = DefaultMount
	}
	return b
}

// Name is "transit".
func (*Backend) Name() string { return "transit" }

// ValidateName accepts a transit key name, and says what to use when given an
// alias or a path.
func (*Backend) ValidateName(name string) error {
	switch {
	case strings.HasPrefix(name, "alias/"):
		return fmt.Errorf("transit key %q: OpenBao has no aliases; use the transit key's own name", name)
	case strings.Contains(name, "/"):
		return fmt.Errorf("transit key %q: a name has no '/'; use the key's name, not a path", name)
	case !nameRe.MatchString(name):
		return fmt.Errorf("transit key %q: a name is letters, digits, '_', '-' and '.', starting with a letter or digit", name)
	}
	return nil
}

func (b *Backend) path(op, key string) string {
	return openbao.EscapePath(b.mount) + "/" + op + "/" + openbao.EscapePath(key)
}

// keyInfo is what keys/<name> says, as far as the backend uses it.
type keyInfo struct {
	typ     string
	derived bool
	version int // latest
	pub     crypto.PublicKey
	expires time.Time
}

func (b *Backend) keyInfo(ctx context.Context, key string) (keyInfo, error) {
	b.mu.Lock()
	ki, ok := b.info[key]
	b.mu.Unlock()
	if ok && b.now().Before(ki.expires) {
		return ki, nil
	}
	resp, err := b.c.Request(ctx, http.MethodGet, b.path("keys", key), nil)
	if err != nil {
		return keyInfo{}, fmt.Errorf("transit: read key %s: %w", key, err)
	}
	var doc struct {
		Type          string                     `json:"type"`
		Derived       bool                       `json:"derived"`
		LatestVersion int                        `json:"latest_version"`
		Keys          map[string]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(resp.Data, &doc); err != nil || doc.LatestVersion < 1 {
		return keyInfo{}, fmt.Errorf("transit: key %s: the answer is not a key", key)
	}
	ki = keyInfo{typ: doc.Type, derived: doc.Derived, version: doc.LatestVersion, expires: b.now().Add(b.infoTTL)}
	// Only an asymmetric key's version is an object with a public key; a
	// symmetric key's is a bare timestamp.
	var v struct {
		PublicKey string `json:"public_key"`
	}
	if json.Unmarshal(doc.Keys[strconv.Itoa(doc.LatestVersion)], &v) == nil && v.PublicKey != "" {
		blk, _ := pem.Decode([]byte(v.PublicKey))
		if blk == nil {
			return keyInfo{}, fmt.Errorf("transit: key %s v%d: the public key is not PEM", key, doc.LatestVersion)
		}
		if ki.pub, err = x509.ParsePKIXPublicKey(blk.Bytes); err != nil {
			return keyInfo{}, fmt.Errorf("transit: key %s v%d: %w", key, doc.LatestVersion, err)
		}
	}
	b.mu.Lock()
	b.info[key] = ki
	b.mu.Unlock()
	return ki, nil
}

func aead(typ string) bool {
	switch typ {
	case "aes128-gcm96", "aes256-gcm96", "chacha20-poly1305", "xchacha20-poly1305":
		return true
	}
	return false
}

// bind adds the encryption context to a request body.
func (b *Backend) bind(ctx context.Context, key string, ec map[string]string, body map[string]any) error {
	if len(ec) == 0 {
		return nil
	}
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return err
	}
	canon, err := json.Marshal(ec) // a map marshals with sorted keys
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(canon)
	mode := b.binding
	if mode == BindAuto {
		mode = BindAssociatedData
		if ki.derived {
			mode = BindDerivation
		}
	}
	switch mode {
	case BindDerivation:
		if !ki.derived {
			return fmt.Errorf("%w: transit key %s is not a derived key, and transit ignores a context on one that is not; "+
				"create it with derived=true, or use associated_data", keys.ErrUnsupported, key)
		}
		body["context"] = enc
	default:
		if ki.derived {
			return fmt.Errorf("%w: transit key %s is a derived key: its context is the key derivation, not associated data", keys.ErrUnsupported, key)
		}
		if !aead(ki.typ) {
			return fmt.Errorf("%w: transit key %s is a %s key, which has no associated data (transit would ignore it)", keys.ErrUnsupported, key, ki.typ)
		}
		body["associated_data"] = enc
	}
	return nil
}

func mapDecryptErr(key string, err error) error {
	if oe, ok := openbao.AsError(err); ok && oe.Status == http.StatusBadRequest && !oe.Says("not found") {
		return fmt.Errorf("%w: %s", keys.ErrDecrypt, strings.Join(oe.Errors, "; "))
	}
	return fmt.Errorf("transit: decrypt under %s: %w", key, err)
}

// Encrypt calls encrypt/<key>.
func (b *Backend) Encrypt(ctx context.Context, key string, pt []byte, ec map[string]string) ([]byte, error) {
	return b.encrypt(ctx, key, pt, ec, 0)
}

// encrypt is Encrypt, pinned to a key version when version is not 0.
func (b *Backend) encrypt(ctx context.Context, key string, pt []byte, ec map[string]string, version int) ([]byte, error) {
	body := map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)}
	if version != 0 {
		body["key_version"] = version
	}
	if err := b.bind(ctx, key, ec, body); err != nil {
		return nil, err
	}
	resp, err := b.c.Request(ctx, http.MethodPost, b.path("encrypt", key), body)
	if err != nil {
		return nil, fmt.Errorf("transit: encrypt under %s: %w", key, err)
	}
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil || out.Ciphertext == "" {
		return nil, fmt.Errorf("transit: encrypt under %s: the answer has no ciphertext", key)
	}
	return []byte(out.Ciphertext), nil
}

// Decrypt calls decrypt/<key>.
func (b *Backend) Decrypt(ctx context.Context, key string, ct []byte, ec map[string]string) ([]byte, error) {
	if !strings.HasPrefix(string(ct), "vault:v") {
		return nil, keys.ErrDecrypt
	}
	body := map[string]any{"ciphertext": string(ct)}
	if err := b.bind(ctx, key, ec, body); err != nil {
		return nil, err
	}
	resp, err := b.c.Request(ctx, http.MethodPost, b.path("decrypt", key), body)
	if err != nil {
		return nil, mapDecryptErr(key, err)
	}
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("transit: decrypt under %s: %w", key, err)
	}
	pt, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("transit: decrypt under %s: the plaintext is not base64", key)
	}
	return pt, nil
}

// GenerateDataKey calls datakey/plaintext/<key> for 256 bits.
func (b *Backend) GenerateDataKey(ctx context.Context, key string, ec map[string]string) ([]byte, []byte, error) {
	body := map[string]any{"bits": 256}
	if err := b.bind(ctx, key, ec, body); err != nil {
		return nil, nil, err
	}
	resp, err := b.c.Request(ctx, http.MethodPost, b.path("datakey/plaintext", key), body)
	if err != nil {
		return nil, nil, fmt.Errorf("transit: generate data key under %s: %w", key, err)
	}
	var out struct {
		Plaintext  string `json:"plaintext"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil || out.Ciphertext == "" {
		return nil, nil, fmt.Errorf("transit: generate data key under %s: the answer has no key", key)
	}
	pt, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil || len(pt) != 32 {
		return nil, nil, fmt.Errorf("transit: generate data key under %s: the plaintext is not 32 bytes", key)
	}
	return pt, []byte(out.Ciphertext), nil
}

type signSpec struct {
	alg     string
	hash    string
	hashLen int
}

func specOf(key string, ki keyInfo) (signSpec, error) {
	switch ki.typ {
	case "ecdsa-p384":
		return signSpec{"ES384", "sha2-384", 48}, nil
	case "rsa-2048", "rsa-3072", "rsa-4096":
		return signSpec{"RS256", "sha2-256", 32}, nil
	}
	return signSpec{}, fmt.Errorf("transit: %s is a %s key; signing keys are ecdsa-p384 (ES384) or rsa-2048/3072/4096 (RS256): %w",
		key, ki.typ, keys.ErrUnsupported)
}

// KeyVersion is the version of key that Sign signs with and PublicKey
// returns: the latest version as last read (see [DefaultInfoTTL]). A signer
// that names its kid after the version reads it here.
func (b *Backend) KeyVersion(ctx context.Context, key string) (int, error) {
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return 0, err
	}
	return ki.version, nil
}

// Sign signs a digest: SHA-384 for an ecdsa-p384 key, SHA-256 for an RSA
// key, pinned to the key version read with the public key. An ECDSA
// signature is ASN.1 DER.
func (b *Backend) Sign(ctx context.Context, key string, digest []byte) ([]byte, error) {
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return nil, err
	}
	spec, err := specOf(key, ki)
	if err != nil {
		return nil, err
	}
	if len(digest) != spec.hashLen {
		return nil, fmt.Errorf("transit: %s signs a %d-byte digest (%s), got %d bytes", key, spec.hashLen, spec.alg, len(digest))
	}
	body := map[string]any{
		"input": base64.StdEncoding.EncodeToString(digest), "prehashed": true,
		"hash_algorithm": spec.hash, "key_version": ki.version,
	}
	if spec.alg == "ES384" {
		body["marshaling_algorithm"] = "asn1"
	} else {
		body["signature_algorithm"] = "pkcs1v15"
	}
	resp, err := b.c.Request(ctx, http.MethodPost, b.path("sign", key), body)
	if err != nil {
		return nil, fmt.Errorf("transit: sign with %s: %w", key, err)
	}
	var out struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("transit: sign with %s: %w", key, err)
	}
	raw, ver, err := payload(out.Signature)
	if err != nil {
		return nil, fmt.Errorf("transit: sign with %s: %w", key, err)
	}
	if ver != ki.version {
		return nil, fmt.Errorf("transit: sign with %s: signed with version %d, asked for %d", key, ver, ki.version)
	}
	return raw, nil
}

// payload splits "vault:v<N>:<base64>".
func payload(s string) ([]byte, int, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[1], "v") {
		return nil, 0, fmt.Errorf("transit returned %q, which is not vault:v<N>:<base64>", s)
	}
	ver, err := strconv.Atoi(parts[1][1:])
	if err != nil {
		return nil, 0, fmt.Errorf("transit returned %q, which is not vault:v<N>:<base64>", s)
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, 0, fmt.Errorf("transit returned %q, whose payload is not base64", s)
	}
	return raw, ver, nil
}

// PublicKey returns the public half of the key's pinned version and its JOSE
// algorithm.
func (b *Backend) PublicKey(ctx context.Context, key string) (crypto.PublicKey, string, error) {
	ki, err := b.keyInfo(ctx, key)
	if err != nil {
		return nil, "", err
	}
	spec, err := specOf(key, ki)
	if err != nil {
		return nil, "", err
	}
	if ki.pub == nil {
		return nil, "", fmt.Errorf("transit: %s v%d has no public key: %w", key, ki.version, keys.ErrUnsupported)
	}
	return ki.pub, spec.alg, nil
}
