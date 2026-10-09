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

	"github.com/truvity/sluis/internal/config"
)

func main() {
	if platformEntry(os.Args[1:], os.Getenv) {
		return
	}
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
