package minter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/truvity/sluis/storage/logattr"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
)

// ReconcileEvery is how often the tick holds a preset's record against the
// tokens Cloudflare lists. The on-demand caps are counted from the record, so
// between two passes they are as good as the record: a token whose recording
// failed, and whose deletion then failed too, is counted from the next one.
// The time is kept in the record, so it holds across replicas and invocations;
// the Lambda schedule's default 15 minutes reconciles at every pass.
const ReconcileEvery = 10 * time.Minute

// reconcileGrace is how old a token Cloudflare lists must be, by its name, to
// be called unrecorded: a younger one may be a mint that has not yet written
// the record.
const reconcileGrace = time.Minute

// The kinds of drift between the record and Cloudflare, as the metric labels
// them.
const (
	// driftUnrecorded is a token of the preset Cloudflare has and the record
	// lacked: one whose recording failed (and whose deletion did), or one
	// minted by a release that recorded nothing.
	driftUnrecorded = "unrecorded"
	// driftGone is an unexpired entry for a token Cloudflare no longer has:
	// deleted outside sluis.
	driftGone = "gone"
)

// reconcileIfDue reconciles the record of a preset the policy grants to anyone
// when it has not been for [ReconcileEvery]. A preset nobody may ask for has no
// on-demand caps to count, so its pass still costs no call to Cloudflare while
// its stored credential is fresh.
func (m *Minter) reconcileIfDue(ctx context.Context, preset string, p config.CloudflarePreset) error {
	if !m.granted(preset) {
		return nil
	}
	now := m.now()
	doc, err := m.recordDoc(ctx, preset)
	if err != nil {
		return err
	}
	if !doc.ReconciledAt.IsZero() && !now.Before(doc.ReconciledAt) && now.Sub(doc.ReconciledAt) < ReconcileEvery {
		return nil
	}
	return m.reconcile(ctx, preset, p, now)
}

// granted is whether any grant of the policy opens the preset.
func (m *Minter) granted(preset string) bool {
	return slices.ContainsFunc(m.cfg.Grants.Grants, func(g config.CloudflareGrant) bool { return slices.Contains(g.Presets, preset) })
}

