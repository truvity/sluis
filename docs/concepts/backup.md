---
title: Backup and restore
description: "What sluis backs up (authoritative state only), what it does not, how archives are encrypted, and how a restore proves and rebuilds the rest."
status: target
sidebar:
  order: 10
---

A backup exists for the day the installation is lost or hostile. It is deliberately small. sluis backs up the **authoritative state that nothing else can recreate**, and rebuilds everything else after a restore.

This page is the design. Nothing on it is shipped yet. Where a detail is not settled it is marked as proposed.

## What is backed up

The backup is a **logical archive**: the `backup` side reads records through the ports (never through a database), and the restore side writes them back through the same ports. The archive is therefore the same on every driver, and it can be restored onto a different driver of the same port (rqlite to DynamoDB, for example).

Only **authoritative, non-recreatable state** goes in. In practice that is the records which exist nowhere else:

- client registrations, including the verifiers of generated client secrets;
- grants and revocations;
- sluis's own generated secrets, stored through the [secrets port](/concepts/secrets/);
- the metadata that other records depend on, such as the signing ring's public side and the record of which keys were expected.

Each record kind in the actor manifests is marked as durable or ephemeral. The list of what is backed up is therefore generated from the manifests, not written here by hand.

## What is not backed up

| Not backed up | Why |
|---|---|
| **Non-extractable keys** (a KMS key, a key on a YubiKey) | They cannot leave the device or the service. Their "backup" is the way they were provisioned: the key policy, the [ceremony](/concepts/keys/#the-roots-and-the-ceremony) and the second card. A restore checks that each key purpose can still `sign` and that the public key matches. |
| **Derived keys** | They are recomputed from a key that is held elsewhere. |
| **Declared configuration** | It lives in version control. The restore starts from `sluis config check` against the archived installation document. |
| **Operator secrets** | The operator put them there and the operator's own store holds them. They are referenced, never inlined. |
| **Learned facts** | Directory snapshots, group memberships and the like are pulled again from their sources. |
| **Sessions and refresh tokens** | Not backed up, on purpose. A restore invalidates every one of them (agreed), so a stolen token does not survive a restore, and everyone signs in again. |
| **Quota counters** | They restart at zero. Metering resumes from the restore. |
| **The transport spool** | It holds records not yet written to the log. Cluster peers and witnesses narrow the window; a backup cannot close it. |

## The audit log is handled separately

The [audit log](/concepts/audit/) is not tarred into the archive. It is the record, and it is protected by other means:

- a **lock mode** on the log store (`compliance` where the installation has it, so that nobody can delete or overwrite before retention ends);
- **witnesses** that have co-signed the heads;
- **replication** to another account or credential, readable by a verifier with no help from sluis.

The **search index** is a rebuildable cache. A snapshot of it is an optional accelerator that shortens a restore, and it is never evidence. Without a snapshot, the indexer replays the log.

The archive records its own existence in the audit log: a `backup.completed` record carries the archive's digest and the fingerprints of its recipients. That record is what a restore later uses to prove where an archive came from.

## Encryption

:::note[Proposed]
The archive is encrypted with a fresh data key, and the data key is wrapped for two kinds of reader.
:::

- **Through the keys port.** The `keys` port gains `wrap` and `unwrap` (or an `ecdh` operation). On AWS the data key is wrapped with KMS `Encrypt`, with an encryption context that names the backup, so only the restore role can decrypt. On boards a YubiKey wraps it.
- **Plus at least one offline operator YubiKey recipient, for break-glass.** The recipient is a public key (for example an `age-plugin-yubikey` recipient). The identity is on a card that is not on any node. This is what lets an operator open an archive when the installation, the cloud account or the node is gone.

On AWS, only the restore role may decrypt. The archive and its KMS key preferably live in a **separate account**, because the backup exists for the case where the primary account is lost or hostile. The backup side holds public material and a wrap permission only, so a compromised backup job cannot read earlier archives.

## Restoring

A restore is started by an operator and never by the console, so that it works when `entry` and `signer` are down. It runs as its own actor with its own identity.

1. **Maintenance mode.** Every actor refuses writes and sign-in stops. A failure at any later step leaves maintenance mode on.
2. **Provenance check.** The archive is accepted only if the audit log has a `backup.completed` record carrying its digest, and the record sits under a checkpoint that a witness has co-signed where the installation has one. When the log cannot be reached, the operator can override, and the override is itself recorded first.
3. **Decrypt.** The restore actor unwraps the data key through the keys port, or the operator brings the offline YubiKey.
4. **Write the records** through the ports, under maintenance mode. Every member's digest is checked first, and every record is read back afterwards.
5. **Roll forward from the audit log.** For record kinds whose audit records carry enough data, the restore replays the log from the archive's time to the present: **revocations, client registration metadata and grants**. The audit records are designed for this, and they never carry secrets. A revocation that happened after the last archive is therefore not lost.
6. **Rebuild derived state.** The indexer rebuilds the search index (or loads a snapshot), learned facts are pulled again, and quota counters start at zero.
7. **Everyone re-signs in.** Sessions and refresh tokens do not exist after a restore. Clients obtain new ones.
8. **Lift maintenance** only after the read-back is clean.

A witness is **not restored**. A witness restored to an older state would co-sign a rolled-back view. After a restore it is reset and re-trusted, or taken from its peers.

### Certificate serial numbers need no ledger

CA serial numbers are **random 128-bit values**. No serial ledger has to be backed up or restored, and a restored CA cannot hand out a serial that collides with one issued after the archive was taken.

### Rehearsal

A restore that has never been tried is a guess. A scheduled **rehearsal** restores the latest archive into an empty scratch installation, compares the per-kind counts and digests with the manifest, and records `restore.rehearsed`. Missing rehearsals alarm, in the same way as missing backups.

## On each runtime

| Runtime | Backup runs as | Restore runs as |
|---|---|---|
| [bare-metal](/runtimes/bare-metal/) | A systemd timer. On a cluster the lease picks one node | A CLI on one node with the other actors stopped |
| [serverless](/runtimes/serverless/) | A scheduled function. It spools to `/tmp`, so one invocation bounds the archive size | A separate function, invokable only by an administrator or a break-glass role |
| [kubernetes](/runtimes/kubernetes/) | A CronJob | A Job started by the operator, never a CronJob |

The archive's blob driver declares its own dependencies and lock mode. `r2` can be unlocked by an account administrator, so an installation on boards pairs it with a second, offline copy.
