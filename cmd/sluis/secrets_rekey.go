//go:build !lambda

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/truvity/sluis/audit/sdk/record"
	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/awsretry"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/internal/rekey"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
)

const secretsUsage = `Usage: sluis secrets rekey --config <file> [--dry-run] [--json] [--include-recent] [--by <name>] [--no-audit]

  rekey
        re-write every SecureString parameter below secrets.root that is not under the
        key secrets.kmsKeyId names, so that the whole installation is encrypted with one
        key. Each parameter is read with decryption and written back with the same name
        and value (PutParameter with Overwrite and KeyId), ten at a time, paced against
        SSM's throttling, with a doubling wait when it is throttled. It prints names,
        versions and counts, never a value.

        A re-write is a new version of the parameter. Earlier versions keep the key they
        were written under (history is not re-keyed), and a parameter modified within
        secrets.grace (default 24h) is left for a later run, because its previous
        version is the overlap of a rotated client secret. --include-recent re-writes it
        anyway. Run it while nothing writes secrets; a parameter that changes under the
        run is skipped or reported, and a second run finishes what is left.

        --dry-run lists what would be re-written and writes nothing. A real run is
        audited (roster.secrets.rekeyed): the record is delivered BEFORE the first write,
        and the run refuses, writing nothing, if it cannot be. --no-audit is the
        break-glass: it goes ahead, warns, and prints the record to stderr as JSON.

The credentials are the caller's: ssm:DescribeParameters, ssm:GetParameters and
ssm:PutParameter on the root, kms:Decrypt on the keys the parameters are under now, and
kms:Encrypt, kms:Decrypt and kms:GenerateDataKey on the new key (see
RekeyOperatorStatements in the Pulumi library), and sqs:SendMessage on the audit queue.`

// secretsCmd is `sluis secrets <command>`.
func secretsCmd(out io.Writer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
			_, _ = fmt.Fprintln(out, secretsUsage)
			return nil
		}
		return fmt.Errorf("%w: sluis secrets needs a command: rekey\n%s", errUsage, secretsUsage)
	}
	if args[0] != "rekey" {
		return fmt.Errorf("%w: sluis secrets %q: the command is rekey", errUsage, args[0])
	}
	var dryRun, asJSON, recent, noAudit bool
	var by string
	var remaining []string
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case !strings.HasPrefix(a, "-"):
			remaining = append(remaining, a)
		case name == "dry-run":
			dryRun = true
		case name == "json":
			asJSON = true
		case name == "include-recent":
			recent = true
		case name == "no-audit":
			noAudit = true
		case name == "by":
			if !hasVal {
				if i+1 >= len(rest) {
					return fmt.Errorf("%w: --by needs a value", errUsage)
				}
				i++
				val = rest[i]
			}
			by = val
		default:
			remaining = append(remaining, a)
			if (a == "--config" || a == "-config" || a == "-c") && i+1 < len(rest) {
				i++
				remaining = append(remaining, rest[i])
			}
		}
	}
	return start(out, "sluis secrets rekey", "sluis", remaining, func(ctx context.Context, file string) error {
		return runRekey(ctx, out, file, rekeyFlags{dryRun: dryRun, asJSON: asJSON, recent: recent, noAudit: noAudit, by: by})
	})
}

type rekeyFlags struct {
	dryRun, asJSON, recent, noAudit bool
	by                              string
}

