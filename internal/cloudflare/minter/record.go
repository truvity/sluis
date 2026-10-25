package minter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/truvity/sluis/storage/logattr"

	"github.com/truvity/sluis/internal/cloudflare"
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
//
// The record is also what the on-demand caps count (see admit): an on-demand
// entry carries a digest of its caller and when it was minted. The pass holds
// it against Cloudflare's list (see reconcile), which catches a token created
// and never recorded, and an entry for a token deleted behind sluis's back.

const recordRetries = 6

// recordSweepAt is the number of recorded tokens at which an on-demand mint
// sweeps the preset first even when none has expired yet by the record: a
// record that size is one the schedule is not keeping short.
const recordSweepAt = 64

// recordMaxBytes is the largest record the smallest store keeps: an
// advanced-tier SSM parameter holds 8 KiB, about 104 stored tokens or 67
// on-demand ones.
const recordMaxBytes = 8 << 10

// callerDigestLen is the length of an entry's caller digest, in hex: 48 bits,
// which a handful of callers per preset never collide in.
const callerDigestLen = 12

// recordWorstEntry is the longest entry a mint adds: a token id, an expiry with
// nanoseconds, a caller digest and a mint time.
var recordWorstEntry = secretstore.CloudflareMintedToken{
	ID: strings.Repeat("f", 32), ExpiresOn: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	Caller: strings.Repeat("f", callerDigestLen), Minted: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Unix(),
}

// callerDigest is what an on-demand entry records of its caller: a digest of the
// caller's segment of the token's name ([cloudflare.SplitName]), so the mint,
// the caps and the pass, which reads the name back from Cloudflare, agree.
func callerDigest(caller string) string {
	sum := sha256.Sum256([]byte(caller))
	return hex.EncodeToString(sum[:])[:callerDigestLen]
}

// entryFor is the record's entry for a token of the preset by its name: an
// on-demand one carries its caller's digest and its mint time from the name,
// a stored one (or a name that is not ours) only its id and expiry.
func (m *Minter) entryFor(preset, id, name string, expires time.Time) secretstore.CloudflareMintedToken {
	e := secretstore.CloudflareMintedToken{ID: id, ExpiresOn: expires.UTC()}
	if cloudflare.IsStored(name, m.cfg.Instance, preset) {
		return e
	}
	if caller, at := cloudflare.SplitName(name, m.cfg.Instance, preset); caller != "" && !at.IsZero() {
		e.Caller, e.Minted = callerDigest(caller), at.Unix()
	}
	return e
}

// makeRoom keeps the preset's record able to take the token an on-demand mint
// is about to create, and returns the record the mint's limits are counted
// from. A record with an expired token, or at recordSweepAt, is swept first:
// the expired tokens are deleted in Cloudflare and only then forgotten, as the
// schedule's sweep does. A record that still cannot take one more entry
// refuses the mint (record_full) before Cloudflare creates a token, which could
// not be recorded and would be deleted at once. A record that cannot be read
// fails the mint: the limits are not skipped when they cannot be counted.
func (m *Minter) makeRoom(ctx context.Context, preset string, p config.CloudflarePreset, now time.Time) ([]secretstore.CloudflareMintedToken, error) {
	doc, err := m.recordDoc(ctx, preset)
	if err != nil {
		return nil, &storeError{err: err}
	}
	expired := func(t secretstore.CloudflareMintedToken) bool {
		return t.ID != p.Prototype && !t.ExpiresOn.After(now)
	}
	if len(doc.Tokens) >= recordSweepAt || slices.ContainsFunc(doc.Tokens, expired) {
		if _, serr := m.sweep(ctx, preset, p, "", now); serr != nil {
			// What could not be deleted stays recorded for the next sweep; the
			// check below still holds the record to its size.
			m.log.WarnContext(ctx, "the expired Cloudflare tokens could not all be deleted before an on-demand mint",
				logattr.SafeString("preset", preset), logattr.SafeError("error", serr))
		}
		if doc, err = m.recordDoc(ctx, preset); err != nil {
			return nil, &storeError{err: err}
		}
	}
	if !recordFits(doc, recordWorstEntry) {
		return nil, &limitError{reasonRecordFull, fmt.Sprintf(
			"the record of the preset's minted tokens holds %d and has no room for another until they expire and are deleted", len(doc.Tokens))}
	}
	return doc.Tokens, nil
}

// recordFits is whether the record can take one more entry in every store.
func recordFits(doc secretstore.CloudflareMinted, entry secretstore.CloudflareMintedToken) bool {
	doc.Tokens = append(slices.Clone(doc.Tokens), entry)
	b, err := json.Marshal(doc)
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

// track records a token minted for the preset under its name.
func (m *Minter) track(ctx context.Context, preset, id, name string, expires time.Time) error {
	entry := m.entryFor(preset, id, name, expires)
	return m.updateRecord(ctx, preset, func(d *secretstore.CloudflareMinted) {
		if !slices.ContainsFunc(d.Tokens, func(t secretstore.CloudflareMintedToken) bool { return t.ID == id }) {
			d.Tokens = append(d.Tokens, entry)
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
	doc, err := m.recordDoc(ctx, preset)
	return doc.Tokens, err
}

// recordDoc is the preset's record; an absent one is empty.
func (m *Minter) recordDoc(ctx context.Context, preset string) (secretstore.CloudflareMinted, error) {
	doc, _, err := m.mintedDoc(preset).Get(ctx)
	if errors.Is(err, state.ErrNotFound) {
		return secretstore.CloudflareMinted{}, nil
	}
	if err != nil {
		return secretstore.CloudflareMinted{}, fmt.Errorf("read the record of minted tokens: %w", err)
	}
	return doc, nil
}
