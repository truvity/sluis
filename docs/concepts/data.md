---
title: Data
description: "How sluis keeps records safe: origins and sensitivity, one writer per record kind, encryption of every value, the data ports, fencing tokens and backups."
status: target
sidebar:
  order: 8
---

sluis keeps records: sessions, directory facts, operator-set configuration, quota balances and meters, the signing ring's metadata, audit batches. Most of the security of the design rests on who is allowed to change which of them. Three rules carry it: every record kind has an origin and a sensitivity, every record kind has one writer, and trust classes are kept apart.

## Origins and sensitivity

Every stored **record kind** declares one **origin**, and every field one **sensitivity**, in the configuration schema ([ADR 0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). Together they decide where a value lives, who may write it, whether it is in a backup, what a restore does with it, whether it may leave sluis, and what an audit record, a log line or a span may say about it. The backup list, the deny matrix, the telemetry allowlist and the audit redaction are generated from them.

To classify a record kind, ask in order; the first "yes" is its origin.

| Origin | The question | Examples | Written by |
|---|---|---|---|
| **declared** | Did an operator write it in a reviewed file, delivered read-only? | the install file, policy, clients, exchange trust, quota limits, export recipients | Nobody at run time |
| **operator-set** | Did a person choose it at run time, on the console, the CLI or the API, as a lasting intent? | chat channels managed from the console, runtime switches, a console-connected directory tenant, a secret put at run time | The actor that owns the kind |
| **learned** | Can sluis rebuild it without a person from an external system, by pull or by push? | directory users and groups, memberships, discovered channels, a client metadata cache | The integration that observed it |
| **derived** | Can sluis rebuild it from its own records or log? | the search index, quota meters and hourly aggregates | The deriving actor |
| **issued** | Otherwise: sluis created it, or a vendor or a person handed it over once. **Durable** or **ephemeral** | durable: client secret versions, revocations, grants, the issuance ledger, vendor-issued secrets, pins, account links, approvals, quota balances; ephemeral: sessions, refresh tokens, codes, leases | The issuing actor |

An act, such as an approval or a consent, is an issued record of the act, not configuration. The audit log itself and the keys in the `keys` port are outside the model.

**Push is notify, then pull.** A product that pushes (a customer identity product pushing membership, for example) sends a small notification to `entry`, which routes it to the integration actor. The integration reads the full state from the product's API, and a periodic full re-sync runs as well. The product authenticates with its workload identity and touches only its own organisations.

| Sensitivity | Meaning | Audit record | Telemetry | Logs |
|---|---|---|---|---|
| **public** | Meant to be published (key sets, CA certificates) | yes | only allowlisted | yes |
| **internal** | Not published, personal or secret (client ids, channel names, digests) | yes | allowlisted identifiers only | yes |
| **personal** | About an identifiable person (an email, a subject, a membership) | with identity treatment | never | redacted by default |
| **secret** | Credential material that is read | a fingerprint and a revision only | never | never |

A field may also carry a **free-text** flag, for text a person typed: it never enters telemetry, and its length in an audit record is bounded.

## One writer per record kind

Every **record kind** has exactly one **record writer**: one actor that is allowed to create and change it. Other actors may read it, if their manifest says so, and may not write it. The writer of a kind is written down in the manifests and enforced in two layers ([Defence in depth](/concepts/drivers/#defence-in-depth)): the port library scopes every call to `<boundary>/<kind>/…` from the actor's own identity, and the store checks the actor's own credentials. The owner of an rqlite cluster has the only user that may `execute`, and its readers have `query`-only users; on DynamoDB, an IAM `dynamodb:LeadingKeys` condition per actor role; on Postgres, grants and row-level security. There is no shared state driver in between.

Examples (illustrative; the full list is generated from the manifests):

| Record kind | Its single writer | Why only one |
|---|---|---|
| Audit batch sequence | The audit `writer` of that log | The sequence must have no gaps or forks, so there is exactly one writer per log. Purge and erasure requests go to the writer through a call and are appended by it. |
| Quota balances | `quota` | Balances are issued records that change only through `quota`; the meters are derived from the log |
| Signing ring | `signer` | The ring is the authority for token keys |
| Sessions | `session` | The owner of sessions and the session cookie |
| Directory snapshots | The facts integration that produced them | Another process holds only a cached copy with an expiry |
| Operator-set records of a kind | The actor that owns the kind, on a request routed by `console` | The owner validates the write with its own logic; the manifest says which kinds accept operator-set writes |

When two processes seem to need to write one record, the record is split instead: the learned fields become a kind the integration writes, and the operator-set fields a kind its owning actor writes, each with its own writer. The word **writer** by itself means this on this page, and not the audit `writer` actor.

## Trust classes are kept apart

In v1 one state store holds hot sessions, the signing key ring, leases, controller journals, directory records and run records. Write access to that store is therefore inside the trust boundary, and on a store without per-record permissions the isolation between them is only a rule in a query layer.

v2 keeps records of different **trust classes** and origins apart, in separate namespaces at least and in separate stores where an installation allows. Operator-set records, learned facts, issued records and sessions never share a namespace, even when pieces of an installation share one driver instance. The signing ring and leases never share a namespace with sessions and directory records. Key material has its own three classes:

1. the ring and its wrapping, used by the issuer only;
2. vendor-issued secrets held for integrations (a GitHub App key, a chat token), behind the `secrets` port;
3. authority keys (the audit log key, the CA keys), which have their own holder and are never read by the issuer.

## Every value is encrypted

**Every state value is encrypted in the port library**, before it reaches any driver (agreed, EC-1). A store, its replicas and its backups hold ciphertext.

- **AES-256-GCM**, which is FIPS-compatible.
- **A data key per (boundary, record kind).** It is wrapped by the boundary's key-encryption key through the `keys` port. Unwrapping is allowed for the kind's writer and its declared readers only: by the `key-holder` access list, or by the KMS key policy with an encryption-context condition.
- **The additional authenticated data is (boundary, kind, key, version)**, so a value moved to another record, kind, boundary or version fails to decrypt.
- **Record keys never contain personal data.** Only the values are encrypted, so the keys must not carry an email address or a name.

## Ports

Actors reach their data through five ports. Each has a contract that does not change with the technology behind it.

| Port | What it holds | The contract |
|---|---|---|
| `state` | Records, with revisions | Get, put with a precondition, compare-and-set, an atomic batch and expiry ([ADR 0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)). Delete, listing by prefix, a conditional adjust and a feed of changes are proposed further operations (ADR 0057) |
| `index` | A search index derived from batches | Apply a batch, read and set a cursor, query, rebuild from the batches |
| `blob` | Immutable objects, such as audit batches | Put once, get and list by key, with a lock mode of none, removable or compliance |
| `transport` | Messages that must survive a restart, such as audit records, or a stream of them to the writer | Put, receive, acknowledge, return with a delay; or a direct stream |
| `lease` | Named, time-limited ownership | Acquire a named lease, renew, release |

The contract has no unconditional put. A caller changes a record only against the revision it read.

### The atomic batch

A **write batch** applies several writes together or none of them. The audit `writer` uses it to register the sealed batches of its audit log and its usage log in the sequence, record the ids it deduplicated and move its cursor, so a crash cannot leave one without the others, and the two logs never disagree ([ADR 0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). The next number of the sequence is allocated by a compare-and-swap inside the same batch.

### Conditional adjust

:::note[Proposed]
The conditional adjust is a proposed further operation of the state port (ADR 0057). Until it is decided, quota uses compare-and-set.
:::

A **conditional adjust** changes a number by a delta, and refuses if the result would cross a floor. It is one operation on the store, not a read followed by a write. If it is decided, quota would use it for strict admission: a counter that many requests hit at once would make a compare-and-set loop contend, and a read-then-write would let two requests both pass.

### Leases and fencing tokens

A lease is **named**: there is one lease for each thing that needs a single owner, and many can be held at once by different holders. Acquiring a lease returns a **fencing token**, a number that grows each time the lease changes hands. A holder passes its token with every write it makes under the lease, and the store refuses a token that is lower than one it has seen. A holder that was paused, lost its lease, and woke up believing it still held it, cannot write over the new holder.

The final form is agreed:

- **Leases live on the state store**, built on compare-and-set with a fencing token, and independent of the Raft leader. A named lease `leader` stays available.
- **Where the target store can fence, the fence is in that store.** A Postgres write checks the token in the same transaction. Blob batches are created with `If-None-Match: *` (create-if-absent), so an audit log cannot fork; with one log per node, the writers on boards need no lease at all.
- **External side effects** (the GitHub, chat and directory reconcilers) rely on the lease plus reconcilers that are idempotent.
- **A session advisory lock is never used alone**, because it gives no fencing token.
- A Kubernetes Lease driver is in the backlog. Whether a Postgres (CNPG) state driver is added is a question for the Kubernetes records.

## Where the data lives

The drivers behind the ports differ by installation (see [Ports and drivers by installation](/concepts/drivers/#ports-and-drivers-by-installation)). Two choices matter for data:

- **State** is the embedded single-node store on one node, **rqlite on Raft** across the three boards, with **one cluster per (boundary, owning actor)**, and **DynamoDB** on serverless and on Kubernetes, with a table per boundary. The Raft peer traffic is each cluster's own port over mutual TLS.
- **Index** is `sqlite` on a single server, and `rqlite` on a cluster of boards, so the reader can run on any node. It is `postgres` on Kubernetes (the `cnpg` mode, or `external` with any connection string), and `postgres` on Aurora on the lambda installation, reached through RDS Proxy ([ADR 0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)). The search index is derived from the audit batches and can always be rebuilt from them.

## Secrets

Two further ports hold values that are not records. The **`secrets`** port holds whole values under addresses: sluis's own secrets and its integrations' secrets. The **`export`** port publishes a small, typed set of documents that something outside sluis may read, with file and bucket drops age-encrypted to the consumer's own recipient. sluis is not a general secrets engine. See [Secrets and export](/concepts/secrets/).

## What is backed up

A backup contains only authoritative state that nothing else can recreate: operator-set records and durable issued records, which include quota balances, pins and vendor-issued secrets (archived sealed). Declared configuration, learned facts, derived data such as quota meters and the search index, sessions, refresh tokens and the transport spool are not in it, and a restore invalidates every session. The audit log is protected separately by its lock, its witnesses and replication. See [Backup and restore](/concepts/backup/).
