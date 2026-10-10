package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/config"
	modbackup "github.com/truvity/sluis/internal/module/backup"
)

// restoreOut is where the restore commands print their answer: stdout, so that
// the log lines (stderr) never mix with it.
var restoreOut io.Writer = os.Stdout

const restoreUsage = `Usage: sluis restore <preview|start|status> [--config <file>] [flags]

  preview <backup-id> [--json]
                            what restoring the backup would do, per module: records to create, to
                            overwrite, already the same; names, never values. Writes nothing.
  start <backup-id> --confirm <instance> [--overwrite] [--by <name>] [--note <text>] [--json]
                            restore the backup. Every module goes under maintenance first, and is lifted
                            only when the restore has been read back and matches. One at a time. A
                            destination that holds different data is refused unless --overwrite.
  start --resume [--json]   continue a restore that paused; --confirm is not needed
  status [--json]           the latest restores and the modules still under maintenance

On Lambda the same work is the function {"kind":"restore"} event (backup.role: restore). A failed restore
leaves the modules under maintenance until a restore completes.

The document is schemas/config/sluis-backup.schema.json.`

// restoreFlags are the flags `sluis restore` adds to --config, by whether they
// take a value.
var restoreFlags = map[string]map[string]bool{
	"preview": {"--json": false},
	"start":   {"--json": false, "--overwrite": false, "--resume": false, "--confirm": true, "--by": true, "--note": true},
	"status":  {"--json": false},
}

// restoreCmd is `sluis restore <preview|start|status>` (docs/decisions/0071,
// point 9): the restore role of the backup zip, run in this process with the
// caller's credentials. The flags it adds to --config are taken out here, since
// the configuration command line refuses any other.
func restoreCmd(out io.Writer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && slices.Contains([]string{"-h", "--help", "-help"}, args[0]) {
			_, _ = fmt.Fprintln(out, restoreUsage)
			return nil
		}
		return fmt.Errorf("%w: sluis restore needs a command: preview, start or status\n%s", errUsage, restoreUsage)
	}
	sub, rest := args[0], args[1:]
	allowed, ok := restoreFlags[sub]
	if !ok {
		return fmt.Errorf("%w: sluis restore %q: the commands are preview, start and status", errUsage, sub)
	}
	set := map[string]string{}
	var positional, remaining []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, val, hasVal := strings.Cut(a, "=")
		takes, known := allowed[name]
		switch {
		case !known:
			if !strings.HasPrefix(a, "-") && !hasVal {
				positional = append(positional, a)
			} else {
				remaining = append(remaining, a)
				// --config takes a value, which is not a positional argument.
				if (a == "--config" || a == "-config" || a == "-c") && i+1 < len(rest) {
					i++
					remaining = append(remaining, rest[i])
				}
			}
		case takes && !hasVal:
			if i+1 >= len(rest) {
				return fmt.Errorf("%w: %s needs a value", errUsage, name)
			}
			i++
			set[name] = rest[i]
		case takes:
			set[name] = val
		case hasVal:
			return fmt.Errorf("%w: %s takes no value", errUsage, name)
		default:
			set[name] = "true"
		}
	}
	if len(positional) > 1 || (sub == "status" && len(positional) > 0) || (sub == "preview" && len(positional) != 1) {
		return fmt.Errorf("%w: sluis restore %s: unexpected arguments %q\n%s", errUsage, sub, positional, restoreUsage)
	}
	id := ""
	if len(positional) == 1 {
		id = positional[0]
	}
	switch {
	case sub == "start" && set["--resume"] != "" && id != "":
		return fmt.Errorf("%w: --resume continues the restore that paused; it takes no backup id", errUsage)
	case sub == "start" && set["--resume"] == "" && id == "":
		return fmt.Errorf("%w: sluis restore start needs the backup id (see `sluis backup list`), or --resume\n%s", errUsage, restoreUsage)
	}
	return start(out, "sluis restore "+sub, "sluis-backup", remaining, func(ctx context.Context, file string) error {
		return runRestore(ctx, sub, file, id, set)
	})
}

