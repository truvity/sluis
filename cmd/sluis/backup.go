package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup/job"
	modbackup "github.com/truvity/sluis/internal/module/backup"
)

// backupOut is where the backup commands print their answer: stdout, so that the
// log lines (stderr) never mix with it.
var backupOut io.Writer = os.Stdout

const backupUsage = `Usage: sluis backup <run|list|status|prune> [--config <file>] [flags]

  run [--resume] [--json]   write a backup now under the backup lease, or continue one that paused
                            (--resume continues and never starts one); exits 0 when another runner
                            holds the lease or the module is under maintenance
  list [--json]             the backups in the archive, newest first (from their manifests)
  status [--json]           the latest runs, the last retention pass
  prune [--dry-run] [--json] apply the retention rule: keep the newest backups and any younger than
                            maxAge, delete the rest, and the chunks of runs that never finished

The document is schemas/config/sluis-backup.schema.json.`

// backupCmd is `sluis backup <run|list|status|prune>` (docs/decisions/0071). The
// flags it adds to --config are taken out here, since the configuration command
// line refuses any other.
func backupCmd(out io.Writer, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		if len(args) > 0 && slices.Contains([]string{"-h", "--help", "-help"}, args[0]) {
			_, _ = fmt.Fprintln(out, backupUsage)
			return nil
		}
		return fmt.Errorf("%w: sluis backup needs a command: run, list, status or prune\n%s", errUsage, backupUsage)
	}
	sub, rest := args[0], args[1:]
	allowed := map[string][]string{
		"run": {"--resume", "--json"}, "list": {"--json"}, "status": {"--json"}, "prune": {"--dry-run", "--json"},
	}[sub]
	if allowed == nil {
		return fmt.Errorf("%w: sluis backup %q: the commands are run, list, status and prune", errUsage, sub)
	}
	set := map[string]bool{}
	rest = slices.DeleteFunc(slices.Clone(rest), func(a string) bool {
		if slices.Contains(allowed, a) {
			set[a] = true
			return true
		}
		return false
	})
	return start(out, "sluis backup "+sub, "sluis-backup", rest, func(ctx context.Context, file string) error {
		return runBackup(ctx, sub, file, set["--json"], set["--resume"], set["--dry-run"])
	})
}

func runBackup(ctx context.Context, sub, file string, asJSON, resume, dryRun bool) error {
	a, _, closeApp, err := modbackup.Module{}.Open(ctx, file)
	if err != nil {
		return err
	}
	defer closeApp()
	print := func(v any, text func(w io.Writer)) error {
		if asJSON {
			enc := json.NewEncoder(backupOut)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		}
		text(backupOut)
		return nil
	}
	switch sub {
	case "run":
		res, err := a.Run(ctx, job.Request{Trigger: job.TriggerCLI, Actor: audit.System(), ResumeOnly: resume})
		// A failed run still has a record to show.
		if perr := print(res, func(w io.Writer) { writeResult(w, res) }); perr != nil && err == nil {
			err = perr
		}
		return err
	case "list":
		infos, err := a.List(ctx)
		if err != nil {
			return err
		}
		return print(infos, func(w io.Writer) { writeList(w, infos) })
	case "status":
		st, err := a.Status(ctx)
		if err != nil {
			return err
		}
		return print(st, func(w io.Writer) { writeStatus(w, st) })
	}
	pass, outcome, err := a.Prune(ctx, audit.System(), dryRun)
	if err != nil {
		return err
	}
	return print(struct {
		Outcome string `json:"outcome"`
		job.Pass
		DryRun bool `json:"dryRun,omitempty"`
	}{outcome, pass, dryRun}, func(w io.Writer) {
		if outcome != "pruned" {
			_, _ = fmt.Fprintf(w, "not pruned: %s\n", outcome)
			return
		}
		verb := "deleted"
		if dryRun {
			verb = "would delete"
		}
		_, _ = fmt.Fprintf(w, "kept %d; %s %d backup(s)", pass.Kept, verb, len(pass.Removed))
		if len(pass.Orphans) > 0 {
			_, _ = fmt.Fprintf(w, " and the objects of %d unfinished run(s)", len(pass.Orphans))
		}
		_, _ = fmt.Fprintln(w)
		for _, id := range slices.Concat(pass.Removed, pass.Orphans) {
			_, _ = fmt.Fprintf(w, "  %s\n", id)
		}
	})
}

func writeResult(w io.Writer, res job.Result) {
	if res.Run == nil {
		_, _ = fmt.Fprintln(w, res.Outcome)
		return
	}
	_, _ = fmt.Fprintf(w, "%s: backup %s, %d records in %d modules\n", res.Outcome, res.ID, res.Run.Records, res.Run.Modules)
	if res.Run.Units > 0 {
		_, _ = fmt.Fprintf(w, "  %d of %d units written; run `sluis backup run --resume` to continue\n", res.Run.Done, res.Run.Units)
	}
	if res.Run.Error != "" {
		_, _ = fmt.Fprintf(w, "  %s: %s\n", res.Run.Reason, res.Run.Error)
	}
}

func writeList(w io.Writer, infos []job.Info) {
	if len(infos) == 0 {
		_, _ = fmt.Fprintln(w, "no backups")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tCREATED\tRECORDS\tMODULES\tFORMAT\tCREATOR")
	for _, in := range infos {
		if in.Error != "" {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\tmanifest unreadable: %s\n", in.ID, in.Created.Format(time.RFC3339), in.Error)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", in.ID, in.Created.Format(time.RFC3339), in.Records, in.Modules, in.Format, in.Creator)
	}
	_ = tw.Flush()
}

func writeStatus(w io.Writer, st job.Status) {
	line := func(label string, v *job.View) {
		if v == nil {
			_, _ = fmt.Fprintf(w, "%-15s none\n", label)
			return
		}
		_, _ = fmt.Fprintf(w, "%-15s %s %s (%s) %d records", label, v.ID, v.State, v.Updated.Format(time.RFC3339), v.Records)
		if v.Error != "" {
			_, _ = fmt.Fprintf(w, ": %s: %s", v.Reason, v.Error)
		}
		_, _ = fmt.Fprintln(w)
	}
	line("latest:", st.Latest)
	line("last completed:", st.LastCompleted)
	if st.Unfinished != nil {
		line("unfinished:", st.Unfinished)
	}
	if st.Retention != nil {
		_, _ = fmt.Fprintf(w, "%-15s %s: kept %d, removed %d, unfinished runs cleaned %d, refused %d\n", "last prune:",
			st.Retention.At.Format(time.RFC3339), st.Retention.Kept, len(st.Retention.Removed), len(st.Retention.Orphans), len(st.Retention.Failed))
	}
}
