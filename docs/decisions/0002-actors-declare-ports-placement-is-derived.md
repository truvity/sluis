# 0002 — Actors declare ports, drivers declare requirements, and placement is derived

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #901](https://github.com/truvity/sluis/issues/901). Rewritten in place on 2026-10-29 during the v2 design phase (see "Design phase" in the decisions README).

## Context

sluis v2 is several processes. Each needs keys, state, a lease, blob storage, a transport, an index or a cloud credential, and each of those has several implementations: a hardware key or a cloud key service, an embedded store or a replicated one, a spool on disk or a managed queue.

Deciding where each process runs, one deployment at a time, duplicates the rules in every deployment and makes each new platform multiply the cases. Audit made it worse: it needs the same kinds of storage, but it had its own interfaces and its own placement rules.

We need one model that says what a process needs, what an implementation needs, and where a process may run, so that a tool can check an installation instead of a reader checking a table.

## Decision

- **An actor declares the ports it requires.** An actor is a process with one responsibility. For each port the actor says required or optional. For example, the signer requires keys, state and transport.
  - **Core actors:** entry, signer, policy and ca. Quota is a separate piece that can be combined with them ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
  - **Integration actors:** upstream, workload verifier, facts source, credential minter, provisioner and notifier. They return facts or carry out effects that core has authorised.
  - **Platform:** `sluis-node`, the node agent (not on serverless or Kubernetes), and the mediating drivers `key-holder` (keys) and `state-owner` (state).
  - **Audit actors:** writer, witness, indexer and reader. There is no notary actor.
- **Some actors declare `instances: exactly 1`.** Examples: the audit writer of one log, the facts-source reconciler and the indexer. With more than one node, or elastic nodes, such an actor needs the lease port. On serverless the platform's concurrency limit of 1 may stand in for it.
- **A driver declares its requirements.** A driver implements one port (or two, as rqlite does for state and lease). It says whether it needs a long-running process, a persistent local disk, how many nodes it supports and which outside dependency it needs. Examples of a dependency: a cloud account, a hardware key on each node, a message service.
- **Ports carry capability flags.** `blob` has a lock mode (none, removable, compliance). `state` has compare-and-set. A consumer asks for the flag it needs, and only drivers that offer it qualify.
- **The ports are keys, state, lease, blob, transport, index, cloud and witness.** Audit uses the same definitions as the rest of sluis and adds only `witness`. The audit log store is a `blob` with a lock mode, quota state is `state` with compare-and-set, and search uses `index`. A new port amends this record.
- **Where an actor runs is derived from these rules.** This holds for every sluis actor and for the Search pieces of audit. There are no per-installation placement decisions, and each tested installation only names its default drivers. Example of a derived constraint: with a sqlite index on a cluster, the reader runs on the indexer's node, because sqlite has one writer and needs local disk.
- **`sluis config check` enforces the rules.** It validates an installation against the requirements its drivers declare: runtime, node count and available dependencies. It also warns about choices that are allowed but weaker, such as an audit log with no witness ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). The command is agreed; this record is the design it follows.
- **Words are fixed.** "Actor" replaces "role" and "process role". "Runtime" is the kind of place processes run ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). The directory that holds the actors' executables is named `actors`, not `roles`.

## Consequences

- A new platform or driver adds a row of requirements. It does not add placement rules to every installation.
- Compatibility between a driver and a runtime is a derived table. It is checked by a tool, not kept by hand.
- A tested installation is short: its runtime, its nodes and a default driver for each port. It has no placement logic of its own.
- Audit reuses the driver code, the tests and the conformance suite of sluis. It adds one port.
- Drivers must state their requirements precisely, and the requirements must be kept true. A wrong declaration lets `sluis config check` pass an installation that fails at run time. Conformance tests for each driver have to verify what it declares.
- `sluis config check` becomes a core part of installing sluis and needs its own tests and documentation.
- Docs, labels and file paths that said "role" say "actor", including the packaging directory for the actors' executables. This is a one-time rename.

## Roads not taken

### Separate audit ports

- Pros: audit evolves its interfaces on its own. No coupling to sluis.
- Cons: every driver would be written and tested twice. The driver and test matrix doubles.
- Why not: audit needs the same kinds of storage. Sharing the definitions costs one extra port, `witness`.

### Placement decided per deployment

- Pros: each deployment can be tuned and explained on its own.
- Cons: the same rules are repeated in every deployment and drift apart. A new platform multiplies the decisions.
- Why not: the rules come from the ports and drivers. Writing them once and deriving the placement is shorter and checkable.

### Share everything between audit and sluis

- Pros: fewest driver instances and processes.
- Cons: a fault or a compromise in one reaches the other, because they share instances and identities.
- Why not: the blast radius is shared. Pieces are separate by default; combined pieces share driver instances only when the installation chooses to combine them (see [0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).

### Keep the name "role"

- Pros: no rename. Familiar from v1.
- Cons: "role" already means a user role in scopes and a cloud role in IAM. A process called a role is ambiguous in every sentence that also mentions access.
- Why not: "actor" is free of those meanings.
