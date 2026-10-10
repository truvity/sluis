package portstore_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/truvity/sluis/internal/portstore"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/secretstore/secretrec"
)

// On layout v5 the console's session key is internal/oidc/console-session-key,
// there is no Secrets port to ask for it, and every replica gets the one key.
func TestSessionKeyOnLayoutV5(t *testing.T) {
	each(t, func(t *testing.T, e env) {
		ctx := context.Background()
		set := e.open(t)
		set.Secrets = nil
		rec := secretrec.New()
		b := portstore.New(set).WithV5(secretstore.FromStoreV5(rec, ""))
		if err := b.RequireSecrets(); err != nil {
			t.Fatalf("RequireSecrets on v5 = %v", err)
		}
		if err := b.CheckSecrets(ctx); err != nil {
			t.Fatalf("CheckSecrets on v5 = %v", err)
		}
		gen := func() ([]byte, error) { return []byte("generated-key-0123456789abcdef!!"), nil }
		first, err := b.SessionKey(ctx, gen)
		if err != nil {
			t.Fatal(err)
		}
		again, err := b.SessionKey(ctx, func() ([]byte, error) { return []byte("another"), nil })
		if err != nil || !bytes.Equal(first, again) {
			t.Fatalf("the second replica read %q, %v, want the first key", again, err)
		}
		if err = b.PutSessionKey(ctx, []byte("carried-over")); err != nil {
			t.Fatal(err)
		}
		if got, _ := b.SessionKey(ctx, gen); string(got) != "carried-over" {
			t.Errorf("after PutSessionKey = %q", got)
		}
		for _, op := range []string{"get", "put"} {
			if got := rec.Addresses(op); !slices.Equal(got, []string{"internal/oidc/console-session-key"}) {
				t.Errorf("%s touched %v", op, got)
			}
		}
	})
}
