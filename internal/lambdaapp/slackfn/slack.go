// Package slackfn is the Slack module's controller function: the workspaces'
// user-group passes, on a schedule (`tick`) or when the console asks (`run`).
// It has no HTTP surface and serves no module call. It registers itself with
// internal/lambdaapp; cmd/sluis-slack and the lambda build of cmd/sluis import
// it.
package slackfn

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
	"github.com/truvity/sluis/internal/modcall"
	"github.com/truvity/sluis/internal/port/invoke"
	slackconsole "github.com/truvity/sluis/internal/slackapp/console"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	slackcontroller "github.com/truvity/sluis/internal/slackroster/controller"
)

func init() { lambdaapp.Register(lambdaapp.ModuleSlack, open) }

// Kinds are the kinds of secret the module's check reads. The module declares
// none: a workspace's credential is made at run time, and the policy's client
// secrets and the directory's keys are the issuer's and the Google module's.
var Kinds []string

// WithCalls adds the module's calls to a handler: the server answers
// `slack.console.*` to the class `console`.
func WithCalls(h *lambdaapp.HTTP, s *modcall.Server) *lambdaapp.HTTP { return h.WithRPC(s) }

// calls is the module's console service, assembled by the first call of a
// container over the module's own stores and kept for the rest of its life. A
// pass opens its own, per invocation, and needs none of it.
type calls struct {
	cfg slackapp.Config
	log *slog.Logger

	mu  sync.Mutex
	app *slackapp.App
}

// service opens the module on the first call. A failure is not kept: the next
// call tries again. With the legacy adapter there are no records for a console.
func (c *calls) service(ctx context.Context) (*slackconsole.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.app == nil {
		app, err := slackapp.New(ctx, c.cfg, c.log)
		if err != nil {
			return nil, err
		}
		if app.Console() == nil {
			_ = app.Close()
			return nil, errors.New("this adapter keeps no records for a console")
		}
		c.app = app
	}
	return c.app.Console(), nil
}

// flush delivers the audit records the calls queued.
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

// Server is the server of the module's calls over the service open assembles.
func Server(log *slog.Logger, open func(context.Context) (*slackconsole.Service, error)) *modcall.Server {
	s := modcall.NewServer(slackconsole.Module)
	slackconsole.RegisterLazy(s, log, open)
	return s
}

// Handler is the function's entry: tick and run events for the targets
// declared says are the policy's, opening one pass per invocation; check is
// the {"kind":"check"} answer. It answers no module call and no HTTP request.
func Handler(log *slog.Logger, declared func(target string) bool, open func(context.Context) (lambdaapp.Pass, error),
	check func(context.Context) (deploycheck.Report, error)) *lambdaapp.HTTP {
	kindsOf := func(target string) []string {
		if declared(target) {
			return []string{invoke.KindSlack}
		}
		return nil
	}
	controllers := map[string]*lambdaapp.Controller{
		invoke.KindSlack: {Name: "slack", Log: log, Open: open,
			Unknown: func(err error) bool { return errors.Is(err, slackcontroller.ErrUnknownTarget) }},
	}
	return lambdaapp.NewHTTP(http.NotFoundHandler(), nil, log).WithControllers(kindsOf, controllers).WithCheck(check)
}

func open(ctx context.Context, file string) (*lambdaapp.Function, error) {
	cfg, err := slackapp.Load(file)
	if err != nil {
		return nil, err
	}
	doc, err := config.LoadConfig[config.ControllerSlack](file, nil)
	if err != nil {
		return nil, err
	}
	log, flush, err := lambdaapp.Logger(ctx, "slack-controller", cfg.LogLevel())
	if err != nil {
		return nil, err
	}
	served := &calls{cfg: cfg, log: log}
	return &lambdaapp.Function{
		Handler: WithCalls(Handler(log, cfg.Declares, func(ctx context.Context) (lambdaapp.Pass, error) {
			// Assembled per invocation and closed at its end (lambdaapp.Controller):
			// the audit queue is flushed by closing it.
			app, err := slackapp.New(ctx, cfg, log)
			if err != nil {
				return nil, err
			}
			return app, nil
		}, lambdaapp.CheckOwn(doc.Service.Secrets, doc.Policy, Kinds...)), Server(log, served.service)),
		Flush: func(ctx context.Context) {
			served.flush(ctx)
			flush(ctx)
		},
		Close: served.close,
	}, nil
}
