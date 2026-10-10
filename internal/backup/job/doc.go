// Package job runs the backup module's work: one backup under the backup
// lease, its status and retention records, the audit records, the listing of
// the archive and the prune. It holds no AWS client: the State, the archive
// Blob, the export Source and the key are handed in (internal/backup/app wires
// the real ones), so every rule here is tested over memory ports.
//
// # A run
//
// A run takes the lease `lease.backup:run` in the backup module's table, so a
// second run, on another replica or a retry of the schedule, ends cleanly
// instead of writing a second archive. It writes a status record
// `rec.backup.run.<id>` before it exports anything and after every unit of the
// export, with the export's checkpoint (the wrapped data key and the chunks
// written; no record and no secret). A run that is stopped, by the function's
// time limit or a lost lease, leaves that record in the state `running` or
// `paused`, and the next run continues it from the checkpoint instead of
// starting over, for up to [ResumeWindow]; an older one is closed as failed.
//
// A run stops by itself, state `paused`, when less than [Margin] of the
// invocation's deadline is left at a unit boundary, so a large installation is
// backed up over several invocations. `Run` with [Request.ResumeOnly] is the
// cheap schedule for that: it continues a paused run and does nothing, after
// one read of the State, when there is none.
//
// # The id
//
// A backup's id is the UTC start time and a random suffix,
// 20261010T020000Z-3fa9c1. It is one path segment of letters, digits and '-',
// sorts by time, and does not repeat.
//
// # Retention
//
// A prune keeps the `keep` newest completed backups and any younger than
// `maxAge`, and deletes the rest, the manifest first, so a backup that is half
// deleted is not a backup. It also removes the chunks of a run that never
// wrote a manifest, once the run is older than [OrphanAge]. It takes the same
// lease, so it never runs beside a backup. On a bucket with Object Lock a
// delete the lock refuses is reported, not retried, and the backup stays
// listed as deleted-failed until its retention ends.
package job
