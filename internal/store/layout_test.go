package store

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	portmemory "github.com/truvity/sluis/internal/port/memory"
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
	v3 := portmemory.NewSecrets()
	base := Config{
		SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsRegion: "eu-west-1",
		SecretsEndpoint: "http://localhost:4566", SecretsGrace: time.Hour, OpenState: open,
		secrets: &port.Choice{Adapter: "ssm"},
	}

	// v3 (the default): the adapter itself, nothing opened.
	got, err := base.overLayout(ctx, v3)
	if err != nil || got != port.Secrets(v3) || opened.prefix != "" {
		t.Fatalf("v3: %T, %v, opened %q", got, err, opened.prefix)
	}

	for _, layout := range []string{config.SecretsLayoutTransition, config.SecretsLayoutV4} {
		c := base
		c.SecretsLayout = layout
		c.v4 = &v4Holder{}
		got, err := c.overLayout(ctx, v3)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*secretstore.Secrets); !ok {
			t.Fatalf("%s: %T", layout, got)
		}
		if c.v4.stores == nil || string(c.v4.stores.Layout) != layout {
			t.Fatalf("%s: stores %+v", layout, c.v4.stores)
		}
		if opened.prefix != "/sluis/example" || opened.opts.Region != "eu-west-1" || opened.opts.Endpoint != "http://localhost:4566" {
			t.Fatalf("%s: opened %q %+v", layout, opened.prefix, opened.opts)
		}
	}

	// Another adapter, or a layout that is not one, is refused.
	c := base
	c.SecretsLayout = config.SecretsLayoutV4
	c.secrets = &port.Choice{Adapter: "openbao"}
	if _, err := c.overLayout(ctx, v3); err == nil {
		t.Error("v4 over the openbao adapter was accepted")
	}
	c = base
	c.SecretsLayout = "v9"
	if _, err := c.overLayout(ctx, v3); err == nil {
		t.Error("layout v9 was accepted")
	}
}