func runRekey(ctx context.Context, out io.Writer, file string, f rekeyFlags) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	one, err := config.Load[config.Sluis](file)
	if err != nil {
		return err
	}
	cfg := &one.Serve
	sec := cfg.Secrets
	switch {
	case sec == nil || sec.Source != "ssm":
		return errors.New("sluis secrets rekey: secrets.source is not ssm: there are no SSM parameters to re-key")
	case sec.KMSKeyID == "":
		return errors.New("sluis secrets rekey: secrets.kmsKeyId is not set: name the estate's key (an alias) there first; " +
			"there is nothing to re-key to")
	}
	instance := cfg.Instance
	if instance == "" {
		instance = file
	}
	api, err := openRekeySSM(ctx, sec)
	if err != nil {
		return err
	}
	opt := rekey.Options{Root: sec.Root, KeyID: sec.KMSKeyID, DryRun: f.dryRun, IncludeRecent: f.recent}
	if sec.Grace != nil {
		opt.Grace = sec.Grace.D()
	}
	var au *rekeyAudit
	if !f.dryRun {
		sc, serr := store.FromServe(cfg)
		if serr != nil {
			return serr
		}
		if sc.Secrets, serr = secrets.Open(ctx, cfg); serr != nil {
			return serr
		}
		st, serr := store.Open(ctx, sc, log)
		if serr != nil {
			return serr
		}
		defer st.Close()
		var closeAudit func()
		if au, closeAudit, serr = openRekeyAudit(ctx, cfg, st.Plan, f.noAudit, os.Stderr, log); serr != nil {
			return serr
		}
		defer closeAudit()
	}
	actor := audit.Workload("cli:secrets")
	if by := rekeyBy(f.by); by != "" {
		actor = audit.Person(by)
	}
	return rekeyRun(ctx, out, instance, api, opt, au, actor, f.asJSON)
}

// rekeyRun is the command over the SSM it was given: the re-key, with its
// record kept before the first write and again when it ends.
func rekeyRun(ctx context.Context, out io.Writer, instance string, api rekey.API, opt rekey.Options, au *rekeyAudit,
	actor audit.Actor, asJSON bool) error {
	started := false
	entry := func(r *rekey.Report, phase string) audit.SecretsRekey {
		e := audit.SecretsRekey{Installation: instance, Key: r.KeyID, Phase: phase, Considered: r.Considered,
			Planned: len(r.Rekey), Rekeyed: r.Rekeyed, Deferred: len(r.Deferred)}
		if phase == audit.RekeyStarted {
			e.Names = r.Rekey
		}
		return e
	}
	if au != nil {
		opt.Before = func(ctx context.Context, r *rekey.Report) error {
			if err := au.keep(ctx, "re-write the secrets under the new key", audit.SecretsRekeyed(actor, entry(r, audit.RekeyStarted), audit.Succeeded())); err != nil {
				return err
			}
			started = true
			return nil
		}
	}
	report, err := rekey.Run(ctx, api, opt)
	if report != nil {
		printRekey(out, report, asJSON)
	}
	if started && report != nil {
		o := audit.Succeeded()
		if err != nil {
			o = audit.Failed(err.Error())
		}
		if cerr := au.keepAfter(ctx, "the re-key", audit.SecretsRekeyed(actor, entry(report, audit.RekeyCompleted), o)); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}
	return err
}

