package hub_test

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/backend/fake"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

// readCounting counts the blob reads that reach the store beneath it, and
// can blank the version of what it returns, as an adapter that keeps none.
type readCounting struct {
	port.Blob
	reads     atomic.Int64
	noVersion bool
}

func (b *readCounting) Read(ctx context.Context, name string) (port.Object, error) {
	b.reads.Add(1)
	o, err := b.Blob.Read(ctx, name)
	if b.noVersion {
		o.Version = ""
	}
	return o, err
}

func snapshotOf(workspace string, takenAt time.Time, members ...string) *hub.Snapshot {
	return hub.NewSnapshot(workspace, takenAt,
		[]backend.Account{{Email: "ada@north.example", Live: true}},
		[]backend.Group{{Email: "eng@north.example", Members: members}}, nil)
}

func cacheStore(t *testing.T) (*hub.BlobSnapshots, *readCounting, *memory.Store) {
	t.Helper()
	mem := memory.New()
	blob := &readCounting{Blob: mem.Blobs()}
	return hub.NewBlobSnapshots(blob, mem), blob, mem
}

func mustGet(t *testing.T, s *hub.BlobSnapshots, workspace string) *hub.Snapshot {
	t.Helper()
	snap, err := s.Get(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Get(%s): %v", workspace, err)
	}
	return snap
}

// The blob is read every time -- what is answered is what is stored -- but
// an unchanged version is decoded once: the same snapshot comes back.
func TestSnapshotOfAnUnchangedVersionIsDecodedOnce(t *testing.T) {
	t.Parallel()
	s, blob, _ := cacheStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}

	first := mustGet(t, s, "ws")
	second := mustGet(t, s, "ws")
	third := mustGet(t, s, "ws")
	if first == nil || first != second || second != third {
		t.Errorf("three reads of one version gave %p %p %p, want one decoded snapshot", first, second, third)
	}
	if got := blob.reads.Load(); got != 3 {
		t.Errorf("the blob was read %d times for 3 Gets, want every time", got)
	}
}

// A new version of the blob is decoded again, and what is answered is the
// new content, including when the writer was another replica.
func TestSnapshotOfANewVersionIsDecodedAgain(t *testing.T) {
	t.Parallel()
	mem := memory.New()
	blob := &readCounting{Blob: mem.Blobs()}
	here := hub.NewBlobSnapshots(blob, mem)
	elsewhere := hub.NewBlobSnapshots(mem.Blobs(), mem)
	ctx := context.Background()

	if err := here.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	old := mustGet(t, here, "ws")
	if err := elsewhere.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example", "bob@north.example")); err != nil {
		t.Fatal(err)
	}

	fresh := mustGet(t, here, "ws")
	if fresh == old {
		t.Fatal("a new version of the blob was answered with the snapshot decoded from the old one")
	}
	if got := fresh.Groups["eng@north.example"].Members; len(got) != 2 {
		t.Errorf("members = %v, want what the other replica stored", got)
	}
	if again := mustGet(t, here, "ws"); again != fresh {
		t.Error("the new version was not cached once decoded")
	}
}

// Put forgets rather than keeps what the caller handed in: the next read is
// of what was stored, not the caller's own object.
func TestPutForgetsTheCachedSnapshot(t *testing.T) {
	t.Parallel()
	s, _, _ := cacheStore(t)
	ctx := context.Background()
	put := snapshotOf("ws", time.Now(), "ada@north.example")
	if err := s.Put(ctx, put); err != nil {
		t.Fatal(err)
	}
	before := mustGet(t, s, "ws")
	if err := s.Put(ctx, put); err != nil {
		t.Fatal(err)
	}
	after := mustGet(t, s, "ws")
	if after == put || after == before {
		t.Errorf("after a Put the read answered the caller's object or the previous decode (%p, %p, %p)", put, before, after)
	}
	if !reflect.DeepEqual(after.Groups, put.Groups) {
		t.Errorf("groups = %v, want %v", after.Groups, put.Groups)
	}
}

// Delete, and a blob that is simply gone, answer nothing -- not a snapshot
// remembered from before.
func TestADeletedSnapshotIsNotServedFromTheCache(t *testing.T) {
	t.Parallel()
	mem := memory.New()
	s := hub.NewBlobSnapshots(mem.Blobs(), mem)
	ctx := context.Background()

	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	if mustGet(t, s, "ws") == nil {
		t.Fatal("no snapshot after Put")
	}
	if err := s.Delete(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, "ws"); got != nil {
		t.Errorf("a deleted snapshot was answered: %+v", got)
	}

	// Removed by something else.
	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	cached := mustGet(t, s, "ws")
	if err := mem.Blobs().Delete(ctx, hub.SnapshotBlobPrefix+"ws"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, "ws"); got != nil {
		t.Errorf("a snapshot removed from the blob was answered: %+v", got)
	}
	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, "ws"); got == nil || got == cached {
		t.Errorf("a snapshot stored after a removal was answered from before it: %p vs %p", got, cached)
	}
}