// reconcile holds the preset's record against the account's list of tokens, the
// one place the minter still lists: a mint counts its caps from the record
// alone.
//
//   - A token of the preset (by its name) that Cloudflare lists and the record
//     lacks is recorded, with its caller and mint time from its name, so it
//     counts toward the caps and the sweep deletes it when it expires. A token
//     younger than [reconcileGrace] is left to the mint that is recording it.
//   - An unexpired entry Cloudflare does not list is asked about by id (the
//     list is read before the record, so an entry recorded since is not
//     missing): one Cloudflare does not have is forgotten. An expired entry is
//     left to the sweep, since Cloudflare hides an expired token from its list.
//   - An entry an older release wrote, with no caller, gets its caller and mint
//     time from the token's name.
//
// Each drift is logged and counted (sluis.cloudflare.record.drift).
func (m *Minter) reconcile(ctx context.Context, preset string, p config.CloudflarePreset, now time.Time) error {
	api, err := m.api(ctx, p.Account)
	if err != nil {
		return err
	}
	all, err := api.ListTokens(ctx)
	if err != nil {
		return fmt.Errorf("list the account's tokens: %w", err)
	}
	listed := map[string]cloudflare.Token{}
	for _, t := range all {
		if t.ID != p.Prototype && cloudflare.IsOwn(t.Name, m.cfg.Instance, preset) {
			listed[t.ID] = t
		}
	}
	doc, err := m.recordDoc(ctx, preset)
	if err != nil {
		return err
	}
	var errs []error
	names := map[string]string{} // a recorded id's name in Cloudflare
	var gone []string
	for _, t := range doc.Tokens {
		if t.ID == p.Prototype {
			continue
		}
		if l, ok := listed[t.ID]; ok {
			names[t.ID] = l.Name
			continue
		}
		if !t.ExpiresOn.After(now) {
			continue
		}
		tok, gerr := api.GetToken(ctx, t.ID)
		switch {
		case errors.Is(gerr, cloudflare.ErrNotFound):
			gone = append(gone, t.ID)
		case gerr != nil:
			errs = append(errs, fmt.Errorf("read %s: %w", t.ID, gerr))
		default:
			// Recorded since the list was read, or not ours by its name (the
			// sweep forgets that one when it expires, and deletes nothing).
			names[t.ID] = tok.Name
		}
	}
	recorded := map[string]bool{}
	for _, t := range doc.Tokens {
		recorded[t.ID] = true
	}
	var unrecorded []secretstore.CloudflareMintedToken
	for id, l := range listed {
		if recorded[id] {
			continue
		}
		if l.ExpiresOn.IsZero() {
			// sluis mints nothing without an expiry: a token named like ours
			// without one is not for the sweep to delete.
			m.log.WarnContext(ctx, "a Cloudflare token is named like the preset's but never expires; it is not recorded",
				logattr.SafeString("preset", preset), logattr.SafeString("token", id))
			continue
		}
		if _, at := cloudflare.SplitName(l.Name, m.cfg.Instance, preset); l.ExpiresOn.After(now) && !at.IsZero() && now.Sub(at) < reconcileGrace {
			continue
		}
		unrecorded = append(unrecorded, m.entryFor(preset, id, l.Name, l.ExpiresOn))
	}
	sort.Slice(unrecorded, func(i, j int) bool { return unrecorded[i].ID < unrecorded[j].ID })

	var added, full []string
	uerr := m.updateRecord(ctx, preset, func(d *secretstore.CloudflareMinted) {
		added, full = nil, nil
		// Set first, so the room counted below is the room the write has.
		d.ReconciledAt = now.UTC()
		d.Tokens = slices.DeleteFunc(d.Tokens, func(t secretstore.CloudflareMintedToken) bool { return slices.Contains(gone, t.ID) })
		for i, t := range d.Tokens {
			if n, ok := names[t.ID]; ok && t.Caller == "" {
				e := m.entryFor(preset, t.ID, n, t.ExpiresOn)
				d.Tokens[i].Caller, d.Tokens[i].Minted = e.Caller, e.Minted
			}
		}
		for _, u := range unrecorded {
			if slices.ContainsFunc(d.Tokens, func(t secretstore.CloudflareMintedToken) bool { return t.ID == u.ID }) {
				continue // the mint recorded it meanwhile
			}
			if !recordFits(*d, u) {
				full = append(full, u.ID)
				continue
			}
			d.Tokens = append(d.Tokens, u)
			added = append(added, u.ID)
		}
	})
	if uerr != nil {
		return errors.Join(append(errs, fmt.Errorf("update the record: %w", uerr))...)
	}
	m.forget(preset, gone...)
	meters.drift(ctx, preset, driftUnrecorded, len(added)+len(full))
	meters.drift(ctx, preset, driftGone, len(gone))
	if len(added) > 0 {
		m.log.WarnContext(ctx, "the record lacked tokens of the preset that Cloudflare has; they are recorded now, so they count and are swept",
			logattr.SafeString("preset", preset), logattr.SafeStrings("tokens", added))
	}
	if len(full) > 0 {
		m.log.WarnContext(ctx, "the record lacks tokens of the preset that Cloudflare has, and has no room for them; they are not counted toward the caps",
			logattr.SafeString("preset", preset), logattr.SafeStrings("tokens", full))
		errs = append(errs, fmt.Errorf("the record has no room for %d tokens Cloudflare has", len(full)))
	}
	if len(gone) > 0 {
		m.log.WarnContext(ctx, "the record held tokens of the preset that Cloudflare no longer has; they are forgotten",
			logattr.SafeString("preset", preset), logattr.SafeStrings("tokens", gone))
	}
	return errors.Join(errs...)
}
