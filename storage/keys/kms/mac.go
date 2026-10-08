package kms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/truvity/sluis/storage/keys"
)

// WrappedStore keeps the wrapped HMAC keys behind keys.Key.MAC. It is
// deliberately small so the caller can back it with whatever it already has
// (a state store, a table, a file): the content is ciphertext, useless
// without the KMS key and the right encryption context.
type WrappedStore interface {
	// Get returns the wrapped key stored under id, found=false if none.
	Get(ctx context.Context, id string) (wrapped []byte, found bool, err error)
	// PutIfAbsent stores wrapped under id unless a value exists, and returns
	// the value now stored (the caller's or the winner's). Concurrent first
	// uses must converge on one key, or two processes would pseudonymise the
	// same tenant differently.
	PutIfAbsent(ctx context.Context, id string, wrapped []byte) (stored []byte, err error)
}

// ErasableStore is a WrappedStore that can also erase a tenant: it keeps a
// tombstone beside the wrapped key, so a destroyed tenant is told apart from
// one never used. Without it Backend.DestroyTenant returns keys.ErrUnsupported
// and MAC knows nothing of erasure. FromState gives one.
type ErasableStore interface {
	WrappedStore
	// Tombstone records that id is destroyed and removes the wrapped key
	// with all its versions. The tombstone goes first, so a first use racing
	// the erasure cannot be left looking valid. It is idempotent: an id with
	// no wrapped key, or already destroyed, succeeds.
	Tombstone(ctx context.Context, id string) error
	// Tombstoned reports whether Tombstone was done for id.
	Tombstoned(ctx context.Context, id string) (bool, error)
}

// DefaultMACKeyTTL is how long a process trusts a plaintext HMAC key it holds
// before it asks the store again whether the tenant was destroyed. It bounds
// how long another replica goes on computing pseudonyms after an erasure.
const DefaultMACKeyTTL = time.Minute

type macEntry struct {
	key     []byte
	checked time.Time
}

func macID(purpose keys.Purpose, tenant string) string {
	return "mac/" + string(purpose) + "/" + base64.RawURLEncoding.EncodeToString([]byte(tenant))
}

// DestroyTenant erases the tenant's HMAC key: a tombstone is written, then
// the wrapped key is deleted from the store with all its versions, and the
// plaintext is dropped from this process. From then on MAC for the tenant
// returns keys.ErrDestroyed here and, within DefaultMACKeyTTL, in every other
// process over the same store. Idempotent.
//
// There is no KMS call: the wrapped key is useless without its store entry,
// and KMS cannot delete a data key it does not keep. Copies of the store
// (backups, versions the store's own retention keeps) stay wrapped under the
// purpose's key, which is where the custody of that key matters.
func (b *Backend) DestroyTenant(ctx context.Context, key string, purpose keys.Purpose, tenant string) error {
	if tenant == "" {
		return errors.New("kms: Destroy needs a tenant")
	}
	es, ok := b.store.(ErasableStore)
	if !ok {
		return fmt.Errorf("%w: kms Destroy needs a WrappedStore that is an ErasableStore (kms.FromState)", keys.ErrUnsupported)
	}
	id := macID(purpose, tenant)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := es.Tombstone(ctx, id); err != nil {
		return fmt.Errorf("kms: destroying %s: %w", id, err)
	}
	delete(b.macKs, key+"\x00"+id)
	return nil
}

// Destroyed reports whether the tenant was destroyed in the store.
func (b *Backend) Destroyed(ctx context.Context, _ string, purpose keys.Purpose, tenant string) (bool, error) {
	es, ok := b.store.(ErasableStore)
	if !ok {
		return false, fmt.Errorf("%w: kms Destroy needs a WrappedStore that is an ErasableStore (kms.FromState)", keys.ErrUnsupported)
	}
	return es.Tombstoned(ctx, macID(purpose, tenant))
}

// MAC returns HMAC-SHA-256 of data under a key unique to (purpose, tenant).
//
// The key is a KMS data key, generated on first use and wrapped under the
// purpose's key with the context {purpose, tenant}, so KMS itself will not
// unwrap one tenant's key as another's. The wrapped form is in the
// WrappedStore; the plaintext is kept in this process's memory only.
func (b *Backend) MAC(ctx context.Context, key string, purpose keys.Purpose, tenant string, data []byte) ([]byte, error) {
	if b.store == nil {
		return nil, fmt.Errorf("%w: kms MAC needs WithWrappedStore", keys.ErrUnsupported)
	}
	if tenant == "" {
		return nil, errors.New("kms: MAC needs a tenant")
	}
	hk, err := b.macKey(ctx, key, purpose, tenant)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, hk)
	m.Write(data)
	return m.Sum(nil), nil
}

func (b *Backend) macKey(ctx context.Context, key string, purpose keys.Purpose, tenant string) ([]byte, error) {
	id := macID(purpose, tenant)
	cacheID := key + "\x00" + id
	es, erasable := b.store.(ErasableStore)
	b.mu.Lock()
	defer b.mu.Unlock() // one miss at a time; a hit is a map read
	now := b.now()
	if e, ok := b.macKs[cacheID]; ok && (!erasable || now.Sub(e.checked) < DefaultMACKeyTTL) {
		return e.key, nil
	}
	if erasable {
		gone, err := es.Tombstoned(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("kms: reading the tombstone of %s: %w", id, err)
		}
		if gone {
			delete(b.macKs, cacheID)
			return nil, fmt.Errorf("%w: %s/%s", keys.ErrDestroyed, purpose, tenant)
		}
		if e, ok := b.macKs[cacheID]; ok {
			b.macKs[cacheID] = macEntry{e.key, now}
			return e.key, nil
		}
	}
	ec := map[string]string{"purpose": string(purpose), "tenant": tenant}
	wrapped, found, err := b.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("kms: reading the wrapped key %s: %w", id, err)
	}
	if !found {
		plain, w, err := b.GenerateDataKey(ctx, key, ec)
		if err != nil {
			return nil, err
		}
		stored, err := b.store.PutIfAbsent(ctx, id, w)
		if err != nil {
			return nil, fmt.Errorf("kms: storing the wrapped key %s: %w", id, err)
		}
		if string(stored) == string(w) {
			b.macKs[cacheID] = macEntry{plain, now}
			return plain, nil
		}
		wrapped = stored // another process won; use its key
	}
	plain, err := b.Decrypt(ctx, key, wrapped, ec)
	if err != nil {
		return nil, fmt.Errorf("kms: unwrapping the key %s: %w", id, err)
	}
	b.macKs[cacheID] = macEntry{plain, now}
	return plain, nil
}
