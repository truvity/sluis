# 0004 — Audit is four pieces joined by a transport port

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #903](https://github.com/truvity/sluis/issues/903). Rewritten in place on 2026-10-29 during the v2 design phase (see "Design phase" in the decisions README).
Rewritten in place on 2026-10-30 (realignment pass).
Rewritten in place on 2026-10-31 (links to ADR 0022, which decided the index drivers, the default index of each tested installation and the path from serverless to Postgres).
Rewritten in place on 2026-11-01 (the emitter builds a message of an audit record and a usage part from one call, as [0064](/decisions/0064-audit-records-are-ocsf-events-with-a-sluis-extension-and-usage-is-a-separate-stream/) decides).

## Context

The audit design had one box. A "queue" port and an "outbox" connected the application to the writer, and the log, the search index and quota were tied together. A small installation had to run all of them. A large one shared their failures. Quota was also unclear: it could be a view over the log or a live check, and those need different state.

Audit uses the same port definitions as sluis ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). The log store is a `blob` with a lock mode, quota state is `state` with compare-and-set, and search uses `index`. Audit adds only the `witness` port.

A cluster has several nodes. If every node wrote to one shared log, that log would need a writer that survives node failures, and a failover would risk two writers at once. A log with two writers cannot keep a single hash chain.

## Decision

### Four pieces and a transport

- **Audit is four separate pieces.**
  - **Emitter**: a library inside each end application, configured by that application. It is not a process. It builds one message from one call: an OCSF audit record and, for an action that uses something, usage events ([0064](/decisions/0064-audit-records-are-ocsf-events-with-a-sluis-extension-and-usage-is-a-separate-stream/)).
  - **Log**: writer, blob store and witness.
  - **Search**: indexer, index and reader.
  - **Quota**: quota engine and its state.

  Each piece can be its own installation. Pieces are separate by default.
- **The audit actors are writer, witness, indexer and reader.** There is no notary actor. The writer asks `key-holder` to sign checkpoints, and the access list of `key-holder` lets only the writer use the log key.
- **Transport is a port, not a piece.** It joins the emitter to the writer and replaces the old queue port and outbox. It has two modes:
  - **direct**: the emitter streams to the writer, over a Unix socket on the same host or over mutual TLS.
  - **queue**: the emitter puts records into a queue and the writer receives them. Queue drivers are **spool**, **SQS** and **NATS JetStream**.

  The default transport follows the runtime: spool on bare-metal, SQS on serverless and Kubernetes. Direct and NATS JetStream are supported drivers with no default. A writer on the same node as sluis is one installation's placement, not part of the model.
- **In direct mode the emitter falls back to a local spool** when the writer is down. Direct mode therefore needs a disk, so it is not available on serverless.
- **Combined pieces share driver instances.** When pieces are combined in one installation, even with sluis, they use the same driver instances. Quota is therefore a separate piece that can be combined with sluis.

### One writer per log

- **Each log has exactly one writer.** The rule is per log, not per installation.
- **A cluster keeps one log per node.** Each node has its own writer, spool and log key. The other nodes are its peer witnesses, which co-sign checkpoints (two of three on a three-node cluster). Readers merge the logs by time.
- **The witness is optional everywhere.** Witness drivers are `none`, `peer` and `external`. `sluis config check` warns when an installation has no witness.
- **The risk is documented.** With no witness, and with no notary, a compromised writer can get a rewritten checkpoint signed. A compliance lock on the blob store still prevents overwriting stored objects, but it does not prevent a split view, where different readers are shown different logs.

### Search and quota

- **Quota meters by default and enforces per policy rule.** A policy rule may enforce quota for chosen subjects or audiences. Enforcement needs state with compare-and-set on the mint path. Strict quota is switched on in production only after the load test.
- **Serverless may run the indexer and reader with a postgres index.**
- **The postgres index is one driver with a provisioning mode:**
  - `rds` or `aurora` on AWS. How a serverless function reaches it is in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/).
  - `cnpg` on Kubernetes, in the same cluster.
  - `external`: a connection string only; sluis provisions nothing. Examples: a CloudNativePG cluster in another Kubernetes cluster behind a load balancer, or a Postgres that someone runs on bare metal. Exposing a database through a load balancer is not a driver; from sluis's side it is `external`.
- **The other index drivers and the default index of each tested installation are in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/).** Among them is a replicated SQLite index (`rqlite`) for the boards, so that the reader can run on any node.
- **The search index and the quota meters are derived data,** rebuilt from the log and never evidence. Quota limits are declared and quota balances are issued records ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).

## Consequences

