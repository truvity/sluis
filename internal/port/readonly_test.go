package port_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/port/memory"
)

func TestAReadOnlyViewReadsAndRefusesEveryWrite(t *testing.T) {
	ctx := context.Background()
	set := memory.New().Set()
	if _, err := set.State.Put(ctx, "k", []byte("v"), time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Blob.Write(ctx, "b", []byte("v")); err != nil {
		t.Fatal(err)
	}
	state, blob := port.ReadOnlyState(set.State), port.ReadOnlyBlob(set.Blob)
	if rec, err := state.Get(ctx, "k"); err != nil || string(rec.Value) != "v" {
		t.Fatalf("read: %v %v", rec, err)
	}
	if o, err := blob.Read(ctx, "b"); err != nil || string(o.Body) != "v" {
		t.Fatalf("read blob: %v", err)
	}
	rev, _ := set.State.Get(ctx, "k")
	for name, err := range map[string]error{
		"put":      func() error { _, e := state.Put(ctx, "k", nil, time.Hour); return e }(),
		"create":   func() error { _, e := state.Create(ctx, "n", nil, time.Hour); return e }(),
		"update":   func() error { _, e := state.Update(ctx, "k", nil, time.Hour, rev.Revision); return e }(),
		"delete":   state.Delete(ctx, "k"),
		"deleteIf": state.DeleteIfRevision(ctx, "k", rev.Revision),
		"write":    func() error { _, e := blob.Write(ctx, "b", nil); return e }(),
		"writeIf":  func() error { _, e := blob.WriteIfVersion(ctx, "b", nil, "x"); return e }(),
		"bdelete":  blob.Delete(ctx, "b"),
	} {
		if !errors.Is(err, port.ErrNotOwner) {
			t.Errorf("%s = %v, want ErrNotOwner", name, err)
		}
	}
	if _, err := set.State.Get(ctx, "k"); err != nil {
		t.Errorf("a refused write changed the table: %v", err)
	}
}
