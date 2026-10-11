package consolecall_test

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// mustServer is a server with a list method, for reading its registration.
func mustServer(t *testing.T) *modcall.Server {
	t.Helper()
	s := modcall.NewServer("github")
	consolecall.Handle(s, "console.list", func(context.Context, consolecall.Caller, *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		return wrapperspb.String(""), nil
	}, consolecall.List())
	return s
}
