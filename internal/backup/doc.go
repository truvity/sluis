// Package backup is the archive format of a sluis backup (format 2). It holds
// no exporter and no restore logic: those write and read records through
// [Writer] and [Reader].
//
// # Objects
//
// A backup is two objects in an S3-compatible bucket, written through a
// [port.Blob]:
//
//	backup/<instance>/<id>.tar.gz.age   the backup
//	backup/<instance>/<id>.json         the sidecar, written last
//
// The id is the UTC start and a random suffix, 20261013T020000Z-ab12cd. The
// sidecar ([Sidecar]) is plain JSON: the object's key, size and SHA-256, the
// recipients it was encrypted to and, per module, the counts. It names no
// secret and holds no value. It is written only after the object, so it is
// the completion marker: an object without a sidecar is not a backup.
//
// # The object
//
// The object is a POSIX tar (PAX), compressed with gzip and encrypted with age
// (https://age-encryption.org/v1) to the configured X25519 recipients, binary,
// not armored. It is read without sluis:
//
//	age -d -i key.txt <id>.tar.gz.age | tar xz
//
// The tar's members, in this order:
//
//	manifest.json
//	<module>/state.jsonl
//	<module>/secrets.jsonl
//	<module>/blobs/<escaped object key>   (sorted by member name)
//
// for each module in [port.Modules] order. A JSONL line is one export record
// (internal/backup/export); a blob is the object's bytes. Every member has mode
// 0600, the backup's start as its modification time and no owner names.
//
// manifest.json ([Manifest]) is first and lists the SHA-256 of every other
// member and the counts of each module, so a reader verifies each member as
// it reads it and refuses a missing, extra, reordered or changed one.
//
// # Escaping a blob key
//
// An object key becomes a relative path that cannot leave its directory and
// reads back to the same key ([EscapeKey], [UnescapeKey]): the key is split at
// '/', and in each segment every byte that is not a letter, a digit, '-', '_'
// or '.' is written %XX (upper-case hex), as is a '.' that begins a segment
// (so no segment is '.', '..' or hidden); an empty segment is written '%'.
//
// # Encryption
//
// The writer needs only the recipients' public keys: it holds no key that
// decrypts, and no KMS. The restore reads the age identity (AGE-SECRET-KEY-1...)
// from its own secret. age authenticates the whole stream, so a changed byte
// fails the decryption; the sidecar's SHA-256 is checked before that, and the
// manifest's hashes after it.
//
// # Versions
//
// The sidecar and the manifest carry the format version; this release reads
// format 2 only ([Version]). Format 1 (chunks sealed under a KMS data key) is
// gone: no format 1 backup was ever kept.
package backup
