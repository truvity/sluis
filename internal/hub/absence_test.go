package hub_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/hub"
)

// absence is a directory module's hub (the writer, which reads the
// directory) and an issuer's (the reader, which only reads the snapshots
// the writer leaves), over one store.
type absence struct {
	t         *testing.T
	directory *fake.Backend
	writer    *hub.Hub
	reader    *hub.Hub
	snapshots *hub.MemorySnapshots
	clock     *time.Time
}

func newAbsence(t *testing.T) *absence {
	t.Helper()
	directory := fake.New("C0north", "north.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithAccount("grace@north.example", "Grace", "North").
		WithAlias("grace@north.example", "grace.hopper@north.example").
		WithGroup("platform@north.example", "ada@north.example", "grace@north.example")
	store, snapshots := hub.NewMemoryStore(), hub.NewMemorySnapshots()
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	writer := hub.New(store, snapshots, hub.Config{}, slog.New(slog.DiscardHandler))
	writer.SetClock(now)
	// Adopting reads the directory once: the first pass.
	if _, err := writer.Adopt(context.Background(), hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	writer.Wait()
	reader := hub.New(store, snapshots, hub.Config{}, slog.New(slog.DiscardHandler))
	reader.SetClock(now)
	reader.UseReader()
	return &absence{t: t, directory: directory, writer: writer, reader: reader, snapshots: snapshots, clock: &clock}
}

// pass is one complete read by the directory module.
func (a *absence) pass() {
	a.t.Helper()
	if _, err := a.writer.Refresh(context.Background(), "C0north"); err != nil {
		a.t.Fatalf("Refresh: %v", err)
	}
}

// failedPass is a read that did not complete: op fails once.
func (a *absence) failedPass(op fake.Op) {
	a.t.Helper()
	a.directory.FailTimes(op, nil, 1)
	if _, err := a.writer.Refresh(context.Background(), "C0north"); err == nil {
		a.t.Fatalf("Refresh with %s failing succeeded", op)
	}
}

// held asserts that the reader answers email as not found and NOT
// authoritatively, so that a removal waits; gone, that it answers it as
// not found authoritatively, so that a removal proceeds.
func (a *absence) held(when, email string) {
	a.t.Helper()
	a.want(when, email, false)
}

func (a *absence) gone(when, email string) {
	a.t.Helper()
	a.want(when, email, true)
}

func (a *absence) want(when, email string, authoritative bool) {
	a.t.Helper()
	got, err := a.reader.ResolveUser(context.Background(), email, nil)
	if err != nil {
		a.t.Fatalf("%s: ResolveUser(%s): %v", when, email, err)
	}
	if got.Found || !got.InDomain || got.Authoritative != authoritative {
		a.t.Errorf("%s: %s = found %t, in domain %t, authoritative %t; want not found, authoritative %t",
			when, email, got.Found, got.InDomain, got.Authoritative, authoritative)
	}
}

func (a *absence) present(when, email string) {
	a.t.Helper()
	got, err := a.reader.ResolveUser(context.Background(), email, nil)
	if err != nil {
		a.t.Fatalf("%s: ResolveUser(%s): %v", when, email, err)
	}
	if !got.Found || !got.Authoritative {
		a.t.Errorf("%s: %s = found %t, authoritative %t; want the account, authoritatively", when, email, got.Found, got.Authoritative)
	}
}

// A deleted account is held for one and two complete reads and answered as
// gone, authoritatively, at the third, by its primary address and by its
// alias. A read that failed between them is no pass: it neither counts nor
// starts the count again.
func TestAReaderAnswersAnAccountGoneAfterThreeCompleteReads(t *testing.T) {
	t.Parallel()
	a := newAbsence(t)
	a.present("before", "grace@north.example")

	a.directory.Remove("grace@north.example")
	a.present("not read since", "grace@north.example")

	a.pass()
	a.held("missing from 1 read", "grace@north.example")
	a.held("missing from 1 read", "grace.hopper@north.example")

	a.failedPass(fake.OpAccounts)
	a.failedPass(fake.OpGroups) // the accounts were read, the groups not: partial
	a.held("a failed and a partial read later", "grace@north.example")

	a.pass()
	a.held("missing from 2 reads", "grace@north.example")
	a.held("missing from 2 reads", "grace.hopper@north.example")

	a.failedPass(fake.OpGroups)
	a.held("a partial read later", "grace@north.example")

	a.pass()
	a.gone("missing from 3 reads", "grace@north.example")
	a.gone("missing from 3 reads", "grace.hopper@north.example")
	a.present("missing from 3 reads", "ada@north.example")

	a.pass()
	a.gone("missing from 4 reads", "grace@north.example")
}

// An account that comes back before the third read starts again from
// nothing when it goes again.
func TestAReaderCountsAgainForAnAccountThatCameBack(t *testing.T) {
	t.Parallel()
	a := newAbsence(t)
	a.directory.Remove("grace@north.example")
	a.pass()
	a.pass()
	a.held("missing from 2 reads", "grace@north.example")

	a.directory.WithAccount("grace@north.example", "Grace", "North")
	a.pass()
	a.present("back", "grace@north.example")

	a.directory.Remove("grace@north.example")
	a.pass()
	a.held("missing from 1 read after coming back", "grace@north.example")
	a.pass()
	a.held("missing from 2 reads after coming back", "grace@north.example")
	a.pass()
	a.gone("missing from 3 reads after coming back", "grace@north.example")
}

// An address no read ever held is as old as its domain's count: missing
// from every complete read since the domain was first read. It is how an
// account deleted before the counts were kept stops holding its removals.
func TestAReaderCountsAnAddressNoReadHeldFromItsDomain(t *testing.T) {
	t.Parallel()
	a := newAbsence(t)
	a.directory.Remove("grace@north.example")
	a.held("1 read of the domain", "nobody@north.example")
	a.pass()
	a.held("2 reads of the domain", "nobody@north.example")
	a.failedPass(fake.OpAccounts)
	a.pass()
	// The domain has been read three times, grace has been missing from two.
	a.gone("3 reads of the domain", "nobody@north.example")
	a.held("missing from 2 reads", "grace@north.example")
}

// A live read in the directory module that finds an account gone counts the
// absence from the next complete read, like a read that drops it, and not
// from the first read of its domain.
func TestALiveMissCountsFromTheNextRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := newAbsence(t)
	a.pass()
	a.pass() // the domain has been read three times
	a.directory.Remove("grace@north.example")
	*a.clock = a.clock.Add(time.Minute) // older than the zero max age asked for
	got, err := a.writer.ResolveUser(ctx, "grace@north.example", new(time.Duration))
	if err != nil || got.Found || !got.Authoritative {
		t.Fatalf("writer ResolveUser = %+v, %v; want gone, read live", got, err)
	}
	a.held("missed live, no read since", "grace@north.example")
	a.held("missed live, no read since", "grace.hopper@north.example")
	a.pass()
	a.pass()
	a.held("missing from 2 reads", "grace@north.example")
	a.pass()
	a.gone("missing from 3 reads", "grace@north.example")
	a.gone("missing from 3 reads", "grace.hopper@north.example")
}

// A snapshot with no counts, as an older replica writes it, counts nobody as
// gone; the counts survive the store's encoding, so a restarted reader
// answers as the one before it.
func TestTheAbsenceCountsAreStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := newAbsence(t)
	a.directory.Remove("grace@north.example")
	for range hub.GoneAfterPasses {
		a.pass()
	}
	snap, err := a.snapshots.Get(ctx, "C0north")
	if err != nil || snap == nil {
		t.Fatalf("Get: %v, %v", snap, err)
	}
	body, err := hub.EncodeSnapshot(snap)
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	back, err := hub.DecodeSnapshot(body)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if got := back.AbsentPasses("grace@north.example"); got != hub.GoneAfterPasses {
		t.Errorf("decoded AbsentPasses(grace) = %d, want %d", got, hub.GoneAfterPasses)
	}
	if got := back.AbsentPasses("ada@north.example"); got != 0 {
		t.Errorf("decoded AbsentPasses(ada) = %d, want 0", got)
	}

	back.Passes, back.Absent = nil, nil
	if err = a.snapshots.Put(ctx, back); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a.held("a snapshot without counts", "grace@north.example")
	a.pass()
	a.held("one read after a snapshot without counts", "grace@north.example")
}

