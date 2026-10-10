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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/config"
	"github.com/truvity/sluis/internal/maintenance"
	"github.com/truvity/sluis/internal/secrets"
	"github.com/truvity/sluis/internal/store"
)

const maintenanceUsage = `Usage: sluis maintenance <on|off|status> --config <file> [flags]

  on [--by <name>] [--reason <text>] [--no-wait]
        freeze the installation for a migration. Every write of state is refused with 503 and
        Retry-After (sessions, refresh rotation, codes, console changes, controller ticks);
        discovery, the key set, userinfo, introspection, a token exchange and a refresh that is
        not rotated keep answering, so deploy tooling still signs in. Returns once every
        replica has seen the flag, unless --no-wait.
  off [--force]
        lift it. A flag a restore holds is lifted only with --force.
  status [--json]
        the flag of each table.

The credentials are the caller's: the roles of the functions cannot write the flag (see
MaintenanceOperatorStatements in the Pulumi library).`

// maintenanceOut is where the command prints its answer.
var maintenanceOut io.Writer = os.Stdout

// maintenanceSleep waits for the replicas to see the flag; a test replaces it.
var maintenanceSleep = func(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func maintenanceCmd(out io.Writer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
			_, _ = fmt.Fprintln(out, maintenanceUsage)
			return nil
		}
		return fmt.Errorf("%w: sluis maintenance needs on, off or status\n%s", errUsage, maintenanceUsage)
	}
	sub, rest := args[0], args[1:]
	if sub != "on" && sub != "off" && sub != "status" {
		return fmt.Errorf("%w: sluis maintenance %q: the commands are on, off and status", errUsage, sub)
	}
	// Our flags are taken out; --config and the rest go to the configuration loader.
	var by, reason string
	var noWait, force, asJSON bool
	var remaining []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case !strings.HasPrefix(a, "-"):
			remaining = append(remaining, a)
		case name == "by" || name == "reason":
			if !hasVal {
				if i+1 >= len(rest) {
					return fmt.Errorf("%w: --%s needs a value", errUsage, name)
				}
				i++
				val = rest[i]
			}
			if name == "by" {
				by = val
			} else {
				reason = val
			}
		case name == "no-wait":
			noWait = true
		case name == "force":
			force = true
		case name == "json":
			asJSON = true
		default:
			remaining = append(remaining, a)
			if (a == "--config" || a == "-config" || a == "-c") && i+1 < len(rest) {
				i++
				remaining = append(remaining, rest[i])
			}
		}
	}
	return start(out, "sluis maintenance "+sub, "sluis", remaining, func(ctx context.Context, file string) error {
		return runMaintenance(ctx, file, sub, by, reason, noWait, force, asJSON)
	})
}

func runMaintenance(ctx context.Context, file, sub, by, reason string, noWait, force, asJSON bool) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	one, err := config.Load[config.Sluis](file)
	if err != nil {
		return err
	}
	cfg := &one.Serve
	sc, err := store.FromServe(cfg)
	if err != nil {
		return err
	}
	if sc.Secrets, err = secrets.Open(ctx, cfg); err != nil {
		return err
	}
	st, err := store.Open(ctx, sc, log)
	if err != nil {
		return err
	}
	defer st.Close()
	flags, err := st.FlagTables()
	if err != nil {
		return err
	}
	tables := make([]maintenance.Table, 0, len(flags))
	for _, f := range flags {
		name := string(f.Module)
		if name == "" {
			name = "all"
		}
		tables = append(tables, maintenance.Table{Name: name, State: f.State})
	}
	if by == "" {
		if u, uerr := user.Current(); uerr == nil {
			by = u.Username
		}
	}
	var rec audit.Recorder
	if sub != "status" {
		ac := audit.Config{Version: "sluis maintenance", Log: log}
		ac.Instance, _ = os.Hostname()
		if a := cfg.Audit; a != nil {
			ac.Writer, ac.TokenFile = a.Writer, a.TokenFile
		}
		if ac, err = audit.FromPlan(ac, st.Plan); err != nil {
			return err
		}
		trail, terr := audit.Open(ctx, ac)
		if terr != nil {
			return terr
		}
		defer func() { _ = trail.Close() }()
		rec = trail
	}
	return maintenanceRun(ctx, maintenanceOut, sub, tables, instanceOf2(cfg, file), by, reason, noWait, force, asJSON, rec)
}

func instanceOf2(cfg *config.Serve, file string) string {
	if cfg.Instance != "" {
		return cfg.Instance
	}
	return file
}

// maintenanceRun is the command over the tables it was given.
func maintenanceRun(ctx context.Context, w io.Writer, sub string, tables []maintenance.Table, instance, by, reason string,
	noWait, force, asJSON bool, rec audit.Recorder) error {
	actor := audit.Workload("cli:maintenance")
	if by != "" {
		actor = audit.Person(by)
	}
	switch sub {
	case "status":
		sts := maintenance.Statuses(ctx, tables)
		if asJSON {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(sts)
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "TABLE\tFLAG\tSINCE\tBY\tREASON")
		bad := false
		for _, s := range sts {
			switch {
			case s.Err != "":
				bad = true
				_, _ = fmt.Fprintf(tw, "%s\tunreadable: %s\t\t\t\n", s.Table, s.Err)
			case !s.On:
				_, _ = fmt.Fprintf(tw, "%s\toff\t\t\t\n", s.Table)
			default:
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Table, s.Flag.State, s.Flag.Since.Format(time.RFC3339), orNone(s.Flag.By), orNone(s.Flag.Reason))
			}
		}
		_ = tw.Flush()
		if bad {
			return errors.New("a flag could not be read")
		}
		return nil
	case "on":
		sts, err := maintenance.Freeze(ctx, tables, by, reason, time.Now())
		if err != nil {
			return err
		}
		rec.Record(ctx, audit.MaintenanceChanged(actor, audit.Maintenance{
			Installation: instance, State: maintenance.StateMigration, Modules: len(sts), Reason: reason}))
		since := sts[0].Flag.Since
		if wait := sts[0].Flag.Quiet(time.Now()); wait > 0 && !noWait {
			_, _ = fmt.Fprintf(w, "frozen since %s on %d table(s); waiting %s for every replica to see it\n",
				since.Format(time.RFC3339), len(sts), wait.Round(time.Second))
			maintenanceSleep(ctx, wait)
		}
		_, _ = fmt.Fprintf(w, "maintenance on (migration): writes are refused, reads and token exchange are served; lift with `sluis maintenance off`\n")
		return ctx.Err()
	default:
		n, forced, err := maintenance.Lift(ctx, tables, force)
		if err != nil {
			return err
		}
		if n > 0 {
			rec.Record(ctx, audit.MaintenanceChanged(actor, audit.Maintenance{Installation: instance, State: audit.MaintenanceCleared, Modules: n, Forced: forced}))
		}
		_, _ = fmt.Fprintf(w, "maintenance off: %d table(s) lifted\n", n)
		return nil
	}
}
