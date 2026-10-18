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

// admit holds an on-demand mint to the preset's limits: how many unexpired
// on-demand tokens the caller holds, how many the preset has, and how many the
// caller has been minted in the last hour. An account may hold only 500 tokens,
// and an expired one counts until it is deleted, so a caller in a loop could
// otherwise take the account's room from every other preset.
//
// The counts come from the names of the preset's tokens in Cloudflare (the
// caller and the time are in them), so they hold across replicas and across
// invocations, with nothing stored. This process adds what it has minted in the
// hour itself, which covers a token that has already expired and one not yet
// created. It returns a function that gives the reservation back when the mint
// fails.
func (m *Minter) admit(ctx context.Context, preset string, p config.CloudflarePreset, name string, now time.Time) (release func(), err error) {
	caller, _ := cloudflare.SplitName(name, m.cfg.Instance, preset)
	live, err := m.Live(ctx, preset)
	if err != nil {
		return nil, &cloudflareError{err: fmt.Errorf("count the preset's live tokens: %w", err)}
	}
	var total, mine, recentRemote int
	for _, t := range live {
		if t.Stored {
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

	key := preset + "\x00" + caller
	m.mu.Lock()
	defer m.mu.Unlock()
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
		return nil, &limitError{reasonPresetCap, fmt.Sprintf("the preset has %d live on-demand tokens, and allows %d", total, p.LiveTotal())}
	case recent >= p.MintsPerCallerPerHour():
		m.minting[key] = local
		return nil, &limitError{reasonRateLimited, fmt.Sprintf(
			"the caller has been minted %d tokens of this preset in the last hour, and the preset allows %d", recent, p.MintsPerCallerPerHour())}
	}
	m.minting[key] = append(local, now)
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if i := slices.Index(m.minting[key], now); i >= 0 {
			m.minting[key] = slices.Delete(m.minting[key], i, i+1)
		}
	}, nil
}
