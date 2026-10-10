// Package restore writes a backup ([backup.Reader]) into an installation of
// layout v5, and shows beforehand what that would do.
//
// # Preview and Apply
//
// [Preview] verifies the archive, reads the destination and reports, per
// module and section, what would be created, overwritten or left alone. It
// writes nothing. The [Report] carries names and versions only; a value, a
// secret or a blob body is read to be compared and never leaves the package.
//
// [Apply] verifies the whole archive before the first write (a tampered or
// truncated archive is refused with nothing written), plans, refuses a
// destination that holds a different value unless [Options.Overwrite] is set,
// writes, and reads everything back: any record that differs makes the call
// fail with [ErrVerify]. The destination is not wiped; records the archive
// does not hold are left as they are.
//
// # What is restored
//
//   - State records with [port.State.Put], carrying the lifetime that is left:
//     a record's absolute expiry in the archive is turned into a remaining
//     lifetime now, and an expired record is skipped.
//   - Index sets with [port.Index.Add]; on overwrite the members the archive
//     does not hold are removed.
//   - Secrets as documents, created with an empty revision, or written over
//     the current revision on overwrite.
//   - Blobs, reassembled from their parts and checked against their hash.
//
// Leases, notifications, gates, caches, dedupe records and the maintenance
// record are never restored: the modules regenerate them, and the maintenance
// record belongs to the restore run that calls this package. An archive that
// holds one anyway has it counted as regenerated.
//
// # Order
//
// Modules are written in [port.Modules] order with oidc last, and inside
// oidc the secrets and blobs and the records first and the signing key ring
// (its records and its index sets) at the very end, so that a restore that stops
// half way never leaves a ring whose sessions and tokens are missing.
//
// # Resuming
//
// Apply is idempotent: a record already equal to the archive's is not written
// again, so a run that was stopped (context cancelled, a write that failed)
// is continued by calling Apply again.
package restore
