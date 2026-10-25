//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
)

// limited is a fixture with the on-demand preset's limits set small.
func limited(t *testing.T, change func(*config.CloudflarePreset)) *env {
	t.Helper()
	return setupWith(t, nil, func(cf *config.Cloudflare) {
		p := config.CloudflarePreset{Account: "main", Prototype: dnsProto, Description: "Ad hoc DNS",
			Lifetime: config.Duration(15 * time.Minute), Store: config.CloudflareStoreNone}
		change(&p)
		cf.Presets["adhoc"] = p
	}, config.CloudflareGrant{Group: "g", Presets: []string{"adhoc"}})
}

func asker(who string) minter.Caller {
	return minter.Caller{Actor: audit.Person(who), Groups: []string{"g"}}
}

func lastReason(t *testing.T, e *env) string {
	t.Helper()
	refused := e.rec.Find("roster.cloudflare.token.refused")
	if len(refused) == 0 {
		t.Fatal("no refusal was recorded")
	}
	return refused[len(refused)-1].String()
}

// One caller cannot hold more than the preset allows; another is not affected by
// it, and the refusal is in the trail with its reason.
func TestACallerCannotHoldMoreLiveTokensThanThePresetAllows(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 2 })
	for range 2 {
		e.advance(time.Second)
		if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(time.Second)
	creates := e.api.creates
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("third token for one caller: %v", err)
	}
	if e.api.creates != creates {
		t.Error("a refused mint created a token")
	}
	if got := lastReason(t, e); !strings.Contains(got, "caller_cap") {
		t.Errorf("the refusal does not say why: %s", got)
	}
	if _, err := e.m.MintFor(ctx, "adhoc", asker("b@example.com"), time.Minute); err != nil {
		t.Fatalf("another caller was refused for the first one's tokens: %v", err)
	}
	// An expired token no longer counts.
	e.advance(2 * time.Minute)
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
		t.Fatalf("after the tokens expired: %v", err)
	}
}

// The preset as a whole is capped, whoever holds the tokens.
func TestAPresetCannotHaveMoreLiveTokensThanItAllows(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLive = 2 })
	for _, who := range []string{"a@example.com", "b@example.com"} {
		e.advance(time.Second)
		if _, err := e.m.MintFor(ctx, "adhoc", asker(who), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(time.Second)
	if _, err := e.m.MintFor(ctx, "adhoc", asker("c@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("a third caller over the preset's cap: %v", err)
	}
	if got := lastReason(t, e); !strings.Contains(got, "preset_cap") {
		t.Errorf("the refusal does not say why: %s", got)
	}
}

// A token that expires inside the hour does not reset the rate: this process
// remembers what it minted.
func TestACallerIsRateLimitedAcrossExpiredTokens(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxMintsPerCallerPerHour = 3 })
	for range 3 {
		e.advance(2 * time.Minute) // each token has expired by the next mint
		if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(2 * time.Minute)
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("a fourth mint in the hour: %v", err)
	}
	if got := lastReason(t, e); !strings.Contains(got, "rate_limited") {
		t.Errorf("the refusal does not say why: %s", got)
	}
	e.advance(time.Hour)
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
		t.Fatalf("after the hour: %v", err)
	}
}

// A mint that fails gives its reservation back: a failing Cloudflare does not
// spend the caller's hour.
func TestAFailedMintDoesNotSpendTheRate(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxMintsPerCallerPerHour = 1 })
	e.api.createErr = errors.New("boom")
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err == nil || errors.Is(err, minter.ErrLimit) {
		t.Fatalf("the failing mint: %v", err)
	}
	e.api.createErr = nil
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
		t.Fatalf("the retry was refused for a mint that never happened: %v", err)
	}
}

// With nothing set, the defaults hold: ten live tokens a caller.
func TestTheDefaultLimitsApply(t *testing.T) {
	e := limited(t, func(*config.CloudflarePreset) {})
	for range config.DefaultCloudflareMaxLivePerCaller {
		e.advance(time.Second)
		if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	e.advance(time.Second)
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("over the default cap: %v", err)
	}
	// The stored token of a preset is not an on-demand one and is not counted.
	if cloudflare.IsStored("sluis/example/adhoc/2026-10-08T12:00:00Z", "example", "adhoc") != true {
		t.Fatal("fixture: stored name not recognised")
	}
}

// An allowlist on the preset refuses a prototype that grants more than it names,
// at the mint, and the floor is still the floor.
func TestAPresetsAllowedPermissionGroupsHoldThePrototype(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.AllowedPermissionGroups = []string{"Workers R2 Storage Write"} })
	_, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute)
	if pe, ok := cloudflare.IsPrototypeError(err); !ok || pe.Reason != cloudflare.ReasonPrototypeForbidden || !strings.Contains(pe.Detail, "DNS Write") {
		t.Fatalf("a prototype outside the allowlist: %v", err)
	}
	e = limited(t, func(p *config.CloudflarePreset) { p.AllowedPermissionGroups = []string{"dns write"} })
	if _, err = e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err != nil {
		t.Fatalf("a prototype inside the allowlist: %v", err)
	}
	if err = e.m.CheckPreset(ctx, "adhoc"); err != nil {
		t.Fatalf("CheckPreset: %v", err)
	}
}

// Concurrent requests in one process cannot each see room that one of them
// takes: with a cap of three, exactly three of thirty succeed, counted from the
// record and the process's reservations, and the burst never lists the
// account's tokens.
func TestAConcurrentBurstCannotOvershootACap(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLivePerCaller = 3 })
	var wg sync.WaitGroup
	var ok, limit atomic.Int32
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, minter.ErrLimit):
				limit.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 3 || limit.Load() != 27 {
		t.Fatalf("%d minted and %d refused, want 3 and 27", ok.Load(), limit.Load())
	}
	if lists := e.listCount(); lists != 0 {
		t.Errorf("the burst listed the account's tokens %d times, want none", lists)
	}
	if rest, _ := e.m.Recorded(ctx, "adhoc"); len(rest) != 3 {
		t.Errorf("recorded %d, want the 3 minted", len(rest))
	}
}

// The same holds for the preset's cap across callers.
func TestAConcurrentBurstOfCallersCannotOvershootThePresetsCap(t *testing.T) {
	e := limited(t, func(p *config.CloudflarePreset) { p.MaxLive = 4 })
	var wg sync.WaitGroup
	var ok, limit atomic.Int32
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.m.MintFor(ctx, "adhoc", asker(fmt.Sprintf("c%d@example.com", i)), time.Minute)
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, minter.ErrLimit):
				limit.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 4 || limit.Load() != 16 {
		t.Fatalf("%d minted and %d refused, want 4 and 16", ok.Load(), limit.Load())
	}
}

// A record that cannot be read fails the mint: a cap that cannot be counted is
// not skipped.
func TestAMintFailsClosedWhenTheTokensCannotBeCounted(t *testing.T) {
	e := limited(t, func(*config.CloudflarePreset) {})
	e.faults.get.Store(true)
	creates := e.api.creates
	if _, err := e.m.MintFor(ctx, "adhoc", asker("a@example.com"), time.Minute); err == nil || errors.Is(err, minter.ErrLimit) {
		t.Fatalf("a mint with no count: %v", err)
	}
	if e.api.creates != creates {
		t.Error("a token was created without being counted")
	}
}
