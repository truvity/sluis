package kms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

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
	id := "mac/" + string(purpose) + "/" + base64.RawURLEncoding.EncodeToString([]byte(tenant))
	cacheID := key + "\x00" + id
	b.mu.Lock()
	defer b.mu.Unlock() // one miss at a time; a hit is a map read
	if k, ok := b.macKs[cacheID]; ok {
		return k, nil
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
			b.macKs[cacheID] = plain
			return plain, nil
		}
		wrapped = stored // another process won; use its key
	}
	plain, err := b.Decrypt(ctx, key, wrapped, ec)
	if err != nil {
		return nil, fmt.Errorf("kms: unwrapping the key %s: %w", id, err)
	}
	b.macKs[cacheID] = plain
	return plain, nil
}
