# 0017 — State is one port with compare-and-set; one writer owns each record kind; clusters replicate with rqlite

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

sluis keeps records: sessions, the signing ring, directory facts and counters. Most of its security rests on who may change which of them, and on what happens when two callers change one at once.

v1 put everything that is not a blob behind one State port (v1 ADR 0027, on `master`). It worked on one key at a time with compare-and-swap by revision, listing by prefix and a lifetime, and it filtered expired records on read because a store may delete them late. It had two engines: NATS JetStream on Kubernetes and DynamoDB on AWS. The JetStream half was removed on 2026-10-04, because no deployment ran it, and the DynamoDB half stands. Before that port, sessions lived in a replicated cache whose asynchronous replication could lose a write that had been acknowledged, so a failover signed everybody out and could bring a rotated refresh token back to life.

v2 adds installations that v1 never had: a single machine with no cloud, and a cluster of three single-board computers with nothing but each other. They need a state store that is not DynamoDB, that survives the loss of one node and that can be run without an operator. v2 also splits the core into actors ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)), and an actor that is compromised should not be able to rewrite a record that it does not own. Some actors must run exactly once, so there is also a need for a lease that a stale holder cannot use.

Two other facts shape the answer. Drivers are services on Unix sockets, and the only TCP port between nodes is the one the state driver uses for its cluster traffic, over mutual TLS ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). And a driver declares what it needs: a long-running process, a disk, a number of nodes ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).

## Decision

### One port, with compare-and-set

- **`state` is one port.** An actor sees one contract whatever sits behind it. [0002](/decisions/0002-actors-declare-ports-placement-is-derived/) fixes the port and its capability flag (`state` has compare-and-set). This record refines that flag into the write rules of the contract.
- **Records carry a revision, and every write has a precondition.** The operations are `get`, `put` with a precondition and `cas`. A `put` creates a record only if it is absent, and `cas` changes a record only against the revision the caller read. There is no unconditional overwrite.
- **Writes that must go together are one atomic batch.** There are no interactive transactions. A flow that touches several records is written as idempotent steps with a recovery marker, as in v1.
- **Expiry is part of the contract.** A record whose lifetime has passed reads as absent, whatever the engine did with it.
- **Quota state is `state` with compare-and-set** ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).

### One writer owns each record kind

- **Every record kind has exactly one writer**: one actor that may create and change it. Other actors may read it if their manifest says so.
- **`state-owner` enforces it** for the stores that have one login and cannot tell callers apart: rqlite and the embedded store. It is a shared driver that owns the store. It authenticates each caller by peer credentials and checks the call against an access list of caller actor, record kind and operation. The store's own listener is closed to every other process, and every use is audited.
- **The enforcement is outside the actors because the store cannot do it.** rqlite has no row-level permissions, and a rule that lives in each actor's code is a rule a compromised actor can skip. Where the backend can tell callers apart, as DynamoDB can under IAM, the same access list is generated for each actor's own driver instance, from the same manifests ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). (inferred)

### One lease port, with fencing

- **`lease` is a separate port**: `acquire` and `renew`, with a named lease for each thing that needs one owner. Acquiring returns a fencing token that grows each time the lease changes hands, and a holder passes it with its writes.
- **An actor that declares `instances: exactly 1` needs a lease** when there is more than one node or the nodes are elastic ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)).
- **Drivers:** a local lock for one node, rqlite on a cluster, DynamoDB on AWS, and the Kubernetes Lease on Kubernetes. On a Raft cluster, leadership is one named lease. Every other lease is built on compare-and-set in the state store.

### Drivers and where each fits

| Driver | Fits | Notes |
|---|---|---|
| `embedded` | One node: the local installation and a single machine | A SQLite file. Exactly 1 node. |
| `rqlite` | Bare-metal, 1, 3 or 5 nodes | Replicated SQLite on Raft. 3 or more nodes to survive a failure. Needs a disk. Also provides the lease. |
| `dynamodb` | Anywhere with an AWS account; serverless | One table. Also provides the lease. |

- **DynamoDB on AWS** is the driver of the lambda installation. It needs no node and no disk.
- **Embedded on one node.** The local installation uses a SQLite file. It is not replicated, and a single machine with no cloud may use it too.
- **rqlite for a cluster.** The boards installation runs rqlite with 3 nodes. rqlite runs beside each sluis node as a service of its own, behind `state-owner`, and the three nodes are the Raft voters. The cluster is 1, 3 or 5 nodes: an even number adds a node without adding fault tolerance.
- **Kubernetes** uses DynamoDB for now. Other state drivers on Kubernetes are not part of this record and will be decided in ADR 0047.

### How the rqlite cluster is run

