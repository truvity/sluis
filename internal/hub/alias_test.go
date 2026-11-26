package hub_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/hub"
)

// aliasHub is one hub over a tenant whose one person has an alias.
func aliasHub(t *testing.T) (*hub.Hub, *fake.Backend) {
	t.Helper()
	directory := fake.New("C0north", "north.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithAlias("ada@north.example", "ada.lovelace@north.example").
		WithGroup("platform@north.example", "ada@north.example")
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return clock })
	if _, err := h.Adopt(context.Background(), hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()
	return h, directory
}

// An alias vouches for its account: the same answer as the primary address,
// authoritative, groups and all, served from the snapshot. A hub that reads
// only snapshots answered an alias as an address it could not vouch for,
// and a roster read that as a reason to remove the person.
func TestAnAliasIsAnsweredAsItsAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, directory := aliasHub(t)
	h.UseReader()
	before := directoryCalls(directory)

	for _, email := range []string{"ada.lovelace@north.example", "Ada.Lovelace@North.Example", "ada@north.example"} {
		got, err := h.ResolveUser(ctx, email, nil)
		if err != nil {
			t.Fatalf("ResolveUser(%s): %v", email, err)
		}
		if !got.Found || got.Suspended || !got.Authoritative || !got.InDomain {
			t.Errorf("%s: result = %+v, want the live account, authoritatively", email, got)
		}
		if got.Primary != "ada@north.example" || got.GivenName != "Ada" {
			t.Errorf("%s: primary = %q, name = %q; want the account it belongs to", email, got.Primary, got.GivenName)
		}
		if !slices.Equal(got.Groups, []string{"platform@north.example"}) {
			t.Errorf("%s: groups = %v, want the account's", email, got.Groups)
		}
	}
	if after := directoryCalls(directory); after != before {
		t.Errorf("an alias cost %d directory calls, want none: the snapshot knows it", after-before)
	}

	// Somebody nobody has is still nobody: an alias index is no licence to
	// answer a stranger.
	got, err := h.ResolveUser(ctx, "grace@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Found || got.Primary != "" || got.Authoritative {
		t.Errorf("a stranger = %+v, want not found and, on a reader, no authority", got)
	}
}

// An alias the snapshot does not know yet is read live, and the answer is
// filed under the account it belongs to: one person, never a second account
// named after the alias.
func TestAnAliasReadLiveIsFiledUnderItsAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, directory := aliasHub(t)
	directory.WithAlias("ada@north.example", "countess@north.example")

	got, err := h.ResolveUser(ctx, "countess@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || !got.Authoritative || got.Primary != "ada@north.example" ||
		!slices.Equal(got.Groups, []string{"platform@north.example"}) {
		t.Errorf("result = %+v, want the account the alias belongs to", got)
	}
	reads := directory.Calls(fake.OpAccount)

	people, total, err := h.People(ctx, hub.PeopleQuery{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(people) != 1 || people[0].Email != "ada@north.example" {
		t.Fatalf("people = %+v, want one person under the primary address", people)
	}
	if !slices.Equal(people[0].Aliases, []string{"ada.lovelace@north.example", "countess@north.example"}) {
		t.Errorf("aliases = %v, want both", people[0].Aliases)
	}
	if _, err = h.ResolveUser(ctx, "countess@north.example", nil); err != nil {
		t.Fatal(err)
	}
	if directory.Calls(fake.OpAccount) != reads {
		t.Error("the alias was read live again; the patched snapshot should know it")
	}
}

// A stored snapshot keeps the aliases and rebuilds their index, so a reader
// in another process answers an alias like the hub that took it.
func TestAStoredSnapshotKeepsTheAliases(t *testing.T) {
	t.Parallel()
	snap := hub.NewSnapshot("C0north", time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC),
		[]backend.Account{{Email: "Ada@North.Example", Live: true, Aliases: []string{"ada.lovelace@north.example"}}},
		nil, nil)
	body, err := hub.EncodeSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := hub.DecodeSnapshot(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"ada.lovelace@north.example", "ada@north.example"} {
		if primary, ok := back.PrimaryOf(email); !ok || primary != "ada@north.example" {
			t.Errorf("PrimaryOf(%s) = %q, %v; want the primary", email, primary, ok)
		}
	}
	if _, ok := back.PrimaryOf("grace@north.example"); ok {
		t.Error("a stranger was given a primary")
	}
}
