package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/deploycheck"
	"github.com/truvity/sluis/internal/module"
	"github.com/truvity/sluis/internal/module/cloudflare"
	"github.com/truvity/sluis/internal/module/github"
	"github.com/truvity/sluis/internal/module/issuer"
	"github.com/truvity/sluis/internal/module/slack"
	"github.com/truvity/sluis/internal/version"
)

// errNotSplit is what a module command answers while the module has no process
// of its own: the role still runs inside `sluis serve`.
var errNotSplit = module.ErrNotSplit

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

// moduleCmd is the command of one module (docs/decisions/0071): `sluis <name>
// --config <file>` runs it, `sluis <name> tick <target> --config <file>` ticks
// it once. schema is the document the module reads.
func moduleCmd(m module.Module, schema, usage string, out io.Writer) *cli.Command {
	return legacy(m.Name(), usage, func(o io.Writer, args []string) error {
		// A module with no process of its own refuses before it reads a file.
		if u, ok := m.(module.Unsplit); ok {
			return u.Run(context.Background(), "")
		}
		command := "sluis " + m.Name()
		if len(args) > 0 && args[0] == "tick" {
			return startTick(o, command+" tick", schema, args[1:], m.Tick)
		}
		return start(o, command, schema, args, m.Run)
	}, out)
}

func init() {
	// `--version` printed "sluis <version>" before the tree; keep it.
	cli.VersionPrinter = func(c *cli.Command) {
		_, _ = fmt.Fprintln(c.Root().Writer, "sluis", version.String())
	}
}

func newApp(out io.Writer) *cli.Command {
	serveRun := func(o io.Writer, a []string) error { return start(o, "sluis serve", "sluis", a, issuer.Module{}.Run) }
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
			// own, the issuer role is the one process `serve` is; github and slack are
			// the controllers' loops and cloudflare is the minter's rotation.
			moduleCmd(issuer.Module{}, "sluis", "the issuer role: today the one process of `serve`", out),
			moduleCmd(module.Unsplit("console"), "sluis", "the console (not yet split)", out),
			moduleCmd(github.Module{}, "controller-github", "the GitHub module: the controller's loop, or `tick <target>` once", out),
			moduleCmd(slack.Module{}, "controller-slack", "the Slack module: the controller's loop, or `tick <target>` once", out),
			moduleCmd(cloudflare.Module{}, "sluis", "the Cloudflare module: the STS minter's rotation loop, or `tick <preset>` once", out),
			moduleCmd(module.Unsplit("google"), "sluis", "the Google directory module (not yet split)", out),
			legacy("backup", "the backup module: run, list, status or prune (sluis backup --help)", backupCmd, out),
			// Today's commands, unchanged.
			legacy("serve", "the one process: the issuer, the directory hub and the console, and the controllers the document names", serveRun, out),
			legacy("controller", "(deprecated) a controller alone: github or slack", controllerCmd, out),
			legacy("tick", "one tick, once: github or slack, then the target", tickCmd, out),
			legacy("check", "verify that every secret the document and its policy declare is in SSM: names and versions, never values", checkCmd, out),
			legacy("migrate", "copy the State from one storage to another: --from <config> --to <config>", migrateCmd, out),
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

// checkCmd is `sluis check --config <file>`: one read of each declared secret
// from the SSM the document's `secrets` section names, and a verdict per name.
// It prints addresses and versions and nothing else, and exits non-zero when
// one is missing, empty or (the state secret) malformed. A deploy runs it
// before the first request would find out (docs/reference/sluis/secrets.md).
func checkCmd(out io.Writer, args []string) error {
	return start(out, "sluis check", "sluis", args, func(ctx context.Context, file string) error {
		c, err := config.LoadConfig[config.Sluis](file, nil)
		if err != nil {
			return err
		}
		rep, err := deploycheck.Check(ctx, &c.Service.Serve, c.Policy)
		if err != nil {
			return err
		}
		rep.Write(out)
		return rep.Err()
	})
}

func controllerCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: sluis controller needs a target: github or slack", errUsage)
	}
	switch args[0] {
	case "github":
		return start(out, "sluis controller github", "controller-github", args[1:], github.Module{Deprecated: "sluis controller github"}.Run)
	case "slack":
		return start(out, "sluis controller slack", "controller-slack", args[1:], slack.Module{Deprecated: "sluis controller slack"}.Run)
	}
	return fmt.Errorf("%w: sluis controller %q: the targets are github and slack", errUsage, args[0])
}

func tickCmd(out io.Writer, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%w: sluis tick needs a kind and a target: github or slack, then the target", errUsage)
	}
	switch args[0] {
	case "github":
		return startTick(out, "sluis tick github", "controller-github", args[1:], github.Module{}.Tick)
	case "slack":
		return startTick(out, "sluis tick slack", "controller-slack", args[1:], slack.Module{}.Tick)
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