- A small installation can run the log alone, and add Search or Quota later. A failure in one piece does not stop the others.
- An emitter has no process to run or supervise. It does need configuration for its transport mode, and the application owns that.
- Combining pieces saves processes and driver instances, but a combined installation shares the failure and the access of those instances. Separate installations are how to avoid that.
- One log per node removes failover from the writer. A lost node leaves its log intact and complete up to its last checkpoint. The cost is more logs to store, verify and merge, and a reader that must handle clock differences between nodes.
- Peer witnesses inside one cluster are not independent of each other. They protect against a single compromised node, not against a compromised operator. An `external` witness is the independent option.
- With no witness the risk above applies, and `sluis config check` only warns. An installation that needs more has to add a witness.
- Spool as the direct-mode fallback keeps records when the writer is down. The price is a disk on every emitting node and a spool that has to be drained, in order, when the writer returns.
- Dropping the notary leaves one fewer actor to run, but the log key's protection rests on the access list of `key-holder`. That list needs tests.
- Quota can run next to sluis and use its state and its driver instances. Enforcement makes the mint path depend on that state, and its availability then affects minting.
- Serverless installations gain search. They need a Postgres service, which is a dependency to provision and check with `sluis config check`.
- Four transport modes and drivers (direct, spool, SQS, NATS JetStream) and three provisioning modes for postgres need tests and docs.
- The `external` mode puts connection security and availability with the operator. sluis cannot check more than the connection string and a reachability probe.
- The index drivers beyond postgres, the default index of each tested installation and the connection from serverless to Postgres are decided in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/).

## Roads not taken

### Separate audit ports

- Pros: audit can change its interfaces freely.
- Cons: the driver and test matrix doubles.
- Why not: audit shares the sluis port definitions and adds only `witness` ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).

### Separate driver instances when pieces are combined

- Pros: a stronger boundary between audit and sluis inside one installation.
- Cons: more processes per node, for no isolation gain once the maintainer has chosen separate pieces.
- Why not: if isolation is wanted, run the pieces as separate installations. If they are combined, they share instances.

### A fixed audit with the log, search and quota in one box

- Pros: one thing to install and operate.
- Cons: a shared blast radius, and every installation pays for every part.
- Why not: pieces are separate by default, and combining is a choice of the installation.

### One shared log per cluster with a leader as the writer

- Pros: one log to store, verify and read.
- Cons: a failover can briefly leave two writers, and a log with two writers cannot keep one hash chain. It also needs a lease that every node trusts.
- Why not: one log per node needs no election. Exactly one writer per log holds by construction.

### A notary actor that signs checkpoints

- Pros: a separate actor holds the log key, so a compromised writer cannot sign by itself.
- Cons: one more process to run and secure, on every node. It still has to be told what to sign.
- Why not: `key-holder` already holds keys and enforces an access list. Letting only the writer use the log key gives most of the protection without a new actor.

### A required witness

- Pros: closes the split-view risk wherever it applies.
- Cons: a single-node installation has no peer, and an external witness adds a dependency. The smallest installations could not run audit.
- Why not: the witness is optional everywhere. `sluis config check` warns when it is missing and the risk is written down.

### In direct mode, fail the operation when the writer is down

- Pros: no spool, no disk, and no record is ever held outside the writer.
- Cons: an unreachable writer stops every operation that has to be audited.
- Why not: falling back to a local spool keeps the service up and keeps the records.

### Buffer in memory when the writer is down

- Pros: no disk needed, so direct mode could run on serverless.
- Cons: records are lost on a restart. An audit trail that drops records under failure is not an audit trail.
- Why not: unsafe. Direct mode needs a disk and is not available on serverless.

### Cloudflare Queues as a transport driver

- Pros: close to the blob store and tunnel on some installations.
- Cons: another driver to write and test, and it adds nothing the existing queue drivers do not cover.
- Why not: the transport drivers stay spool, SQS, NATS JetStream and direct.

### No index inside serverless

- Pros: fewer dependencies in the most constrained runtime.
- Cons: serverless installations cannot search the log. RDS and Aurora now make a managed Postgres practical.
- Why not: serverless may run the indexer and reader with a postgres index.

### Enforcement only, or metering only, for quota

- Pros: one mode is simpler to explain and to build.
- Cons: enforcement only needs live compare-and-set state for every subject. Metering only cannot stop abuse.
- Why not: metering by default with enforcement per policy rule gives both, and only the rules that enforce pay for the state.

### Treat a database behind a load balancer as its own driver

- Pros: sluis could tell this case apart.
- Cons: from sluis's side it is a connection string. Another driver adds code and no behaviour.
- Why not: it is the `external` mode of the postgres driver.
