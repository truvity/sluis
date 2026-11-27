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

// memberByAliasHub is one hub over a tenant whose group lists its one
// person by an alias, the way a directory lists somebody added to the
// group by an alias or renamed since, beside a member from outside.
func memberByAliasHub(t *testing.T) (*hub.Hub, *fake.Backend) {
	t.Helper()
	directory := fake.New("C0north", "north.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithAlias("ada@north.example", "ada.lovelace@north.example").
		WithGroup("platform@north.example", "Ada.Lovelace@north.example", "guest@elsewhere.example")
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	clock := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return clock })
	if _, err := h.Adopt(context.Background(), hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()
	return h, directory
}

// A group member listed by an alias is the account it belongs to: the
// snapshot files the membership under the primary address, where the
// person's account, groups and every holder are kept, and keeps a member
// that is nobody's alias as it is.
func TestAGroupMemberByAliasIsItsAccount(t *testing.T) {
	t.Parallel()
	snap := hub.NewSnapshot("C0north", time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC),
		[]backend.Account{
			{Email: "ada@north.example", Live: true, Aliases: []string{"ada.lovelace@north.example"}},
			// An alias that is another account's primary address stays that
			// account's.
			{Email: "grace@north.example", Live: true, Aliases: []string{"ada@north.example", "hopper@north.example"}},
		},
		[]backend.Group{
			{Email: "platform@north.example", Members: []string{"Ada.Lovelace@North.Example", "guest@elsewhere.example"}},
			{Email: "both@north.example", Members: []string{"ada@north.example", "ada.lovelace@north.example"}},
			{Email: "ops@north.example", Members: []string{"ada@north.example"}},
		}, nil)

	if got, want := snap.GroupsOf("ada@north.example"),
		[]string{"both@north.example", "ops@north.example", "platform@north.example"}; !slices.Equal(got, want) {
		t.Errorf("GroupsOf(primary) = %v, want %v", got, want)
	}
	if got := snap.GroupsOf("ada.lovelace@north.example"); len(got) != 0 {
		t.Errorf("GroupsOf(alias) = %v, want none: the alias is not a second person", got)
	}
	if got := snap.GroupsOf("grace@north.example"); len(got) != 0 {
		t.Errorf("GroupsOf(grace) = %v, want none: ada@ is ada's primary, not grace's alias", got)
	}
	if got, want := snap.GroupsOf("guest@elsewhere.example"), []string{"platform@north.example"}; !slices.Equal(got, want) {
		t.Errorf("GroupsOf(external) = %v, want %v", got, want)
	}
	if got, want := snap.Groups["platform@north.example"].Members,
		[]string{"ada@north.example", "guest@elsewhere.example"}; !slices.Equal(got, want) {
		t.Errorf("platform members = %v, want %v", got, want)
	}
	if got, want := snap.Groups["both@north.example"].Members, []string{"ada@north.example"}; !slices.Equal(got, want) {
		t.Errorf("both members = %v, want the person once", got)
	}

	// A stored snapshot is rebuilt the same way.
	body, err := hub.EncodeSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := hub.DecodeSnapshot(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.GroupsOf("ada@north.example"); len(got) != 3 {
		t.Errorf("decoded GroupsOf(primary) = %v, want all three", got)
	}
}

// Every answer the hub gives about the group and the person agrees: the
// person holds the group under the primary address, and the group's
// members are the person, once, under that address, and the outsider.
func TestAHubAnswersAGroupMemberByAliasAsItsAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h, _ := memberByAliasHub(t)

	user, err := h.ResolveUser(ctx, "ada@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !user.Found || !slices.Equal(user.Groups, []string{"platform@north.example"}) {
		t.Errorf("ResolveUser = %+v, want the group listing the alias", user)
	}

	people, total, err := h.People(ctx, hub.PeopleQuery{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || people[0].Email != "ada@north.example" ||
		!slices.Equal(people[0].DirectoryGroups, []string{"platform@north.example"}) {
		t.Errorf("people = %+v, want ada in platform", people)
	}

	group, err := h.Group(ctx, "platform@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ada@north.example", "guest@elsewhere.example"}; !slices.Equal(group.Members, want) {
		t.Errorf("Group members = %v, want %v", group.Members, want)
	}

	resolved, err := h.ResolveGroups(ctx, []string{"platform@north.example"}, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || len(resolved[0].Members) != 2 {
		t.Fatalf("ResolveGroups = %+v, want two members", resolved)
	}
	if m := resolved[0].Members[0]; m.Email != "ada@north.example" || !m.Known || !m.Live || m.GivenName != "Ada" {
		t.Errorf("resolved member = %+v, want ada, known and live", m)
	}
	if m := resolved[0].Members[1]; m.Email != "guest@elsewhere.example" || m.Known {
		t.Errorf("resolved member = %+v, want the outsider as listed, unknown", m)
	}

	dir, err := h.DirectoryGroup(ctx, "platform@north.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(dir.Members) != 2 || dir.Members[0].Email != "ada@north.example" || !dir.Members[0].Known ||
		dir.Members[1].Email != "guest@elsewhere.example" || dir.Members[1].Known {
		t.Errorf("DirectoryGroup members = %+v, want ada known and the outsider as listed", dir.Members)
	}
}

// An alias the snapshot learns from a live read takes the memberships
// listed under it along: the patched snapshot has the person once, under
// the primary address, in the groups the directory lists them in by
// either address, and the outsider where they were.
func TestALiveReadFilesAliasMembershipsUnderTheAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := fake.New("C0north", "north.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithGroup("platform@north.example", "countess@north.example", "guest@elsewhere.example").
		WithGroup("ops@north.example", "ada@north.example")
	h := hub.New(hub.NewMemoryStore(), hub.NewMemorySnapshots(), hub.Config{}, slog.New(slog.DiscardHandler))
	clock := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return clock })
	if _, err := h.Adopt(ctx, hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()

	// The alias arrives after the full read, which filed countess@ as a
	// member nobody was.
	directory.WithAlias("ada@north.example", "countess@north.example")
	got, err := h.ResolveUser(ctx, "countess@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ops@north.example", "platform@north.example"}
	if !got.Found || got.Primary != "ada@north.example" || !slices.Equal(got.Groups, want) {
		t.Errorf("live read = %+v, want ada in %v", got, want)
	}

	reads := directory.Calls(fake.OpAccount)
	for _, email := range []string{"ada@north.example", "countess@north.example"} {
		got, err = h.ResolveUser(ctx, email, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Groups, want) {
			t.Errorf("%s from the patched snapshot: groups = %v, want %v", email, got.Groups, want)
		}
	}
	if directory.Calls(fake.OpAccount) != reads {
		t.Error("read live again; the patched snapshot should answer")
	}
	group, err := h.Group(ctx, "platform@north.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ada@north.example", "guest@elsewhere.example"}; !slices.Equal(group.Members, want) {
		t.Errorf("platform members = %v, want %v: the alias went, the outsider stayed", group.Members, want)
	}
	people, total, err := h.People(ctx, hub.PeopleQuery{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || !slices.Equal(people[0].DirectoryGroups, want) {
		t.Errorf("people = %+v, want ada once in %v", people, want)
	}
}
