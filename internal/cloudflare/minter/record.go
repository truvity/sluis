package minter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
)

// The record of minted ids. A sweep deletes by the ids sluis recorded and does
// not list the account: Cloudflare hides an expired token from its list, still
// answers a GET for it as `expired`, lets it be deleted, and (assumed) counts it
// toward the account's 500 tokens until it is. The record is the only place a
// sweep can find them. It is written when a token is minted, before the value
// leaves this package, so a token that was not recorded was never handed out.

const recordRetries = 6

// update applies edit to the preset's record under a conditional write.
func (m *Minter) updateRecord(ctx context.Context, preset string, edit func(*secretstore.CloudflareMinted)) error {
	v := m.mintedDoc(preset)
	var err error
	for range recordRetries {
		doc, rev, gerr := v.Get(ctx)
		if gerr != nil && !errors.Is(gerr, state.ErrNotFound) {
			return gerr
		}
		edit(&doc)
		if _, err = v.Put(ctx, doc, rev); err == nil {
			return nil
		}
		if !errors.Is(err, state.ErrConflict) {
			return err
		}
	}
	return err
}

// track records a token minted for the preset.
func (m *Minter) track(ctx context.Context, preset, id string, expires time.Time) error {
	return m.updateRecord(ctx, preset, func(d *secretstore.CloudflareMinted) {
		if !slices.ContainsFunc(d.Tokens, func(t secretstore.CloudflareMintedToken) bool { return t.ID == id }) {
			d.Tokens = append(d.Tokens, secretstore.CloudflareMintedToken{ID: id, ExpiresOn: expires.UTC()})
		}
	})
}

// untrack forgets tokens that are deleted.
func (m *Minter) untrack(ctx context.Context, preset string, ids ...string) error {
	return m.updateRecord(ctx, preset, func(d *secretstore.CloudflareMinted) {
		d.Tokens = slices.DeleteFunc(d.Tokens, func(t secretstore.CloudflareMintedToken) bool { return slices.Contains(ids, t.ID) })
	})
}

// Recorded are the ids the preset has minted and not yet deleted.
func (m *Minter) Recorded(ctx context.Context, preset string) ([]secretstore.CloudflareMintedToken, error) {
	doc, _, err := m.mintedDoc(preset).Get(ctx)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the record of minted tokens: %w", err)
	}
	return doc.Tokens, nil
}
