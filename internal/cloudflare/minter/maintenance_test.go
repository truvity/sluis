package minter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/port/memory"
)

// While the module is under maintenance it mints nothing, rotates nothing,
// revokes nothing and sweeps nothing, and says so by name; once the flag is
// lifted it does all of it again.
func TestTheMinterRefusesEveryWriteUnderMaintenance(t *testing.T) {
	flags := memory.New()
	gate := maintenance.New(flags, maintenance.WithTTL(time.Nanosecond))
	e := setupPaused(t, gate, config.CloudflareGrant{Group: "g", Presets: []string{"dns"}})
	person := minter.Caller{Actor: audit.Person("u@example.com"), Groups: []string{"g"}}

	if err := maintenance.Write(ctx, flags, maintenance.Flag{State: maintenance.StateRestoring, By: "restore"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)

	if _, err := e.m.MintFor(ctx, "dns", person, time.Minute); !errors.Is(err, minter.ErrMaintenance) {
		t.Errorf("MintFor = %v, want ErrMaintenance", err)
	}
	if _, err := e.m.RotateNow(ctx, "dns", person.Actor); !errors.Is(err, minter.ErrMaintenance) {
		t.Errorf("RotateNow = %v, want ErrMaintenance", err)
	}
	if _, err := e.m.Revoke(ctx, "dns", "tok001", person.Actor); !errors.Is(err, minter.ErrMaintenance) {
		t.Errorf("Revoke = %v, want ErrMaintenance", err)
	}
	if _, err := e.m.Sweep(ctx, "dns"); !errors.Is(err, minter.ErrMaintenance) {
		t.Errorf("Sweep = %v, want ErrMaintenance", err)
	}
	if res := e.m.TickPreset(ctx, "dns"); res.Outcome != minter.OutcomeMaintenance || res.Err != nil {
		t.Errorf("TickPreset = %+v, want a skip that is not a failure", res)
	}
	if e.api.creates != 0 || e.api.deletes != 0 {
		t.Errorf("Cloudflare was written to under maintenance: %d creates, %d deletes", e.api.creates, e.api.deletes)
	}
	if got := e.m.Tick(ctx); got.Err() != nil || got.Failed() != 0 {
		t.Errorf("a pass under maintenance failed: %v", got.Err())
	}

	if err := maintenance.Clear(ctx, flags); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := e.m.MintFor(ctx, "dns", person, time.Minute); err != nil {
		t.Errorf("MintFor after maintenance: %v", err)
	}
	if res := e.m.TickPreset(ctx, "dns"); res.Outcome != minter.OutcomeRotated {
		t.Errorf("TickPreset after maintenance = %+v", res)
	}
}

// A flag that cannot be read refuses writes, with its own name.
func TestTheMinterRefusesWhenTheFlagCannotBeRead(t *testing.T) {
	e := setupPaused(t, unreadable{}, config.CloudflareGrant{Group: "g", Presets: []string{"dns"}})
	person := minter.Caller{Actor: audit.Person("u@example.com"), Groups: []string{"g"}}
	if _, err := e.m.MintFor(ctx, "dns", person, time.Minute); !errors.Is(err, minter.ErrMaintenanceUnknown) {
		t.Errorf("MintFor = %v, want ErrMaintenanceUnknown", err)
	}
}

type unreadable struct{}

func (unreadable) Writable(_ context.Context) error {
	return &maintenance.Error{Err: errors.New("store down")}
}
