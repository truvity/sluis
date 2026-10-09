package signer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultTokenLifetime is the lifetime of an access token where a deployment
// names none; the key ring's default overlap is built on it.
const DefaultTokenLifetime = time.Hour

// State is the part of the shared state the key ring uses: the ring's
// schedule and entries live there so that every replica agrees on them. The
// issuer's State satisfies it; so does a store of the storage port.
type State interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	SetIfAbsent(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	Delete(ctx context.Context, key string) error
	Add(ctx context.Context, key, member string, ttl time.Duration) error
	Remove(ctx context.Context, key, member string) error
	Members(ctx context.Context, key string) ([]string, error)
}

// getJSON reads a value and decodes it.
func getJSON[T any](ctx context.Context, state State, key string) (*T, error) {
	raw, found, err := state.Get(ctx, key)
	if err != nil || !found {
		return nil, err
	}
	var out T
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("signer: %s is not readable: %w", key, err)
	}
	return &out, nil
}
