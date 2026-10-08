package minter_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare"
)

func TestTheProviderMintsRenewsWithAThirdLeftAndMintsAgainOn403(t *testing.T) {
	e := setup(t) // no grants, no tick: the provider needs neither
	p, err := e.m.Provider("r2")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := p.Retrieve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "tok001" || creds.SecretAccessKey != cloudflare.R2Secret("value-tok001") || !creds.CanExpire {
		t.Fatalf("creds = %+v", creds)
	}
	// Renewal is with a third of the 15m lifetime left: at 10m.
	if want := e.clock.Add(10 * time.Minute); !creds.Expires.Equal(want) {
		t.Errorf("renew at %v, want %v", creds.Expires, want)
	}
	got, _ := e.api.GetToken(ctx, "tok001")
	if got.Name != "sluis/example/r2/sluis/2026-10-08T12:00:00Z" {
		t.Errorf("name = %q", got.Name)
	}
	e.advance(9 * time.Minute)
	if _, err = p.Retrieve(ctx); err != nil || e.api.creates != 1 || !p.Valid() {
		t.Fatalf("before the renewal point: creates %d, %v", e.api.creates, err)
	}
	e.advance(2 * time.Minute)
	if p.Valid() {
		t.Error("valid past the renewal point")
	}
	if creds, err = p.Retrieve(ctx); err != nil || creds.AccessKeyID != "tok002" {
		t.Fatalf("renewed = %+v %v", creds, err)
	}
	// A 403: the consumer asks for new ones while these are valid.
	m, err := p.Remint(ctx)
	if err != nil || m.AccessKeyID != "tok003" || e.api.creates != 3 {
		t.Fatalf("remint = %+v %v creates %d", m, err, e.api.creates)
	}
	// It used the minter credential and no static document.
	if e.api.gotMinter != "minter-secret" {
		t.Errorf("opened with %q", e.api.gotMinter)
	}
	// The sweep of the preset takes the provider's expired tokens too.
	e.advance(time.Hour)
	if swept, _ := e.m.Sweep(ctx, "r2"); len(swept) != 3 {
		t.Errorf("swept = %v", swept)
	}
}

func TestTheProviderRefusesAPresetThatIsNotR2AndReportsAFailureWithoutTheValue(t *testing.T) {
	e := setup(t)
	if _, err := e.m.Provider("dns"); err == nil || !strings.Contains(err.Error(), "R2") {
		t.Errorf("a token preset: %v", err)
	}
	if _, err := e.m.Provider("nope"); err == nil {
		t.Error("an unknown preset")
	}
	p, _ := e.m.Provider("r2")
	e.api.createErr = errors.New("denied")
	if _, err := p.Retrieve(ctx); err == nil || strings.Contains(err.Error(), "value-") {
		t.Errorf("failure: %v", err)
	}
	// An active prototype is refused for sluis itself as for anyone.
	e.api.createErr = nil
	proto, _ := e.api.GetToken(ctx, r2Proto)
	proto.Status = cloudflare.StatusActive
	e.api.put(proto)
	if _, err := p.Retrieve(ctx); err == nil {
		t.Error("an active prototype was cloned")
	}
}
