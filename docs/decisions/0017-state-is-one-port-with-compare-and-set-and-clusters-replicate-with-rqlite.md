# 0017 — State is one port with compare-and-set; one writer owns each record kind; clusters replicate with rqlite

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass), and again on 2026-10-30 (alignment with 0007, 0058 and 0012).
Rewritten in place on 2026-10-31 (after spike S5, [issue #943](https://github.com/truvity/sluis/issues/943): rqlite's memory and write limit on a board).

## Context

sluis keeps records: sessions, the signing ring's metadata, directory facts, operator-set configuration, quota balances and meters. Most of its security rests on who may change which of them, and on what happens when two callers change one at once.

v1 put everything that is not a blob behind one State port (v1 ADR 0027, on `master`). It worked on one key at a time with compare-and-swap by revision, listing by prefix and a lifetime, and it filtered expired records on read because a store may delete them late. It had two engines: NATS JetStream on Kubernetes and DynamoDB on AWS. The JetStream half was removed on 2026-10-04, because no deployment ran it, and the DynamoDB half stands. Before that port, sessions lived in a replicated cache whose asynchronous replication could lose a write that had been acknowledged, so a failover signed everybody out and could bring a rotated refresh token back to life.

v2 adds installations that v1 never had: a single machine with no cloud, and a cluster of three single-board computers with nothing but each other. They need a state store that is not DynamoDB, that survives the loss of one node and that can be run without an operator. v2 also splits the core into actors ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)), and an actor that is compromised should not be able to rewrite a record that it does not own. Some actors must run exactly once, so there is also a need for a lease that a stale holder cannot use.

Two other facts shape the answer. Drivers are services on Unix sockets, and the only TCP ports between nodes are the state clusters' Raft ports, over mutual TLS ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). And a driver declares what it needs: a long-running process, a disk, a number of nodes ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).

## Decision

### One port, with compare-and-set

- **`state` is one port.** An actor sees one contract whatever sits behind it. [0002](/decisions/0002-actors-declare-ports-placement-is-derived/) fixes the port and its capability flag (`state` has compare-and-set). This record refines that flag into the write rules of the contract.
- **Records carry a revision, and every write has a precondition.** The operations are `get`, `put` with a precondition and `cas`. A `put` creates a record only if it is absent, and `cas` changes a record only against the revision the caller read. There is no unconditional overwrite.
- **Writes that must go together are one atomic batch.** There are no interactive transactions. A flow that touches several records is written as idempotent steps with a recovery marker, as in v1.
- **Expiry is part of the contract.** A record whose lifetime has passed reads as absent, whatever the engine did with it.
- **Quota state is `state` with compare-and-set** ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).

### One writer owns each record kind

- **Every record kind has exactly one writer**: one actor that may create and change it. Other actors may read it if their manifest says so.
- **The state backend enforces it, with its own check of the caller.** There is no `state-owner` mediator ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). Each actor's state driver runs `per-actor` and holds only that actor's credential ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)):
  - **rqlite:** one cluster for each pair of boundary and owning actor. The owning actor holds the `execute` user, and each declared reader holds a `query`-only user.
  - **DynamoDB:** an IAM condition on `dynamodb:LeadingKeys` for each actor's role.
  - **PostgreSQL**, where it is used for state (ADR 0047): grants between actor kinds and row-level security between boundaries, as 0058 sets out.
  - **Embedded:** a SQLite file per actor, owned by that actor's uid.
- **The enforcement is in the backend because a rule in each actor's code is a rule a compromised actor can skip.** It is layer 2 of three: the port library scopes every call to the actor's own boundary and kind (layer 1), and at the higher isolation levels the store itself is separate (layer 3) ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). The users, roles and policies are generated from the same manifests ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)).
- **Operator-set records are written by the kind's owning actor,** on a request routed through the console API, and the manifest says which kinds accept them ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). The console never writes a record itself, and the backend would refuse it if it tried.
- **Namespaces are per origin and trust class, with one writer per kind.** When pieces of an installation share one driver instance ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)), operator-set records, learned facts, issued records, sessions and quota state sit in separate namespaces, and each writer's credential reaches only its own kind.

### One lease port, with fencing

