# 0002 — Actors declare ports, drivers declare requirements, and placement is derived

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #901](https://github.com/truvity/sluis/issues/901).

## Context

sluis v2 is several processes. Each needs keys, state, a lease, blob storage, a transport, an index or a cloud credential, and each of those has several implementations: a hardware key or a cloud key service, an embedded store or a replicated one, a spool on disk or a managed queue.

Until now, where each process runs was decided shape by shape. That duplicates the rules in every shape and makes each new platform multiply the cases. Audit made it worse: it needs the same kinds of storage, but it had its own interfaces and its own placement rules.

We need one model that says what a process needs, what an implementation needs, and where a process may run, so that a tool can check an installation instead of a reader checking a table.

## Decision

- **An actor declares the ports it requires.** An actor is a process with one responsibility. The term replaces "role" and "process role". For each port the actor says required or optional. For example, the signer requires keys, state and transport. Some actors also declare `instances: exactly 1`: the audit writer, the facts-source reconciler and the indexer. With more than one node, or elastic nodes, such an actor needs the lease port. On serverless the platform's concurrency limit of 1 may stand in for it.
- **A driver declares its requirements.** A driver implements one port (or two, as rqlite does for state and lease). It says whether it needs a long-running process, a persistent local disk, how many nodes it supports and which outside dependency it needs. Examples of a dependency: a cloud account, a hardware key on each node, a message service.
- **Ports carry capability flags.** `blob` has a lock mode (none, removable, compliance). `state` has compare-and-set. A consumer asks for the flag it needs, and only drivers that offer it qualify.
- **Where an actor runs is derived from these rules.** This holds for every sluis actor and for the Search pieces of audit. There are no per-preset placement decisions. A preset only names its default drivers. Example of a derived constraint: with a sqlite index on a cluster, the reader runs on the indexer's node, because sqlite has one writer and needs local disk.
- **`sluis config check` enforces the rules.** It validates an installation against the requirements its drivers declare: environment, node count and available dependencies. Its existence is agreed; this record is the design it follows.
- **Audit shares the port definitions.** The ports are keys, state, lease, blob, transport, index and cloud. `witness` is the only port audit adds. The audit log store is a `blob` with a lock mode, quota state is `state` with compare-and-set, and search uses `index`. Whether audit runs its own instances of those drivers under its own identities is a separate question; [0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/) settles it for combined installations.

## Consequences

- A new platform or driver adds a row of requirements. It does not add placement rules to every preset.
- Compatibility between a driver and an environment is a derived table. It is checked by a tool, not kept by hand.
- Presets get shorter. They are named installations with default drivers and no placement logic of their own.
- Audit reuses the driver code, the tests and the conformance suite of sluis. It adds one port.
- Drivers must state their requirements precisely, and the requirements must be kept true. A wrong declaration lets `sluis config check` pass an installation that fails at run time. Conformance tests for each driver have to verify what it declares.
- `sluis config check` becomes a core part of installing sluis and needs its own tests and documentation.
- Docs and labels that said "role" say "actor". The package layout under the existing roles directory is not renamed by this record; it stays an open follow-up.

## Roads not taken

### Separate audit ports

- Pros: audit evolves its interfaces on its own. No coupling to sluis.
- Cons: every driver would be written and tested twice. The driver and test matrix doubles.
- Why not: audit needs the same kinds of storage. Sharing the definitions costs one extra port, `witness`.

### Per-preset placement decisions

- Pros: each preset can be tuned and explained on its own.
- Cons: the same rules are repeated in every preset and drift apart. A new platform multiplies the decisions.
- Why not: the rules come from the ports and drivers. Writing them once and deriving the placement is shorter and checkable.

### Share everything between audit and sluis

- Pros: fewest driver instances and processes.
- Cons: a fault or a compromise in one reaches the other, because they share instances and identities.
- Why not: the blast radius is shared. Pieces are separate by default; combined pieces share driver instances only when the installation chooses to combine them (see [0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
