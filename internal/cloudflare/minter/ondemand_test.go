//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/storage/state"
)

// onDemand adds a `store: none` preset beside the fixture's stored ones.
func onDemand(t *testing.T) *env {
	t.Helper()
	return setupWith(t, nil, func(cf *config.Cloudflare) {
		cf.Presets["adhoc"] = config.CloudflarePreset{Account: "main", Prototype: dnsProto, Description: "Ad hoc DNS",
			Lifetime: config.Duration(15 * time.Minute), Store: config.CloudflareStoreNone}
	}, config.CloudflareGrant{Group: "g", Presets: []string{"adhoc", "dns"}})
}

func TestAnOnDemandPresetIsNeverTickedMintedOrStored(t *testing.T) {
	e := onDemand(t)
	res := e.m.TickPreset(ctx, "adhoc")
	if res.Err != nil || res.Outcome != minter.OutcomeOnDemand || e.api.creates != 0 {
		t.Fatalf("tick = %+v, creates %d", res, e.api.creates)
	}
	if _, _, err := e.external("adhoc").Get(ctx); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("something is stored for a store: none preset: %v", err)
	}
	// The pass over every preset ticks the stored ones and skips this one.
	all := e.m.Tick(ctx)
	if all.Err() != nil || e.api.creates != 3 {
		t.Fatalf("tick = %s, creates %d (want dns, dns-edge and r2 only)", all.Summary(), e.api.creates)
	}
	for _, p := range all.Presets {
		if p.Preset == "adhoc" && p.Outcome != minter.OutcomeOnDemand {
			t.Errorf("adhoc outcome = %s", p.Outcome)
		}
	}
	if _, _, err := e.external("adhoc").Get(ctx); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("something is stored for a store: none preset after a full tick: %v", err)
	}
	if _, err := e.m.RotateNow(ctx, "adhoc", audit.Person("op@example.com")); !errors.Is(err, minter.ErrOnDemandOnly) {
		t.Errorf("RotateNow = %v, want ErrOnDemandOnly", err)
	}
	if info, _ := e.m.Preset("adhoc"); info.Store != "none" {
		t.Errorf("store = %q", info.Store)
	}
	if info, _ := e.m.Preset("dns"); info.Store != "" {
		t.Errorf("default store = %q, want empty", info.Store)
	}
}

func TestAnOnDemandPresetIsMintedForAGrantedCallerAndSweptWhenItExpires(t *testing.T) {
	e := onDemand(t)
	person := minter.Caller{Actor: audit.Person("user@example.com"), Groups: []string{"g"}}
	got, err := e.m.MintFor(ctx, "adhoc", person, time.Minute)
	if err != nil || got.Name != "sluis/example/adhoc/user@example.com/2026-10-08T12:00:00Z" || got.Token == "" {
		t.Fatalf("minted = %+v %v", got, err)
	}
	if _, err = e.m.MintFor(ctx, "adhoc", minter.Caller{Actor: audit.Person("x@example.com"), Groups: []string{"other"}}, 0); !errors.Is(err, minter.ErrNotGranted) {
		t.Errorf("an ungranted caller: %v", err)
	}
	if _, _, gerr := e.external("adhoc").Get(ctx); !errors.Is(gerr, state.ErrNotFound) {
		t.Fatalf("a mint on demand stored something: %v", gerr)
	}
	e.advance(2 * time.Minute)
	res := e.m.TickPreset(ctx, "adhoc")
	if res.Err != nil || len(res.Swept) != 1 || res.Swept[0] != got.TokenID {
		t.Fatalf("the tick swept %+v, want %s", res, got.TokenID)
	}
	if e.api.creates != 1 {
		t.Errorf("the tick minted: creates %d", e.api.creates)
	}
}

func TestAnExternalPresetIsUnchangedByTheStoreField(t *testing.T) {
	e := setupWith(t, nil, func(cf *config.Cloudflare) {
		p := cf.Presets["dns"]
		p.Store = config.CloudflareStoreExternal
		cf.Presets["dns"] = p
	})
	if res := e.m.TickPreset(ctx, "dns"); res.Err != nil || res.Outcome != minter.OutcomeRotated {
		t.Fatalf("tick = %+v", res)
	}
	doc, _, err := e.external("dns").Get(ctx)
	if err != nil || doc.Token != "value-tok001" {
		t.Fatalf("stored = %+v %v", doc, err)
	}
}