// Without a version there is nothing to key on: every read decodes.
func TestSnapshotWithoutAVersionIsNeverCached(t *testing.T) {
	t.Parallel()
	s, blob, _ := cacheStore(t)
	blob.noVersion = true
	if err := s.Put(context.Background(), snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	if a, b := mustGet(t, s, "ws"), mustGet(t, s, "ws"); a == b {
		t.Error("a snapshot read with no blob version was served from the cache")
	}
}

// Workspaces do not share an entry.
func TestSnapshotCacheIsPerWorkspace(t *testing.T) {
	t.Parallel()
	s, _, _ := cacheStore(t)
	ctx := context.Background()
	for _, ws := range []string{"a", "b"} {
		if err := s.Put(ctx, snapshotOf(ws, time.Now(), "ada@north.example")); err != nil {
			t.Fatal(err)
		}
	}
	a, b := mustGet(t, s, "a"), mustGet(t, s, "b")
	if a.Workspace != "a" || b.Workspace != "b" || a == b {
		t.Errorf("workspaces a and b answered %q and %q", a.Workspace, b.Workspace)
	}
}

// Safe under concurrent readers and a writer (run with -race).
func TestSnapshotCacheIsRaceFree(t *testing.T) {
	t.Parallel()
	s, _, _ := cacheStore(t)
	ctx := context.Background()
	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				if _, err := s.Get(ctx, "ws"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for range 20 {
			_ = s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example"))
		}
	})
	wg.Wait()
}

// The cache decides nothing about authority: a snapshot older than the
// freshness window is answered as non-authoritative however recently it was
// decoded, and a replacement is authoritative again.
func TestACachedSnapshotPastTheFreshnessWindowIsNotAuthoritative(t *testing.T) {
	t.Parallel()
	mem := memory.New()
	snapshots := hub.NewBlobSnapshots(mem.Blobs(), mem)
	h := hub.New(hub.NewMemoryStore(), snapshots, hub.Config{}, slog.New(slog.DiscardHandler))
	clock := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	h.SetClock(func() time.Time { return clock })

	ctx := context.Background()
	b := fake.New("C0one", "one.example").WithAccount("alice@one.example", "Alice", "Ant")
	if _, err := h.Adopt(ctx, hub.Workspace{ID: "C0one", Admin: "admin@one.example"}, b); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	h.Wait()

	resolve := func() hub.UserResult {
		t.Helper()
		got, err := h.ResolveUser(ctx, "alice@one.example", nil)
		if err != nil {
			t.Fatalf("ResolveUser: %v", err)
		}
		return got
	}
	if got := resolve(); !got.Found || !got.Authoritative {
		t.Fatalf("a fresh snapshot: %+v, want found and authoritative", got)
	}
	cached := mustGet(t, snapshots, "C0one")
	if resolve(); mustGet(t, snapshots, "C0one") != cached {
		t.Fatal("the snapshot was not served from the cache, so this test proves nothing")
	}

	clock = clock.Add(hub.DefaultFreshnessWindow + time.Minute)
	if got := resolve(); got.Authoritative {
		t.Error("a snapshot past the freshness window was answered as authoritative from the cache")
	}
	if mustGet(t, snapshots, "C0one") != cached {
		t.Fatal("the blob changed, so the cache was not what was exercised")
	}

	if err := snapshots.Put(ctx, snapshotOf("C0one", clock, "alice@one.example")); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); !got.Authoritative {
		t.Error("a new snapshot was not authoritative")
	}
}

// The snapshot of a workspace is the blob `google/<workspace>`: the name in
// storage is the module's, and the old `snapshots/` name is not read, so a
// cache written by an earlier release is a cache miss, never a wrong answer.
func TestSnapshotBlobIsNamedForTheGoogleModule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, mem := cacheStore(t)
	if hub.SnapshotBlobPrefix != "google/" {
		t.Fatalf("SnapshotBlobPrefix = %q, want google/", hub.SnapshotBlobPrefix)
	}
	if _, err := mem.Blobs().Write(ctx, "snapshots/ws", []byte("an earlier release's cache")); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, "ws"); got != nil {
		t.Fatalf("a snapshot from before the first write = %+v, want none", got)
	}
	if err := s.Put(ctx, snapshotOf("ws", time.Now(), "ada@north.example")); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Blobs().Read(ctx, "google/ws"); err != nil {
		t.Errorf("the snapshot is not at google/ws: %v", err)
	}
	if got := mustGet(t, s, "ws"); got == nil {
		t.Error("the snapshot written at google/ws is not read back")
	}
}
