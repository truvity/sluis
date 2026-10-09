package store

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

func TestSecretsLayoutPutsThePortOverV4(t *testing.T) {
	ctx := context.Background()
	var opened struct {
		prefix string
		opts   state.Options
	}
	open := func(_ context.Context, prefix string, opts ...state.Option) (state.Store, error) {
		opened.prefix, opened.opts = prefix, state.ResolveOptions(state.Options{}, opts...)
		return statememory.New(), nil
	}
	base := Config{
		SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsRegion: "eu-west-1",
		SecretsEndpoint: "http://localhost:4566", SecretsGrace: time.Hour, OpenState: open,
		secrets: &port.Choice{Adapter: "ssm"},
	}

	// The default layout and an explicit v4 are the same.
	for _, layout := range []string{"", "v4"} {
		c := base
		c.SecretsLayout = layout
		c.v4 = &v4Holder{}
		got, err := c.overLayout(ctx, c.SecretsRoot, c.SecretsKMSKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*secretstore.Secrets); !ok {
			t.Fatalf("%q: %T", layout, got)
		}
		if c.v4.stores == nil {
			t.Fatalf("%q: no stores", layout)
		}
		if opened.prefix != "/sluis/example" || opened.opts.Region != "eu-west-1" || opened.opts.Endpoint != "http://localhost:4566" {
			t.Fatalf("%q: opened %q %+v", layout, opened.prefix, opened.opts)
		}
	}

	// Layouts that are gone, or never were, are refused.
	for _, layout := range []string{"v3", "transition", "v9"} {
		c := base
		c.SecretsLayout = layout
		if _, err := c.overLayout(ctx, c.SecretsRoot, c.SecretsKMSKey); err == nil {
			t.Errorf("layout %q was accepted", layout)
		}
	}
}
