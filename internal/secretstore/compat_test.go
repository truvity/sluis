package secretstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/clientcreds"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state/memory"
)

type fixture struct {
	sec   *secretstore.Secrets
	st    *secretstore.Stores
	clock *time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clock := &now
	root := memory.New(memory.WithClock(func() time.Time { return *clock }))
	st := secretstore.FromStore(root, "")
	sec := secretstore.NewSecrets(st, 24*time.Hour).WithClock(func() time.Time { return *clock })
	return fixture{sec: sec, st: st, clock: clock}
}

func record(t *testing.T, current, previous string) []byte {
	t.Helper()
	b, err := clientcreds.Record{Current: current, Previous: previous}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCredentialsMoveToInternal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	path := "credentials/console/session-key"

	if _, err := f.sec.Get(ctx, path); !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("absent = %v", err)
	}
	v1, err := f.sec.PutIfVersion(ctx, path, []byte{0, 1, 2}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.sec.PutIfVersion(ctx, path, []byte("x"), ""); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("create-only over a secret = %v, want ErrConflict", err)
	}
	got, err := f.sec.Get(ctx, path)
	if err != nil || string(got.Value) != "\x00\x01\x02" || got.Version != v1 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := f.sec.PutIfVersion(ctx, path, []byte("y"), "no-such-rev"); !errors.Is(err, port.ErrConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	if _, err := f.sec.Put(ctx, path, []byte("z")); err != nil {
		t.Fatal(err)
	}
	// It is at the internal address.
	if _, _, err := f.st.Internal.ConsoleSessionKey().Get(ctx); err != nil {
		t.Fatalf("not at internal/credentials/console/session-key: %v", err)
	}
	names, err := f.sec.List(ctx, "credentials/console")
	if err != nil || len(names) != 1 || names[0] != path {
		t.Fatalf("list = %v, %v", names, err)
	}
	if err := f.sec.Delete(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := f.sec.Delete(ctx, path); err != nil {
		t.Fatalf("delete of an absent secret = %v", err)
	}
	if _, err := f.sec.Put(ctx, "credentials/bad path", []byte("x")); err == nil {
		t.Fatal("a bad path was accepted")
	}
}

// A generated client's record is the oidc/v1 document: the previous secret is
// the previous revision for the grace period, and the record callers see is
// the one clientcreds always read.
func TestClientRecordIsTheOIDCDocument(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	path := clientcreds.Path("rp")

	if _, err := f.sec.PutIfVersion(ctx, path, record(t, "one", ""), ""); err != nil {
		t.Fatal(err)
	}
	doc, _, err := f.st.External.OIDC("rp").Get(ctx)
	if err != nil || doc.ClientID != "rp" || doc.ClientSecret != "one" {
		t.Fatalf("document = %+v, %v", doc, err)
	}
	got, _ := f.sec.Get(ctx, path)
	rec, err := clientcreds.DecodeRecord(got.Value)
	if err != nil || rec.Current != "one" || rec.Previous != "" || rec.Created.IsZero() {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	created := rec.Created

	// An orphan mark changes nothing in the store.
	marked := clientcreds.Record{Current: "one", Orphaned: *f.clock}
	body, _ := marked.Encode()
	if _, err := f.sec.PutIfVersion(ctx, path, body, got.Version); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.sec.Get(ctx, path); again.Version != got.Version {
		t.Fatal("an orphan mark wrote a revision")
	}

	// A rotation with an overlap: the previous secret is the previous revision.
	*f.clock = f.clock.Add(time.Hour)
	if _, err := f.sec.PutIfVersion(ctx, path, record(t, "two", "one"), got.Version); err != nil {
		t.Fatal(err)
	}
	got, _ = f.sec.Get(ctx, path)
	rec, _ = clientcreds.DecodeRecord(got.Value)
	if rec.Current != "two" || rec.Previous != "one" || !rec.Created.Equal(created) || rec.Rotated.IsZero() {
		t.Fatalf("after a rotation: %+v (created was %v)", rec, created)
	}
	if want := f.clock.Add(24 * time.Hour); !rec.PreviousValidUntil.Equal(want) {
		t.Fatalf("previous valid until %v, want %v", rec.PreviousValidUntil, want)
	}
	*f.clock = f.clock.Add(25 * time.Hour)
	got, _ = f.sec.Get(ctx, path)
	if rec, _ = clientcreds.DecodeRecord(got.Value); rec.Previous != "" {
		t.Fatalf("previous after the grace: %+v", rec)
	}

	// A rotation with no overlap is a hard cut.
	if _, err := f.sec.PutIfVersion(ctx, path, record(t, "three", ""), got.Version); err != nil {
		t.Fatal(err)
	}
	got, _ = f.sec.Get(ctx, path)
	if rec, _ = clientcreds.DecodeRecord(got.Value); rec.Current != "three" || rec.Previous != "three" {
		// The previous revision is the new secret itself: "one" and "two" are refused.
		t.Fatalf("hard cut: %+v", rec)
	}

	ids, err := f.sec.List(ctx, "credentials/oidc-client")
	if err != nil || len(ids) != 1 || ids[0] != path {
		t.Fatalf("list = %v, %v", ids, err)
	}
	if f.sec.Overlap() != 24*time.Hour {
		t.Fatal("overlap")
	}
}

// An operator-seeded document is served as a record, and a write keeps the
// client id it names.
func TestOperatorSeededClientSecret(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if _, err := f.st.External.OIDC("rp").Put(ctx, secretstore.OIDCv1{ClientID: "the-id", ClientSecret: "seeded"}, ""); err != nil {
		t.Fatal(err)
	}
	got, err := f.sec.Get(ctx, clientcreds.Path("rp"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := clientcreds.DecodeRecord(got.Value); rec.Current != "seeded" {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := f.sec.PutIfVersion(ctx, clientcreds.Path("rp"), record(t, "rotated", "seeded"), got.Version); err != nil {
		t.Fatal(err)
	}
	if doc, _, _ := f.st.External.OIDC("rp").Get(ctx); doc.ClientID != "the-id" || doc.ClientSecret != "rotated" {
		t.Fatalf("document = %+v", doc)
	}
}
