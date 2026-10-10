//nolint:lll // messages and fixtures are prose and one-line tables
package minter_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
)

func TestTheConsoleSeesAccountsAndPrototypes(t *testing.T) {
	e := setup(t)
	accounts := e.m.Accounts()
	if len(accounts) != 1 || accounts[0].Name != "main" || accounts[0].ID != acct {
		t.Fatalf("accounts = %+v", accounts)
	}
	if id, ok := e.m.PrototypeID("dns"); !ok || id != dnsProto {
		t.Fatalf("prototype = %q %v", id, ok)
	}
	if err := e.m.CheckPreset(ctx, "dns"); err != nil {
		t.Fatalf("a disabled prototype is usable: %v", err)
	}
	if err := e.m.CheckPreset(ctx, "absent"); !errors.Is(err, minter.ErrUnknownPreset) {
		t.Fatalf("unknown: %v", err)
	}
	e.api.put(cloudflare.Token{ID: dnsProto, Name: "proto dns", Status: cloudflare.StatusActive, Policies: policiesJSON("g-dns")})
	pe, ok := cloudflare.IsPrototypeError(e.m.CheckPreset(ctx, "dns"))
	if !ok || pe.Reason != cloudflare.ReasonPrototypeActive {
		t.Fatalf("an active prototype: %v", pe)
	}
}

func TestStoredDescribesTheStoredTokenWithoutItsValue(t *testing.T) {
	e := setup(t)
	if got, err := e.m.Stored(ctx, "dns"); err != nil || got.Present {
		t.Fatalf("nothing stored yet: %+v %v", got, err)
	}
	e.m.TickPreset(ctx, "dns")
	e.m.TickPreset(ctx, "r2")
	got, err := e.m.Stored(ctx, "dns")
	if err != nil || !got.Present {
		t.Fatalf("stored = %+v %v", got, err)
	}
	if !got.ExpiresOn.Equal(e.clock.Add(15*time.Minute)) || !got.MintedAt.Equal(*e.clock) {
		t.Errorf("minted %v expires %v", got.MintedAt, got.ExpiresOn)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{got.AccessKeyID}, "")), "value") {
		t.Error("the description carries a value")
	}
	r2, err := e.m.Stored(ctx, "r2")
	if err != nil || r2.AccessKeyID == "" {
		t.Fatalf("an R2 preset's stored token is known by its access key id: %+v %v", r2, err)
	}
	if _, err = e.m.Stored(ctx, "absent"); !errors.Is(err, minter.ErrUnknownPreset) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestLiveNamesWhoATokenWasMintedFor(t *testing.T) {
	e := setup(t, config.CloudflareGrant{Group: "g", Presets: []string{"dns"}})
	e.m.TickPreset(ctx, "dns")
	if _, err := e.m.MintFor(ctx, "dns", minter.Caller{Actor: audit.Person("ada@example.com"), Groups: []string{"g"}}, 0); err != nil {
		t.Fatal(err)
	}
	live, err := e.m.Live(ctx, "dns")
	if err != nil || len(live) != 2 {
		t.Fatalf("live = %+v %v", live, err)
	}
	var onDemand, stored int
	for _, l := range live {
		switch {
		case l.Stored && l.Caller == "":
			stored++
		case !l.Stored && l.Caller == "ada@example.com" && l.MintedAt.Equal(*e.clock):
			onDemand++
		}
	}
	if stored != 1 || onDemand != 1 {
		t.Fatalf("live = %+v", live)
	}
}

func TestRotateNowMintsBeforeItIsDueAndAttributesIt(t *testing.T) {
	e := setup(t)
	e.m.TickPreset(ctx, "dns") // tok001, fresh
	e.advance(time.Minute)     // well before rotation (5m)
	actor := audit.Person("admin@example.com")
	minted, err := e.m.RotateNow(ctx, "dns", actor)
	if err != nil {
		t.Fatal(err)
	}
	if minted.TokenID != "tok002" {
		t.Fatalf("minted %s, want a new token though the stored one was not due", minted.TokenID)
	}
	if doc, _, _ := e.external("dns").Get(ctx); doc.Token != "value-tok002" {
		t.Errorf("stored = %+v", doc)
	}
	recs := e.rec.Find("roster.cloudflare.token.minted")
	last := recs[len(recs)-1]
	if !strings.Contains(last.String(), "admin@example.com") {
		t.Errorf("the mint is not attributed to the operator: %v", last)
	}
	if _, err = e.m.RotateNow(ctx, "absent", actor); !errors.Is(err, minter.ErrUnknownPreset) {
		t.Errorf("unknown: %v", err)
	}
	// Rotating a preset with nothing stored mints the first one.
	if _, err = e.m.RotateNow(ctx, "r2", actor); err != nil {
		t.Errorf("first rotation: %v", err)
	}
}

func TestRotateNowRefusesAnActivePrototype(t *testing.T) {
	e := setup(t)
	e.api.put(cloudflare.Token{ID: dnsProto, Name: "proto dns", Status: cloudflare.StatusActive, Policies: policiesJSON("g-dns")})
	_, err := e.m.RotateNow(ctx, "dns", audit.Person("admin@example.com"))
	if _, ok := cloudflare.IsPrototypeError(err); !ok {
		t.Fatalf("got %v", err)
	}
	if e.api.creates != 0 {
		t.Error("a token was created from an active prototype")
	}
}
