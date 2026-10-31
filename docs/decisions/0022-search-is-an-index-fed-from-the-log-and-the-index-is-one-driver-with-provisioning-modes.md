# 0022 — Search is an index fed from the log, and the index is one driver with provisioning modes

**Status:** Accepted
**Date:** 2026-10-31

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build and parity: [issue #839](https://github.com/truvity/sluis/issues/839).
Rewritten in place on 2026-10-31 (the record format of [0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/) is decided: the core columns are mapped from OCSF paths, and the usage log needs no index).
Rewritten in place on 2026-10-31 (the points that the previous rewrite marked as inferred are confirmed by the maintainer, and their markers are removed).

## Context

Search is one of the four audit pieces ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)): the indexer, the index and the reader. 0004 decided that the index is derived data, rebuilt from the log and never evidence, that serverless may run the indexer and the reader with a `postgres` index, and that the `postgres` driver has the provisioning modes `rds` or `aurora`, `cnpg` and `external`. It left to this record the other index drivers, the default index of each tested installation, how serverless reaches Postgres, and what the reader offers.

v1 built the same split. The indexer followed the bucket by listing it from a cursor, with notifications only waking it (v1 ADR 0062, on `master`). The indexer and the query service were separate processes with separate database roles, and the query service never held the index's write credential (v1 ADR 0066). The query contract was a closed filter grammar with keyset cursors, facets, a get with provenance and an asynchronous export (v1 ADR 0048), and every read emitted an access record. The query service turned a grant into a filter term of every query (v1 ADR 0049).

Since then:

- **The log is a C2SP tiled log** ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). Following it means following checkpoints and fetching new entry bundles, not listing objects. Each entry is a clear envelope plus the record encrypted to its company. A record is a plain OCSF event in RFC 8785 JSON, and a writer also writes a usage log of CloudEvents usage events beside the audit log.
- **One log per node.** On the boards the indexer follows three logs.
- **Full-text search works on rqlite.** FTS5 was tested on 2026-10-30 on rqlite 10.6.6 (SQLite 3.53.4), on one node and on a three-node cluster, with the arm64 image under emulation. Followers answer reads locally only at the `none` consistency level, so a reader uses a small freshness bound, and `weak` to read its own write. Writes are best batched. The search table is an external-content FTS table. Native arm64 hardware and the fsync cost on eMMC are not measured yet.
- **Companies are reader tenants with their own keys** ([0009](/decisions/0009-one-installation-per-boundary/)), so the index holds data of several companies.
- **The public API is Connect only, for now.** Connect unary calls work on every runtime; server streaming needs a long-lived host.
- **Authorisation is Cedar in `policy`,** which has no state and no network ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)).
- **The audit installation has its own front** ([0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/)): a slim `entry`, the reader as its API, and its own console.

The threats this record answers are T-11, T-16 and T-26 of the [threat model](/security/threat-model/).

## Decision

### The actors

