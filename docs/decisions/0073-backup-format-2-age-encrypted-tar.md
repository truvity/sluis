# 0073 — Backup format 2: one age-encrypted tar.gz per backup

**Status:** Accepted (2026-10-12). Amends point 9 of [0071](0071-least-privilege-modules-one-credential-holder-per-function.md)
(a backup is encrypted to age recipients, not under a KMS or OpenBao key) and replaces backup format 1 of
[0072](0072-storage-layout-v5-module-first.md).
**Date:** 2026-10-12

## Context

Format 1 wrote a backup as `manifest.json` and many 8 MiB chunk objects. Each chunk was sealed with AES-256-GCM under
a data key from the archive key (KMS), with keys derived by HKDF and a manifest MAC. A run could pause and resume
across invocations. No format 1 backup was ever kept on an installation, so nothing has to stay readable.

Three faults decided it:

- **Bespoke cryptography.** The chunk sealing, the key derivation and the MAC were ours, reviewed by us alone.
- **The KMS key is a single point of failure.** A deleted, disabled or unreachable key, or a lost grant, makes every
  backup unreadable. The disaster a backup is for may be the one that took the key.
- **Unreadable without sluis.** Only the sluis reader, with that key's decrypt right, could open a backup.

## Decision

1. A backup is two objects: `backup/<instance>/<id>.tar.gz.age` and the plain sidecar `backup/<instance>/<id>.json`,
   written last. An object without its sidecar is not a backup.
2. The object is a POSIX (PAX) tar, compressed with gzip and encrypted with age to the X25519 recipients in
   `backup.recipients`. `manifest.json` is the first member and lists the SHA-256 of every other member. A module's
   records are JSONL files and its blobs are files of their own.
3. The backup role holds only public keys: it can write a backup and cannot read one. The restore role reads the age
   identity from its own secret, `internal/backup/restore-identity` by default (`backup.identity`).
4. A backup is one invocation. The resume call of format 1 answers idle.
5. Anyone with an identity opens a backup with standard tools: `age -d -i key.txt <id>.tar.gz.age | tar xz`.

## Consequences

- The archive key and its KMS grants are no longer used by the backup. `backup.key`, `backup.region` and
  `keys.archive` are accepted for one release and ignored.
- An installation must set `backup.recipients` before it upgrades, and keep the identities offline. Losing every
  identity loses every backup; two recipients, held apart, are the guard.
- Integrity rests on age's authenticated encryption, the sidecar's SHA-256 and the manifest's hashes.
- A run needs local disk for the backup twice over (the spool and the encrypted file); `/tmp` on Lambda.
- A backup larger than one invocation can write is not possible; such an installation needs a larger function.

## Alternatives considered

- **Keep format 1.** Rejected for the three faults above; no backup in it existed to keep.
- **zstd or xz instead of gzip.** Better ratios, but gzip is in Go's standard library and every `tar`. A backup is
  small next to its bucket; readability without sluis came first.
- **Cloud-native only (S3 server-side encryption, Object Lock, AWS Backup).** The data would be readable by anyone
  with read on the bucket and the key. It keeps the KMS single point of failure and does not exist on Kubernetes.
  Server-side encryption and Object Lock stay on the bucket, under age.

## Amendment: a restore proves a backup genuine (2026-10-18)

The recipients are public keys and the sidecar is unsigned JSON. Anyone who can put an object under `backup/<instance>/` could plant a backup that a restore accepted, and a restore writes every module's table, signing keys included. The 2026-10-18 review found several principals with that right.

Decision D682 (b): the backup function records each backup's hashes in the audit trail, and a restore checks them there.

1. `roster.backup.completed` carries the object's key, size and SHA-256, the sidecar's SHA-256 and the SHA-256 of each recipient. The record is written after the sidecar.
2. Before it reads the identity, a restore asks the audit query service for the record of the backup, reading every page of the search up to a bound. It requires a record stamped by the backup function's observer (or an explicit `anyObserver`), of the installation, and every record of the backup to agree, with the hashes of the object and sidecar it fetched, the recipients of the sidecar, and an hour that is sealed.
3. A restore with `backup.recipients` set requires the sidecar to list those. No flag overrides it. This catches a backup written before the recipients changed; it is not a defence against forgery, since the recipients are public.
4. `allowUnverified` goes ahead without the record, for the day the audit pipeline is down: for no record, an unsealed hour or a trail that cannot be asked, and never for a record that disagrees with the backup. It needs `backup.recipients`. The decision is kept in the trail first (`roster.restore.unverified`, delivery `block`), or the restore is refused unless `allowUnaudited` is passed; `.started` and `.completed` say `unverified` and why.
5. A restore that paused is continued against the hashes it began with and does not ask the trail again.

Consequences:

- A restore depends on the audit pipeline. Unconfigured, unreachable, or with the record lost in delivery, it refuses until an operator passes `allowUnverified`. The owner chose this knowing that.
- A backup made before this change has no hashes in its record. It restores only with `allowUnverified`.
- A backup is available to restore once the notary has sealed the hour of its record: at the notary's first run after that hour ends, up to about an hour and a quarter after the run.
- "Sealed" is what the query service reports: a seal object exists for the hour. The restore checks neither the seal's signature (`audit verify --root` does) nor that the seal covers the record's object. Both are follow-ups.

Alternatives rejected:

- **Sign the manifest with the notary or a KMS key.** It needs a new signing key for the backup function and a verify path in the restore. The audit trail already holds records nobody who can write the bucket can change.
- **Compare the sidecar's recipients only.** The recipients are public: the attacker lists them too.
