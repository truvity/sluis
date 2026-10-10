package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/truvity/sluis/internal/audit"
	"github.com/truvity/sluis/internal/backup"
	"github.com/truvity/sluis/internal/port"
	"github.com/truvity/sluis/storage/logattr"
)

const manifestFile = "manifest.json"

// idLayout is the timestamp an id begins with.
const idLayout = "20060102T150405Z"

// idTime is when the run an id names started; false for an id this job did not
// make, which a prune never touches.
func idTime(id string) (time.Time, bool) {
	stamp, suffix, ok := strings.Cut(id, "-")
	if !ok || suffix == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(idLayout, stamp)
	return t, err == nil
}

func (j *Job) root() string { return "backup/" + j.c.Installation + "/" }

// ids are the backup ids that have any object in the archive, and which of
// them have a manifest.
func (j *Job) ids(ctx context.Context) (all []string, withManifest map[string]bool, err error) {
	names, err := j.c.Archive.List(ctx, j.root())
	if err != nil {
		return nil, nil, fmt.Errorf("list the archive: %w", err)
	}
	seen := map[string]bool{}
	withManifest = map[string]bool{}
	for _, n := range names {
		rest, ok := strings.CutPrefix(n, j.root())
		if !ok {
			continue
		}
		id, file, _ := strings.Cut(rest, "/")
		if !backup.ValidName(id) {
			continue
		}
		if !seen[id] {
			seen[id] = true
			all = append(all, id)
		}
		if file == manifestFile {
			withManifest[id] = true
		}
	}
	sort.Strings(all)
	return all, withManifest, nil
}

// List is the backups in the archive, newest first: the ones with a manifest.
func (j *Job) List(ctx context.Context) ([]Info, error) {
	all, with, err := j.ids(ctx)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, id := range all {
		if !with[id] {
			continue
		}
		in := Info{ID: id}
		m, err := backup.Peek(ctx, j.c.Archive, j.c.Installation, id)
		if err != nil {
			in.Error = oneLine(err)
			if t, ok := idTime(id); ok {
				in.Created = t
			}
			out = append(out, in)
			continue
		}
		in.Created, _ = time.Parse(time.RFC3339, m.Created)
		in.Creator, in.Format, in.Layout, in.Modules = m.Creator, m.Format, string(m.Layout), len(m.Modules)
		in.Records = m.Records(backup.State) + m.Records(backup.Secrets) + m.Records(backup.Blobs)
		for _, e := range m.Modules {
			in.Chunks += len(e.State.Chunks) + len(e.Secrets.Chunks) + len(e.Blobs.Chunks)
		}
		out = append(out, in)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if !out[a].Created.Equal(out[b].Created) {
			return out[a].Created.After(out[b].Created)
		}
		return out[a].ID > out[b].ID
	})
	return out, nil
}

// Prune applies the retention rule under the lease. dryRun reports what would
// go and deletes nothing. The outcome is [OutcomeContended] or
// [OutcomeMaintenance] when it did not run.
func (j *Job) Prune(ctx context.Context, actor audit.Actor, dryRun bool) (Pass, string, error) {
	if actor.Kind == "" {
		actor = audit.System()
	}
	var pass Pass
	var perr error
	ran, err := j.leases.Do(ctx, LeaseKind, LeaseTarget, func(ctx context.Context) {
		pass, perr = j.prune(ctx, actor, dryRun)
	})
	switch {
	case err != nil:
		return Pass{}, "", err
	case !ran:
		if j.c.Maintenance != nil && j.c.Maintenance.Writable(ctx) != nil {
			return Pass{}, OutcomeMaintenance, nil
		}
		return Pass{}, OutcomeContended, nil
	}
	return pass, "pruned", perr
}

