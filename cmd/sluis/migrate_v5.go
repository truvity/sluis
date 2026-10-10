//go:build !lambda

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
	"slices"
	"strings"
	"syscall"

	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/kube"
	"github.com/truvity/sluis/internal/migrate"
	"github.com/truvity/sluis/internal/version"
)

const migrateV5Command = "sluis migrate v5"

// migrateV5Flags are the flags of `migrate v5 plan`: the source and destination
// files, which parts to plan, and where a layout v4 source on Kubernetes is. A
// plan writes nothing, so it has none of the flags that allow a write.
type migrateV5Flags struct {
	from, to, skip, blobs, sessions    string
	kubeconfig, kubeContext, namespace string
	version                            bool
}

func migrateV5Usage(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprint(out, `Usage: sluis migrate v5 plan --from <v4 config> --to <v5 config> [flags]

Reads a layout v4 installation (--from: one table, the old parameter names) and a
layout v5 one (--to: a table per module, secrets.layout: v5), and prints per module
what a copy would find: new, same, different or refused (docs/decisions/0072). It
prints names and versions and never a value, and writes nothing to either side.

The report is JSON on stdout and a summary is on stderr. The exit status is 0 only
when nothing is refused. copy and verify are the next steps of the migration and are
not part of this build.

`)
	fs.PrintDefaults()
}

func parseMigrateV5(args []string, out io.Writer) (migrateV5Flags, bool, error) {
	var f migrateV5Flags
	fs := flag.NewFlagSet(migrateV5Command+" plan", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { migrateV5Usage(out, fs) }
	fs.StringVar(&f.from, "from", "", "the source: the configuration file of the layout v4 installation")
	fs.StringVar(&f.to, "to", "", "the destination: the configuration file of the layout v5 installation")
	fs.StringVar(&f.sessions, "sessions", "copy",
		"the issuer's sessions, refresh tokens, codes in flight and Index sets: copy (plan them) or skip (plan the key ring only)")
	fs.StringVar(&f.skip, "skip", "", "domains to leave out, comma separated: "+strings.Join(migrate.AllDomains, ", "))
	fs.StringVar(&f.blobs, "blobs", string(migrate.BlobsAuto),
		"the controllers' reports: auto (plan them when the two sides keep them in different places), copy or skip")
	fs.StringVar(&f.kubeconfig, "kubeconfig", "", "read the source's namespace through this kubeconfig, from a workstation (default: the pod's ServiceAccount)")
	fs.StringVar(&f.kubeContext, "kube-context", "", "the kubeconfig's context for the source (default: its current one)")
	fs.StringVar(&f.namespace, "namespace", "", "the source's namespace, with --kubeconfig or --kube-context (default: the context's own)")
	fs.BoolVar(&f.version, "version", false, "print this build's version and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return f, true, nil
		}
		return f, false, err
	}
	switch {
	case f.version:
		_, _ = fmt.Fprintln(out, migrateV5Command, version.String())
		return f, true, nil
	case fs.NArg() > 0:
		return f, false, fmt.Errorf("%s plan takes no arguments: only flags", migrateV5Command)
	case f.from == "" || f.to == "":
		return f, false, fmt.Errorf("%w: give the source and the destination: --from <v4 config> --to <v5 config>", errUsage)
	case f.from == f.to:
		return f, false, fmt.Errorf("%w: --from and --to are the same file", errUsage)
	case f.sessions != "copy" && f.sessions != "skip":
		return f, false, fmt.Errorf("%w: --sessions %q is none of copy, skip", errUsage, f.sessions)
	}
	switch migrate.BlobMode(f.blobs) {
	case migrate.BlobsAuto, migrate.BlobsCopy, migrate.BlobsSkip:
	default:
		return f, false, fmt.Errorf("%w: --blobs %q is none of auto, copy, skip", errUsage, f.blobs)
	}
	return f, false, nil
}

// migrateV5Cmd is `sluis migrate v5`.
func migrateV5Cmd(out io.Writer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprintf(out, "Usage: %s plan --from <v4 config> --to <v5 config> [flags]\n", migrateV5Command)
		if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
			return nil
		}
		return fmt.Errorf("%w: %s takes a subcommand: plan", errUsage, migrateV5Command)
	}
	switch args[0] {
	case "plan":
		return migrateV5Plan(out, args[1:])
	case "copy", "verify":
		return fmt.Errorf("%w: %s %s is not part of this build; only plan is", errUsage, migrateV5Command, args[0])
	}
	return fmt.Errorf("%w: %s has no subcommand %q (plan)", errUsage, migrateV5Command, args[0])
}

