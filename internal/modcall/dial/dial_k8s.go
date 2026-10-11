//go:build !lambda

package dial

import (
	"context"

	"github.com/truvity/sluis/internal/modcall"
)

// lambdaCaller is none: this build carries no Lambda client, so [modcall.NewRouter]
// refuses a module named by function, at Dial rather than at its first call.
func lambdaCaller(context.Context, modcall.Config, string) (modcall.Caller, error) { return nil, nil }
