package rosterapp

import (
	"context"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/modcall/dial"
)

// MintDeadline bounds one installation-token mint through the GitHub module:
// a token request is a sign-in-class call, and a mint that has not answered by
// then is the issuer's refusal, not a request held open.
const MintDeadline = 10 * time.Second

// dialMinter is the issuer's [githubtokens.Minter] when the GitHub module is
// another function: a [githubtokens.Client] dialled to it as the caller class
// `issuer`, the only class the mint answers, with a [MintDeadline] per call.
func dialMinter(ctx context.Context, cfg Config, r config.ModuleRemote) (githubtokens.Minter, error) {
	routing := dial.FromTargets(map[string]config.ModuleRemote{githubtokens.Module: r})
	var caller modcall.Caller
	var err error
	if cfg.Dial != nil {
		caller, err = cfg.Dial(ctx, routing, githubtokens.CallerIssuer)
	} else {
		caller, err = dial.Dial(ctx, routing, githubtokens.CallerIssuer, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("github.remote: %w", err)
	}
	return githubtokens.NewClient(withDeadline{caller, MintDeadline}), nil
}

// withDeadline bounds every call it makes.
type withDeadline struct {
	modcall.Caller
	d time.Duration
}

func (w withDeadline) Call(ctx context.Context, module, method string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, w.d)
	defer cancel()
	return w.Caller.Call(ctx, module, method, payload)
}

// dialConsole is the GitHub module as the console calls it: as the caller class
// `console`, so the module answers the console's methods and nothing else. The
// calls carry their own deadlines.
func dialConsole(ctx context.Context, cfg Config, r config.ModuleRemote) (modcall.Caller, error) {
	routing := dial.FromTargets(map[string]config.ModuleRemote{githubtokens.Module: r})
	var caller modcall.Caller
	var err error
	if cfg.Dial != nil {
		caller, err = cfg.Dial(ctx, routing, consolecall.Class)
	} else {
		caller, err = dial.Dial(ctx, routing, consolecall.Class, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("github.remote: %w", err)
	}
	return caller, nil
}
