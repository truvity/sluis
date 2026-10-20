//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
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

// seedRecord records n tokens of the adhoc preset expiring at expires, as if
// minted earlier and never swept; when inCloudflare, the fake account holds
// them too.
func seedRecord(t *testing.T, e *env, n int, expires time.Time, inCloudflare bool) []string {
	t.Helper()
	var doc secretstore.CloudflareMinted
	var ids []string
	for i := range n {
		id := fmt.Sprintf("old%029d", i)
		ids = append(ids, id)
		doc.Tokens = append(doc.Tokens, secretstore.CloudflareMintedToken{ID: id, ExpiresOn: expires})
		if inCloudflare {
			e.api.put(cloudflare.Token{ID: id, Name: cloudflare.OnDemandName("example", "adhoc", "ci@example.com", expires.Add(-time.Minute)),
				Status: cloudflare.StatusActive, ExpiresOn: expires})
		}
	}
	if _, err := e.v5.Cloudflare().Minted("adhoc").Put(ctx, doc, ""); err != nil {
		t.Fatal(err)
	}
	return ids
}

// A record grown past what the store holds, with no schedule to sweep it, is
// swept by the next on-demand mint: the expired tokens are deleted in
// Cloudflare, forgotten, and the new one is the only entry left.
func TestAnOnDemandMintSweepsTheExpiredTokensItsRecordHolds(t *testing.T) {
	e := onDemand(t)
	ids := seedRecord(t, e, 120, e.clock.Add(-time.Hour), true)
	got, err := e.m.MintFor(ctx, "adhoc", minter.Caller{Actor: audit.Person("user@example.com"), Groups: []string{"g"}}, time.Minute)
	if err != nil {
		t.Fatalf("the mint over a full record of expired tokens: %v", err)
	}
	names := e.api.names()
	for _, id := range ids {
		if _, ok := names[id]; ok {
			t.Fatalf("the expired token %s is still in Cloudflare", id)
		}
	}
	rest, err := e.m.Recorded(ctx, "adhoc")
	if err != nil || len(rest) != 1 || rest[0].ID != got.TokenID {
		t.Fatalf("recorded = %+v %v, want only %s", rest, err, got.TokenID)
	}
	if e.api.creates != 1 || e.api.deletes != len(ids) {
		t.Errorf("creates %d, deletes %d; want 1 and %d", e.api.creates, e.api.deletes, len(ids))
	}
	if len(e.rec.Find("roster.cloudflare.tokens.swept")) == 0 {
		t.Error("the sweep is not in the trail")
	}
}

// An expired token that Cloudflare would not delete stays recorded: the record
// never forgets a token that may still count toward the account.
func TestAnOnDemandMintKeepsWhatItCouldNotDelete(t *testing.T) {
	e := onDemand(t)
	seedRecord(t, e, 3, e.clock.Add(-time.Hour), true)
	e.api.deleteErr = errors.New("boom")
	if _, err := e.m.MintFor(ctx, "adhoc", minter.Caller{Actor: audit.Person("user@example.com"), Groups: []string{"g"}}, time.Minute); err != nil {
		t.Fatalf("a failed sweep failed the mint: %v", err)
	}
	if rest, _ := e.m.Recorded(ctx, "adhoc"); len(rest) != 4 {
		t.Fatalf("recorded %d, want the 3 undeleted and the new one", len(rest))
	}
}

// A record with no room for another token, none of it expired, refuses the
// mint before Cloudflare is asked to create anything.
func TestAFullRecordRefusesAnOnDemandMintBeforeCreatingAToken(t *testing.T) {
	e := onDemand(t)
	seedRecord(t, e, 110, e.clock.Add(time.Hour), false)
	_, err := e.m.MintFor(ctx, "adhoc", minter.Caller{Actor: audit.Person("user@example.com"), Groups: []string{"g"}}, time.Minute)
	if !errors.Is(err, minter.ErrLimit) {
		t.Fatalf("the mint over a full record: %v, want ErrLimit", err)
	}
	if e.api.creates != 0 {
		t.Errorf("a refused mint created %d tokens", e.api.creates)
	}
	refused := e.rec.Find("roster.cloudflare.token.refused")
	if len(refused) == 0 || !strings.Contains(refused[len(refused)-1].String(), "record_full") {
		t.Fatalf("the refusal does not say record_full: %v", refused)
	}
	if rest, _ := e.m.Recorded(ctx, "adhoc"); len(rest) != 110 {
		t.Errorf("recorded %d, want the 110 untouched", len(rest))
	}
}
