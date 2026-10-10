// Package backup is the archive format of a sluis backup and its encryption.
// It holds no exporter and no restore logic: those write and read records
// through [Writer] and [Reader].
//
// # Layout
//
// A backup is a set of objects in an S3-compatible bucket, written through a
// [port.Blob], under
//
//	backup/<installation>/<id>/
//	    manifest.json
//	    state/<module>/<n>
//	    secrets/<module>/<n>
//	    blobs/<module>/<n>
//
// The three sections hold the module's State records, its secrets and its
// blobs. Each is a run of chunks numbered from 0. A chunk is a sealed run of
// records; the records are opaque bytes to this package (the exporters
// define them). The manifest is written last, so a backup without a manifest
// is not a backup.
//
// # Sealing
//
// One data key is generated per backup with the configured key
// (keys.Archive; a KMS alias or an OpenBao transit key) under its
// configured encryption context, and stored wrapped in the manifest. Two
// keys are derived from it with HKDF-SHA-256: one per chunk, bound to the
// chunk's object name, and one for the manifest's MAC. A chunk is sealed
// with AES-256-GCM; its additional data names the installation, the backup
// id, the section, the module and the chunk number, so a chunk moved to
// another place, another backup or another position does not open. The
// manifest lists every chunk's SHA-256 (of the stored bytes) and size and is
// authenticated by an HMAC-SHA-256, so a changed, dropped, added or
// reordered chunk is found before any record is used.
//
// Nothing in the manifest is secret: it holds the module names, counts,
// sizes and hashes, the wrapped data key and the creator. Every record,
// whether a State item, a secret value or a blob, is inside a sealed chunk;
// there is no plaintext copy of any of them in the archive. Without the right
// to decrypt with the key the data key is not recovered and [Open] fails
// closed.
//
// # Versions
//
// The manifest carries a format version. A reader opens the current version
// and the one before it ([Readable]); anything else is refused with
// [ErrFormat]. The golden archives under testdata keep that promise: a
// released format's archive stays in the tree and is opened by every later
// release.
package backup
