# 0029 — The audit log is audit's: the tiled log, sealing, witnesses, locks, pseudonyms, retention and tiers moved to truvity/audit

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

The log is what the audit pieces write and witness: a tiled, sealed, witnessed log of encrypted records, one per project and log kind, with pseudonyms for people, per-tenant keys, retention and lifecycle tiers. None of it depends on an identity product, and every product that emits needs the same log. The log therefore belongs to the audit product, [truvity/audit](https://github.com/truvity/audit), and the decisions about it moved there ([0028](/decisions/0028-audit-pieces/) says why). This record stays under its number and says where each part went and what sluis keeps.

## Decision

### What moved to audit

| Subject of the old record | Now in audit |
|---|---|
| Projects, buckets and prefixes, one writer per log, the origin line | [0003 the neutral model](https://github.com/truvity/audit/blob/master/docs/decisions/0003-the-neutral-model.md), [0008 the log](https://github.com/truvity/audit/blob/master/docs/decisions/0008-the-log.md) | <!-- terms-allow: origin (the origin line of a checkpoint) -->
| The tiled log, the write-once fence, sealing and seal entries, the writer's state, duplicates, quarantine, the clock rules | 0008 |
| Witnesses, what a verifier checks, merging the logs of a node cluster | [0009 witnesses and verification](https://github.com/truvity/audit/blob/master/docs/decisions/0009-witnesses-and-verification.md) |
| Lock modes, retention, legal holds, lifecycle tiers, the bucket guard, compliance profiles | [0010 lock modes, retention, legal holds, lifecycle tiers and compliance floors](https://github.com/truvity/audit/blob/master/docs/decisions/0010-lock-modes-retention-and-compliance-floors.md) |
| Evidence encryption, pseudonyms, a tenant's exit | [0011 evidence encryption and pseudonyms](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md) |
| The three ways to emit, and facts that are lost being counted | [0005 the security class](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md) |
| Usage records are never best effort | [0006 the usage class](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md) |

### What atoom owns

The blob port, its write-once create, and the file system drivers are atoom's ([blob port](https://github.com/truvity/atoom/blob/master/blob/docs/blob-port.md)); the S3 driver is planned there. The mechanics of the keys the log uses (purposes, data keys, wraps, Seal, Open, Mac, signing by purpose) are atoom's `seal` block ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)).

### What sluis keeps

- **sluis's key service is the host of `seal` where audit runs beside sluis** ([0006](/decisions/0006-keys-and-sealing/)). Its rules let the writer sign only with its own log key and Seal under `evidence` but never Open, and let the indexer and the reader Open only under a grant that `policy` sealed for the tenant ([0007](/decisions/0007-policy-and-sealed-grants/)). The log key is P-256, the one exception to P-384, because the checkpoint format fixes it; each writer instance has its own log key, and on a board its own card slot.
- **The tenants' keys are the boundary's.** A tenant's exit deletes its `data`, `pseudonym`, `chain` and `credential` wraps at once and its `evidence` wrap when the evidence retention ends ([0008](/decisions/0008-boundaries-and-tenants/)).
- **sluis declares the emit call of each of its actions,** in its action table, by audit's three calls. The required call, which fails the operation when the record is not durable, is for: a grant, a token signature, a PIN release, a read of the log, the successful sign-in, code and token of `signin`, the successful outcomes of `exchange` and the adapters, and the successful actions of KV ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)). A revocation and a change of the signer's ring wait for their fact and never fail because of it. Every refusal, denial, start and reload is best effort.
- **sluis chooses the identifier that a pseudonym is made from:** the sluis subject for a person, the upstream's issuer and subject where sluis assigned none, the workload or client id, and the vendor and the vendor's account id for an external account of a reconciler ([0026](/decisions/0026-reconcilers/)).
- **sluis's `ca` keeps its issuance log** under its own project, in another C2SP format ([0023](/decisions/0023-certificate-authorities/)).
- **sluis does not back up the log.** A restore re-registers a node with its peer witnesses and never restores a witness to an older state ([0010](/decisions/0010-backup-and-recovery/)). A break-glass recovery spools its audit record locally and delivers it later, so a down audit path does not block the way in ([0017](/decisions/0017-identity-and-sign-in/)).
- **The command stays `sluis audit verify`** until the planned rename to `audit verify`, which audit owns.

## Consequences

- One home for the log. A change to its format, keys, retention or witnesses is a record in audit.
- sluis's rules for audit's purposes stay in sluis's key service, so a change of audit's purposes needs a change of the host's rules.
- The code of the writer and the emitters is still in this repository until it moves.

## Roads not taken

- **Keep the log's decisions in sluis.** Every product that emits would read an identity product's records to know how its audit trail is kept.
- **A log format of sluis's own for the identity product.** Two formats to verify, and a standard witness could not check one of them.

## Follow-ups

- Open: Garage as a blob backend is backlog ([#982](https://github.com/truvity/sluis/issues/982)): it ignores conditional writes, so it cannot hold a log.
- Open: time-stamp anchoring of checkpoints (RFC 3161) is planned as REQ-AUDIT-031 ([#1027](https://github.com/truvity/sluis/issues/1027)) and is in no record of audit beyond a road not taken.
- Open: indexing the emitter's `lost` note for the reader, so that an auditor need not scan, has no issue yet. Audit's record 0005 lists it as planned.
- The build of the writer, the blob drivers and the tiers: [#837](https://github.com/truvity/sluis/issues/837). Seals and verification: [#838](https://github.com/truvity/sluis/issues/838). The emitter's pseudonyms and the reader's reversal: [#841](https://github.com/truvity/sluis/issues/841), [#839](https://github.com/truvity/sluis/issues/839). The profile files and the floor check: [#840](https://github.com/truvity/sluis/issues/840).
