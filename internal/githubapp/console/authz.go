package console

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/consoleauthz"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// The handlers authorize on the snapshot of who the console vouched for, which
// they carry in the context: the same gates as the console's own, from
// internal/consoleauthz, with their refusals as the Connect errors the handlers
// have always returned.

type callerKey struct{}

// WithCaller is ctx with the snapshot of who the console authorized.
func WithCaller(ctx context.Context, c consolecall.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerOf(ctx context.Context) consolecall.Caller {
	c, _ := ctx.Value(callerKey{}).(consolecall.Caller)
	return c
}

// identityFrom is the identity of the snapshot, when it names one.
func identityFrom(ctx context.Context) (access.Identity, bool) {
	c := callerOf(ctx)
	if c.Actor == "" {
		return access.Identity{}, false
	}
	return c.Identity(), true
}

// connectErr is a refusal of internal/consoleauthz as a Connect error.
func connectErr(err error) error {
	var e *modcall.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &e) && e.Code == consoleauthz.CodeUnauthenticated:
		return connect.NewError(connect.CodeUnauthenticated, errors.New(e.Message))
	case errors.As(err, &e) && e.Code == modcall.CodeBadRequest:
		return connect.NewError(connect.CodeInvalidArgument, errors.New(e.Message))
	case errors.As(err, &e) && e.Code == modcall.CodeForbidden:
		return connect.NewError(connect.CodePermissionDenied, errors.New(e.Message))
	}
	return err
}

func requireRole(ctx context.Context, want access.Role) (access.Identity, error) {
	id, err := consoleauthz.RequireRole(callerOf(ctx), want)
	return id, connectErr(err)
}

func requireAnywhere(ctx context.Context, want access.Role) (access.Identity, error) {
	id, err := consoleauthz.RequireAnywhere(callerOf(ctx), want)
	return id, connectErr(err)
}

func requireOwner(ctx context.Context, want access.Role, owner, subject string) (access.Identity, error) {
	id, err := consoleauthz.RequireOwner(callerOf(ctx), want, owner, subject)
	return id, connectErr(err)
}
