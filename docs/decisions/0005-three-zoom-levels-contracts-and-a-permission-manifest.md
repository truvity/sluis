# 0005 — The design has three zoom levels, every component boundary is a ConnectRPC contract, and a permission manifest per actor is its spine

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).
Rewritten in place on 2026-10-30 (alignment with 0007, 0058).

## Context

sluis v1 is one issuer function that serves sign-in, machine token exchange and downstream minting, and it touches 25 resources. Least privilege cannot be expressed for something that broad. v1 later split its parts by credential holder (v1 ADR 0071 on `master`), and its contracts are Protobuf served over Connect (v1 ADR 0064 on `master`). This record is the v2 successor of both, for the method.

v2 is a rewrite, and it is several processes. It needs one method that says how the design is described, how a component talks to another, and where the permissions of every process come from. Three things go wrong without it:

- A description at one level of detail mixes use cases with processes. A reader cannot tell which resource a use case touches, or which process is allowed to touch it.
- Permissions written by hand for each runtime drift apart. An IAM policy, a key policy, a systemd unit and a network rule each state a part of what a process may do, and nobody checks them against each other.
- A component that calls another through a plain function call cannot be moved into its own process without a rewrite. Least privilege wants exactly that move.

## Decision

- **The design has three zoom levels.** Each answers one question, and the output of one level is the input of the next.
  - **L1, black box:** what does sluis do, for whom, and which resources does each use case touch? It produces the use cases, the external API and the resources each use case touches.
  - **L2, components:** which component is responsible for each operation, and through which contract? It produces components with one responsibility each, their contracts and ports, the call graph, and one writer per record kind.
  - **L3, processes:** what runs where, with which credentials, and what enforces that in each runtime? It produces the actors, one permission manifest per actor, packaging, and the enforcement for each runtime.
- **Every major component is a ConnectRPC contract, even when caller and callee share a process.** In-process calls use an in-memory transport, so moving a component into its own process changes configuration, not code. The same contract is served over a Unix socket with peer credentials locally, a Lambda invoke on AWS, and HTTPS with mutual TLS remotely.
- **Contracts are written in Protobuf.** The `proto/` directory is the source of every contract. A contract change is reviewed there first.
- **The permission manifest, one per actor, is the spine.** It states, for that actor, which record kinds it reads and writes, which of the kinds it owns accept operator-set writes, which secrets and keys it may use, which hosts it may reach, which actors may call it and which actors it calls. The origin and sensitivity of each record kind come from the configuration schema ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)); the manifest names the writer. It is derived from L1 (the operations on each resource) and L2 (data ownership and the call graph). The manifests live in `manifest/`.
- **Nothing that enforces least privilege is written by hand. The manifest generates it, for every runtime:**
  - IAM policies and trust policies;
  - key policies, with one actor per key;
  - the access lists and the per-caller sockets of the one mediating driver, `key-holder`, and the users of each per-actor rqlite cluster (there is no `state-owner`: [0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/));
  - systemd units and sandbox drop-ins;
  - network rules (nftables, Kubernetes network policies);
  - the call graph, which is default deny: one interceptor in every process refuses a call that is not an edge of the reviewed graph;
  - the deny-matrix tests and the blast-radius report of every actor;
  - with the schema's origin and sensitivity annotations, the backup list, the telemetry allowlist and the audit redaction ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
- **Enforcement is tested, not assumed.** The deny matrix is generated from the same data. For every pair of actors, actor X is denied actor Y's records, secrets, keys and sockets, and the test proves it.
- **A change in permissions is a change in one file.** A pull request that adds a use case says which component owns each step and which actor's manifest changes. The blast-radius report for every actor is checked in, so a reviewer sees what the change grants.

The levels, the manifest and the contracts are described on [Architecture](/architecture/) and [Components](/architecture/components/). The actors the manifest is written for are on [Actors](/concepts/actors/) and in [0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/).

## Consequences

- A reviewer can follow one use case from the black box down to the credential that serves it, and see the same facts at each level.
- Least privilege is checked by a tool. The same actor runs on every runtime, and the runtimes differ in the mechanism that enforces the manifest, not in the actors.
- A component can start inside another process and move out later, because its boundary was a contract from the first day. The cost is a contract, and a generated client and server, for components that would otherwise be one function call.
- The manifest is a large piece of generation work, and the generators (for IAM, key policies, units, network rules and tests) must be kept true to what each runtime enforces. A generator that is wrong grants something the manifest does not say. Each generator needs golden tests and the deny matrix.
- A wrong manifest is a single point of failure for the design. Review of the manifest, and of the checked-in blast-radius report, is where the design is held to account.
- Contracts in Protobuf need a code generator in every language that implements a component. The kits for the languages in use carry a ConnectRPC server and client over Unix sockets with peer credentials, and a conformance suite checks that they agree.
- Until the manifest exists, the diagrams on the architecture pages are written by hand in Mermaid. Once it exists, they are generated from the design model.
- Packaging, the actors and their fusion are decided separately ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). This record decides the method only.

## Roads not taken

### Hand-written policies per runtime

- Pros: no generator to build. Each runtime's policy can be tuned by the person who knows that runtime best.
- Cons: the IAM policy, the key policy, the unit and the network rule for one actor are written separately and drift apart. A reviewer cannot see what a change grants without reading all of them.
- Why not: one source (the manifest) that generates all of them is shorter to review and can be tested as a whole. (inferred)

### Plain interfaces in a process, contracts only between processes

- Pros: less generated code. No serialisation cost inside a process.
- Cons: moving a component into its own process becomes a rewrite, and the process boundary is the one least privilege wants to move.
- Why not: with an in-memory transport behind the same contract, moving a component changes configuration, not code.

### Hand-written OpenAPI as the contract

- Pros: the format that most tools read.
- Cons: the description and the code drift apart the first time one is edited without the other (v1 ADR 0064, on `master`).
- Why not: Protobuf is the source of the contract, and other descriptions are generated from it.

### gRPC only

- Pros: well known, with mature tooling.
- Cons: not reachable from a browser or a function gateway without a translation layer, which is what Connect already is (v1 ADR 0064, on `master`).
- Why not: ConnectRPC serves the same Protobuf contracts to a browser, a function gateway and a Unix socket.

### A single description of the design at one level

- Pros: one page to read. No hand-off between levels.
- Cons: use cases, components and processes are mixed, so a reader cannot trace a resource to the process that holds its credential.
- Why not: the three questions are different, and the output of each is the input of the next. (inferred)

### Describe the processes first and derive permissions from them

- Pros: starts from what is built and deployed.
- Cons: the permissions of a process then describe what it happens to do. They are not a consequence of what each use case needs.
- Why not: deriving the manifest from L1 and L2 keeps permissions tied to the use cases and to data ownership. (inferred)
