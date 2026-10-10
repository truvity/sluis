package state_test

import (
	"errors"
	"testing"

	"github.com/truvity/sluis/storage/state"
)

func TestResolveOptions(t *testing.T) {
	o := state.ResolveOptions(state.Options{Region: "r1"}, state.WithKeyAlias("alias/k"), state.WithEndpoint("http://e"), nil)
	if o.KeyAlias != "alias/k" || o.Endpoint != "http://e" || o.Region != "r1" {
		t.Fatalf("%+v", o)
	}
	if o = state.ResolveOptions(o, state.WithRegion("r2")); o.Region != "r2" {
		t.Fatalf("%+v", o)
	}
}

func TestValidateKey(t *testing.T) {
	if err := state.ValidateKey("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := state.ValidateKey("a//b"); !errors.Is(err, state.ErrInvalidKey) {
		t.Fatal(err)
	}
}

type listOnly struct{ state.Store }

func TestListAllNeedsAWalker(t *testing.T) {
	if _, err := state.ListAll(t.Context(), listOnly{}); !errors.Is(err, state.ErrNoWalk) {
		t.Fatalf("a store that cannot walk: %v", err)
	}
}
