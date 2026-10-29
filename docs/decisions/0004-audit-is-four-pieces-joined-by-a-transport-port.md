# 0004 — Audit is four pieces joined by a transport port

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #903](https://github.com/truvity/sluis/issues/903).

## Context

The audit design had one box. A "queue" port and an "outbox" connected the application to the writer, and the log, the search index and quota were tied together. A small installation had to run all of them. A large one shared their failures. Quota was also unclear: it could be a view over the log or a live check, and those need different state.

Audit uses the same port definitions as sluis ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). The log store is a `blob` with a lock mode, quota state is `state` with compare-and-set, and search uses `index`.

This record replaces the earlier lean towards separate driver instances for audit and sluis when they are combined, and it revises the earlier rule that serverless cannot hold a search index.

## Decision

- **Audit is four separate pieces.**
  - **Emitter**: a library inside each end application, configured by that application. It is not a process.
  - **Log**: writer, blob store and witness.
  - **Search**: indexer, index and reader.
  - **Quota**: quota engine and its state.

  Each piece can be its own installation. Pieces are separate by default.
- **Transport is a port, not a piece.** It joins the emitter to the writer and replaces the old queue port and outbox. It has two modes:
  - **direct**: the emitter streams to the writer.
  - **queue**: the emitter puts records into a queue and the writer receives them. Queue drivers are **spool**, **SQS** and **NATS JetStream**.

  A writer on the same node as sluis is one installation's placement, not part of the model.
- **Combined pieces share driver instances.** When pieces are combined in one installation, even with sluis, they use the same driver instances. Quota is therefore a separate piece that can be combined with sluis.
- **Quota meters by default and enforces per policy rule.** Metering is the default. A policy rule may enforce quota for chosen subjects or audiences. Enforcement needs state with compare-and-set on the mint path. The earlier decision that strict quota is switched on in production only after the load test still applies to turning enforcement on.
- **Serverless may run the indexer and reader with a postgres index.** The old rule "no index inside serverless" is withdrawn, because managed Postgres makes it practical. The default index of the serverless preset stays `none`; that default is a proposal.
- **The postgres index is one driver with a provisioning mode:**
  - `rds` or `aurora` on AWS. On serverless, through a connection proxy (proposed).
  - `cnpg` on Kubernetes, in the same cluster.
  - `external`: a connection string only; sluis provisions nothing. Examples: a CloudNativePG cluster in another Kubernetes cluster behind a load balancer, or a Postgres that someone runs on bare metal. Exposing a database through a load balancer is not a driver; from sluis's side it is `external`.
- **Open, and not decided here:** the transport per preset, direct mode when the writer is down, whether the witness is optional or required, and whether the actors notary, audit-writer and audit-read are replaced by writer, indexer, reader and witness (proposed).

## Consequences

- A small installation can run the log alone, and add Search or Quota later. A failure in one piece does not stop the others.
- An emitter has no process to run or supervise. It does need configuration for its transport mode, and the application owns that.
- Combining pieces saves processes and driver instances, but a combined installation shares the failure and the access of those instances. Separate installations are how to avoid that.
- Quota can run next to sluis and use its state and its driver instances. Enforcement makes the mint path depend on that state, and its availability then affects minting.
- Serverless installations gain search. They need a Postgres service, which is a dependency to provision and check with `sluis config check`.
- Four drivers for the transport (direct, spool, SQS, NATS JetStream) and four provisioning modes for postgres need tests and docs.
- The `external` mode puts connection security and availability with the operator. sluis cannot check more than the connection string and a reachability probe.
- The open items above stay open decisions in the docs until they are settled.

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

### No index inside serverless

- Pros: fewer dependencies in the most constrained environment.
- Cons: serverless installations cannot search the log. RDS and Aurora now make a managed Postgres practical.
- Why not: the rule is withdrawn. Serverless may run the indexer and reader with a postgres index.

### Enforcement only, or metering only, for quota

- Pros: one mode is simpler to explain and to build.
- Cons: enforcement only needs live compare-and-set state for every subject. Metering only cannot stop abuse.
- Why not: metering by default with enforcement per policy rule gives both, and only the rules that enforce pay for the state.

### Treat a database behind a load balancer as its own driver

- Pros: sluis could tell this case apart.
- Cons: from sluis's side it is a connection string. Another driver adds code and no behaviour.
- Why not: it is the `external` mode of the postgres driver.
