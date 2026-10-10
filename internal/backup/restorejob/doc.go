// Package restorejob is the restore function's work: one restore at a time,
// asynchronous, with every module under maintenance while it writes
// (docs/decisions/0071 and 0072, point 9).
//
// A restore is a status record `rec.backup.restore.<id>` in the backup module's
// table and the lease `lease.restore:run`, so a second start is refused while
// one holds it. Starting sets the maintenance flag in EVERY module's own table,
// waits for the modules' cached view of it to expire, and only then calls
// [restore.Apply], which verifies the archive, writes, and reads everything back.
// The flag is lifted only when Apply returned cleanly. Any failure (a verify
// that differs, a destination that holds other data and no overwrite was asked
// for, an archive that does not belong, a key, a store) leaves the flag set and
// says so in the record.
//
// A slice runs under the invocation's deadline. When time runs out the run is
// paused and the next invocation continues it: Apply is idempotent, so what was
// written is found again and only the rest is written. The record is the only
// state; nothing is kept in the process.
package restorejob
