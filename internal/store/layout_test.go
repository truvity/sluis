package store

import (
	"context"
	"testing"
	"time"

	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/storage/state"
	statememory "github.com/truvity/sluis/storage/state/memory"
)

// The ssm secrets are the layout v5 stores, opened at the document's root,
// region and endpoint; there is no Secrets port over them.
func TestTheSSMSecretsAreTheLayoutV5Stores(t *testing.T) {
	ctx := context.Background()
	var opened struct {
		prefix string
		opts   state.Options
	}
	open := func(_ context.Context, prefix string, opts ...state.Option) (state.Store, error) {
		opened.prefix, opened.opts = prefix, state.ResolveOptions(state.Options{}, opts...)
		return statememory.New(), nil
	}
	c := Config{
		SecretsRoot: "/sluis/example", SecretsKMSKey: "alias/example", SecretsRegion: "eu-west-1",
		SecretsEndpoint: "http://localhost:4566", SecretsGrace: time.Hour, OpenState: open,
		SecretsSSM: true, SecretsLayout: "v5",
		secrets: &port.Choice{Adapter: "ssm"},
		held:    &secretsHolder{},
	}
	if err := c.overLayout5(ctx, c.SecretsRoot, c.SecretsKMSKey); err != nil {
		t.Fatal(err)
	}
	if c.held.v5 == nil {
		t.Fatal("no stores")
	}
	if opened.prefix != "/sluis/example" || opened.opts.Region != "eu-west-1" || opened.opts.Endpoint != "http://localhost:4566" {
		t.Fatalf("opened %q %+v", opened.prefix, opened.opts)
	}
	if v5, err := c.layout5(); err != nil || !v5 {
		t.Fatalf("layout5 = %v, %v", v5, err)
	}

	// Layouts that are gone, never were, or are not named, are refused: an empty
	// layout is no longer read as v4.
	for _, layout := range []string{"", "v4", "v3", "transition", "v9"} {
		bad := c
		bad.SecretsLayout = layout
		if _, err := bad.layout5(); err == nil {
			t.Errorf("layout %q was accepted", layout)
		}
	}
}
