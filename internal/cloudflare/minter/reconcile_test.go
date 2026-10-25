//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
)

func recordedIDs(t *testing.T, e *env, preset string) []string {
	t.Helper()
	rest, err := e.m.Recorded(ctx, preset)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rest {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids
}

// The caps are counted from the record: a mint never lists the account, a
// Cloudflare whose list fails does not stop one, and another replica, with
// nothing in memory, counts what the first minted.
func TestTheCapsAreCountedFromTheRecordWithoutListing(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 2; p.MaxMintsPerCallerPerHour = 3 })
	e.api.listErr = errors.New("listing is slow and down")
	for range 2 {
		e.advance(time.Second)
		if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	other := e.replica()
	e.advance(time.Second)
	if _, err := other.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("another replica minted a third live token for one caller: %v", err)
	}
	if got := lastReason(t, e); !strings.Contains(got, "caller_cap") {
		t.Errorf("the refusal does not say why: %s", got)
	}
	if lists := e.listCount(); lists != 0 {
		t.Errorf("the mints listed the account's tokens %d times, want none", lists)
	}
}

// The hourly rate is counted from the record too: a replica that minted nothing
// refuses the caller's third mint of the hour.
func TestTheRateIsCountedFromTheRecordAcrossReplicas(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxMintsPerCallerPerHour = 2 })
	for range 2 {
		e.advance(time.Second)
		if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), 10*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(time.Second)
	if _, err := e.replica().MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("a third mint in the hour on another replica: %v", err)
	}
	if got := lastReason(t, e); !strings.Contains(got, "rate_limited") {
		t.Errorf("the refusal does not say why: %s", got)
	}
	if lists := e.listCount(); lists != 0 {
		t.Errorf("the mints listed the account's tokens %d times, want none", lists)
	}
}

// A token created in Cloudflare and never recorded (the process died between
// the two, or the record could not be written and the token not deleted) is
// uncounted only until the next pass, which records it; it then counts toward
// the caps, and is swept when it expires.
func TestThePassRecoversATokenCreatedAndNeverRecorded(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 1 })
	e.faults.put.Store(true)
	e.api.deleteErr = errors.New("cloudflare is down")
	_, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), 10*time.Minute)
	if err == nil || errors.Is(err, minter.ErrLimit) {
		t.Fatalf("the mint whose record failed: %v", err)
	}
	e.faults.put.Store(false)
	e.api.deleteErr = nil
	orphan := "tok001"
	if _, ok := e.api.names()[orphan]; !ok {
		t.Fatal("fixture: the orphan is not in Cloudflare")
	}
	if ids := recordedIDs(t, e, "adhoc"); len(ids) != 0 {
		t.Fatalf("fixture: recorded %v", ids)
	}

	e.advance(2 * time.Minute)
	if res := e.m.TickPreset(ctx, "adhoc"); res.Err != nil {
		t.Fatalf("pass: %+v", res)
	}
	rest, _ := e.m.Recorded(ctx, "adhoc")
	if len(rest) != 1 || rest[0].ID != orphan || rest[0].Caller == "" || rest[0].Minted == 0 {
		t.Fatalf("record after the pass = %+v, want the orphan with its caller", rest)
	}
	if _, err = e.replica().MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("the orphan does not count toward the caller's cap: %v", err)
	}
	e.advance(10 * time.Minute)
	res := e.m.TickPreset(ctx, "adhoc")
	if res.Err != nil || !slices.Equal(res.Swept, []string{orphan}) {
		t.Fatalf("the pass after it expired = %+v, want it swept", res)
	}
	if _, ok := e.api.names()[orphan]; ok {
		t.Error("the orphan is still in Cloudflare")
	}
}