func rekeyBy(by string) string {
	if by != "" {
		return by
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func openRekeySSM(ctx context.Context, sec *config.Secrets) (rekey.API, error) {
	var load []func(*awsconfig.LoadOptions) error
	if sec.Region != "" {
		load = append(load, awsconfig.WithRegion(sec.Region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, fmt.Errorf("sluis secrets rekey: load AWS config: %w", err)
	}
	return awsssm.NewFromConfig(cfg, func(o *awsssm.Options) {
		o.Retryer = awsretry.New()
		if sec.Endpoint != "" {
			o.BaseEndpoint = aws.String(sec.Endpoint)
		}
	}), nil
}

// printRekey says what the run found: names, versions and counts.
func printRekey(w io.Writer, r *rekey.Report, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	mode := "secrets rekey"
	if r.DryRun {
		mode += " --dry-run (nothing was written)"
	}
	_, _ = fmt.Fprintf(w, "\n%s %s -> %s\n  %d SecureString parameters, %d already under the key\n", mode, r.Root, r.KeyID, r.Considered, r.Current)
	verb := "to re-write"
	if !r.DryRun {
		verb = "selected"
	}
	_, _ = fmt.Fprintf(w, "  %s: %d\n", verb, len(r.Rekey))
	for _, n := range r.Rekey {
		_, _ = fmt.Fprintf(w, "    %s\n", n)
	}
	for _, n := range r.Deferred {
		_, _ = fmt.Fprintf(w, "  deferred (changed within the grace window): %s\n", n)
	}
	for _, n := range r.Skipped {
		_, _ = fmt.Fprintf(w, "  skipped (changed under the run): %s\n", n)
	}
	for _, n := range r.Concurrent {
		_, _ = fmt.Fprintf(w, "  CONCURRENT WRITE: %s\n", n)
	}
	if !r.DryRun {
		_, _ = fmt.Fprintf(w, "  %d re-written in %d batches\n", r.Rekeyed, r.Batches)
	}
	for _, n := range r.Notes {
		_, _ = fmt.Fprintf(w, "  note: %s\n", n)
	}
	if !r.OK {
		_, _ = fmt.Fprintf(w, "  NOT ok: %s\n", r.Error)
	}
}

// rekeyAudit delivers the records of a re-key: synchronously, or the command
// refuses. With noAudit it prints the record to stderr and goes on.
type rekeyAudit struct {
	rec     audit.Recorder
	dest    string
	noAudit bool
	stderr  io.Writer
	warned  bool
}

func openRekeyAudit(ctx context.Context, cfg *config.Serve, plan port.Table, noAudit bool, stderr io.Writer, log *slog.Logger) (*rekeyAudit, func(), error) {
	host, _ := os.Hostname()
	ac := audit.Config{Version: "sluis secrets rekey", Instance: host, Log: log}
	if noAudit {
		trail, err := audit.Open(ctx, ac)
		if err != nil {
			return nil, nil, err
		}
		return &rekeyAudit{rec: trail, noAudit: true, stderr: stderr, dest: "nowhere (--no-audit)"}, func() { _ = trail.Close() }, nil
	}
	if a := cfg.Audit; a != nil {
		ac.Writer, ac.TokenFile = a.Writer, a.TokenFile
	}
	ac, err := audit.FromPlan(ac, plan)
	if err != nil {
		return nil, nil, err
	}
	trail, err := audit.Open(ctx, ac)
	if err != nil {
		return nil, nil, err
	}
	dest := "the audit log (no installation is connected)"
	switch {
	case ac.SQS != nil:
		dest = "queue " + ac.SQS.QueueURL
	case ac.Writer != "":
		dest = "writer " + ac.Writer
	}
	return &rekeyAudit{rec: trail, dest: dest, stderr: stderr}, func() { _ = trail.Close() }, nil
}

var deniedPrincipalRekey = regexp.MustCompile(`User: (\S+) is not authorized`)

func (a *rekeyAudit) keep(ctx context.Context, what string, r *record.Record) error {
	if a.noAudit {
		return a.printLocal(ctx, what, r)
	}
	err := a.rec.RecordDurable(ctx, r)
	if err == nil {
		return nil
	}
	who := "the caller"
	if m := deniedPrincipalRekey.FindStringSubmatch(err.Error()); m != nil {
		who = m[1] + " (denied)"
	}
	return fmt.Errorf("sluis secrets rekey: refusing to %s: the audit record could not be delivered to %s by %s: %v; "+
		"nothing was written. Fix the operator role's access to the destination, or, as break-glass, repeat with --no-audit "+
		"(the record is then printed to stderr and the command goes ahead unaudited)", what, a.dest, who, err)
}

func (a *rekeyAudit) keepAfter(ctx context.Context, what string, r *record.Record) error {
	if err := a.keep(ctx, what, r); err != nil {
		return fmt.Errorf("%s already happened, and its closing record was not kept: %w", what, err)
	}
	return nil
}

func (a *rekeyAudit) printLocal(ctx context.Context, what string, r *record.Record) error {
	if !a.warned {
		a.warned = true
		_, _ = fmt.Fprintf(a.stderr, "WARNING: --no-audit: %s proceeds WITHOUT its audit record reaching the installation. "+
			"The record is printed below; keep it and report it.\n", what)
	}
	a.rec.Record(ctx, r)
	line, err := record.Canonical(r)
	if err != nil {
		return fmt.Errorf("--no-audit: the record cannot be printed: %w", err)
	}
	_, _ = fmt.Fprintf(a.stderr, "%s\n", line)
	return nil
}
