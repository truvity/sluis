//go:build !lambda

package dial

import (
	"context"
	"fmt"

	"github.com/truvity/sluis/internal/modcall"
)

// lambdaCaller refuses: this build carries no Lambda client, so a module named
// by function cannot be reached from it.
func lambdaCaller(_ context.Context, cfg modcall.Config, _ string) (modcall.Caller, error) {
	return nil, fmt.Errorf("modcall: modules %v are Lambda functions, and this build has no Lambda transport", Functions(cfg))
}