func runRestore(ctx context.Context, sub, file, id string, set map[string]string) error {
	a, _, closeApp, err := modbackup.Module{}.OpenRole(ctx, file, config.BackupRoleRestore)
	if err != nil {
		return err
	}
	defer closeApp()
	asJSON := set["--json"] != ""
	show := func(v any, text func(w io.Writer)) error {
		if asJSON {
			enc := json.NewEncoder(restoreOut)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		}
		text(restoreOut)
		return nil
	}
	switch sub {
	case "preview":
		rep, err := a.PreviewRestore(ctx, id)
		if err != nil {
			return err
		}
		return show(rep, func(w io.Writer) { writePreview(w, rep) })
	case "status":
		st, err := a.RestoreStatus(ctx)
		if err != nil {
			return err
		}
		return show(st, func(w io.Writer) { writeRestoreStatus(w, st) })
	}
	// start: the name of the installation is typed, so that a restore into the
	// wrong one by a stale terminal is refused.
	if set["--resume"] == "" && set["--confirm"] != instanceOf(file) {
		return fmt.Errorf("%w: restoring replaces data in the installation %q: pass --confirm %s to say you mean it",
			errUsage, instanceOf(file), instanceOf(file))
	}
	by := set["--by"]
	if by == "" {
		if u, err := user.Current(); err == nil {
			by = u.Username
		}
	}
	res, err := a.Restore(ctx, restorejob.Request{BackupID: id, Overwrite: set["--overwrite"] != "", Actor: audit.Workload("cli:restore"),
		By: by, Note: set["--note"]})
	if err != nil {
		return err
	}
	if perr := show(res, func(w io.Writer) { writeRestoreResult(w, res) }); perr != nil {
		return perr
	}
	switch res.Outcome {
	case restorejob.OutcomeCompleted, restorejob.OutcomeIdle:
		return nil
	case restorejob.OutcomePaused:
		return errors.New("the restore paused; the modules stay under maintenance: run `sluis restore start --resume` to continue")
	case restorejob.OutcomeContended:
		return errors.New("another restore holds the lease; nothing was changed")
	case restorejob.OutcomeRefused:
		return fmt.Errorf("the restore was refused (%s); nothing was changed", res.Reason)
	}
	return fmt.Errorf("the restore failed (%s); the modules stay under maintenance until a restore completes", res.Run.Reason)
}

// instanceOf is the installation's name in the document; the document was read
// by the open that came before, so a failure is not possible here.
func instanceOf(file string) string {
	doc, err := config.LoadBackup(file)
	if err != nil {
		return ""
	}
	return doc.Name()
}

func writePreview(w io.Writer, rep *restore.Report) {
	_, _ = fmt.Fprintf(w, "backup %s of %s (%s), %s layout\n", rep.ID, rep.Installation, rep.Created, rep.Layout)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MODULE\tSECTION\tCREATE\tOVERWRITE\tSAME\tEXPIRED\tREGENERATED")
	for _, s := range rep.Sections {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n", s.Module, s.Section, s.Create, s.Overwrite, s.Same, s.Expired, s.Regenerated)
	}
	t := rep.Totals()
	_, _ = fmt.Fprintf(tw, "total\t\t%d\t%d\t%d\t%d\t%d\n", t.Create, t.Overwrite, t.Same, t.Expired, t.Regenerated)
	_ = tw.Flush()
	if t.Overwrite > 0 {
		_, _ = fmt.Fprintf(w, "%d records differ: a restore refuses them unless --overwrite is passed\n", t.Overwrite)
	}
}

func writeRestoreResult(w io.Writer, res restorejob.Result) {
	if res.Run == nil {
		_, _ = fmt.Fprintf(w, "%s", res.Outcome)
		if res.Reason != "" {
			_, _ = fmt.Fprintf(w, ": %s: %s", res.Reason, res.Error)
		}
		_, _ = fmt.Fprintln(w)
		return
	}
	r := res.Run
	_, _ = fmt.Fprintf(w, "%s: restore %s from backup %s (%d created, %d overwritten, %d unchanged); maintenance %s\n",
		res.Outcome, r.ID, r.BackupID, r.Create, r.Overwritten, r.Same, orNone(r.Maintenance))
	if r.Error != "" {
		_, _ = fmt.Fprintf(w, "  %s: %s\n", r.Reason, r.Error)
	}
}

func writeRestoreStatus(w io.Writer, st restorejob.Status) {
	line := func(label string, r *restorejob.Run) {
		if r == nil {
			_, _ = fmt.Fprintf(w, "%-15s none\n", label)
			return
		}
		_, _ = fmt.Fprintf(w, "%-15s %s %s from %s (%s), maintenance %s", label, r.ID, r.State, r.BackupID, r.Updated.Format(time.RFC3339), orNone(r.Maintenance))
		if r.Error != "" {
			_, _ = fmt.Fprintf(w, ": %s: %s", r.Reason, r.Error)
		}
		_, _ = fmt.Fprintln(w)
	}
	line("latest:", st.Latest)
	line("last completed:", st.LastCompleted)
	if st.Unfinished != nil {
		line("unfinished:", st.Unfinished)
	}
	if len(st.Maintenance) == 0 {
		_, _ = fmt.Fprintln(w, "no module is under maintenance")
		return
	}
	for _, m := range st.Maintenance {
		if m.Unreadable {
			_, _ = fmt.Fprintf(w, "%-15s flag unreadable\n", m.Module)
			continue
		}
		_, _ = fmt.Fprintf(w, "%-15s %s since %s by %s\n", m.Module, m.State, m.Since.Format(time.RFC3339), orNone(m.By))
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
