package rpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare"
	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/modcall"
)

// fake is the minter's answers, and records what it was asked.
type fake struct {
	err      error
	preset   string
	caller   rpc.Caller
	lifetime time.Duration
}

func (f *fake) MintFor(_ context.Context, preset string, c rpc.Caller, lifetime time.Duration) (*rpc.Minted, error) {
	f.preset, f.caller, f.lifetime = preset, c, lifetime
	if f.err != nil {
		return nil, f.err
	}
	return &rpc.Minted{Preset: preset, R2: true, AccessKeyID: "id", SecretAccessKey: "secret", Endpoint: "https://r2.example",
		ExpiresOn: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)}, nil
}

func (f *fake) Granted(c rpc.Caller) []rpc.PresetInfo {
	if len(c.Groups) == 0 {
		return nil
	}
	return []rpc.PresetInfo{{Name: "dns", Lifetime: time.Hour}}
}

// The rest of what the module holds that the console's methods read.

func (f *fake) Accounts() []rpc.AccountInfo {
	return []rpc.AccountInfo{{Name: "main", ID: "0123456789abcdef0123456789abcdef"}}
}

func (f *fake) Presets() []rpc.PresetInfo {
	return []rpc.PresetInfo{{Name: "dns", Account: "main", Lifetime: time.Hour, Rotation: 20 * time.Minute}}
}

func (f *fake) PrototypeID(string) (string, bool) { return "proto1234", true }

func (f *fake) CheckPreset(context.Context, string) error { return f.err }

func (f *fake) Stored(context.Context, string) (minter.StoredInfo, error) {
	return minter.StoredInfo{Present: true, MintedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), ExpiresOn: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)}, nil
}

func (f *fake) Live(context.Context, string) ([]minter.LiveToken, error) {
	return []minter.LiveToken{{ID: "tok1", Caller: "a@example.com", ExpiresOn: time.Date(2026, 10, 9, 12, 30, 0, 0, time.UTC)}}, nil
}

func (f *fake) RotateNow(_ context.Context, preset string, actor audit.Actor) (*rpc.Minted, error) {
	f.preset, f.caller = preset, rpc.Caller{Actor: actor}
	if f.err != nil {
		return nil, f.err
	}
	return &rpc.Minted{Preset: preset, TokenID: "new1", Token: "never-crosses", ExpiresOn: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)}, nil
}

func (f *fake) Revoke(_ context.Context, preset, id string, actor audit.Actor) (rpc.RevokeResult, error) {
	f.preset, f.caller = preset, rpc.Caller{Actor: actor}
	return rpc.RevokeResult{Rotated: id == "stored"}, f.err
}

func client(f *fake) *rpc.Client {
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, f)
	rpc.RegisterConsole(s, f)
	return rpc.NewClient(modcall.Local{rpc.Module: s}.As(rpc.CallerIssuer), nil)
}

