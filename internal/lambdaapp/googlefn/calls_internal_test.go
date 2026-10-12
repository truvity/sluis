package googlefn

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	googleapp "github.com/truvity/sluis/internal/googleroster/app"
)

// The module a call opens outlives the call, and the Google clients it opens
// fetch their tokens with the context they were opened with. Lambda cancels an
// invocation's context when the invocation returns, so an app opened with it
// could not authenticate on any later call of the container: the first console
// action that reached Google after another call opened the app failed.
func TestTheModuleACallOpensDoesNotDieWithTheCall(t *testing.T) {
	type key struct{}
	var opened context.Context
	c := &calls{log: slog.Default(), start: func(ctx context.Context, _ googleapp.Config, _ *slog.Logger) (*googleapp.App, error) {
		opened = ctx
		return nil, errors.New("stop here")
	}}
	call, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "kept"))
	if _, err := c.service(call); err == nil {
		t.Fatal("the stand-in's error was swallowed")
	}
	cancel()
	if opened == nil {
		t.Fatal("the module was not opened")
	}
	if err := opened.Err(); err != nil {
		t.Errorf("the module was opened with the call's cancellation: %v", err)
	}
	if opened.Value(key{}) != "kept" {
		t.Error("the call's values (trace, logger) were dropped")
	}
}
