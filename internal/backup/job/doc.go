// Package job runs the backup module's work: one backup under the backup
// lease, its status and retention records, the audit records, the listing of
// the archive and the prune. It holds no AWS client: the State, the archive
// Blob and the export Source are handed in (internal/backup/app wires the real
// ones), so every rule here is tested over memory ports.
//
// # A run
//
// A run takes the lease `lease.backup:run` in the backup module's table, so a
// second run, on another replica or a retry of the schedule, ends cleanly
// instead of writing a second archive. It writes a status record
// `rec.backup.run.<id>` (running) before it exports anything, exports the
// installation into a local file encrypted with age to the configured
// recipients (internal/backup), uploads it with one write, writes the sidecar
// last, and closes the record as completed or failed. A run is one invocation:
// a record still running when the next run takes the lease belongs to a run
// that died, and is closed as abandoned. The resume call of an earlier
// release ([Request.ResumeOnly]) answers idle.
//
// # The id
//
// A backup's id is the UTC start time and a random suffix,
// 20261013T020000Z-ab12cd. It is one path segment of letters, digits and '-',
// sorts by time, and does not repeat.
//
// # Retention
//
// A prune keeps the `keep` newest completed backups and any younger than
// `maxAge`, and deletes the rest, the sidecar first and then the object, so a
// backup that is half deleted is not a backup. It also removes the object of a
// run that never wrote a sidecar, once the run is older than [OrphanAge]. It
// takes the same lease, so it never runs beside a backup. On a bucket with
// Object Lock a delete the lock refuses is counted and reported, not retried,
// and the backup stays listed until its retention ends.
package job
