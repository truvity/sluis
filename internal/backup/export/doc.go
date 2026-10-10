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
//     the restore itself.
//   - secrets: every secret under internal/<module> and, where the module has
//     an external namespace, external/<module>, as the stored document (its
//     latest version).
//   - blobs: the objects under the module's blob prefixes ([BlobPrefixes]); an
//     object over [BlobPart] bytes is split into parts, each its own record.
//
// # Records
//
// A record is one JSON object and the archive's chunks hold it as opaque
// bytes. Every record carries `v` (the record version, [RecordVersion]) and
// `t` (its type), so it is read without the section that held it:
//
//	state   {"v":1,"t":"state","key":..., "value":<base64>, "expires":<RFC 3339>}
//	index   {"v":1,"t":"index","key":..., "members":[...], "expires":<RFC 3339>}
//	secret  {"v":1,"t":"secret","ns":"internal|external","name":..., "doc":<the stored JSON object>}
//	blob    {"v":1,"t":"blob","name":..., "size":N, "sha256":..., "part":i, "parts":n, "data":<base64>}
//
// A State key is the logical key, which [port.Locate5] addresses; `expires`
// is absolute and absent for a permanent record, so a restore later computes
// what is left. The state section holds state and index records, the secrets
// section secret records and the blobs section blob records. The values of
// secrets and records appear only in the sealed chunks.
//
// # Resuming
//
// A run is a fixed list of units (one State family, one set family, one secret
// namespace or one blob prefix of one module). After each unit the run flushes
// the writer and hands a [Checkpoint] to [Options.Checkpoint]. A run that was
// stopped starts again from the last checkpoint with [backup.Resume] and
// [Options.Resume]: the unit that was in progress is read again from its start,
// and the chunks it had already written are written over, so the order in
// which an adapter lists records does not matter.
package export
