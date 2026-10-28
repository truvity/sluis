---
title: Data
description: "How sluis keeps records safe: one writer per record kind, separate trust classes, the state, index, blob, queue and lease ports, batches and fencing tokens."
status: target
sidebar:
  order: 7
---

sluis keeps records: sessions, directory facts, rate-limit counters, the signing ring, audit batches. Most of the security of the design rests on who is allowed to change which of them. Two rules carry it.

## One writer per record kind

Every **record kind** has exactly one **record writer**: one role that is allowed to create and change it. Other roles may read it, if their manifest says so, and may not write it. The writer of a kind is written down in the manifests and enforced by the `state-owner` shared driver, which checks every call against `(caller role, record kind, operation)` and lets exactly one role write each kind. See [Drivers](/concepts/drivers/).

Examples (illustrative; the full list is generated from the manifests):

| Record kind | Its single writer | Why only one |
|---|---|---|
| Audit batch sequence | `audit-writer` | The sequence must have no gaps or forks. Purge and erasure requests go to the writer through a call and are appended by it. |
| Quota balances | `quota` | Counters change by adjust only |
| Signing ring | `signer` | The ring is the authority for token keys |
| Sessions | `entry` | The protocol owner |
| Directory snapshots | The facts integration that produced them | Another process holds only a cached copy with an expiry |
| Console-set configuration | The console API | Learned and declared origins stay with their own writers |

When two processes seem to need to write one record, the record is split instead: the learned fields go to the integration and the console-set fields go to the console, each with its own writer. The word **writer** by itself means this on this page, and not the audit writer.

## Trust classes are kept apart

In v1 one state store holds hot sessions, the signing key ring, leases, controller journals, directory records and run records. Write access to that store is therefore inside the trust boundary, and on a store without per-record permissions the isolation between them is only a rule in a query layer.

v2 keeps records of different **trust classes** apart, in separate namespaces at least and in separate stores where a shape allows. The signing ring and leases never share a namespace with sessions and directory records. Key material has its own three classes:

1. the ring and its wrapping, used by the issuer only;
2. software keys held by integrations (a GitHub App key, a chat token);
3. authority keys (notary, CA), which have their own holder and are never read by the issuer.

## Ports

Roles reach their data through five ports. Each has a contract that does not change with the technology behind it.

| Port | What it holds | Shape of the contract |
|---|---|---|
| `state` | Records, with revisions | Get, create, compare-and-swap, delete-if, list by prefix, an atomic batch, a conditional adjust, and a feed of changes since a cursor |
| `index` | A searchable view derived from batches | Apply a batch, read and set a cursor, query, rebuild from the batches |
| `blob` | Immutable objects, such as audit batches | Put and get by key |
| `queue` | Messages that must survive a restart, such as audit records | Publish, receive, acknowledge, return with a delay |
| `lease` | Named, time-limited ownership | Acquire a named lease, renew, release |

The contract has no unconditional put. A caller changes a record only against the revision it read.

### The atomic batch

A **write batch** applies several writes together or none of them. The audit writer uses it to register a batch in the sequence, record the ids it deduplicated and move its cursor, so a crash cannot leave one without the others. The next number of the sequence is allocated by a compare-and-swap inside the same batch.

### Conditional adjust

A **conditional adjust** changes a number by a delta, and refuses if the result would cross a floor. It is one operation on the store, not a read followed by a write. Quota uses it for strict admission: a counter that many requests hit at once would make a compare-and-swap loop contend, and a read-then-write would let two requests both pass.

### Leases and fencing tokens

A lease is **named**: there is one lease for each thing that needs a single owner, and many can be held at once by different holders. Acquiring a lease returns a **fencing token**, a number that grows each time the lease changes hands. A holder passes its token with every write it makes under the lease, and the store refuses a token that is lower than one it has seen. A holder that was paused, lost its lease, and woke up believing it still held it, cannot write over the new holder.

On a Raft cluster, leadership is just one named lease called `leader`. Every other lease is built on compare-and-swap in the state store.

## Where the data lives

The drivers behind the ports differ by shape (see [Ports and drivers by shape](/concepts/drivers/)). Two choices matter for data:

- **State** is a single-node store on one node, **rqlite on Raft** across three nodes, and **DynamoDB** on Lambda and the single EC2 shape. The Raft peer traffic is the state driver's own port over mutual TLS.
- **Index** is `sqlite` on servers and `postgres` on Kubernetes (any connection string, for example CNPG). Lambda ships no index: `audit-read` and its index run in a separate audit install that reads the same bucket. The index is derived from the audit batches and can always be rebuilt from them.