- **The Raft port is the only TCP port between nodes.** It is mutually authenticated with TLS, and the firewall limits it to the other nodes' addresses ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)).
- **Compare-and-set is a conditional update** that checks the revision and the number of rows it changed. Writes go through the Raft log as atomic batches.
- **Timestamps come from the application.** rqlite does not rewrite non-deterministic values such as the current time in a default or a trigger, so a replica would otherwise compute its own.
- **Authority-relevant reads are linearizable.** A read that decides who may do what must see the latest write. Reads that tolerate lag may be served by a follower.
- **A new node joins by discovery.** It finds the cluster and joins it, with no operator step.
- **An unreachable node is reaped.** After a timeout of minutes, a member that has not come back is removed from the cluster.
- **A node leaves cleanly when it is scaled in.** Where the platform has a scale-in hook, such as an auto scaling lifecycle hook on AWS, the hook makes the node leave the cluster before it goes.
- **A replacement node enrols its identity through `sluis-node`** (the node agent), and is then a new member and not a returning one. (inferred)

### Sealed values in state

- **State may hold a sealed value on every runtime, when it is sealed properly.** A sealed value is a record that holds ciphertext under a key that the `keys` port keeps. This supersedes v1's retirement of sealing: v1 ADR 0028 (on `master`) sealed written secrets in the state store, and two days later sealing was retired in favour of a secrets store that holds the secret, with state holding only the record that names it.
- **What "properly" means is a record of its own, ADR 0034.** It will fix the encryption context and the additional authenticated data, a re-seal path, recovery when a sealing key is lost, one immutable item for each version with compare-and-set on the record that names it, and how a backup treats sealed values. The sealed-state driver and its details belong to that record and not to this one.

### Not decided here

- Kubernetes state beyond DynamoDB: ADR 0047.
- Further operations on the port beyond get, put with a precondition, cas and expiry (delete, listing by prefix, watch), which v1's state port had: ADR 0057.
- The sealing format and the secrets port: ADR 0034.
- The `rqlite` index driver, which uses rqlite for search and not for state: ADR 0022. Full-text search (FTS5) was checked on rqlite v10.6.6 on 2026-10-30 and is compiled in, on a single node and in a three-node cluster with replication, and in the arm64 image. The result belongs to that record, and nothing here depends on it.

## Consequences

- An actor that is compromised can write only the record kinds it owns. It can read what its manifest lists and nothing else. This is checked at `state-owner`, and the access list is generated from the manifests ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)).
- Replacing a state driver is a change of installation and not of code, because a driver passes the same conformance suite for the contract. A new driver needs only that suite.
- The cluster needs no external service: three boards, a disk each, and the Raft port between them. The cost is that sluis operates a replicated database. Quorum, snapshots and restarts are now ours to watch, and a loss of 2 of 3 nodes stops writes until one returns.
- rqlite is a single point of project risk. The project has one main maintainer as of 2026-10-26. Its SQLite is compiled in through cgo, and it cannot be embedded as a library. It had three security advisories in October 2026, fixed in v10.6.x. The state driver is kept thin so that a replacement is a driver, and the embedded Raft option below is the fallback.
- A write goes through consensus, so it costs a round trip to a majority. rqlite documents 10 to several hundred writes per second without batching and about a hundred times that with batching (read on 2026-10-26). Quota enforcement puts a conditional update on the mint path, so the first use of enforcement is measured under load before it is turned on in production.
- A read at the default level on a follower is forwarded to the leader, and a local read on a follower can be stale. Measured on 2026-10-30, a local read straight after an acknowledged write missed it every time, and the follower caught up within about 50 ms. Linearizable and the default level were never stale. Callers choose the level per read.
- A reap timeout that is shorter than a planned outage removes a healthy node. The timeout is a setting to be set against rolling updates and restarts. (inferred)
- On a full power loss the cluster comes back only when a quorum is up. When keys are locked until an operator unlocks them, the cluster waits for that too.
- A snapshot copy of the database is not the backup. The backup is a logical archive read through the ports ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/) describes the audit side, ADR 0035 the rest), so it is the same on every driver.
- State holds no secret in the clear. A secret is either in the secrets port or sealed.
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

### PostgreSQL as the state store

- Pros: it fits the port, and an installation may already run it.
- Cons: nothing in state needs a relational engine. It is a run-time dependency on serverless that needs a connection pool (v1 ADR 0027, on `master`), and on the boards it is one more service to run and replicate. Its place in v2 is the index driver, not the state driver.
- Why not: deferred in v1 and not needed in v2. It can be added later as a driver that passes the same suite.

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
- Why not: `state-owner` enforces it. A rule that depends on every actor's code being right is not a rule against a compromised actor.

### Leave sealed state out of v2

- Pros: v1 retired sealing within two days, in favour of a secrets store. Every installation with a secrets store would have one model for secrets.
- Cons: an installation with no secrets store, such as the boards, would have no place to put a learned secret. v1's retirement was made where a managed store already existed. (inferred)
- Why not: sealed values are allowed on every runtime when sealed properly. The rules for that are ADR 0034.
