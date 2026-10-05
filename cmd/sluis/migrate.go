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
	"strings"
	"syscall"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
	"github.com/truvity/sluis/internal/version"
)

const migrateCommand = "sluis migrate"

// migrateFlags is the whole command line of `migrate`. It is the one command
// that takes flags beside its files: it reads two configurations, and what it
// does with them (a dry run, an overwrite, the operator's statement that the
// writers are stopped) is a decision of this run and not of either file.
type migrateFlags struct {
	from, to, skip, blobs, reportBlob          string
	dryRun, overwrite, writersStopped, version bool
	// withSessions also copies the issuer's logins in progress.
	withSessions bool
	// kubeconfig, kubeContext and namespace say where the source's namespace
	// is when this runs from a workstation and not in the cluster.
	kubeconfig, kubeContext, namespace string
}

func migrateUsage(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprint(out, `Usage: sluis migrate --from <config> --to <config> [flags]

Copies the State of one installation's storage to another's, through the domain
stores and the ports (docs/decisions/0031, docs/operations/migrate.md). Each side is
the configuration file of 'sluis serve' for that storage: the ports.* keys
(and, for the legacy storage, store, release and valkey) say where it is. Domain
records and secrets are copied through their stores, so a secret is written to the
destination's Secrets; the issuer's sessions, refresh tokens and keyring schedule are
copied with the lifetime each has left.

To migrate a whole installation to AWS, --from is the legacy serve configuration
(store: kubernetes, and valkey.address if its keyring is to be copied: a port-forward
from a workstation) and --to a serve configuration with ports.adapter: dynamodb,
the ssm secrets adapter and an s3 blob. State, secrets, the controllers' reports and
the issuer's key ring schedule are copied; the issuer's sessions, refresh tokens and
codes in flight (people sign in again) and the Index sets are not, unless
--with-sessions. docs/operations/runbook.md, "Cutover: migrating an installation".

Nothing is written unless the source is quiet: stop every writer (scale the issuer,
the console and both controllers to 0) and pass --i-have-stopped-writers. The report is
JSON on stdout (counts and the keys of anything wrong, never a value); the log is on
stderr. The exit status is 0 only when everything copied and verified.

`)
	fs.PrintDefaults()
}

func parseMigrate(args []string, out io.Writer) (migrateFlags, bool, error) {
	var f migrateFlags
	fs := flag.NewFlagSet(migrateCommand, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { migrateUsage(out, fs) }
	fs.StringVar(&f.from, "from", "", "the source: the configuration file of the storage to read")
	fs.StringVar(&f.to, "to", "", "the destination: the configuration file of the storage to write")
	fs.BoolVar(&f.dryRun, "dry-run", false, "plan and report what would be copied, with counts per domain and kind; touch nothing")
	fs.BoolVar(&f.overwrite, "overwrite", false,
		"replace what the destination holds when it differs (default: a different value fails the run, naming the key, before anything is written)")
	fs.BoolVar(&f.writersStopped, "i-have-stopped-writers", false,
		"the source is quiet: the issuer, the console and both controllers are scaled to 0. Required to write")
	fs.BoolVar(&f.withSessions, "with-sessions", false,
		"also copy the issuer's sessions, refresh tokens, codes in flight and Index sets (default: only the key ring's schedule)")
	fs.StringVar(&f.kubeconfig, "kubeconfig", "", "read the source's namespace through this kubeconfig, from a workstation (default: the pod's ServiceAccount)")
	fs.StringVar(&f.kubeContext, "kube-context", "", "the kubeconfig's context for the source (default: its current one)")
	fs.StringVar(&f.namespace, "namespace", "", "the source's namespace, with --kubeconfig or --kube-context (default: the context's own)")
	fs.StringVar(&f.skip, "skip", "", "domains to leave out, comma separated: "+strings.Join(migrate.AllDomains, ", "))
	fs.StringVar(&f.blobs, "blobs", string(migrate.BlobsAuto),
		"the controllers' reports: auto (copy when the two sides keep them in different places), copy or skip")
	fs.StringVar(&f.reportBlob, "report-blob", "", "also write the report to this Blob name on the destination")
	fs.BoolVar(&f.version, "version", false, "print this build's version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return f, true, nil
		}
		return f, false, err
	}
	switch {
	case f.version:
		_, _ = fmt.Fprintln(out, migrateCommand, version.String())
		return f, true, nil
	case fs.NArg() > 0:
		return f, false, fmt.Errorf("%s takes no arguments: only flags", migrateCommand)
	case f.from == "" || f.to == "":
		return f, false, fmt.Errorf("%w: give the source and the destination: --from <config> --to <config>", errUsage)
	case f.from == f.to:
		return f, false, fmt.Errorf("%w: --from and --to are the same file", errUsage)
	}
	switch migrate.BlobMode(f.blobs) {
	case migrate.BlobsAuto, migrate.BlobsCopy, migrate.BlobsSkip:
	default:
		return f, false, fmt.Errorf("%w: --blobs %q is none of auto, copy, skip", errUsage, f.blobs)
	}
	return f, false, nil
}

