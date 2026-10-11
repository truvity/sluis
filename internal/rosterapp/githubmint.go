package rosterapp

import (
	"context"
	"fmt"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/githubapp/githubtokens"
	googleconsole "github.com/truvity/sluis/internal/googleapp/console"
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

// SignInDeadline bounds a call the issuer makes to the Google module while
// somebody is signing in, `google.sign_in_url` and `google.identify`: a cold
// function can take several seconds to open its storage, and a browser is
// waiting on it. ConsoleCallDeadline bounds the others.
const (
	SignInDeadline      = 25 * time.Second
	ConsoleCallDeadline = 55 * time.Second
)

// dialGoogle reaches the Google module in another function: the issuer's calls
// (the sign-in, as the class `issuer`) and the console's (as `console`). The
// module is the holder of the OAuth client secret and the keys; this process
// asks it.
func dialGoogle(ctx context.Context, cfg Config, r config.ModuleRemote) (issuerCaller, consoleCaller modcall.Caller, err error) {
	routing := dial.FromTargets(map[string]config.ModuleRemote{moduleGoogle: r})
	open := func(class string) (modcall.Caller, error) {
		if cfg.Dial != nil {
			return cfg.Dial(ctx, routing, class)
		}
		return dial.Dial(ctx, routing, class, nil)
	}
	if issuerCaller, err = open(googleconsole.CallerIssuer); err != nil {
		return nil, nil, fmt.Errorf("google.remote: %w", err)
	}
	if consoleCaller, err = open(consolecall.Class); err != nil {
		return nil, nil, fmt.Errorf("google.remote: %w", err)
	}
	return withDeadline{issuerCaller, SignInDeadline}, withDeadline{consoleCaller, ConsoleCallDeadline}, nil
}
