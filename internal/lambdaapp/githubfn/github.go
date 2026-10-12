// Package githubfn is the GitHub module's function: the organisations' team
// passes and the links pass, on a schedule (`tick`) or when the console asks
// (`run`), and the installation-token mint, the `github` module's
// `mint_installation` method, which the issuer calls over modcall through its
// `live-issuer` alias and no other class may (D-v176-7 a: one function serves
// both, with the concurrency limit and the mint deadline of the one). It has no
// HTTP surface.
// It registers itself with internal/lambdaapp; cmd/sluis-github and the lambda
// build of cmd/sluis import it.
package githubfn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	githubcontroller "github.com/truvity/sluis/internal/githubroster/controller"
	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port/invoke"
)

func init() { lambdaapp.Register(lambdaapp.ModuleGitHub, open) }

// Kinds are the kinds of secret the module's check reads. The module declares
// none: an App's credential is made at run time, and the policy's client
// secrets and the directory's keys are the issuer's and the Google module's.
var Kinds []string

// Handler is the function's entry: tick and run events for the targets
// declared says are the policy's, opening one pass per invocation; check is
// the {"kind":"check"} answer; rpc answers the module calls (the mint), or is
// nil for a function that serves none. It answers no HTTP request.
func Handler(log *slog.Logger, declared func(target string) bool, open func(context.Context) (lambdaapp.Pass, error),
	rpc *modcall.Server, check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	kindsOf := func(target string) []string {
		if declared(target) {
			return []string{invoke.KindGitHub}
		}
		return nil
	}
	controllers := map[string]*lambdaapp.Controller{
		invoke.KindGitHub: {Name: "github", Log: log, Open: open,
			Unknown: func(err error) bool { return errors.Is(err, githubcontroller.ErrUnknownTarget) }},
	}
	h := lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithControllers(kindsOf, controllers).WithCheck(check)
	if rpc != nil {
		h = h.WithRPC(rpc)
	}
	return h
}

// CheckFile is the {"kind":"check"} answer of the function whose document is in
// file: the adapters a pass and the mint build (State, the blobs, the secrets,
// the trigger, the audit sink), built from the document and refused with the
// setting that is missing; then the secrets it declares. Nothing it reaches
// mutates.
func CheckFile(file string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		cfg, err := githubapp.Load(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		doc, err := config.LoadConfig[config.ControllerGitHub](file, nil)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return lambdaapp.CheckAdapters(func(ctx context.Context) error { return cfg.Verify(ctx, nil) },
			lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy, Kinds...))(ctx)
	}
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := githubapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "github-controller", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	// The mint holds its ports for the life of the function; a pass opens its own.
	minter, err := githubapp.NewMinter(ctx, cfg, log)
	if err != nil {
		flush(ctx)
		return nil, err
	}
	// And the console's GitHub operations, on the same server (the console class).
	if err = minter.ServeConsole(ctx, cfg, log); err != nil {
		minter.Close()
		flush(ctx)
		return nil, err
	}
	return &lambdaapp.Function{
		Handler: Handler(log, cfg.Declares, func(ctx context.Context) (lambdaapp.Pass, error) {
			// Assembled per invocation and closed at its end (lambdaapp.Controller):
			// the audit queue is flushed by closing it.
			app, err := githubapp.New(ctx, cfg, log)
			if err != nil {
				return nil, err
			}
			return app, nil
		}, minter.RPC(), CheckFile(file)),
		Flush: flush,
		Close: minter.Close,
	}, nil
}
