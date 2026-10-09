package transit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/truvity/sluis/storage/keys"
	"github.com/truvity/sluis/storage/openbao"
)

// The pseudonym purpose has a transit key of its own for every tenant, so a
// tenant can be erased by destroying its key; see the package documentation,
// "Per-tenant keys". This file is that: the naming, the creation on first
// use, and the erasure.

// tenantVersion is the key version every per-tenant call is pinned to, so a
// rotation by somebody else changes nothing.
const tenantVersion = 1

var (
	_ keys.DestroyBackend      = (*Backend)(nil)
	_ keys.TenantCipherBackend = (*Backend)(nil)
)

func perTenant(purpose keys.Purpose) bool { return purpose == keys.Pseudonym }

// EscapeTenant maps a tenant to the part of a transit key name that stands
// for it. A byte of a-z, 0-9 or '-' stands for itself; any other byte
// (including an upper-case letter, '.', '_', '/' and '@') is '_' and the two
// lower-case hex digits of the byte. '_' is therefore always the start of an
// escape, never a plain character, so the mapping is injective: two tenants
// never give one name, and the name is read back unambiguously. Upper-case is
// escaped too, so two tenants that differ only in case stay apart on a store
// that folds case. The result is readable ("@platform" is "_40platform") and
// keeps a leading part of the tenant as a leading part of the name, so a
// policy can allow one profile with a glob.
func EscapeTenant(tenant string) string {
	const hexdigits = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(tenant); i++ {
		c := tenant[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteByte(hexdigits[c>>4])
		b.WriteByte(hexdigits[c&0xf])
	}
	return b.String()
}

// TenantKeyName is the transit key of a tenant: <prefix>.<purpose>.<escaped
// tenant>, where prefix is the name configured for the purpose. It is an
// error if the name is not a valid transit key name or is too long (a name is
// never truncated, which could join two tenants).
func TenantKeyName(prefix string, purpose keys.Purpose, tenant string) (string, error) {
	if tenant == "" {
		return "", errNoTenant
	}
	name := prefix + "." + string(purpose) + "." + EscapeTenant(tenant)
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("transit: the key for tenant %q would be named %q, which is not a valid transit key name "+
			"(at most 128 characters; use a shorter prefix or tenant)", tenant, name)
	}
	return name, nil
}

// tenantDoc is what keys/<name> says about a tenant's key.
type tenantDoc struct {
	LatestVersion        int                        `json:"latest_version"`
	MinAvailableVersion  int                        `json:"min_available_version"`
	MinDecryptionVersion int                        `json:"min_decryption_version"`
	Keys                 map[string]json.RawMessage `json:"keys"`
}

// destroyed reports whether version 1, which everything is made under, is
// gone or refused: the tombstone.
func (d tenantDoc) destroyed() bool {
	_, has := d.Keys[strconv.Itoa(tenantVersion)]
	return d.MinAvailableVersion > tenantVersion || d.MinDecryptionVersion > tenantVersion || !has
}

func isNotFound(err error) bool {
	oe, ok := openbao.AsError(err)
	return ok && (oe.Status == http.StatusNotFound || oe.Says("not found"))
}

// readTenant reads the key; found is false when there is none.
func (b *Backend) readTenant(ctx context.Context, name string) (doc tenantDoc, found bool, err error) {
	resp, err := b.c.Request(ctx, http.MethodGet, b.path("keys", name), nil)
	switch {
	case isNotFound(err):
		return doc, false, nil
	case err != nil:
		return doc, false, fmt.Errorf("transit: read key %s: %w", name, err)
	}
	if err := json.Unmarshal(resp.Data, &doc); err != nil || doc.LatestVersion < 1 {
		return doc, false, fmt.Errorf("transit: key %s: the answer is not a key", name)
	}
	return doc, true, nil
}

// create makes the key through the encrypt endpoint, which creates one when
// the policy grants create there. A writer's policy then needs nothing on
// transit/keys, a grant that would reach rotate and trim, which is erasure.
// It is idempotent in the engine, so two replicas that meet a new tenant at
// once end up with one key. It is only called for a key that was not found,
// and a destroyed key is found, so a destroyed tenant never gets a fresh key.
func (b *Backend) create(ctx context.Context, name string) error {
	_, err := b.c.Request(ctx, http.MethodPost, b.path("encrypt", name), map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString([]byte{0}),
		"type":      "aes256-gcm96",
	})
	if err != nil {
		return fmt.Errorf("transit: create key %s: %w", name, err)
	}
	return nil
}

// withTenantKey runs call, creating the key first when the engine says there
// is none, and turns a refusal by a destroyed key into keys.ErrDestroyed.
func (b *Backend) withTenantKey(ctx context.Context, name string, create bool, call func() error) error {
	err := call()
	if err != nil && create && isNotFound(err) {
		if err = b.create(ctx, name); err == nil {
			err = call()
		}
	}
	return b.asDestroyed(ctx, name, err)
}

// asDestroyed is keys.ErrDestroyed when the engine refused a call on a key
// that carries the tombstone. The engine words that refusal per operation, so
// the key is read instead of the sentence.
func (b *Backend) asDestroyed(ctx context.Context, name string, err error) error {
	if err == nil {
		return nil
	}
	oe, ok := openbao.AsError(err)
	if !ok && !errors.Is(err, keys.ErrDecrypt) {
		return err
	}
	if ok && oe.Status != http.StatusBadRequest {
		return err
	}
	if doc, found, rerr := b.readTenant(ctx, name); rerr == nil && found && doc.destroyed() {
		return fmt.Errorf("%w: %s", keys.ErrDestroyed, name)
	}
	return err
}