func migrateV5Plan(out io.Writer, args []string) error {
	f, done, err := parseMigrateV5(args, out)
	if err != nil || done {
		return err
	}
	if err = config.RefuseRetired("serve", os.Environ()); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var kc func(string) (*kube.Client, error)
	if f.kubeconfig != "" || f.kubeContext != "" || f.namespace != "" {
		kc = func(release string) (*kube.Client, error) {
			return kube.FromKubeconfig(release, f.kubeconfig, f.kubeContext, f.namespace)
		}
	}
	from, _, err := openSide(ctx, f.from, log, kc)
	if err != nil {
		return fmt.Errorf("--from %s: %w", f.from, err)
	}
	defer from.Stores.Close()
	to, _, err := openSide(ctx, f.to, log, nil)
	if err != nil {
		return fmt.Errorf("--to %s: %w", f.to, err)
	}
	defer to.Stores.Close()

	src, err := readPlanInfo(f.from)
	if err != nil {
		return fmt.Errorf("--from %s: %w", f.from, err)
	}
	dst, err := readPlanInfo(f.to)
	if err != nil {
		return fmt.Errorf("--to %s: %w", f.to, err)
	}
	var skip []string
	for _, s := range strings.Split(f.skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			skip = append(skip, s)
		}
	}
	report, err := migrate.Plan(ctx, from, to, migrate.PlanOptions{
		Skip: skip, Sessions: f.sessions == "copy", Blobs: migrate.BlobMode(f.blobs),
		// The destination's policy says which Apps an organisation uses; the
		// source's which Apps are exported.
		ExportedGitHubApp: src.exported, AppRef: dst.appRef,
		ConfigNames: src.configNames, CloudflareAccounts: src.accounts,
		S3Ref: src.s3Ref, S3RefTo: dst.s3Ref, Log: log,
	})
	if report != nil {
		_, _ = out.Write(report.JSON())
		printPlanSummary(os.Stderr, report)
	}
	return err
}

// planInfo is what a configuration file says that a plan needs beyond the
// storage it opens.
type planInfo struct {
	exported    func(id string) bool
	appRef      func(org string) string
	configNames []string
	accounts    []string
	s3Ref       string
}

func readPlanInfo(file string) (planInfo, error) {
	one, err := config.Load[config.Sluis](file)
	if err != nil {
		return planInfo{}, err
	}
	var pi planInfo
	cfg := &one.Serve
	if cfg.Ports != nil && cfg.Ports.Blob != nil && cfg.Ports.Blob.S3 != nil {
		pi.s3Ref = cfg.Ports.Blob.S3.CredentialsRef
	}
	if o := cfg.OAuthClient; o != nil && o.Provider != "" {
		pi.configNames = append(pi.configNames, "providers/google/"+o.Provider+"/client-id", "providers/google/"+o.Provider+"/client-secret")
	}
	if cfg.Directory != nil {
		for _, w := range cfg.Directory.Workspaces {
			if w.KeySecret != "" {
				pi.configNames = append(pi.configNames, w.KeySecret)
			}
		}
	}
	if cfg.Cloudflare != nil {
		for name := range cfg.Cloudflare.Accounts {
			pi.accounts = append(pi.accounts, name)
		}
		slices.Sort(pi.accounts)
	}
	if pol, perr := config.PolicyOf(one, nil); perr == nil {
		pi.exported, pi.appRef = pol.GitHubAppExported, pol.AppRef
	}
	return pi, nil
}

// printPlanSummary says per module what the plan found, for the operator who
// reads the terminal and not the JSON.
func printPlanSummary(w io.Writer, r *migrate.PlanReport) {
	_, _ = fmt.Fprintf(w, "\nmigrate v5 plan %s -> %s (nothing was written)\n", r.From, r.To)
	for _, m := range r.Modules {
		_, _ = fmt.Fprintf(w, "  %-10s %s\n", m.Module, m.PlanCounts)
	}
	_, _ = fmt.Fprintf(w, "  %-10s %s\n", "all", r.Totals)
	for _, m := range r.Modules {
		for i := range m.Items {
			if it := &m.Items[i]; it.Status == migrate.PlanRefused {
				_, _ = fmt.Fprintf(w, "  REFUSED %s %s %s: %s\n", m.Module, it.Concern, it.From, it.Reason)
			}
		}
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