func TestAMintCrossesTheBoundaryWithItsCallerAndLifetime(t *testing.T) {
	f := &fake{}
	caller := rpc.Caller{Actor: audit.Person("a@example.com"), Groups: []string{"ops"}}
	got, err := client(f).MintFor(context.Background(), "r2", caller, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if f.preset != "r2" || f.caller.Actor != caller.Actor || f.caller.Groups[0] != "ops" || f.lifetime != 10*time.Minute {
		t.Errorf("the module was asked %q %+v %v", f.preset, f.caller, f.lifetime)
	}
	if got.SecretAccessKey != "secret" || !got.R2 || got.ExpiresOn.Hour() != 13 {
		t.Errorf("%+v", got)
	}
}

func TestTheThreeRefusalsComeBackAsTheMintersErrors(t *testing.T) {
	for _, want := range []error{rpc.ErrUnknownPreset, rpc.ErrNotGranted, rpc.ErrLifetime} {
		_, err := client(&fake{err: errors.Join(want, errors.New("detail"))}).MintFor(context.Background(), "x", rpc.Caller{}, 0)
		if !errors.Is(err, want) {
			t.Errorf("%v: got %v", want, err)
		}
	}
}

// A module under maintenance answers under its own code, so the issuer can tell
// 503 from a grant it does not give, whether the flag was set or unreadable.
func TestMaintenanceCrossesTheBoundaryUnderItsOwnName(t *testing.T) {
	_, err := client(&fake{err: &maintenance.Error{Flag: maintenance.Flag{State: maintenance.StateRestoring}}}).MintFor(context.Background(), "x", rpc.Caller{}, 0)
	if !errors.Is(err, rpc.ErrMaintenance) {
		t.Errorf("a flag that is set: got %v", err)
	}
	_, err = client(&fake{err: &maintenance.Error{Err: errors.New("store down")}}).MintFor(context.Background(), "x", rpc.Caller{}, 0)
	if !errors.Is(err, rpc.ErrMaintenance) {
		t.Errorf("an unreadable flag: got %v", err)
	}
}

func TestAnyOtherFailureIsInternalAndTellsNothing(t *testing.T) {
	_, err := client(&fake{err: errors.New("cloudflare said token abc")}).MintFor(context.Background(), "x", rpc.Caller{}, 0)
	var e *modcall.Error
	if !errors.As(err, &e) || e.Code != modcall.CodeInternal || err.Error() != "module call: internal" {
		t.Fatalf("%v", err)
	}
}

func TestGrantedListsAndAnUnreachableModuleListsNothing(t *testing.T) {
	c := client(&fake{})
	if got := c.Granted(rpc.Caller{Groups: []string{"ops"}}); len(got) != 1 || got[0].Name != "dns" || got[0].Lifetime != time.Hour {
		t.Errorf("%+v", got)
	}
	if got := c.Granted(rpc.Caller{}); len(got) != 0 {
		t.Errorf("%+v", got)
	}
	if got := rpc.NewClient(modcall.Local{}.As(rpc.CallerIssuer), nil).Granted(rpc.Caller{Groups: []string{"ops"}}); got != nil {
		t.Errorf("%+v", got)
	}
}

func TestDialRefusesWhatItCannotReach(t *testing.T) {
	if _, err := rpc.Dial(context.Background(), nil, nil); err == nil {
		t.Error("nil remote")
	}
	c, err := rpc.Dial(context.Background(), &config.CloudflareRemote{URL: "http://cloudflare.example:8080", TokenFile: "/nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.MintFor(context.Background(), "x", rpc.Caller{}, 0); err == nil {
		t.Error("a call with no token mounted succeeded")
	}
}

func consoleClient(f *fake, class string) *rpc.Client {
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, f)
	rpc.RegisterConsole(s, f)
	return rpc.NewClient(modcall.Local{rpc.Module: s}.As(class), nil)
}

func TestTheConsoleMethodsAnswerTheConsoleAndNoOtherClass(t *testing.T) {
	ctx := context.Background()
	actor := audit.Person("ops@example.com")
	c := consoleClient(&fake{}, rpc.CallerConsole)
	if st, err := c.Status(ctx); err != nil || len(st.Presets) != 1 || st.Accounts[0].IDLast4 != "cdef" ||
		st.Presets[0].Live[0].ID != "tok1" || st.Presets[0].Stored.ExpiresOn.Hour() != 13 {
		t.Errorf("status %+v %v", st, err)
	}
	if p, err := c.Presets(ctx); err != nil || len(p) != 1 {
		t.Errorf("presets %+v %v", p, err)
	}
	if a, err := c.Accounts(ctx); err != nil || len(a) != 1 || a[0].ID == "" {
		t.Errorf("accounts %+v %v", a, err)
	}
	f := &fake{}
	c = consoleClient(f, rpc.CallerConsole)
	m, err := c.Rotate(ctx, "dns", actor)
	if err != nil || m.TokenID != "new1" || m.Token != "" || f.caller.Actor != actor {
		t.Errorf("rotate %+v %v (the module was asked as %+v)", m, err, f.caller)
	}
	if r, err := c.Revoke(ctx, "dns", "stored", actor); err != nil || !r.Rotated {
		t.Errorf("revoke %+v %v", r, err)
	}
	if m, err := c.Credential(ctx, "dns", rpc.Caller{Actor: actor, Groups: []string{"ops"}}, time.Minute); err != nil || m.SecretAccessKey != "secret" {
		t.Errorf("credential %+v %v", m, err)
	}
	for _, class := range []string{rpc.CallerIssuer, "github", ""} {
		f := &fake{}
		c := consoleClient(f, class)
		_, e1 := c.Status(ctx)
		_, e2 := c.Presets(ctx)
		_, e3 := c.Accounts(ctx)
		_, e4 := c.Rotate(ctx, "dns", actor)
		_, e5 := c.Revoke(ctx, "dns", "x", actor)
		_, e6 := c.Credential(ctx, "dns", rpc.Caller{}, 0)
		for i, err := range []error{e1, e2, e3, e4, e5, e6} {
			if !errors.Is(err, &modcall.Error{Code: modcall.CodeForbidden}) {
				t.Errorf("class %q method %d: got %v", class, i, err)
			}
		}
		if f.preset != "" {
			t.Errorf("class %q reached the minter", class)
		}
	}
	// The console is not an issuer: the on-demand exchange stays the issuer's.
	if _, err := consoleClient(&fake{}, rpc.CallerConsole).MintFor(ctx, "dns", rpc.Caller{}, 0); !errors.Is(err, &modcall.Error{Code: modcall.CodeForbidden}) {
		t.Errorf("the console minted through the issuer's method: %v", err)
	}
}

func TestTheConsoleRefusalsCrossAndTheLocalModuleAnswersTheSame(t *testing.T) {
	ctx := context.Background()
	actor := audit.Person("ops@example.com")
	for _, tc := range []struct {
		in   error
		want error
	}{
		{minter.ErrNotOurs, rpc.ErrNotOurs}, {minter.ErrContended, rpc.ErrContended}, {minter.ErrNoMinter, rpc.ErrNoMinter},
		{minter.ErrUnknownPreset, rpc.ErrUnknownPreset}, {minter.ErrMaintenanceUnknown, rpc.ErrMaintenance},
	} {
		f := &fake{err: tc.in}
		if _, err := consoleClient(f, rpc.CallerConsole).Rotate(ctx, "dns", actor); !errors.Is(err, tc.want) {
			t.Errorf("remote %v: got %v", tc.in, err)
		}
		if _, err := rpc.NewLocal(f).Rotate(ctx, "dns", actor); !errors.Is(err, tc.want) {
			t.Errorf("local %v: got %v", tc.in, err)
		}
	}
	refused := &cloudflare.PrototypeError{Reason: cloudflare.ReasonPrototypeActive, Detail: "the prototype is active"}
	for name, err := range map[string]error{
		"remote": func() error {
			_, err := consoleClient(&fake{err: refused}, rpc.CallerConsole).Rotate(ctx, "dns", actor)
			return err
		}(),
		"local": func() error { _, err := rpc.NewLocal(&fake{err: refused}).Rotate(ctx, "dns", actor); return err }(),
	} {
		var re *rpc.RefusedError
		if !errors.As(err, &re) || re.Detail != "the prototype is active" {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// The status carries a refused prototype as data, not as the call's error.
	st, err := consoleClient(&fake{err: refused}, rpc.CallerConsole).Status(ctx)
	if err != nil || st.Presets[0].Prototype.Status != rpc.PrototypeActive {
		t.Errorf("%+v %v", st, err)
	}
}