// prune is Prune under a lease the caller holds.
func (j *Job) prune(ctx context.Context, actor audit.Actor, dryRun bool) (Pass, error) {
	pass := Pass{At: j.now(), Keep: j.c.Keep, MaxAge: j.c.MaxAge.String(), DryRun: dryRun}
	all, with, err := j.ids(ctx)
	if err != nil {
		return pass, err
	}
	infos, err := j.List(ctx)
	if err != nil {
		return pass, err
	}
	runs, err := j.records(ctx)
	if err != nil {
		return pass, err
	}
	open := map[string]bool{}
	for _, r := range runs {
		if r.run.State == StateRunning || r.run.State == StatePaused {
			open[r.run.ID] = true
		}
	}
	now := j.now()
	for i, in := range infos {
		if i < j.c.Keep || now.Sub(in.Created) < j.c.MaxAge {
			continue
		}
		pass.Removed = append(pass.Removed, in.ID)
	}
	for _, id := range all {
		t, ok := idTime(id)
		if with[id] || open[id] || !ok || now.Sub(t) < OrphanAge {
			continue
		}
		pass.Orphans = append(pass.Orphans, id)
	}
	pass.Kept = len(infos) - len(pass.Removed)
	if dryRun {
		return pass, nil
	}
	var okRemoved, okOrphans []string
	for _, id := range pass.Removed {
		if err := j.delete(ctx, id); err != nil {
			pass.Failed = append(pass.Failed, id)
			j.log.WarnContext(ctx, "a backup beyond retention could not be deleted", logattr.SafeString("id", id), logattr.SafeError("error", err))
			continue
		}
		okRemoved = append(okRemoved, id)
	}
	for _, id := range pass.Orphans {
		if err := j.delete(ctx, id); err != nil {
			pass.Failed = append(pass.Failed, id)
			j.log.WarnContext(ctx, "the objects of an unfinished run could not be deleted", logattr.SafeString("id", id), logattr.SafeError("error", err))
			continue
		}
		okOrphans = append(okOrphans, id)
	}
	pass.Removed, pass.Orphans = okRemoved, okOrphans
	pass.Kept = len(infos) - len(okRemoved)
	if err := j.trimRuns(ctx, runs, okRemoved, okOrphans); err != nil {
		j.log.WarnContext(ctx, "old backup run records could not be trimmed", slog.Any("error", err))
	}
	raw, _ := json.Marshal(pass)
	if _, err := j.c.State.Put(ctx, RetentionKey, raw, 0); err != nil {
		return pass, fmt.Errorf("write the retention record: %w", err)
	}
	if len(pass.Removed)+len(pass.Orphans) > 0 {
		j.record(ctx, audit.BackupPruned(actor, pass.Removed, pass.Orphans, pass.Kept, pass.Rule()))
	}
	if len(pass.Failed) > 0 {
		return pass, fmt.Errorf("retention: %d backup(s) could not be deleted (Object Lock keeps them until their retention ends): %s",
			len(pass.Failed), strings.Join(pass.Failed, ", "))
	}
	return pass, nil
}

// delete removes one backup's objects, the manifest first: a backup missing
// its manifest is not a backup, so a delete that stops half-way leaves nothing
// that restores wrongly.
func (j *Job) delete(ctx context.Context, id string) error {
	names, err := j.c.Archive.List(ctx, backup.Prefix(j.c.Installation, id))
	if err != nil {
		return err
	}
	manifest := backup.ManifestName(j.c.Installation, id)
	sort.SliceStable(names, func(a, b int) bool { return names[a] == manifest && names[b] != manifest })
	var errs []error
	for _, n := range names {
		if err := j.c.Archive.Delete(ctx, n); err != nil {
			errs = append(errs, err)
			if n == manifest {
				// The manifest still stands: the rest are not orphans.
				return err
			}
		}
	}
	return errors.Join(errs...)
}

// trimRuns deletes the records of the backups just removed and the oldest
// finished records beyond [KeepRuns].
func (j *Job) trimRuns(ctx context.Context, runs []recorded, removed, orphans []string) error {
	gone := map[string]bool{}
	for _, id := range append(append([]string{}, removed...), orphans...) {
		gone[id] = true
	}
	var finished []recorded
	for _, r := range runs {
		if r.run.State == StateCompleted || r.run.State == StateFailed {
			finished = append(finished, r)
		}
	}
	var errs []error
	for i, r := range finished {
		if gone[r.run.ID] || i < len(finished)-KeepRuns {
			if err := j.c.State.DeleteIfRevision(ctx, RunPrefix+r.run.ID, r.rev); err != nil && !errors.Is(err, port.ErrConflict) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