func (b *Backend) tenantMAC(ctx context.Context, key string, tenant string, data []byte) ([]byte, error) {
	name, err := TenantKeyName(key, keys.Pseudonym, tenant)
	if err != nil {
		return nil, err
	}
	var out struct {
		HMAC string `json:"hmac"`
	}
	err = b.withTenantKey(ctx, name, true, func() error {
		resp, err := b.c.Request(ctx, http.MethodPost, b.path("hmac", name), map[string]any{
			"input":       base64.StdEncoding.EncodeToString(data),
			"algorithm":   "sha2-256",
			"key_version": tenantVersion,
		})
		if err != nil {
			return err
		}
		return json.Unmarshal(resp.Data, &out)
	})
	if err != nil {
		if errors.Is(err, keys.ErrDestroyed) {
			return nil, err
		}
		return nil, fmt.Errorf("transit: hmac with %s: %w", name, err)
	}
	raw, ver, err := payload(out.HMAC)
	if err != nil || ver != tenantVersion || len(raw) != sha256.Size {
		return nil, fmt.Errorf("transit: hmac with %s: want version %d and %d bytes, got %q", name, tenantVersion, sha256.Size, out.HMAC)
	}
	return raw, nil
}

// EncryptTenant encrypts under the tenant's key, creating it on first use.
// Only the pseudonym purpose has one; another purpose gets keys.ErrUnsupported.
func (b *Backend) EncryptTenant(ctx context.Context, key string, purpose keys.Purpose, tenant string, pt []byte, ec map[string]string) ([]byte, error) {
	if !perTenant(purpose) {
		return nil, fmt.Errorf("%w: transit has a key per tenant for the %s purpose only", keys.ErrUnsupported, keys.Pseudonym)
	}
	name, err := TenantKeyName(key, purpose, tenant)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = b.withTenantKey(ctx, name, true, func() (err error) {
		out, err = b.encrypt(ctx, name, pt, ec, tenantVersion)
		return err
	})
	return out, err
}

// DecryptTenant opens under the tenant's key. It never creates one: a
// ciphertext under a key that does not exist was not made here.
func (b *Backend) DecryptTenant(ctx context.Context, key string, purpose keys.Purpose, tenant string, ct []byte, ec map[string]string) ([]byte, error) {
	if !perTenant(purpose) {
		return nil, fmt.Errorf("%w: transit has a key per tenant for the %s purpose only", keys.ErrUnsupported, keys.Pseudonym)
	}
	name, err := TenantKeyName(key, purpose, tenant)
	if err != nil {
		return nil, err
	}
	var out []byte
	err = b.withTenantKey(ctx, name, false, func() (err error) {
		out, err = b.Decrypt(ctx, name, ct, ec)
		return err
	})
	return out, err
}

// DestroyTenant erases the tenant: rotate once, require a version past 1 to
// encrypt and to decrypt, and trim version 1. The key stays, holding versions
// nothing uses; that is the tombstone, and it is found before a key would be
// made, so the tenant never gets a second identity. A tenant never seen has
// its key made and destroyed at once. It needs rights on the key itself
// (rotate, config, trim) that the writer's role must not have; it is the
// eraser's.
//
// The steps are ordered so that stopping between any two leaves nothing
// usable that should not be (version 1 is refused before it is trimmed), and
// a second call finishes what the first began.
func (b *Backend) DestroyTenant(ctx context.Context, key string, purpose keys.Purpose, tenant string) error {
	if !perTenant(purpose) {
		return fmt.Errorf("%w: transit erases the %s purpose only", keys.ErrUnsupported, keys.Pseudonym)
	}
	name, err := TenantKeyName(key, purpose, tenant)
	if err != nil {
		return err
	}
	doc, found, err := b.readTenant(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		if err := b.create(ctx, name); err != nil {
			return err
		}
		if doc, _, err = b.readTenant(ctx, name); err != nil {
			return err
		}
	}
	if doc.LatestVersion <= tenantVersion {
		if _, err := b.c.Request(ctx, http.MethodPost, b.path("keys", name)+"/rotate", nil); err != nil {
			return fmt.Errorf("transit: rotate %s: %w", name, err)
		}
	}
	steps := []struct {
		path string
		body map[string]any
	}{
		{"/config", map[string]any{"min_decryption_version": tenantVersion + 1, "min_encryption_version": tenantVersion + 1}},
		{"/trim", map[string]any{"min_available_version": tenantVersion + 1}},
	}
	for _, s := range steps {
		if _, err := b.c.Request(ctx, http.MethodPost, b.path("keys", name)+s.path, s.body); err != nil {
			return fmt.Errorf("transit: destroy %s%s: %w", name, s.path, err)
		}
	}
	b.mu.Lock()
	delete(b.info, name)
	b.mu.Unlock()
	return nil
}

// Destroyed reports whether the tenant's key carries the tombstone. A tenant
// never seen is not destroyed. Only the pseudonym purpose can be.
func (b *Backend) Destroyed(ctx context.Context, key string, purpose keys.Purpose, tenant string) (bool, error) {
	if !perTenant(purpose) {
		return false, fmt.Errorf("%w: transit erases the %s purpose only", keys.ErrUnsupported, keys.Pseudonym)
	}
	name, err := TenantKeyName(key, purpose, tenant)
	if err != nil {
		return false, err
	}
	doc, found, err := b.readTenant(ctx, name)
	return found && doc.destroyed(), err
}
