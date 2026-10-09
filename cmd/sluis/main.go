// Command sluis is the whole product in one binary, one image and one
// chart (docs/decisions/0032), and one process: `sluis serve` is the issuer, the
// hub, the console and, beside them, the controllers its document names.
//
//	sluis serve --config <file>              the one process: the issuer, the hub, the console and its controllers
//	sluis controller github --config <file>  the GitHub controller's loop alone (deprecated)
//	sluis controller slack --config <file>   the Slack controller's loop alone (deprecated)
//	sluis tick github <target> --config <file>  one GitHub target's tick, once
//	sluis tick slack <target> --config <file>   one Slack workspace's tick, once
//	sluis migrate --from <config> --to <config>  copy the State between storages (docs/decisions/0031)
//
// The command line is a urfave/cli v3 tree (commands.go): one multi-call binary,
// one module per process (docs/decisions/0071). The module commands (issuer, console, github, slack,
// cloudflare, google, backup) run today's behaviour for their role where one exists, and answer "not yet
// split" where it does not; the commands above stay as they are.
//
// Each subcommand is configured by one file and reads nothing else (see
// internal/config). Everything a subcommand decides is assembled in its own
// package, internal/rosterapp and internal/{github,slack}roster/app; this file
// only chooses which one to start and stops it.
//
// `serve` runs every controller its service document (apiVersion
// sluis.truvity.github.io/sluis/v3) names under `controllers`, each in a loop of
// its own beside the service. `controller <kind>` is the
// same loop as a process of its own, kept for one release for a deployment that
// still runs the controllers apart; it reads either its own v2 document or the
// one document, and says it is deprecated. `tick <kind> <target>` is the unit of work the loop is made
// of (docs/decisions/0029), run once under the target's lease and then exit: an
// operator's command now, and the shape of a function that lives for one
// invocation later. A target is an organisation's login or `github:links` for
// GitHub, and a workspace's key for Slack.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/truvity/sluis/internal/config"
	githubapp "github.com/truvity/sluis/internal/githubroster/app"
	"github.com/truvity/sluis/internal/rosterapp"
	slackapp "github.com/truvity/sluis/internal/slackroster/app"
	"github.com/truvity/sluis/internal/telemetry"
)

func main() {
	err := run(os.Args[1:], os.Stderr)
	switch {
	case err == nil, errors.Is(err, context.Canceled):
	case errors.Is(err, errUsage):
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	default:
		slog.Default().ErrorContext(context.Background(), "sluis stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

// unsafeLocalLease is the opt-in of `tick` when its lease State is this
// process's own memory.
const unsafeLocalLease = "--unsafe-local-lease"

// errUsage is a command line that names no subcommand, or one that does not
// exist: reported as usage, not as a crash.
var errUsage = errors.New("usage error")

// runner is one subcommand's body: it reads the file it is given, assembles
// what it runs, and runs it until ctx ends.
type runner func(ctx context.Context, file string) error

// start reads one subcommand's command line and its retired environment, then
// runs it under the process's signals.
func start(out io.Writer, command, schema string, args []string, body runner) error {
	file, done, err := config.Command(command, schema, args, out)
	if err != nil || done {
		return err
	}
	// A retired variable that is still set is a deployment that believes it is
	// configuring something: refuse it, naming what replaces it.
	if err := config.RefuseRetired(schema, os.Environ()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return body(ctx, file)
}

// ticker is a tick's body: it reads the file it is given, assembles the same
// controller the loop runs, ticks one target once and returns.
type ticker func(ctx context.Context, file, target string, unsafeLocal bool) error

// startTick is [start] for `tick <kind> <target>`: the target comes first, then
// the flags.
func startTick(out io.Writer, command, schema string, args []string, body ticker) error {
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	// The one flag a tick adds to --config: taken out here, since the
	// configuration command line refuses any other.
	unsafeLocal := false
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool {
		if a == unsafeLocalLease {
			unsafeLocal = true
		}
		return a == unsafeLocalLease
	})
	return start(out, command, schema, args, func(ctx context.Context, file string) error {
		if target == "" {
			return fmt.Errorf("%w: %s needs the target to tick, before the flags", errUsage, command)
		}
		return body(ctx, file, target, unsafeLocal)
	})
}

// logger builds the process's JSON logger at the level its file chose, and
// starts telemetry under service. The returned function flushes the last
// pass's metrics, bounded: never a hung stop.
//
// service is the name each of the three binaries reported before they were one
// (access-issuer, github-roster, slack-roster), so a dashboard or an alert that
// selects on it still finds the process.
func logger(ctx context.Context, service string, level slog.Level) (*slog.Logger, func(), error) {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	shutdown, err := telemetry.Start(ctx, service, log)
	if err != nil {
		return nil, nil, err
	}
	return log, func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(flush); err != nil {
			log.WarnContext(ctx, "metrics could not be flushed", slog.Any("error", err))
		}
	}, nil
}

func serve(ctx context.Context, file string) error {
	cfg, err := rosterapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := logger(ctx, "access-issuer", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()

	service, err := rosterapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer service.Close()
	return service.Run(ctx)
}

// deprecatedController is what a controller that runs apart says at start.
const deprecatedController = "running a controller as a process of its own is deprecated and goes in the next release: " +
	"`sluis serve` runs the controllers its document names under `controllers` (apiVersion sluis.truvity.github.io/sluis/v3)"

func controllerGitHub(ctx context.Context, file string) error {
	cfg, err := githubapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := logger(ctx, "github-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	log.WarnContext(ctx, deprecatedController, slog.String("command", "sluis controller github"))

	controller, err := githubapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeEmitter(log, controller)
	return controller.Run(ctx)
}

func controllerSlack(ctx context.Context, file string) error {
	cfg, err := slackapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := logger(ctx, "slack-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()
	log.WarnContext(ctx, deprecatedController, slog.String("command", "sluis controller slack"))

	controller, err := slackapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeEmitter(log, controller)
	return controller.Run(ctx)
}

func tickGitHub(ctx context.Context, file, target string, unsafeLocal bool) error {
	cfg, err := githubapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := logger(ctx, "github-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()

	controller, err := githubapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeEmitter(log, controller)
	return controller.Tick(ctx, target, unsafeLocal)
}

func tickSlack(ctx context.Context, file, target string, unsafeLocal bool) error {
	cfg, err := slackapp.Load(file)
	if err != nil {
		return err
	}
	log, flush, err := logger(ctx, "slack-roster", cfg.LogLevel())
	if err != nil {
		return err
	}
	defer flush()

	controller, err := slackapp.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeEmitter(log, controller)
	return controller.Tick(ctx, target, unsafeLocal)
}

func closeEmitter(log *slog.Logger, c interface{ Close() error }) {
	if err := c.Close(); err != nil {
		log.WarnContext(context.Background(), "the audit emitter could not be closed cleanly; what its queue held is dropped", slog.Any("error", err))
	}
}