// migrateCmd is `sluis migrate`.
func migrateCmd(out io.Writer, args []string) error {
	if len(args) > 0 && args[0] == "ssm-layout" {
		return migrateSSMLayout(out, args[1:])
	}
	f, done, err := parseMigrate(args, out)
	if err != nil || done {
		return err
	}
	if err = config.RefuseRetired("serve", os.Environ()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The report is the stdout; the log goes to stderr.
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var kc func(string) (*kube.Client, error)
	if f.kubeconfig != "" || f.kubeContext != "" || f.namespace != "" {
		kc = func(release string) (*kube.Client, error) {
			return kube.FromKubeconfig(release, f.kubeconfig, f.kubeContext, f.namespace)
		}
	}
	from, err := openSide(ctx, f.from, log, kc)
	if err != nil {
		return fmt.Errorf("--from %s: %w", f.from, err)
	}
	defer from.Stores.Close()
	to, err := openSide(ctx, f.to, log, nil)
	if err != nil {
		return fmt.Errorf("--to %s: %w", f.to, err)
	}
	defer to.Stores.Close()

	var skip []string
	for _, s := range strings.Split(f.skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			skip = append(skip, s)
		}
	}
	report, err := migrate.Run(ctx, from, to, migrate.Options{
		DryRun: f.dryRun, Overwrite: f.overwrite, Sessions: f.withSessions, WritersStopped: f.writersStopped,
		Skip: skip, Blobs: migrate.BlobMode(f.blobs), ReportBlob: f.reportBlob, Log: log,
	})
	if report != nil {
		_, _ = out.Write(report.JSON())
		printSummary(os.Stderr, report)
	}
	return err
}

// openSide reads one configuration and opens its storage.
func openSide(ctx context.Context, file string, log *slog.Logger, kc func(string) (*kube.Client, error)) (migrate.Side, error) {
	cfg, err := config.Load[config.Serve](file)
	if err != nil {
		return migrate.Side{}, err
	}
	sc, err := store.FromServe(cfg)
	if err != nil {
		return migrate.Side{}, err
	}
	if sc.Secrets, err = secrets.Open(ctx, cfg); err != nil {
		return migrate.Side{}, err
	}
	sc.KubeClient = kc
	st, err := store.Open(ctx, sc, log)
	if err != nil {
		return migrate.Side{}, err
	}
	return migrate.Side{Name: file, Stores: st, BlobID: migrate.BlobID(sc)}, nil
}

// printSummary says, per concern, what the run found, for the operator who reads
// the terminal and not the JSON: counts and bytes, and what would be refused.
func printSummary(w io.Writer, r *migrate.Report) {
	mode := "run"
	if r.DryRun {
		mode = "dry run: nothing was written"
	}
	_, _ = fmt.Fprintf(w, "\nmigrate %s -> %s (%s -> %s), %s\n", r.From, r.To, r.FromAdapter, r.ToAdapter, mode)
	for _, c := range r.Concerns {
		_, _ = fmt.Fprintf(w, "  %-8s %6d items %10d bytes  refused %d\n", c.Concern, c.Items, c.Bytes, c.Refused)
	}
	for _, p := range r.Refused {
		_, _ = fmt.Fprintf(w, "  REFUSED %s/%s %s: %s\n", p.Domain, p.Kind, p.Key, p.Reason)
	}
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "  note: %s\n", n)
	}
	status := "ok"
	if !r.OK {
		status = "NOT ok: " + r.Error
	}
	_, _ = fmt.Fprintf(w, "  %s\n", status)
}