// The pass records the tokens Cloudflare has that the record lacks, forgets
// the entries for tokens Cloudflare no longer has, and leaves alone a token
// too young to have been recorded yet, an expired entry (the sweep's), and
// anything not named for the preset.
func TestThePassReconcilesExtraAndMissingTokens(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 1 })
	kept, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := e.m.MintFor(ctx, "adhoc", asker("b@example.com"), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Deleted in the dashboard, behind sluis's back.
	if err = e.api.DeleteToken(ctx, deleted.TokenID); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Minute)
	minted := e.clock.Add(-90 * time.Second)
	e.api.put(cloudflare.Token{ID: "extra", Name: cloudflare.OnDemandName("example", "adhoc", "c@example.com", minted), Status: cloudflare.StatusActive, ExpiresOn: e.clock.Add(time.Hour)})
	e.api.put(cloudflare.Token{ID: "sibling", Name: cloudflare.OnDemandName("example", "dns", "c@example.com", minted), Status: cloudflare.StatusActive, ExpiresOn: e.clock.Add(time.Hour)})
	e.api.put(cloudflare.Token{ID: "foreign", Name: "ci-deploy-key", Status: cloudflare.StatusActive, ExpiresOn: e.clock.Add(time.Hour)})

	// Before the pass, the extra token is not counted: c may mint.
	if _, err = e.replica().MintFor(ctx, "adhoc", asker("c@example.com"), time.Minute); err != nil {
		t.Fatalf("before the pass: %v", err)
	}
	e.advance(2 * time.Minute) // c's minute token expires
	e.api.put(cloudflare.Token{ID: "young", Name: cloudflare.OnDemandName("example", "adhoc", "d@example.com", *e.clock), Status: cloudflare.StatusActive, ExpiresOn: e.clock.Add(time.Hour)})
	if res := e.m.TickPreset(ctx, "adhoc"); res.Err != nil {
		t.Fatalf("pass: %+v", res)
	}
	got := recordedIDs(t, e, "adhoc")
	want := []string{"extra", kept.TokenID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("record after the pass = %v, want %v (the expired one swept, the deleted one forgotten, the young one left to its mint)", got, want)
	}
	rest, _ := e.m.Recorded(ctx, "adhoc")
	for _, r := range rest {
		if r.ID == "extra" && (r.Minted != minted.Unix() || r.Caller == "" || !r.ExpiresOn.Equal(e.clock.Add(-2*time.Minute).Add(time.Hour))) {
			t.Errorf("the extra token is recorded as %+v", r)
		}
	}
	// Now c's extra token counts: the cap is one.
	if _, err = e.replica().MintFor(ctx, "adhoc", asker("c@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("after the pass the extra token does not count: %v", err)
	}
	// b's deleted token no longer does, in the process that minted it either.
	if _, err = e.m.MintFor(ctx, "adhoc", asker("b@example.com"), time.Minute); err != nil {
		t.Fatalf("a token deleted in Cloudflare still counts: %v", err)
	}
}

// The pass reconciles at most once every ReconcileEvery, whichever replica
// runs it: the time is in the record.
func TestThePassReconcilesOnceAnInterval(t *testing.T) {
	e := onDemand(t)
	e.m.TickPreset(ctx, "adhoc")
	if n := e.listCount(); n != 1 {
		t.Fatalf("the first pass listed %d times, want 1", n)
	}
	e.advance(minter.ReconcileEvery - time.Minute)
	e.replica().TickPreset(ctx, "adhoc")
	if n := e.listCount(); n != 1 {
		t.Fatalf("a pass inside the interval listed: %d", n)
	}
	e.advance(time.Minute)
	e.replica().TickPreset(ctx, "adhoc")
	if n := e.listCount(); n != 2 {
		t.Fatalf("a pass after the interval listed %d times in all, want 2", n)
	}
	// A list that fails is the pass's error, and the next pass tries again.
	e.advance(minter.ReconcileEvery)
	e.api.listErr = errors.New("cloudflare is down")
	if res := e.m.TickPreset(ctx, "adhoc"); res.Err == nil || !strings.Contains(res.Err.Error(), "reconcile") {
		t.Fatalf("a failed reconcile: %+v", res)
	}
	e.api.listErr = nil
	e.m.TickPreset(ctx, "adhoc")
	if n := e.listCount(); n != 4 {
		t.Fatalf("the pass after a failed one listed %d times in all, want 4", n)
	}
}

// An entry written before entries carried a caller is filled in from the
// token's name by the pass, and counts from then on.
func TestThePassFillsInTheCallerOfAnOlderEntry(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 1 })
	expires := e.clock.Add(time.Hour)
	e.api.put(cloudflare.Token{ID: "older", Name: cloudflare.OnDemandName("example", "adhoc", "a@example.com", e.clock.Add(-time.Minute)), Status: cloudflare.StatusActive, ExpiresOn: expires})
	if _, err := e.v5.Cloudflare().Minted("adhoc").Put(ctx, secretstore.CloudflareMinted{Tokens: []secretstore.CloudflareMintedToken{{ID: "older", ExpiresOn: expires}}}, ""); err != nil {
		t.Fatal(err)
	}
	if res := e.m.TickPreset(ctx, "adhoc"); res.Err != nil {
		t.Fatalf("pass: %+v", res)
	}
	rest, _ := e.m.Recorded(ctx, "adhoc")
	if len(rest) != 1 || rest[0].Caller == "" || rest[0].Minted == 0 {
		t.Fatalf("record = %+v, want the caller filled in", rest)
	}
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("the older entry does not count: %v", err)
	}
	if _, err := e.m.MintFor(ctx, "adhoc", asker("b@example.com"), time.Minute); err != nil {
		t.Fatalf("another caller: %v", err)
	}
}
