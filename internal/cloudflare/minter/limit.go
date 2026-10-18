package minter

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/config"
)

// The reasons an on-demand mint is refused for a limit, as the audit record
// says them.
const (
	reasonCallerCap   = "caller_cap"
	reasonPresetCap   = "preset_cap"
	reasonRateLimited = "rate_limited"
)

// mintWindow is the span the per-caller mint rate is counted over.
const mintWindow = time.Hour

// limitError is a mint refused for a limit. It is an [ErrLimit].
type limitError struct{ reason, detail string }

func (e *limitError) Error() string {
	return ErrLimit.Error() + " (" + e.reason + ": " + e.detail + ")"
}
func (e *limitError) Is(target error) bool { return target == ErrLimit }

// liveListTTL is how long the list of a preset's live tokens is reused. Cloudflare
// allows an account about 1200 API requests in 5 minutes, and a burst of mints
// would spend them on counting. A token this process mints in the meantime is
// added to the count, so a burst cannot overshoot a cap by the age of the list.
const liveListTTL = 10 * time.Second

// liveList is a preset's live tokens as listed at a moment, and the on-demand
// tokens this process admitted since.
type liveList struct {
	at     time.Time
	tokens []LiveToken
	added  []admitted
}

type admitted struct {
	caller  string
	expires time.Time
	at      time.Time
}

// admit holds an on-demand mint to the preset's limits: how many unexpired
// on-demand tokens the caller holds, how many the preset has, and how many the
// caller has been minted in the last hour. An account may hold only 500 tokens,
// and an expired one counts until it is deleted, so a caller in a loop could
// otherwise take the account's room from every other preset.
//
// The counts come from the names of the preset's tokens in Cloudflare (the
// caller and the time are in them), so they hold across replicas and across
// invocations, with nothing stored; and from what this process has admitted,
// which covers a token not yet created, one created since the list was read,
// and one that has expired within the hour. Admission is one at a time, so
// concurrent requests in one process cannot each see room that one of them
// takes. Across Lambda containers or replicas the caps are soft: each counts
// what Cloudflare listed, up to [liveListTTL] ago, and what it minted itself.
// A list that fails fails the mint: the limits are not skipped when they cannot
// be counted.
//
// It returns a function that gives the reservation back when the mint fails.
func (m *Minter) admit(
	ctx context.Context, preset string, p config.CloudflarePreset, name string, now time.Time, lifetime time.Duration,
) (release func(), err error) {
	caller, _ := cloudflare.SplitName(name, m.cfg.Instance, preset)
	key := preset + "\x00" + caller

	m.limitMu.Lock()
	defer m.limitMu.Unlock()

	cache := m.lists[preset]
	if cache == nil || now.Sub(cache.at) >= liveListTTL || now.Before(cache.at) {
		live, lerr := m.Live(ctx, preset)
		if lerr != nil {
			return nil, &cloudflareError{err: fmt.Errorf("count the preset's live tokens: %w", lerr)}
		}
		cache = &liveList{at: now, tokens: live}
		m.lists[preset] = cache
	}

	var total, mine, recentRemote int
	for _, t := range cache.tokens {
		if t.Stored || (!t.ExpiresOn.IsZero() && !t.ExpiresOn.After(now)) {
			continue
		}
		total++
		if t.Caller == caller {
			mine++
			if !t.MintedAt.IsZero() && now.Sub(t.MintedAt) < mintWindow {
				recentRemote++
			}
		}
	}
	for _, a := range cache.added {
		if !a.expires.After(now) {
			continue
		}
		total++
		if a.caller == caller {
			mine++
		}
	}

	local := slices.DeleteFunc(m.minting[key], func(at time.Time) bool { return now.Sub(at) >= mintWindow })
	// A token this process minted and Cloudflare lists is in both counts. The
	// larger is the better estimate of the hour's, and neither undercounts the
	// other's blind spot.
	recent := max(len(local), recentRemote)
	switch {
	case mine >= p.LivePerCaller():
		m.minting[key] = local
		return nil, &limitError{reasonCallerCap, fmt.Sprintf(
			"the caller holds %d live tokens of this preset, and the preset allows %d each", mine, p.LivePerCaller())}
	case total >= p.LiveTotal():
		m.minting[key] = local
		return nil, &limitError{reasonPresetCap, fmt.Sprintf(
			"the preset has %d live on-demand tokens, and allows %d", total, p.LiveTotal())}
	case recent >= p.MintsPerCallerPerHour():
		m.minting[key] = local
		return nil, &limitError{reasonRateLimited, fmt.Sprintf(
			"the caller has been minted %d tokens of this preset in the last hour, and the preset allows %d", recent, p.MintsPerCallerPerHour())}
	}
	m.minting[key] = append(local, now)
	entry := admitted{caller: caller, expires: now.Add(lifetime), at: now}
	cache.added = append(cache.added, entry)
	return func() {
		m.limitMu.Lock()
		defer m.limitMu.Unlock()
		if i := slices.Index(m.minting[key], now); i >= 0 {
			m.minting[key] = slices.Delete(m.minting[key], i, i+1)
		}
		if c := m.lists[preset]; c != nil {
			if i := slices.Index(c.added, entry); i >= 0 {
				c.added = slices.Delete(c.added, i, i+1)
			}
		}
	}, nil
}
