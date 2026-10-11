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

  on [--by <name>] [--reason <text>] [--no-wait] [--no-audit]
        freeze the installation for a migration. Every write of state is refused with 503 and
        Retry-After (sessions, refresh rotation, codes, console changes, controller ticks);
        discovery, the key set, userinfo, introspection, a token exchange and a refresh that is
        not rotated keep answering, so deploy tooling still signs in. Returns once every
        replica has seen the flag, unless --no-wait.
  off [--force] [--no-audit]
        lift it. A flag a restore holds is lifted only with --force.
  status [--json]
        the flag of each table.

on and off are audited (roster.maintenance.changed) and the record is delivered to the audit
destination BEFORE the flag changes: if it cannot be delivered the command refuses, naming the
queue and the principal it refused. --no-audit is the break-glass: it goes ahead, warns, and
prints the record to stderr as JSON. status writes nothing and records nothing.

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
	var noWait, force, asJSON, noAudit bool
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
		case name == noAuditFlag:
			noAudit = true
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
		return runMaintenance(ctx, file, sub, by, reason, noWait, force, asJSON, noAudit)
	})
}

func runMaintenance(ctx context.Context, file, sub, by, reason string, noWait, force, asJSON, noAudit bool) error {
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
	var au *operatorAudit
	if sub != "status" {
		var closeAudit func()
		au, closeAudit, err = openOperatorAudit(ctx, cfg, st.Plan, "sluis maintenance", hostname(), noAudit, os.Stderr, log)
		if err != nil {
			return err
		}
		defer closeAudit()
	}
	return maintenanceRun(ctx, maintenanceOut, sub, tables, instanceOf2(cfg, file), by, reason, noWait, force, asJSON, au)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func instanceOf2(cfg *config.Serve, file string) string {
	if cfg.Instance != "" {
		return cfg.Instance
	}
	return file
}

// maintenanceRun is the command over the tables it was given.
func maintenanceRun(ctx context.Context, w io.Writer, sub string, tables []maintenance.Table, instance, by, reason string,
	noWait, force, asJSON bool, au *operatorAudit) error {
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
		m := audit.Maintenance{Installation: instance, State: maintenance.StateMigration, Modules: len(tables), Reason: reason}
		// The record is kept before the flag is set. When the flag cannot be set
		// anyway (a table is unreadable, a restore holds it) nothing is recorded:
		// nothing would change.
		if planned := freezeWillWrite(ctx, tables); planned {
			if err := au.keep(ctx, "freeze the installation", audit.MaintenanceChanged(actor, m)); err != nil {
				return err
			}
		}
		sts, err := maintenance.Freeze(ctx, tables, by, reason, time.Now())
		if err != nil {
			if planned := freezeWillWrite(ctx, tables); planned {
				_ = au.keepAfter(ctx, "the freeze", audit.MaintenanceFailed(actor, m, err.Error()))
			}
			return err
		}
		since := sts[0].Flag.Since
		if wait := sts[0].Flag.Quiet(time.Now()); wait > 0 && !noWait {
			_, _ = fmt.Fprintf(w, "frozen since %s on %d table(s); waiting %s for every replica to see it\n",
				since.Format(time.RFC3339), len(sts), wait.Round(time.Second))
			maintenanceSleep(ctx, wait)
		}
		_, _ = fmt.Fprintf(w, "maintenance on (migration): writes are refused, reads and token exchange are served; lift with `sluis maintenance off`\n")
		return ctx.Err()
	default:
		n, forced := liftWillWrite(ctx, tables, force)
		m := audit.Maintenance{Installation: instance, State: audit.MaintenanceCleared, Modules: n, Forced: forced}
		if n > 0 {
			if err := au.keep(ctx, "lift the maintenance flag", audit.MaintenanceChanged(actor, m)); err != nil {
				return err
			}
		}
		lifted, _, err := maintenance.Lift(ctx, tables, force)
		if err != nil {
			if n > 0 {
				_ = au.keepAfter(ctx, "lifting the flag", audit.MaintenanceFailed(actor, m, err.Error()))
			}
			return err
		}
		_, _ = fmt.Fprintf(w, "maintenance off: %d table(s) lifted\n", lifted)
		return nil
	}
}

// freezeWillWrite reports whether a freeze would change anything: every table
// reads, and none is held by a restore. Such a freeze is recorded before it is
// made.
func freezeWillWrite(ctx context.Context, tables []maintenance.Table) bool {
	if len(tables) == 0 {
		return false
	}
	for _, s := range maintenance.Statuses(ctx, tables) {
		if s.Err != "" || (s.Flag.Active() && !s.Flag.Frozen()) {
			return false
		}
	}
	return true
}

// liftWillWrite is how many tables a lift would clear, and whether one of them
// is a restore's; zero when the lift would be refused or find nothing.
func liftWillWrite(ctx context.Context, tables []maintenance.Table, force bool) (n int, forced bool) {
	for _, s := range maintenance.Statuses(ctx, tables) {
		switch {
		case s.Err != "":
			return 0, false
		case s.Flag.Active() && !s.Flag.Frozen():
			if !force {
				return 0, false
			}
			forced = true
		}
		if s.On {
			n++
		}
	}
	return n, forced
}
