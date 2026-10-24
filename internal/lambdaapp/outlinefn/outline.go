// Package outlinefn is the Outline module's function (docs/decisions/0077): the
// workspaces' passes, on a schedule (`tick`) or when asked (`run`), and the
// console's Outline calls (`outline.console.*`). It registers itself with
// internal/lambdaapp; cmd/sluis-outline and the lambda build of cmd/sluis import
// it.
package outlinefn

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/lambdaapp"
	outlineapp "github.com/truvity/sluis/internal/outlineroster/app"
	outlineconsole "github.com/truvity/sluis/internal/outlineroster/console"
	outlinecontroller "github.com/truvity/sluis/internal/outlineroster/controller"
)

func init() { lambdaapp.Register(lambdaapp.ModuleOutline, open) }

// Kind is the controller a workspace's event names:
// {"kind":"tick","controller":"outline","target":"<key>"}.
const Kind = "outline"

// calls is the module's console service, assembled by the first call of a
// container and kept for the rest of its life. A pass opens its own.
type calls struct {
	cfg outlineapp.Config
	log *slog.Logger

	mu  sync.Mutex
	app *outlineapp.App
}

func (c *calls) service(ctx context.Context) (*outlineconsole.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app == nil {
		app, err := outlineapp.New(context.WithoutCancel(ctx), c.cfg, c.log)
		if err != nil {
			return nil, err
		}
		c.app = app
	}
	return c.app.Console(), nil
}

func (c *calls) flush(ctx context.Context) {
	c.mu.Lock()
	app := c.app
	c.mu.Unlock()
	if app == nil {
		return
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := app.Flush(fctx); err != nil {
		c.log.WarnContext(ctx, "audit records were not delivered before the response", slog.Any("error", err))
	}
}

func (c *calls) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app != nil {
		_ = c.app.Close()
		c.app = nil
	}
}

// Handler is the function's entry: tick and run events for the declared
// workspaces, one pass per invocation, and the {"kind":"check"} answer.
func Handler(log *slog.Logger, declared func(target string) bool, open func(context.Context) (lambdaapp.Pass, error),
	check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	kindsOf := func(target string) []string {
		if declared(target) {
			return []string{Kind}
		}
		return nil
	}
	controllers := map[string]*lambdaapp.Controller{
		Kind: {Name: Kind, Log: log, Open: open,
			Unknown: func(err error) bool { return errors.Is(err, outlinecontroller.ErrUnknownTarget) }},
	}
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithControllers(kindsOf, controllers).WithCheck(check)
}

// CheckFile is the {"kind":"check"} answer: the adapters a pass builds, built
// from the document and refused with the setting that is missing. Nothing it
// reaches mutates.
func CheckFile(file string) func(context.Context) (deploycheck.Report, error) {
	return func(ctx context.Context) (deploycheck.Report, error) {
		cfg, err := outlineapp.Load(file)
		if err != nil {
			return deploycheck.Report{}, err
		}
		doc, err := config.LoadConfig[config.SluisOutline](file, nil)
		if err != nil {
			return deploycheck.Report{}, err
		}
		return lambdaapp.CheckAdapters(func(ctx context.Context) error { return cfg.Verify(ctx, nil) },
			lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy))(ctx)
	}
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := outlineapp.Load(file)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "outline-controller", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	served := &calls{cfg: cfg, log: log}
	handler := Handler(log, cfg.Declares, func(ctx context.Context) (lambdaapp.Pass, error) {
		// Assembled per invocation and closed at its end: the audit queue is
		// flushed by closing it.
		return outlineapp.New(ctx, cfg, log)
	}, CheckFile(file))
	return &lambdaapp.Function{
		Handler: handler.WithRPC(outlineconsole.NewServer(log, served.service)),
		Flush: func(ctx context.Context) {
			served.flush(ctx)
			flush(ctx)
		},
		Close: served.close,
	}, nil
}
