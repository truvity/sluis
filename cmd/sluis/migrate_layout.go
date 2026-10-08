package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/secretstore"
	"github.com/truvity/sluis/internal/store"
	ssmstate "github.com/truvity/sluis/storage/state/ssm"
)

const secretsLayoutUsage = `Usage: sluis migrate secrets-layout --config <sluis.yaml> --to v4 [--dry-run]
       sluis migrate secrets-layout --config <sluis.yaml> --delete-v3 [--dry-run]

Moves an installation's secrets from layout v3 (<root>/private/...) to layout v4
(<root>/internal/... and <root>/external/<kind>/<id>, docs/decisions/0041).

--to v4 copies every v3 item to its v4 address: the operator-seeded config
parameters and the credentials to internal/, a confidential client's secret to the
oidc/v1 document, an installed runner App (and a catalogue App with export: true)
to the github/v1 document, a Slack App's bot token to the slack/v1 document. Each is
read back and compared. The command is idempotent and resumable: run it again after
a failure and it writes what is missing. An address that already holds a different
value is a refusal naming both revisions, and nothing is written. v3 is not touched.
Run it while secrets.layout is transition, then set secrets.layout: v4.

--delete-v3 removes the v3 items, and is a separate, later step: it refuses unless
the installation's secrets.layout is already v4 and every v3 item has its v4 address.
The exports copies (<root>/export/...) are retired: they are not copied, and --delete-v3 removes them.

--dry-run prints the addresses and what would happen, never a value.
The report is JSON on stdout. The AWS credentials are the SDK's own.

`

func migrateSecretsLayout(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("sluis migrate secrets-layout", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { _, _ = fmt.Fprint(out, secretsLayoutUsage); fs.PrintDefaults() }
	var file, to string
	var del, dry bool
	fs.StringVar(&file, "config", "", "the installation's configuration file (the document 'sluis serve' reads)")
	fs.StringVar(&to, "to", "", "the layout to move to: v4")
	fs.BoolVar(&del, "delete-v3", false, "delete the v3 items once the installation runs on v4")
	fs.BoolVar(&dry, "dry-run", false, "print the addresses and what would happen; write and delete nothing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch {
	case file == "" || fs.NArg() > 0:
		return fmt.Errorf("%w: sluis migrate secrets-layout takes --config <file> and flags only", errUsage)
	case del && to != "":
		return fmt.Errorf("%w: --to v4 and --delete-v3 are separate steps; run them one after the other", errUsage)
	case !del && to != "v4":
		return fmt.Errorf("%w: give --to v4 (or --delete-v3)", errUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	one, err := config.Load[config.Sluis](file)
	if err != nil {
		return err
	}
	cfg := &one.Serve
	if cfg.Secrets == nil || cfg.Secrets.Source != "ssm" {
		return fmt.Errorf("%s: secrets.source is not ssm: only an ssm installation has layouts", file)
	}
	pol, err := config.PolicyOf(one, nil)
	if err != nil {
		return err
	}
	sc, err := store.FromServe(cfg)
	if err != nil {
		return err
	}
	// The plain layout-v3 port, whatever layout the document says: this is the
	// tree being moved.
	layout := cfg.Secrets.Layout
	sc.SecretsLayout = ""
	st, err := store.Open(ctx, sc, log)
	if err != nil {
		return err
	}
	defer st.Close()
	dest, err := secretstore.Open(ctx, cfg.Secrets, ssmstate.Open)
	if err != nil {
		return err
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if cfg.Secrets.Region != "" {
		loaders = append(loaders, awsconfig.WithRegion(cfg.Secrets.Region))
	}
	acfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return fmt.Errorf("ssm: %w", err)
	}
	api := awsssm.NewFromConfig(acfg, func(o *awsssm.Options) {
		if cfg.Secrets.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Secrets.Endpoint)
		}
	})
	o := migrate.SecretsLayoutOptions{
		V3: st.Ports.Secrets, State: st.Ports.State, Dest: dest, DryRun: dry, Layout: layout,
		Config:            migrate.SSMConfig{API: api, Root: cfg.Secrets.Root, KMSKeyID: cfg.Secrets.KMSKeyID},
		ExportedGitHubApp: pol.GitHubCatalogue().Exported,
	}
	var report *migrate.SecretsLayoutReport
	if del {
		report, err = migrate.DeleteV3(ctx, o)
	} else {
		report, err = migrate.MoveSecretsLayout(ctx, o)
	}
	if report != nil {
		_, _ = out.Write(report.JSON())
	}
	return err
}