- **`lease` is a separate port**: `acquire` and `renew`, with a named lease for each thing that needs one owner. Acquiring returns a fencing token that grows each time the lease changes hands, and a holder passes it with its writes.
- **An actor that declares `instances: exactly 1` needs a lease** when there is more than one node or the nodes are elastic ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)).
- **Leases live on the state store**, as compare-and-set with a fencing token, on every state driver ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). They are independent of the Raft leader. The named `leader` lease stays available. On a single node a local lock is enough.
- **Where the target store can fence, it fences.** A PostgreSQL write checks the token in the same transaction. Audit blob batches are created only if absent (`If-None-Match: *`), so the audit log cannot fork, and with one log per node the boards' audit writers need no lease.
- **External side effects rely on the lease plus idempotent reconcilers.** A database session lock is never used alone, because it gives no fencing token.
- **The Kubernetes Lease driver is in the backlog.**

### Drivers and where each fits

| Driver | Fits | Notes |
|---|---|---|
| `embedded` | One node: the local installation and a single machine | A SQLite file per actor. Exactly 1 node. |
| `rqlite` | Bare-metal, 1, 3 or 5 nodes | Replicated SQLite on Raft, one cluster per boundary and owning actor. 3 or more nodes to survive a failure. Needs a disk. Also provides the lease. |
| `dynamodb` | Anywhere with an AWS account; serverless | A table per boundary, with a leading key per actor role. Also provides the lease. |

- **DynamoDB on AWS** is the driver of the lambda installation. It needs no node and no disk.
- **Embedded on one node.** The local installation uses a SQLite file. It is not replicated, and a single machine with no cloud may use it too.
- **rqlite for a cluster.** The boards installation runs one rqlite cluster for each pair of boundary and owning actor, each on the 3 nodes. Each cluster runs beside sluis as a service of its own, from the systemd template `sluis-rqlite@<boundary>-<actor>`, and the three nodes are its Raft voters. A cluster is 1, 3 or 5 nodes: an even number adds a node without adding fault tolerance.
- **Kubernetes** uses DynamoDB for now. Other state drivers on Kubernetes are not part of this record and will be decided in ADR 0047.

### How the rqlite clusters are run

- **Each cluster's Raft port is the only TCP port it opens between nodes.** Node-to-node and client traffic use mutual TLS, with a certificate for each cluster, so a node of one cluster cannot join another. The ports come from a port scheme, and the firewall limits the Raft ports to the other nodes' addresses ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)).
- **Each rqlite unit is sandboxed by systemd** so that it can write only its own data directory. That contains `ATTACH`, which an `execute` user can otherwise use to create files anywhere the rqlite user can write ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- **Compare-and-set is a conditional update** that checks the revision and the number of rows it changed. Writes go through the Raft log as atomic batches.
- **Timestamps come from the application.** rqlite does not rewrite non-deterministic values such as the current time in a default or a trigger, so a replica would otherwise compute its own.
- **Authority-relevant reads are linearizable.** A read that decides who may do what must see the latest write. Reads that tolerate lag may be served by a follower.
- **A new node joins by discovery.** It finds each cluster and joins it, with no operator step.
- **An unreachable node is reaped.** After a timeout of minutes, a member that has not come back is removed from the cluster.
- **A node leaves cleanly when it is scaled in.** Where the platform has a scale-in hook, such as an auto scaling lifecycle hook on AWS, the hook makes the node leave the cluster before it goes.
- **A replacement node enrols the same way as any host**, through `exchange`, and that gives its node agent `sluis-node` its identity ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). The ceremony that certifies a node's intermediate with the root cards stays a separate step (ADR 0032). The node then joins each cluster as a new member and not a returning one. (The last point is inferred.)

### Encrypted and sealed values in state

- **Every value in state is encrypted in the port library**, whatever its sensitivity ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). The cipher is AES-256-GCM, with one data key for each pair of boundary and record kind, wrapped by the boundary's key-encryption key through the `keys` port. The additional authenticated data is (boundary, kind, key, version), so a value copied to another record does not decrypt. The store sees ciphertext and record keys only, and record keys never contain personal data.

- **State may hold a sealed secret on every runtime, when it is sealed properly.** The encryption above is the floor under every record; sealing is the stricter rule for a secret. A sealed value is a record that holds ciphertext under a key that the `keys` port keeps. This supersedes v1's retirement of sealing: v1 ADR 0028 (on `master`) sealed written secrets in the state store, and two days later sealing was retired in favour of a secrets store that holds the secret, with state holding only the record that names it.
- **What "properly" means is a record of its own, ADR 0034.** It will fix the encryption context and the additional authenticated data, a re-seal path, recovery when a sealing key is lost, and one immutable item for each version with compare-and-set on the record that names it. The sealed-state driver and its details belong to that record and not to this one.
- **A backup archives sealed values sealed,** with an escrow ceremony for the sealing key ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). The archive format is ADR 0035.

