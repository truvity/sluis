package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/truvity/sluis/storage/keys/kms"
)

// WrappedKeys keeps the KMS-wrapped per-tenant secrets behind pseudonyms in the
// deployment's database (audit_wrapped_keys), with a tombstone table beside it
// for erasure. It is the kms adapter's WrappedStore for an installation with no
// SSM Parameter Store: what it holds is ciphertext, useless without the KMS key
// and its encryption context.
type WrappedKeys struct{ db DB }

var _ kms.ErasableStore = (*WrappedKeys)(nil)

// NewWrappedKeys returns the store over a connection as the writer's role.
func NewWrappedKeys(db DB) *WrappedKeys { return &WrappedKeys{db: db} }

// Get implements kms.WrappedStore.
func (w *WrappedKeys) Get(ctx context.Context, id string) ([]byte, bool, error) {
	var wrapped []byte
	err := w.db.QueryRow(ctx, `select wrapped from audit_wrapped_keys where id = $1`, id).Scan(&wrapped)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres: reading a wrapped key: %w", err)
	}
	return wrapped, true, nil
}

// PutIfAbsent implements kms.WrappedStore. Concurrent first uses converge on one
// row: the loser reads the winner's. A destroyed id is never stored again.
func (w *WrappedKeys) PutIfAbsent(ctx context.Context, id string, wrapped []byte) ([]byte, error) {
	if _, err := w.db.Exec(ctx,
		`insert into audit_wrapped_keys (id, wrapped)
		 select $1, $2 where not exists (select 1 from audit_wrapped_key_tombstones where id = $1)
		 on conflict (id) do nothing`, id, wrapped); err != nil {
		return nil, fmt.Errorf("postgres: storing a wrapped key: %w", err)
	}
	stored, found, err := w.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("postgres: %s was destroyed while its key was being made", id)
	}
	return stored, nil
}

// Tombstone implements kms.ErasableStore: one statement writes the tombstone and
// deletes the wrapped key, so no state has the key without its tombstone.
func (w *WrappedKeys) Tombstone(ctx context.Context, id string) error {
	if _, err := w.db.Exec(ctx,
		`with t as (insert into audit_wrapped_key_tombstones (id) values ($1) on conflict (id) do nothing)
		 delete from audit_wrapped_keys where id = $1`, id); err != nil {
		return fmt.Errorf("postgres: destroying a wrapped key: %w", err)
	}
	return nil
}

// Tombstoned implements kms.ErasableStore.
func (w *WrappedKeys) Tombstoned(ctx context.Context, id string) (bool, error) {
	var gone bool
	if err := w.db.QueryRow(ctx,
		`select exists (select 1 from audit_wrapped_key_tombstones where id = $1)`, id).Scan(&gone); err != nil {
		return false, fmt.Errorf("postgres: reading a tombstone: %w", err)
	}
	return gone, nil
}
