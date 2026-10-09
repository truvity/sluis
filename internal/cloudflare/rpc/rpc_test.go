package rpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/cloudflare/rpc"
	"github.com/truvity/sluis/internal/config"
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

func client(f *fake) *rpc.Client {
	s := modcall.NewServer(rpc.Module)
	rpc.Register(s, f)
	return rpc.NewClient(modcall.Local{rpc.Module: s}, nil)
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
	if got := rpc.NewClient(modcall.Local{}, nil).Granted(rpc.Caller{Groups: []string{"ops"}}); got != nil {
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