### Not decided here

- Kubernetes state beyond DynamoDB, including a PostgreSQL or CloudNativePG state driver: ADR 0047.
- A Kubernetes Lease driver: in the backlog ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- Further operations on the port beyond get, put with a precondition, cas and expiry (delete, listing by prefix, a conditional adjust of a counter, watch or a change feed), which v1's state port had or the design pages propose: ADR 0057.
- The sealing format and the secrets port: ADR 0034.
- The `rqlite` index driver, which uses rqlite for search and not for state: [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/). Full-text search (FTS5) was checked on rqlite v10.6.6 on 2026-10-30 and is compiled in, on a single node and in a three-node cluster with replication, and in the arm64 image. The result belongs to that record, and nothing here depends on it.

## Consequences

- An actor that is compromised can write only the record kinds it owns. It can read what its manifest lists and nothing else. This is checked by the state backend itself, with users, roles and policies generated from the manifests ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)), and the check has a test per driver with the other layers switched off ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). No mediator is in the path of a state call.
- Replacing a state driver is a change of installation and not of code, because a driver passes the same conformance suite for the contract. A new driver needs only that suite.
- The cluster needs no external service: three boards, a disk each, and the Raft ports between them. The cost is that sluis operates replicated databases, one per boundary and owning actor, each with its own ports, certificates, users file, backups and upgrades. Quorum, snapshots and restarts are ours to watch for each, and a loss of 2 of 3 nodes stops writes until one returns. Each cluster needs about 16 to 26 MB per node under load on a desktop (2026-10-30), and about 9 MiB per cluster on a board (spike S5, 2026-10-31). On a board the CPU, not the eMMC's fsync rate, is the first limit: about 400 commits/s per board, summed over all its clusters.
- rqlite is a single point of project risk. The project has one main maintainer as of 2026-10-26. Its SQLite is compiled in through cgo, and it cannot be embedded as a library. It had three security advisories in October 2026, fixed in v10.6.x, and each release now means a rolling upgrade of every cluster. The state driver is kept thin so that a replacement is a driver, and the embedded Raft option below is the fallback.
- A write goes through consensus, so it costs a round trip to a majority. rqlite documents 10 to several hundred writes per second without batching and about a hundred times that with batching (read on 2026-10-26). Quota enforcement puts a conditional update on the mint path, so the first use of enforcement is measured under load before it is turned on in production.
- A read at the default level on a follower is forwarded to the leader, and a local read on a follower can be stale. Measured on 2026-10-30, a local read straight after an acknowledged write missed it every time, and the follower caught up within about 50 ms. Linearizable and the default level were never stale. Callers choose the level per read.
- A reap timeout that is shorter than a planned outage removes a healthy node. The timeout is a setting to be set against rolling updates and restarts. (inferred)
- On a full power loss the cluster comes back only when a quorum is up. When keys are locked until an operator unlocks them, the cluster waits for that too.
- A snapshot copy of the database is not the backup. The backup is a logical archive read through the ports (ADR 0035), so it is the same on every driver. Which record kinds it holds follows from their origin ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
- State holds no value in the clear: every value is encrypted, and a secret is either in the secrets port or sealed. The cost is an unwrap per data key and an AEAD per value, and record keys have to be designed to carry no personal data.
- A lease works the same on every state driver and does not move when the Raft leader does. A stale holder is refused by its fencing token wherever the target store checks it.
- `sluis config check` refuses a driver that does not fit the runtime, such as rqlite or the embedded driver on serverless, and refuses an actor that needs a lease with no lease driver.

## Roads not taken

### NATS JetStream key-value for clusters

- Pros: replicated three ways, with a key-value store, revisions and compare-and-set. It is also a transport driver.
- Cons: v1 removed it from the State port because no deployment ran it (v1 ADR 0027, on `master`). It is a second service to run, with its own flush interval that has to be reviewed, and it would add nothing to the index, which wants SQL.
- Why not: NATS JetStream stays a transport driver only.

### Keep a cache with asynchronous replication