// Narrowing the served domains drops their counts: a domain served again
// has not been read in between, and its accounts are missing from the
// narrowed snapshot for that reason alone.
func TestNarrowingDropsTheCountsOfTheDomainsItDrops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	directory := fake.New("C0north", "north.example", "south.example").
		WithAccount("ada@north.example", "Ada", "North").
		WithAccount("bob@south.example", "Bob", "South")
	store, snapshots := hub.NewMemoryStore(), hub.NewMemorySnapshots()
	writer := hub.New(store, snapshots, hub.Config{}, slog.New(slog.DiscardHandler))
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	writer.SetClock(func() time.Time { return clock })
	if _, err := writer.Adopt(ctx, hub.Workspace{Admin: "admin@north.example"}, directory); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	writer.Wait()
	for range hub.GoneAfterPasses {
		if _, err := writer.Refresh(ctx, "C0north"); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
	}
	if _, err := writer.SetServed(ctx, "C0north", []string{"north.example"}); err != nil {
		t.Fatalf("SetServed: %v", err)
	}
	writer.Wait()
	snap, err := snapshots.Get(ctx, "C0north")
	if err != nil || snap == nil {
		t.Fatalf("Get: %v, %v", snap, err)
	}
	if got := snap.AbsentPasses("bob@south.example"); got != 0 {
		t.Errorf("AbsentPasses(bob) after narrowing south away = %d, want 0", got)
	}
	if got := snap.AbsentPasses("nobody@north.example"); got < hub.GoneAfterPasses {
		t.Errorf("AbsentPasses(nobody@north) after narrowing = %d, want the domain's count kept", got)
	}
}
