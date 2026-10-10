package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/truvity/sluis/backend"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/hub"
	"github.com/truvity/sluis/internal/lazy"
	"github.com/truvity/sluis/internal/secrets"
)

// keyedBackend is a backend that knows which key it was opened with.
type keyedBackend struct {
	backend.Backend
	key string
}

func (k *keyedBackend) Kind() string { return "google" }

func (k *keyedBackend) Tenant(context.Context) (backend.Tenant, error) {
	return backend.Tenant{ID: "tenant-" + k.key}, nil
}

// countingSource counts the reads of a secrets source.
type countingSource struct {
	secrets.Source
	values map[string]string
	reads  int
}

func (c *countingSource) Get(_ context.Context, name string) (string, error) {
	c.reads++
	v, ok := c.values[name]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (c *countingSource) Describe(name string) string { return name }

// A declared workspace's key is read when the workspace is first used, reused
// for the TTL, and read again by the call after it; the backend is opened again
// only when the key changed.
func TestADeclaredWorkspacesKeyIsReadAtFirstUseAndAgainAfterTheTTL(t *testing.T) {
	ctx := context.Background()
	src := &countingSource{values: map[string]string{"workspaces/acme/key": "key-1"}}
	opened := 0
	lb := newLazyBackend(hub.Declared{Backend: "google", Admin: "admin@acme.example"}, src, "workspaces/acme/key")
	lb.opener = func(_ context.Context, d *hub.Declared) (backend.Backend, error) {
		opened++
		return &keyedBackend{key: string(d.Key)}, nil
	}
	now := time.Unix(1_000_000, 0)
	lb.now = func() time.Time { return now }
	if src.reads != 0 || lb.Kind() != "google" {
		t.Fatalf("building it read %d times", src.reads)
	}
	tenant := func() string {
		t.Helper()
		got, err := lb.Tenant(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got.ID
	}
	if got := tenant(); got != "tenant-key-1" {
		t.Errorf("tenant = %s", got)
	}
	tenant()
	if src.reads != 1 || opened != 1 {
		t.Fatalf("two calls within the TTL: %d reads, %d opens; want 1, 1", src.reads, opened)
	}

	now = now.Add(lazy.TTL)
	tenant()
	if src.reads != 2 || opened != 1 {
		t.Errorf("an unchanged key after the TTL: %d reads, %d opens; want 2, 1", src.reads, opened)
	}
	src.values["workspaces/acme/key"] = "key-2"
	now = now.Add(lazy.TTL)
	if got := tenant(); got != "tenant-key-2" || opened != 2 {
		t.Errorf("a rotated key after the TTL: tenant %s, %d opens", got, opened)
	}

	delete(src.values, "workspaces/acme/key")
	now = now.Add(lazy.TTL)
	if _, err := lb.Tenant(ctx); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("a missing key = %v, want the secret's absence", err)
	}
}

// A declared workspace the store already holds as declared is attached without
// a read; one that is new, or declared differently, is adopted now.
func TestADeclaredWorkspaceTheStoreHoldsIsAttachedWithoutReadingItsKey(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	store := hub.NewMemoryStore()
	if err := store.Put(ctx, hub.Workspace{
		ID: "acme.example", Backend: "google", Admin: "admin@acme.example", Declared: true,
		Serve: []string{"acme.example"}, SyncGroups: []string{"all@acme.example"},
	}); err != nil {
		t.Fatal(err)
	}
	directory := hub.New(store, nil, hub.Config{}, log)
	src := &countingSource{values: map[string]string{"workspaces/acme/key": "unparseable"}}
	declared := func(serve ...string) []config.DirectoryWorkspace {
		return []config.DirectoryWorkspace{{
			ID: "acme.example", Backend: "google", Admin: "Admin@acme.example", KeySecret: "workspaces/acme/key",
			Serve: serve, SyncGroups: []string{"all@acme.example"},
		}}
	}

	adopted, err := adoptDeclared(ctx, directory, store, declared("ACME.example"), src, log)
	if err != nil || !adopted["acme.example"] {
		t.Fatalf("adoptDeclared = %v, %v", adopted, err)
	}
	if src.reads != 0 {
		t.Errorf("attaching read the key %d times", src.reads)
	}

	// Declared differently: adopted, which needs the key.
	if _, err = adoptDeclared(ctx, directory, store, declared("acme.example", "other.example"), src, log); err == nil {
		t.Error("a changed declaration was attached on the stored record")
	}
	if src.reads != 1 {
		t.Errorf("adopting a changed declaration read the key %d times, want 1", src.reads)
	}
}
