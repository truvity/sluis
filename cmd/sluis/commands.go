package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/truvity/sluis/internal/version"
)

// errNotSplit is what a module command answers while the module has no process
// of its own: the role still runs inside `sluis serve`.
var errNotSplit = errors.New("not yet split")

const usageText = `Each command but migrate takes --config <file> and nothing else but --version and --help (a tick also
takes its target, first); migrate takes --from and --to, each a configuration file, and its own flags
(--dry-run, --overwrite, --i-have-stopped-writers: see 'sluis migrate --help'). A file is
validated against schemas/config/<command>.schema.json before anything starts. A tick runs under the
target's lease and exits 0 when another runner holds it.

A tick REFUSES to run while the leases are held in this process only (no shared State): a running
controller would not be kept off the same target. After scaling the controller to 0,
--unsafe-local-lease runs the tick anyway.`

// legacy is a command whose arguments belong to the body: urfave only routes to
// it, and the body parses its own command line as it did before the tree.
func legacy(name, usage string, body func(out io.Writer, args []string) error, out io.Writer) *cli.Command {
	return &cli.Command{
		Name:            name,
		Usage:           usage,
		SkipFlagParsing: true,
		HideHelp:        true,
		Action: func(_ context.Context, c *cli.Command) error {
			return body(out, c.Args().Slice())
		},
	}
}

// module is a command of the multi-call binary. run is today's behaviour for
// the role, or nil while the role has none of its own.
func module(name, usage string, out io.Writer, run func(out io.Writer, args []string) error) *cli.Command {
	if run == nil {
		return legacy(name, usage+" (not yet split)", func(io.Writer, []string) error {
			return fmt.Errorf("sluis %s: %w: this module has no process of its own yet; its role still runs inside `sluis serve`", name, errNotSplit)
		}, out)
	}
	return legacy(name, usage, run, out)
}

func init() {
	// `--version` printed "sluis <version>" before the tree; keep it.
	cli.VersionPrinter = func(c *cli.Command) {
		_, _ = fmt.Fprintln(c.Root().Writer, "sluis", version.String())
	}
}

func newApp(out io.Writer) *cli.Command {
	serveRun := func(o io.Writer, a []string) error { return start(o, "sluis serve", "sluis", a, serve) }
	controllerRun := func(kind string, schema string, body runner) func(io.Writer, []string) error {
		return func(o io.Writer, a []string) error { return start(o, "sluis controller "+kind, schema, a, body) }
	}
	app := &cli.Command{
		Name:           "sluis",
		Usage:          "sluis: one multi-call binary, one module per process",
		UsageText:      "sluis <command> [--config <file>]",
		Description:    usageText,
		Version:        version.String(),
		Writer:         out,
		ErrWriter:      out,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return fmt.Errorf("%w: %v", errUsage, err)
		},
		Commands: []*cli.Command{
			// The modules (docs/decisions/0071). Until a module has a process of its
			// own, the issuer role is the one process `serve` is, and github and slack
			// are the controllers' loops.
			module("issuer", "the issuer role: today the one process of `serve`", out, serveRun),
			module("console", "the console", out, nil),
			module("github", "the GitHub module: today the GitHub controller's loop alone (deprecated form)", out,
				controllerRun("github", "controller-github", controllerGitHub)),
			module("slack", "the Slack module: today the Slack controller's loop alone (deprecated form)", out,
				controllerRun("slack", "controller-slack", controllerSlack)),
			module("cloudflare", "the Cloudflare module", out, nil),
			module("google", "the Google directory module", out, nil),
			module("backup", "the scheduled backup", out, nil),
			// Today's commands, unchanged.
			legacy("serve", "the one process: the issuer, the directory hub and the console, and the controllers the document names", serveRun, out),
			legacy("controller", "(deprecated) a controller alone: github or slack", controllerCmd, out),
			legacy("tick", "one tick, once: github or slack, then the target", tickCmd, out),
			legacy("migrate", "copy the State from one storage to another: --from <config> --to <config>", func(o io.Writer, a []string) error { return migrateCmd(o, a) }, out),
			{
				Name:  "version",
				Usage: "print the version",
				Action: func(context.Context, *cli.Command) error {
					_, _ = fmt.Fprintln(out, "sluis", version.String())
					return nil
				},
			},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			if c.Args().Len() == 0 {
				_ = cli.ShowRootCommandHelp(c)
				return fmt.Errorf("%w: give a command", errUsage)
			}
			return fmt.Errorf("%w: %q is not a command", errUsage, c.Args().First())
		},
	}
	return app
}

func controllerCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: sluis controller needs a target: github or slack", errUsage)
	}
	switch args[0] {
	case "github":
		return start(out, "sluis controller github", "controller-github", args[1:], controllerGitHub)
	case "slack":
		return start(out, "sluis controller slack", "controller-slack", args[1:], controllerSlack)
	}
	return fmt.Errorf("%w: sluis controller %q: the targets are github and slack", errUsage, args[0])
}

func tickCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: sluis tick needs a kind and a target: github or slack, then the target", errUsage)
	}
	switch args[0] {
	case "github":
		return startTick(out, "sluis tick github", "controller-github", args[1:], tickGitHub)
	case "slack":
		return startTick(out, "sluis tick slack", "controller-slack", args[1:], tickSlack)
	}
	return fmt.Errorf("%w: sluis tick %q: the kinds are github and slack", errUsage, args[0])
}

// run parses args as the process's command line and runs the command.
func run(args []string, out io.Writer) error {
	// The old spellings of the two flags every command line answered.
	if len(args) > 0 && strings.TrimLeft(args[0], "-") == "help" {
		args = []string{"--help"}
	}
	if len(args) > 0 && args[0] == "-version" {
		args = []string{"--version"}
	}
	return newApp(out).Run(context.Background(), append([]string{"sluis"}, args...))
}
