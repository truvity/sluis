package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/cloudflare/minter"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/storage/state/memory"
)

const proto = "0123456789abcdef0123456789abcdef"

type lock struct{ ran bool }

func (l lock) Do(ctx context.Context, _, _ string, fn func(context.Context)) (bool, error) {
	if l.ran {
		fn(ctx)
	}
	return l.ran, nil
}

func doc() *config.Cloudflare {
	return &config.Cloudflare{
		Accounts: map[string]config.CloudflareAccount{"main": {ID: proto, Minter: "internal/cloudflare/main/minter"}},
		Presets: map[string]config.CloudflarePreset{"dns": {Account: "main", Prototype: proto, Description: "DNS",
			Lifetime: config.Duration(time.Hour), Rotation: config.Duration(10 * time.Minute)}},
	}
}

// fixture is an App over an in-memory secrets store and a Dialer that fails the
// test if Cloudflare is reached.
func fixture(t *testing.T, shared, leased bool, stored string) *App {
	t.Helper()
	stores := secretstore.FromStore(memory.New(), "")
	if stored != "" {
		v := stores.External.Cloudflare("dns")
		if _, err := v.Put(context.Background(), secretstore.Cloudflarev1{ExpiresOn: stored, Token: "t"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := minter.New(minter.Config{
		Instance: "example", Cloudflare: doc(), Internal: stores.Internal, External: stores.External, Log: log,
		Dial: func(context.Context, string, string) (minter.API, error) {
			return nil, errors.New("dialled")
		},
		Lock: lock{ran: leased},
		Now:  func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{minter: m, log: log, sharedLease: shared}
}

func TestATickRefusesLocalLeasesUnlessOptedIn(t *testing.T) {
	a := fixture(t, false, true, "2026-10-09T13:00:00Z")
	if err := a.Tick(context.Background(), "dns", false); !errors.Is(err, store.ErrLocalLease) {
		t.Fatalf("got %v", err)
	}
	if err := a.Tick(context.Background(), "dns", true); err != nil {
		t.Fatalf("with the flag: %v", err)
	}
}

// A credential that is not due costs no call to Cloudflare.
func TestATickOfAFreshCredentialDoesNothing(t *testing.T) {
	a := fixture(t, true, true, "2026-10-09T13:00:00Z")
	if err := a.Tick(context.Background(), "dns", false); err != nil {
		t.Fatal(err)
	}
}

// A credential that is due is rotated, which reaches Cloudflare: the failure
// to is the tick's.
func TestATickOfADueCredentialReachesCloudflare(t *testing.T) {
	a := fixture(t, true, true, "2026-10-09T12:30:00Z")
	err := a.Tick(context.Background(), "dns", false)
	if err == nil {
		t.Fatal("a due credential was not rotated")
	}
}

func TestATickOfAnUnknownPresetFails(t *testing.T) {
	a := fixture(t, true, true, "")
	if err := a.Tick(context.Background(), "nope", false); !errors.Is(err, minter.ErrUnknownPreset) {
		t.Fatalf("got %v", err)
	}
}

// A preset another runner holds is left to it, and that is not a failure.
func TestATickOfAContendedPresetIsLeftToTheHolder(t *testing.T) {
	a := fixture(t, true, false, "")
	if err := a.Tick(context.Background(), "dns", false); err != nil {
		t.Fatal(err)
	}
}

func TestFromServiceRefusesADocumentWithNoPreset(t *testing.T) {
	for name, f := range map[string]*config.Serve{"no section": {}, "no presets": {Cloudflare: &config.Cloudflare{}}} {
		if _, err := FromService(f, nil); err == nil || !strings.Contains(err.Error(), "cloudflare.presets") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFromServiceDefaultsTheInstanceAndReadsTheLogLevel(t *testing.T) {
	f := &config.Serve{Cloudflare: doc(), Release: "rel", Log: &config.Log{Level: "debug"}}
	c, err := FromService(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.instance != "rel" || c.LogLevel() != slog.LevelDebug {
		t.Errorf("instance %q, level %v", c.instance, c.LogLevel())
	}
	if c, err = FromService(&config.Serve{Cloudflare: doc()}, nil); err != nil || c.instance != "sluis" {
		t.Errorf("instance %q: %v", c.instance, err)
	}
}

func TestRunStopsWhenItsContextEnds(t *testing.T) {
	a := fixture(t, true, true, "2026-10-09T13:00:00Z")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end with its context")
	}
}
