// Package export reads an installation through the ports and writes it into a
// backup archive ([backup.Writer]). It is the producer half of the archive:
// the restore engine decodes what it writes with [Decode].
//
// # What is exported
//
// For each module of layout v5:
//
//   - state: every State record the module owns, with the lifetime it has left,
//     and, for oidc, every Index set. The leases, notifications, gates, caches
//     and dedupe records of the controllers, and the maintenance record, are
//     not exported: they are rebuilt, or (the maintenance record) managed by
//     the restore itself. The backup module's own records (its runs, its
//     restores, its retention pass) are not exported either.
//   - secrets: every secret under internal/<module> and, where the module has
//     an external namespace, external/<module>, as the stored document (its
//     latest version). The backup module's own secrets (the restore identity)
//     are not exported: the backup role never reads them.
//   - blobs: the objects under the module's blob prefixes ([BlobPrefixes]),
//     each a file of its own in the archive.
//
// # Records
//
// A record is one JSON object on one line of the module's state.jsonl or
// secrets.jsonl. Every record carries `v` (the record version,
// [RecordVersion]) and `t` (its type), so it is read without the file that
// held it:
//
//	state   {"v":1,"t":"state","key":..., "value":<base64>, "expires":<RFC 3339>}
//	index   {"v":1,"t":"index","key":..., "members":[...], "expires":<RFC 3339>}
//	secret  {"v":1,"t":"secret","ns":"internal|external","name":..., "doc":<the stored JSON object>}
//
// A State key is the logical key, which [port.Locate5] addresses; `expires`
// is absolute and absent for a permanent record, so a restore later computes
// what is left. The values of secrets and records appear only inside the
// encrypted archive.
//
// # One pass
//
// A run is one pass over a fixed list of units (one State family, one set
// family, one secret namespace or one blob prefix of one module); it is not
// resumed. The writer spools to local disk, so memory is one record or one
// blob.
package export
