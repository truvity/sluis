# 0002 — Actors declare ports, drivers declare requirements, and placement is derived

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #901](https://github.com/truvity/sluis/issues/901). Rewritten in place on 2026-10-29 during the v2 design phase (see "Design phase" in the decisions README).
Rewritten in place on 2026-10-30 (realignment pass), and again on 2026-10-30 (alignment with 0007, 0058 and 0012).

## Context

sluis v2 is several processes. Each needs keys, state, a lease, blob storage, a transport, an index, a secret or a cloud credential, and each of those has several implementations: a hardware key or a cloud key service, an embedded store or a replicated one, a spool on disk or a managed queue.

Deciding where each process runs, one deployment at a time, duplicates the rules in every deployment and makes each new platform multiply the cases. Audit made it worse: it needs the same kinds of storage, but it had its own interfaces and its own placement rules.

We need one model that says what a process needs, what an implementation needs, and where a process may run, so that a tool can check an installation instead of a reader checking a table.

## Decision

- **An actor declares the ports it requires.** An actor is a process with one responsibility. For each port the actor says required or optional. For example, the signer requires keys, state and transport.
  - **Core actors:** entry, session, console, exchange, signer, policy, ca and ssh-ca ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)).
  - **Quota:** the `quota` actor is the engine of the Quota piece, which can be combined with sluis ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
  - **Integration actors:** upstream, facts source, credential minter, provisioner and notifier. They return facts or carry out effects that core has authorised. They are split by credential, one vendor tenant each ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). The verifiers of proofs are not actors: they are verifier drivers of `exchange` ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)).
  - **Platform:** `sluis-node`, the node agent (not on serverless, and on Kubernetes only as a fallback, [0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)), and the one mediating driver, `key-holder` (keys, below).
  - **Audit actors:** writer, witness, indexer and reader. There is no notary actor.
- **Some actors declare `instances: exactly 1`.** Examples: the audit writer of one log, the facts-source reconciler and the indexer. With more than one node, or elastic nodes, such an actor needs the lease port. On serverless the platform's concurrency limit of 1 may stand in for it, once that is verified ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)).
- **A driver declares its requirements.** A driver implements one port (or two, as rqlite does for state and lease). It says whether it needs a long-running process, a persistent local disk, how many nodes it supports and which outside dependency it needs. Examples of a dependency: a cloud account, a hardware key on each node, a message service.
- **A mediator is only for a backend that cannot tell callers apart** ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). A backend that authorises each caller itself is used with a per-actor credential, and its driver runs per actor. Today that leaves one mediator, `key-holder`, for the keys on a node's card: a YubiKey has one PIN, so the card cannot tell its callers apart. State has no mediator. One writer per record kind is enforced by the state backend's own check of the caller ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)).
- **Every port is protected in three layers**, all generated from the manifest: the port library in the actor, the driver's or the backend's own check of the caller, and physical separation at the higher isolation levels ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). A driver's declaration says which check it has.
- **Ports carry capability flags.** `blob` has a lock mode (none, removable, compliance). `state` has compare-and-set. A consumer asks for the flag it needs, and only drivers that offer it qualify.
- **The ports are keys, state, lease, blob, transport, index, cloud, witness, secrets and export.**
  - `keys`: `sign`, `public-key`, `wrap` and `unwrap`. A key never leaves its store ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).
  - `state`: records with a revision and compare-and-set ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)). Every value in `state` is encrypted in the port library ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). `lease`: `acquire` and `renew`, with a fencing token. Leases live on the state store, independent of any Raft leader.
  - `blob`: `put-once`, `get` and `list`, with a lock mode. `transport`: `put`, `receive` and `ack`, or a stream. `index`: `write` and `query`. `witness`: `cosign`. `cloud`: credentials for a cloud role.
  - `secrets` holds sluis's own secrets and its integrations' secrets. `export` publishes a small, typed set of documents that something outside sluis may read. sluis is not a general secrets engine. The interfaces and drivers of these two ports are in ADR 0034.
  - Audit uses the same definitions as the rest of sluis and adds only `witness`. The audit log store is a `blob` with a lock mode, quota state is `state` with compare-and-set, and search uses `index`.
  - A new port amends this record.
- **Every record kind declares its origin and every field its sensitivity** ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). They decide which port holds a value: a secret is behind `secrets`, never a plain record in `state`.
- **Where an actor runs is derived from these rules.** This holds for every sluis actor and for the Search pieces of audit. There are no per-installation placement decisions, and each tested installation only names its default drivers. Example of a derived constraint: with a sqlite index on a cluster, the reader runs on the indexer's node, because sqlite has one writer and needs local disk.
- **`sluis config check` enforces the rules.** It validates an installation against the requirements its drivers declare: runtime, node count and available dependencies. It also warns about choices that are allowed but weaker, such as an audit log with no witness ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). The command is agreed; this record is the design it follows.
- **Words are fixed.** An actor instance is named `<actor>@<boundary>`, for example `ca@prod`, and an integration instance `<vendor>[-<capability>]@<tenant>` ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). "Actor" replaces "role" and "process role". "Runtime" is the kind of place processes run, and "environment" is development, staging or production ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). The directory that holds the actors' executables is named `actors`, not `roles`.

## Consequences

- A new platform or driver adds a row of requirements. It does not add placement rules to every installation.
- Compatibility between a driver and a runtime is a derived table. It is checked by a tool, not kept by hand.
- A tested installation is short: its runtime, its nodes and a default driver for each port. It has no placement logic of its own.
- Audit reuses the driver code, the tests and the conformance suite of sluis. It adds one port.
- Drivers must state their requirements precisely, and the requirements must be kept true. A wrong declaration lets `sluis config check` pass an installation that fails at run time. Conformance tests for each driver have to verify what it declares.
- With one mediator left, most drivers hold one actor's credential and rely on the backend's own check. That check has to be generated right for every driver and tested with the other layers switched off ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). `key-holder` is the one shared driver whose own code is the check.
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

### A mediating driver for every backend with one login

- Pros: one place that decides which actor may do what, whatever the backend. It was the earlier version of this record, with `key-holder` for keys and `state-owner` for state.
- Cons: a mediator is in the data path, its access list is code, and a bug in it reaches every actor that uses the port. For a backend that checks each caller itself it adds a hop and no check.
- Why not: rqlite is light enough to run a cluster per owning actor, so state needs no mediator. A mediator stays only where the backend cannot tell callers apart, which leaves `key-holder` ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).

### Keep the name "role"

- Pros: no rename. Familiar from v1.
- Cons: "role" already means a user role in scopes and a cloud role in IAM. A process called a role is ambiguous in every sentence that also mentions access.
- Why not: "actor" is free of those meanings.