- Pros: simple, fast, widely run.
- Cons: a primary can acknowledge a write and fail before a replica sees it. For sessions and refresh tokens that means everyone is signed out and a rotated token can return (v1 ADR 0027, on `master`).
- Why not: v1 retired it, and v2 starts from the replacement.

### Kubernetes objects as the store

- Pros: nothing to install on Kubernetes.
- Cons: session-rate writes load the API server, there is no lifetime for a record, and there is no equivalent on serverless or bare metal (v1 ADR 0027, on `master`).
- Why not: it fits one runtime and not the other two.

### DynamoDB everywhere

- Pros: one driver. No cluster to run.
- Cons: every installation would need an AWS account, including a local one and a board cluster with no cloud (v1 ADR 0027, on `master`). A local emulator is not the engine.
- Why not: the dependency belongs to the installations that have a cloud.

### PostgreSQL as the default state store

- Pros: it fits the port, and an installation may already run it.
- Cons: nothing in state needs a relational engine. It is a run-time dependency on serverless that needs a connection pool (v1 ADR 0027, on `master`), and on the boards it is one more service to run and replicate. Its first place in v2 is the index driver.
- Why not: deferred in v1 and not the default in v2. It can be added as a driver that passes the same suite. How it is isolated, with grants and row-level security, is in [0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/), and whether Kubernetes gets one is ADR 0047.

### Embed Raft in sluis, on `hashicorp/raft` and a local log store

- Pros: no separate rqlite process and no cgo SQLite fork. It uses the same Raft core that rqlite uses. No dependence on one maintainer's project.
- Cons: we would write and run a replicated store: snapshots, membership, upgrades. There is no SQL, so compare-and-set and the batch are ours to build, and the index driver would not be able to reuse the cluster. (inferred)
- Why not: rqlite already does it. This stays open as the fallback, to be revisited if memory use, cgo or the single-maintainer risk become a problem.

### A general-purpose replicated key-value store, such as etcd

- Pros: mature, built for consensus and leases.
- Cons: a separate service with its own operation, tuned for small data sets, and with no SQL for the index to share. (inferred)
- Why not: it adds a service without removing one, and rqlite serves both state and lease.

### Manual cluster membership

- Pros: nothing to automate. The operator decides who is a voter.
- Cons: a scaled-in or replaced node leaves a dead voter, and enough of them cost the cluster its quorum. A replacement machine would wait for a person. (inferred)
- Why not: discovery on join, reaping on loss and a clean leave on scale-in make replacement an ordinary event.

### One writer per record kind kept by convention

- Pros: no enforcement point to build.
- Cons: a bug or a compromise in any actor can overwrite a record that it does not own. A convention is not checked. (inferred)
- Why not: the backend's own check of the caller enforces it. A rule that depends on every actor's code being right is not a rule against a compromised actor.

### A `state-owner` mediator in front of the store

- Pros: works with rqlite as it is, which authorises per endpoint only. One policy point, easy to audit. One cluster per installation. It was the earlier version of this record.
- Cons: the isolation between actors is the mediator's code, not something the backend enforces. The mediator is in every state call's path and is the whole state class as blast radius. Its access list is one more thing that can be wrong.
- Why not: rqlite is light enough to run a cluster per owning actor, so the backend can enforce one writer per kind and no mediator is needed ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).

### One rqlite cluster per boundary, with a column for the actor

- Pros: the fewest clusters.
- Cons: rqlite cannot restrict a user to rows or tables, so every actor with `execute` could rewrite every record of the boundary, and `ATTACH` can write files.
- Why not: it leaves the backend with no per-caller check.

### The Kubernetes Lease as the lease on Kubernetes

- Pros: native to the platform. No state store needed for leader election.
- Cons: the chart would need RBAC for leases, the stores that the holder writes to would not check its fencing token, and it is a lease driver for one runtime. (The last two are inferred.)
- Why not: leases on the state store work on every runtime, and fencing belongs in the store that is written. The Kubernetes Lease driver stays in the backlog ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).

### Leave sealed state out of v2

- Pros: v1 retired sealing within two days, in favour of a secrets store. Every installation with a secrets store would have one model for secrets.
- Cons: an installation with no secrets store, such as the boards, would have no place to put a vendor-issued secret. v1's retirement was made where a managed store already existed. (inferred)
- Why not: sealed values are allowed on every runtime when sealed properly. The rules for that are ADR 0034.
