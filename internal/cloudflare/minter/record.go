package minter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/storage/logattr"

	"github.com/truvity/sluis/internal/config"
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

// recordSweepAt is the number of recorded tokens at which an on-demand mint
// sweeps the preset first even when none has expired yet by the record: a
// record that size is one the schedule is not keeping short.
const recordSweepAt = 64

// recordMaxBytes is the largest record the smallest store keeps: an
// advanced-tier SSM parameter holds 8 KiB, about 104 tokens.
const recordMaxBytes = 8 << 10

// recordWorstEntry is the longest entry a mint adds: a token id and an
// expiry with nanoseconds.
var recordWorstEntry = secretstore.CloudflareMintedToken{
	ID: strings.Repeat("f", 32), ExpiresOn: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
}

// makeRoom keeps the preset's record able to take the token an on-demand mint
// is about to create. A record with an expired token, or at recordSweepAt, is
// swept first: the expired tokens are deleted in Cloudflare and only then
// forgotten, as the schedule's sweep does. A record that still cannot take one
// more entry refuses the mint (record_full) before Cloudflare creates a token,
// which could not be recorded and would be deleted at once.
func (m *Minter) makeRoom(ctx context.Context, preset string, p config.CloudflarePreset, now time.Time) error {
	recorded, err := m.Recorded(ctx, preset)
	if err != nil {
		return &storeError{err: err}
	}
	expired := func(t secretstore.CloudflareMintedToken) bool {
		return t.ID != p.Prototype && !t.ExpiresOn.After(now)
	}
	if len(recorded) >= recordSweepAt || slices.ContainsFunc(recorded, expired) {
		if _, serr := m.sweep(ctx, preset, p, "", now); serr != nil {
			// What could not be deleted stays recorded for the next sweep; the
			// check below still holds the record to its size.
			m.log.WarnContext(ctx, "the expired Cloudflare tokens could not all be deleted before an on-demand mint",
				logattr.SafeString("preset", preset), logattr.SafeError("error", serr))
		}
		if recorded, err = m.Recorded(ctx, preset); err != nil {
			return &storeError{err: err}
		}
	}
	if !recordFits(recorded) {
		return &limitError{reasonRecordFull, fmt.Sprintf(
			"the record of the preset's minted tokens holds %d and has no room for another until they expire and are deleted", len(recorded))}
	}
	return nil
}

// recordFits is whether the record can take one more entry in every store.
func recordFits(tokens []secretstore.CloudflareMintedToken) bool {
	b, err := json.Marshal(secretstore.CloudflareMinted{Tokens: append(slices.Clone(tokens), recordWorstEntry)})
	return err == nil && len(b) <= recordMaxBytes
}

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
