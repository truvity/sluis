package portstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/truvity/sluis/internal/port"
)

const sessionKeyKey = "rec.console.session-key"

// SessionKey returns the key the console signs its sessions with, creating it
// the first time. It is a secret, so every replica reads the same one: the
// first writer wins the create and the others read what it wrote.
func (b *Base) SessionKey(ctx context.Context, generate func() ([]byte, error)) ([]byte, error) {
	if b.Secrets == nil {
		return nil, errNoSecrets
	}
	path := secretPath(sessionKeyKey, "")
	for range attempts {
		got, err := b.Secrets.Get(ctx, path)
		switch {
		case err == nil:
			return got.Value, nil
		case !errors.Is(err, port.ErrNotFound):
			return nil, err
		}
		key, err := generate()
		if err != nil {
			return nil, err
		}
		if _, err = b.Secrets.PutIfVersion(ctx, path, key, ""); err == nil {
			return key, nil
		} else if !errors.Is(err, port.ErrConflict) {
			return nil, err
		}
	}
	return nil, ErrBusy
}

// PutSessionKey replaces the key the console signs its sessions with, which a
// migration does when it carries the source's key over a different one.
func (b *Base) PutSessionKey(ctx context.Context, key []byte) error {
	if b.Secrets == nil {
		return errNoSecrets
	}
	_, err := b.Secrets.Put(ctx, secretPath(sessionKeyKey, ""), key)
	return err
}

// CheckSecrets proves the Secrets port is there and answers, so that a
// deployment whose adapters have none stops at start naming the setting,
// instead of failing on the first credential an operator connects. The proof
// is one read, of the console's session key, which may be absent: a listing of
// every credential would be pages of reads at every start, and every new
// Lambda environment of a herd starts.
func (b *Base) CheckSecrets(ctx context.Context) error {
	if err := b.RequireSecrets(); err != nil {
		return err
	}
	if _, err := b.Secrets.Get(ctx, secretPath(sessionKeyKey, "")); err != nil && !errors.Is(err, port.ErrNotFound) {
		return fmt.Errorf("the Secrets port does not answer: %w", err)
	}
	return nil
}

// RequireSecrets is CheckSecrets without the read, for a caller that reads the
// session key next anyway.
func (b *Base) RequireSecrets() error {
	if b.Secrets == nil {
		return fmt.Errorf("credentials are kept in Secrets, and none is configured: choose a secrets adapter (adapters.secrets, or a preset): %w", errNoSecrets)
	}
	return nil
}
