package rosterapp

import (
	"context"
	"fmt"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

// dialConsole is the console's Caller for a provider module that is another
// function: dialled to it as the caller class `console`, the only class its
// console methods answer, with room for a page that lists. The console sends
// each call with the deadline it chooses.
func dialConsole(ctx context.Context, cfg Config, module string, r config.ModuleRemote) (modcall.Caller, error) {
	routing := dial.FromTargets(map[string]config.ModuleRemote{module: r})
	target := routing.Modules[module]
	target.MaxBytes = consolecall.ListMaxBytes
	routing.Modules[module] = target
	var caller modcall.Caller
	var err error
	if cfg.Dial != nil {
		caller, err = cfg.Dial(ctx, routing, consolecall.Class)
	} else {
		caller, err = dial.Dial(ctx, routing, consolecall.Class, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("%s.remote: %w", module, err)
	}
	return caller, nil
}