- **Search is the `indexer`, the `index` and the `reader`.** The indexer is the only writer of the index and declares `instances: exactly 1`, so with more than one node it needs a lease on the state store. The reader serves queries and holds only a read credential on the index. Both are in Go ([0060](/decisions/0060-the-language-follows-the-executable/)).
- **The indexer and the reader are separate actors with separate credentials,** so a flaw in the reader's query parsing is not a flaw in a writer of the index.
- **Where they run is derived from the port and driver rules** ([OD-32](/open-decisions/#od-32)). With `sqlite` on a cluster the reader runs on the indexer's node. With `rqlite` or `postgres` it can run on any node.

### The index follows the log

- **The indexer follows each audit log of its boundary by checkpoint.** It keeps one cursor per log, reads the latest checkpoint, and fetches the entry bundles from its cursor up to the checkpoint's size. Notifications only wake it.
- **It indexes only entries that a signed checkpoint covers,** and it checks each new checkpoint's signature and its consistency with the previous one before it moves the cursor. (inferred: tiles mean nothing without a checkpoint)
- **Indexing is idempotent, keyed by the record's hash,** so a repeated pass or a rebuilt range changes nothing.
- **The index is derived.** It can always be rebuilt from the log alone (REQ-AUDIT-016), it is not backed up, and verification never trusts it ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
- **The index keeps what the log keeps.** Rows of records past the log's retention go when the log's objects do. (inferred)
- **The index port is:** apply a batch, read and set a cursor, query, rebuild from the batches ([Data](/concepts/data/#ports)).
- **The usage log needs no search index.** Nobody searches it: quota follows it to derive meters and roll balances forward, and the finance export is derived from it ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)). The indexer neither follows nor holds a key for it.

### What the index stores

- **Fixed columns for the core fields,** mapped from the OCSF record of [0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/):

  | Column | Source |
  |---|---|
  | time | `time` |
  | recorded at | the writer's stamp in the envelope |
  | log | the log the entry came from |
  | boundary | the log's boundary |
  | company | `metadata.tenant_uid` |
  | action | `metadata.event_code`, with `class_uid` and `activity_id` as columns of their own for OCSF-shaped queries |
  | outcome | `status_id` and `status_code` |
  | actor | `actor.user.uid` and `actor.user.type_id` |
  | subject pseudonym | the subject path the catalogue entry declares, for example `user.uid` |
  | target | the target path the catalogue entry declares, for example `entity.uid` or `resources[0].uid` |
  | request id | the request-id path the catalogue entry declares, for example `api.request.uid` |
  | trace id | `metadata.correlation_uid` |
  | record id | `metadata.uid` |

  The column set is the same on every driver. (the table is from the research of 2026-10-31, adjusted to OCSF with no extension)
- **A typed attribute table** for the properties that the catalogue marks filterable or facetable, keyed by their OCSF path (a JSON Pointer).
- **The record itself, once.** The full-text table is an external-content table over it.
- **One full-text table over the fields that the catalogue marks searchable,** and no other field. A personal field is not searchable unless the catalogue says so.

### Companies in the index

- **One index per boundary, with a company column.** The indexer is a declared reader of each company's audit tenant key, so it can decrypt what it indexes ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/), T-11).
- **The reader filters every query by the caller's grant** (below), and on `postgres` row-level security on the company column adds the database's own check.
- **A shred purges a company's rows.** When a company's audit tenant key is shredded, its rows leave the index, and every later rebuild skips it (T-26). The shred is complete once the purge has run.

### Full-text search

- **Word search without stemming, the same on every driver:** FTS5 `unicode61` with `remove_diacritics 2`, and Postgres `simple` with `unaccent`. Prefix queries are allowed. There is no substring search.

### Rebuilds

- **A rebuild builds a new generation beside the serving one, from the log, then switches the reader to it atomically and drops the old one.** A failed rebuild leaves the old index serving. A rebuild runs after a restore, after a schema or tokenizer change, after a catalogue change that marks a new field, after a shred, and on demand (`sluis audit reindex`).

### Index drivers

| Driver | Where | Layer 2: the backend's own check |
|---|---|---|
| `sqlite` | one node; the local installation | a file per indexer uid; the reader opens it read-only |
| `rqlite` | a cluster of nodes; the boards | an rqlite cluster for the index, the indexer with the `execute` user and the reader with a `query`-only user |
| `postgres` | any runtime, by provisioning mode | roles, grants and row-level security as for state (PG-1): a schema per actor kind, `FORCE ROW LEVEL SECURITY`, actors never own tables, PostgreSQL 15 or newer |
| `none` (scan) | any runtime | no index; the reader scans the log |

- **The `postgres` driver has one provisioning mode per installation:** `rds` or `aurora` on AWS, `cnpg` on Kubernetes, or `external`, a connection string only ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). A database exposed through a load balancer is `external`.
- **`rqlite` is the index of the boards.** It uses the tested findings: reads at `none` with a small freshness bound, `weak` where a caller must read its own write, batched writes in one transaction per request, and an external-content FTS5 table so a record is stored once. The board spike confirms it on arm64 hardware and eMMC. If the spike fails, the boards fall back to `sqlite` on the indexer's node, with the reader beside it, and no new decision is needed.
- **The default index of each tested installation:**

  | Installation | Default index |
  |---|---|
  | local | `sqlite` |
  | boards | `rqlite` |
  | lambda | `postgres` in the `aurora` mode |
  | kubernetes | `postgres` in the `cnpg` mode |

- **A serverless indexer and reader reach Postgres through RDS Proxy with IAM authentication,** with one database role per actor kind ([0033](/decisions/0033-database-access-uses-short-lived-credentials/)). The functions run in the VPC and use endpoints for the other AWS APIs.
- **`sluis audit search` on the CLI builds a local `sqlite` index from the log,** for a small installation or an auditor with read access to the bucket.

### The reader

- **The reader is the audit API.** It authenticates every call itself and serves queries from the index, or by scanning the log with `none`. It is reached through the audit installation's `entry` ([0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/)).
- **Its contract is Connect, with unary calls only,** like every public sluis API, with a TypeScript client from connect-es (inferred from the public-API decision).
- **It offers:**
  - **Search,** with a closed filter grammar, keyset cursors whose next page never disappears, and the free-text `q` over the searchable fields;
  - **Facets;**
  - **Get,** which returns a record with its inclusion proof against a cosigned checkpoint, so a record can be proven to an outsider;
  - **Export,** an asynchronous job that writes NDJSON of enriched OCSF events ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/#export)) and ends in a short-lived signed URL;
  - **a tail,** which is a repeated Search with the cursor.

  Server streaming may come later, as an option on long-lived hosts.
- **Every call is authorised by `policy`.** The reader asks `policy` for a grant: the boundaries, the companies, the operations among search, get, export and tail, a time window, and a full or envelope view. The grant is a mandatory filter of the query (T-16). An operator's grant gives the envelope view (REQ-AUDIT-038).
- **Every read is itself audited,** with the rule that granted it.
- **A reader merges the logs of a boundary by time** (REQ-AUDIT-035), with the rule of [0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/).

## Consequences

- A Search installation can be added to a running log at any time, and dropped, because everything it holds comes from the log. A rebuild costs time, not data, and that time is part of a restore (ADR 0035). During a rebuild the index needs twice its storage.
- Following checkpoints means the index never shows an entry the log has not sealed. The index lags the log by the seal interval plus the indexer's pass, a few seconds.
- Four drivers and three provisioning modes need one conformance suite for the index port. The tokenizer is the same on every driver; ranking that differs between drivers is declared.
- Word search without stemming finds names, ids and codes the same everywhere, but "login" does not find "logged".
- A catalogue change that marks a new field filterable or searchable covers old records only after a rebuild.
- The index holds readable copies of what the log holds encrypted per company, and the indexer can read every company's records. A shred has to reach the index too, which the purge does.
- One policy language serves sluis and audit, and reads are accountable. A read costs a policy call, and reads of the log add records to the log.
- The rqlite driver lets the reader run on any board, at the cost of one more small rqlite cluster per boundary and of reads that may lag the leader by tens of milliseconds.
- Search works out of the box on every tested installation. Every lambda installation pays for an Aurora database and an RDS Proxy, and its indexer and reader run in the VPC with endpoints for the other AWS APIs. `sluis config check` probes the database; in the `external` mode its security and availability are the operator's.
- A tail by polling costs a call per interval.
- The index columns follow OCSF paths, so an OCSF version upgrade that moves a field changes the mapping and needs a rebuild, not a new column. A record of each OCSF version is indexed by the mapping of its own version.

## Roads not taken

### The writer indexes as it writes

- Pros: no indexer to run, and the index is never behind.
- Cons: the writer would hold the index's credential and would fail when the index does. A failed index write would stall the log.
- Why not: the index is derived and follows the log.

### One process for indexing and queries

- Pros: one actor fewer to deploy.
- Cons: the process that parses callers' queries would hold the index's write credential.
- Why not: the indexer and the reader are separate actors with separate credentials.

### List the bucket, as v1 did

- Pros: proven in v1, and needs no checkpoint parsing.
- Cons: a listing shows objects whether or not a checkpoint covers them, and with tiles the object names say nothing about time or tenant.
- Why not: the indexer follows signed checkpoints.

### The record as JSON with each database's JSON functions, or no full text

- Pros: JSON needs no schema and makes any field searchable; no full text keeps the index smallest.
- Cons: JSON functions differ per driver, so results differ, and full text over everything indexes personal fields. No full text gives up the search the FTS5 test was meant to unlock.
- Why not: fixed columns, typed attributes and full text over catalogue-marked fields only.

### An index per company, or only the clear envelope

- Pros: an index per company separates physically and a shred drops one index; an envelope-only index holds no company plaintext and needs no purge.
- Cons: many indexes per boundary, with a fan-out for cross-company reviews; an envelope-only index has no full-text or field search beyond the envelope.
- Why not: one index per boundary with a company column, a grant filter, row-level security on Postgres and a purge on shred.

### English stemming, or a trigram table

- Pros: stemming is friendlier for prose; trigrams find fragments of ids and hosts.
- Cons: the FTS5 and Postgres stemmers differ, so drivers would disagree, and stemming is wrong for ids and non-English text. A trigram table makes the index much larger and adds a table to rebuild.
- Why not: word search without stemming, the same on every driver.

### Rebuild in place

- Pros: no extra storage; newest-first gives recent records quickly.
- Cons: search is down or partial for the whole rebuild, and a partial index is a state callers must understand.
- Why not: a new generation beside the old one, switched atomically.

### A server-streaming tail now, or Search and Get only

- Pros: streaming gives a live tail without polling; Search and Get only is the smallest first version.
- Cons: streaming behaves differently by runtime and has to be tested through every edge connector. Without exports and facets the reader regresses from v1.
- Why not: v1's surface on unary Connect, with streaming later.

### A static claim mapping, or row-level security only

- Pros: a mapping from token claims to grants needs no policy call; row-level security is enforced by the database.
- Cons: the mapping is a second authorisation language beside Cedar, and company ceilings are not enforced by `forbid`. Row-level security exists only on Postgres and has no operations or time windows.
- Why not: the reader asks `policy` for a grant on every call.

### `sqlite` on the boards now, or decide after the spike

- Pros: fewer moving parts first; no commitment before measurement.
- Cons: `sqlite` pins the reader to one board, which is then a single point for search. Waiting keeps the boards' design open.
- Why not: `rqlite` now, with `sqlite` as the fallback the model already has.

### No index by default on lambda, or an external Postgres on Kubernetes

- Pros: no database by default in the most constrained runtime; no CloudNativePG dependency in the chart.
- Cons: searching a lambda installation would need a second installation or the CLI. An external database must be brought before search works on Kubernetes.
- Why not: the maintainer chose search out of the box: `postgres` on Aurora for lambda and on `cnpg` for Kubernetes.

### The RDS Data API, or direct connections

- Pros: the Data API needs no VPC for the functions; direct connections need nothing extra to run.
- Cons: the Data API is Aurora only, needs a Secrets Manager secret per role, has result size limits and is a second SQL path to test. Direct connections storm the database under load, and the functions still need the VPC.
- Why not: RDS Proxy with IAM authentication.

### A search engine of its own (OpenSearch, a log store)

- Pros: rich full-text search and aggregations.
- Cons: a heavy service to run on boards, another driver to secure, and a second place that holds the records.
- Why not: SQLite FTS5 on `sqlite` and `rqlite`, and Postgres, cover the volume (about four million records a year at the measured rate), and they are already in the design.
