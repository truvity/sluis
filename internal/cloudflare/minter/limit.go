package minter

import (
	"fmt"
	"slices"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
)

// The reasons an on-demand mint is refused for a limit, as the audit record
// says them.
const (
	reasonCallerCap   = "caller_cap"
	reasonPresetCap   = "preset_cap"
	reasonRateLimited = "rate_limited"
	// reasonRecordFull is a record of minted tokens with no room for another
	// (see makeRoom).
	reasonRecordFull = "record_full"
)

// mintWindow is the span the per-caller mint rate is counted over.
const mintWindow = time.Hour

// limitError is a mint refused for a limit. It is an [ErrLimit].
type limitError struct{ reason, detail string }

func (e *limitError) Error() string {
	return ErrLimit.Error() + " (" + e.reason + ": " + e.detail + ")"
}
func (e *limitError) Is(target error) bool { return target == ErrLimit }

// reservation is an on-demand mint this process admitted: counted until its
// token expires, and deduplicated by id against the record once the token has
// one, so a mint between reading the record and writing it is never missed.
type reservation struct {
	caller  string // the caller's digest, as the record has it
	at      time.Time
	expires time.Time
	id      string // the token's id once it is created
}

// hold is an admitted mint's claim on the limits.
type hold struct {
	m      *Minter
	preset string
	key    string
	r      *reservation
}

// release gives the claim back: the mint failed and created nothing.
func (h *hold) release() {
	h.m.limitMu.Lock()
	defer h.m.limitMu.Unlock()
	h.m.reserved[h.preset] = slices.DeleteFunc(h.m.reserved[h.preset], func(r *reservation) bool { return r == h.r })
	if i := slices.Index(h.m.minting[h.key], h.r.at); i >= 0 {
		h.m.minting[h.key] = slices.Delete(h.m.minting[h.key], i, i+1)
	}
}

// created names the token the claim became, so it is counted once whether or
// not a record read shows it.
func (h *hold) created(id string, expires time.Time) {
	h.m.limitMu.Lock()
	defer h.m.limitMu.Unlock()
	h.r.id, h.r.expires = id, expires
}

// forget drops this process's reservations for tokens that are gone (revoked,
// or found missing by the pass), so they stop counting before they expire.
func (m *Minter) forget(preset string, ids ...string) {
	m.limitMu.Lock()
	defer m.limitMu.Unlock()
	m.reserved[preset] = slices.DeleteFunc(m.reserved[preset], func(r *reservation) bool { return r.id != "" && slices.Contains(ids, r.id) })
}

// admit holds an on-demand mint to the preset's limits: how many unexpired
// on-demand tokens the caller holds, how many the preset has, and how many the
// caller has been minted in the last hour. An account may hold only 500 tokens,
// and an expired one counts until it is deleted, so a caller in a loop could
// otherwise take the account's room from every other preset.
//
// The counts come from the preset's record of minted tokens (read by makeRoom
// for this mint), whose on-demand entries carry their caller's digest and when
// they were minted, so they hold across replicas and invocations without
// asking Cloudflare, whose list of the account took seconds a mint to read.
// To that it adds what this process has admitted and the record may not show
// yet, a token not yet created or created after the record was read,
// deduplicated by id; and, for the hourly rate, what this process minted and
// has since expired and been swept. Admission is one at a time, so concurrent requests in one process
// cannot each see room that one of them takes. Across Lambda containers or
// replicas the caps are soft: two mints that read the record before either
// writes it can both be admitted. A token Cloudflare has and the record lacks
// (one whose recording failed and whose deletion failed too) is counted from
// the next pass, which holds the record against Cloudflare's list (reconcile).
//
// It returns the claim, which the caller releases when the mint fails and
// names the token by when it succeeds.
func (m *Minter) admit(
	preset string, p config.CloudflarePreset, name string, recorded []secretstore.CloudflareMintedToken, now time.Time, lifetime time.Duration,
) (*hold, error) {
	callerName, _ := cloudflare.SplitName(name, m.cfg.Instance, preset)
	caller := callerDigest(callerName)
	key := preset + "\x00" + callerName

	m.limitMu.Lock()
	defer m.limitMu.Unlock()

	var total, mine, recentRecorded int
	seen := make(map[string]bool, len(recorded))
	for _, t := range recorded {
		seen[t.ID] = true
		// The stored token, and an entry an older release wrote before the pass
		// fills in its caller, carry none and are not on-demand ones.
		if t.Caller == "" || t.ID == p.Prototype {
			continue
		}
		if t.Caller == caller && t.Minted != 0 && now.Sub(time.Unix(t.Minted, 0)) < mintWindow {
			recentRecorded++
		}
		if !t.ExpiresOn.After(now) {
			continue
		}
		total++
		if t.Caller == caller {
			mine++
		}
	}
	live := m.reserved[preset][:0]
	for _, r := range m.reserved[preset] {
		if !r.expires.After(now) {
			continue
		}
		live = append(live, r)
		if r.id != "" && seen[r.id] {
			continue
		}
		total++
		if r.caller == caller {
			mine++
			if now.Sub(r.at) < mintWindow {
				recentRecorded++
			}
		}
	}
	m.reserved[preset] = live

	local := slices.DeleteFunc(m.minting[key], func(at time.Time) bool { return now.Sub(at) >= mintWindow })
	// A token this process minted that is still recorded is in both counts. The
	// larger is the better estimate of the hour's, and neither undercounts the
	// other's blind spot: the record forgets a token once it is swept, and this
	// process does not see the other replicas' mints.
	recent := max(len(local), recentRecorded)
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
	r := &reservation{caller: caller, at: now, expires: now.Add(lifetime)}
	m.reserved[preset] = append(m.reserved[preset], r)
	return &hold{m: m, preset: preset, key: key, r: r}, nil
}
