package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/truvity/sluis/internal/valkey"
)

func TestSwapAndDropIfCompareTheRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	state := valkey.NewState(client, "test")

	if out, err := state.Swap(ctx, "k", valkey.Revision([]byte("a")), []byte("b"), time.Minute); err != nil || out != valkey.Gone {
		t.Fatalf("swap of an absent key = %v, %v; want Gone", out, err)
	}
	if out, err := state.DropIf(ctx, "k", valkey.Revision([]byte("a"))); err != nil || out != valkey.Gone {
		t.Fatalf("drop of an absent key = %v, %v; want Gone", out, err)
	}

	if err := state.Set(ctx, "k", []byte("a"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if out, err := state.Swap(ctx, "k", valkey.Revision([]byte("other")), []byte("b"), time.Minute); err != nil || out != valkey.Moved {
		t.Fatalf("swap from a stale revision = %v, %v; want Moved", out, err)
	}
	if got, _ := server.Get("test:k"); got != "a" {
		t.Fatalf("a refused swap wrote %q", got)
	}
	if out, err := state.Swap(ctx, "k", valkey.Revision([]byte("a")), []byte("b"), time.Minute); err != nil || out != valkey.Done {
		t.Fatalf("swap from the live revision = %v, %v; want Done", out, err)
	}
	if got, _ := server.Get("test:k"); got != "b" {
		t.Fatalf("value after swap = %q; want b", got)
	}

	if out, err := state.DropIf(ctx, "k", valkey.Revision([]byte("a"))); err != nil || out != valkey.Moved {
		t.Fatalf("drop at a stale revision = %v, %v; want Moved", out, err)
	}
	if out, err := state.DropIf(ctx, "k", valkey.Revision([]byte("b"))); err != nil || out != valkey.Done {
		t.Fatalf("drop at the live revision = %v, %v; want Done", out, err)
	}
	if server.Exists("test:k") {
		t.Fatal("key survived a drop")
	}
}
