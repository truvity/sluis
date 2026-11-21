# 0005 — store@ is the thin host of atoom's state block: it holds every storage credential, selects the stores of each class, enforces sluis's rules and supplies signed configuration

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

sluis keeps its data as kinds ([0004](/decisions/0004-the-data-model/)). Much of its security rests on who may change which item and on what happens when two callers change one at once. A work actor holds no storage credential at rest (principle 16, [0002](/decisions/0002-families-of-actors/)), so one holder in the data family has to take the stores, the config sources and the secret sources, and every actor reaches data through it.

v1 put everything that is not a blob behind one State contract (v1 ADR 0027, on `master`): one key at a time, compare-and-swap by revision, listing by prefix and a lifetime, with expired items filtered on read. It had two engines, NATS JetStream on Kubernetes and DynamoDB on AWS; the JetStream half was removed because no deployment ran it. Before that port, sessions lived in a cache with asynchronous replication, so a failover could lose an acknowledged write, sign everyone out and bring a rotated refresh token back.

v2 has installations v1 never had: a single machine with no cloud, and three small boards with nothing but each other. They need a store that is not DynamoDB, survives the loss of one node and runs without an operator. Some actors must run exactly once, which needs a lease that a stale holder cannot use. Config has to be trusted wherever it comes from: a file, a ConfigMap or AWS AppConfig. A service that serves config is not a reason to trust what it serves (principle 4).

None of this is specific to sluis. The store's mechanics are the `state` block of [atoom](https://github.com/truvity/atoom): addresses, a kind registry that any product extends, signed config, layers, watch and the bindings per storage class ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). audit declares its kinds in the same block. store@ is therefore a thin host of it, and this record states what sluis adds. The facts about each engine (what rqlite, DynamoDB and PostgreSQL measured) live with the drivers in atoom.

## Decision

**store@ is the only holder of storage credentials.** It hosts atoom's `state` block for sluis, serves one generic store service to work actors on their data port, enforces the kind registry ([0004](/decisions/0004-the-data-model/)) on every call, and has exactly four ports downward, one per storage class. exporter@ holds the credentials of its export destinations ([0009](/decisions/0009-export/)); a work actor holds none.

### What is atoom's

These are the block's, described and tested there. This record does not restate them.

- The store API: items, versions, `get`, `list`, `create`, `update`, `delete`, `apply` and `watch`, the batch, and the sealed envelope.
- Watch and the resume point: the change log, the epoch of a log, delivery and the catching-up claim of a stream.
- Leases and their fencing token, the engines' behaviour, and time to live.
- The item contract and the source contract, with their conformance kits: [store port](https://github.com/truvity/atoom/blob/master/store/docs/store-port.md) and [source port](https://github.com/truvity/atoom/blob/master/source/docs/source-port.md).
- Signed-config loading: the document format and its checks, the snapshot, freshness at a cold start, a failed reload and the format of the bootstrap file.
- The durability that a store driver declares, `strict` or `lossy` ([atoom 0012](https://github.com/truvity/atoom/blob/master/docs/decisions/0012-store-durability.md)).

The block is planned in atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md). Until it moves, store@ implements these mechanics in `actors/store` and the pages [Store API](/reference/store-api/), [Signed config](/reference/signed-config/) and [Bootstrap file](/reference/bootstrap-file/) describe what it does.

### The store API

- **The four ports downward, one per storage class.** store@ reads config and secrets through the source contract and keeps durable and temp data through the item contract.

| Port | Class | Access | Drivers sluis selects |
|---|---|---|---|
| Config source | config | Read-only | `file` (a directory, a mounted ConfigMap or a Lambda layer); `appconfig` (an AWS AppConfig profile; planned) |
| Secret source | secret | Read-only | A systemd credential (boards and local); AWS SSM (planned) |
| Durable | durable | Read-write | `rqlite` (boards and local), `dynamodb` (AWS), `postgres` (Kubernetes; planned) |
| Temp | temp | Read-write | `rqlite`, `dynamodb`, `postgres` |

- **The durable and temp ports may sit on one store.** They are two ports because the classes differ in backup and in time to live. The bootstrap file names one destination per port ([The bootstrap file](/decisions/0005-the-store/#the-bootstrap-file)).
- **There is one generic service on the wire,** served as ConnectRPC. A kind adds no method and no generated code on the wire. There is no query language and no SQL: a new question is a new declared index, reviewed with the kind ([0004](/decisions/0004-the-data-model/)).
- **A sealed kind is opaque to the store.** It keeps the ciphertext the writer sealed and the hash the writer computed, and validates the envelope, never the content ([0006](/decisions/0006-keys-and-sealing/)).
- **A read of a layered kind returns its provenance:** the effective value and, for each field, the layer that set it and the version.
- **A flow over several items is idempotent steps with a recovery marker.** Writes that must go together are one batch by one actor to its own kinds; there are no interactive transactions.

### Typed clients

- **Actors use typed clients, generic on the wire.** In Go the client is `store.Client[T]`; in Rust a kind is a trait and the client is `Store<K: Kind>`. Both are built (`kits/go/store`, `kits/rust/sluis-store`).
- **The client is generated from the kind's Protobuf message** ([0004](/decisions/0004-the-data-model/)). It builds the address from the boundary it is connected to, the tenant and id the caller passes, and the kind's name. It cannot choose who the caller is: the store learns that from the transport.
- **There is one client library per language,** not a generated service for each kind.

### Rules

The rules are what sluis plugs into the block's rules hook. They decide who may write which kind.

- **A rule is one line of the kind registry:** an actor may use a verb on a kind. The store checks the rule on every call, before it touches data, and denies by default. The rules are generated from the kind registry and the actor registry ([0002](/decisions/0002-families-of-actors/)); the matrix is the generated page [Rules](/reference/rules/).
- **The store learns the caller from the transport** (a peer uid, a Lambda role, or a projected service-account token). Fused work actors present one identity, and the store applies the union of their rules ([0003](/decisions/0003-units-transports-and-fusion/)).
- **A rule may limit an actor to ids under its own name,** as for the tunables, the log settings and a self-scoped kind ([0004](/decisions/0004-the-data-model/)).
- **A rule may allow an actor to watch a kind** (`watched_by`). A tenant-scoped kind is watched under a tenant, as a read of it needs; the empty tenant is for a kind with no tenant, and for the boundary's own `DataKeyWrap` and `KeyRevocation`. policy recompiles on watch, and exporter@ watches the exportable kinds ([0009](/decisions/0009-export/)). A reader that fails closed, such as the key set that `entry` serves, treats its state as unconfirmed until the stream says it has caught up.
- **The store also checks, on every write:**
  - the schema of the kind, and for a temp kind a TTL within the kind's maximum;
  - the version, for an update;
  - the bounds and the direction rule of a layered kind ([0004](/decisions/0004-the-data-model/)), reading the config layer from the snapshot;
  - the links that the registry says are checked at write;
  - that the writer is the kind's one writer, or the actor named in the id of a self-scoped kind.
- **Every write to a tenant-scoped kind carries a grant sealed for that tenant.** The store has key@ open it and refuses the write unless it names the address's tenant and allows the verb ([0007](/decisions/0007-policy-and-sealed-grants/)). That is the store's half of the two independent checks; key@'s grant check on every Seal and Open is the other. key@'s own writes of `DataKeyWrap` carry no grant: key@ is itself the other check, and the items are ciphertext. Its writes of `KeyRevocation` carry none because they follow an operator's declaration, not a tenant's grant ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)). A rewrap for a new board has no request to carry one.
- **While `MaintenanceMode` is set, the store refuses every write** except those of the restore command, which writes back what it restores ([0010](/decisions/0010-backup-and-recovery/)).
- **exporter@ and the restore command are callers like any other,** with rules of their own. The backup reads authoritative kinds through the store as ciphertext.
- **A refusal returns a code that names the rule, and every refusal is an audit fact,** on every method and for every reason ([Refusals](/reference/store-api/#refusals)). The fact is best effort: when the spool is full or absent it can be lost, and the loss is counted ([audit 0005](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)). The refusal is answered whatever happens to its fact.
- **Refusal facts are counted past a rate,** so that a stranger or a compromised actor with a socket cannot fill the shared spool and stop every signature, whose fact must be durable. key@ limits them at its transport guard and in its handlers, one limiter per socket or caller. For each key (the peer uid and reason at the guard; the reason in a handler) the first 10 of each minute are facts one by one, and the rest are one fact at the end of the minute that carries their number. A key makes at most 60 facts an hour and a limiter at most 120; what is past either is counted in one summary that names nobody. Nothing is lost while the process lives, but a crash loses the counts that are pending. These are not facts, on purpose: a non-root peer refused at the root-only admin socket (its refusals are journal lines), and the failure of a dependency, which is not a refusal of the caller. The bound for an idle board is [REQ-AUDIT-180](/requirements/audit/#REQ-AUDIT-180).

### Drivers

- **sluis consumes atoom's drivers.** It builds none of its own and pins atoom's by release (`third_party/atoom`, `just atoom-sync`). Where a driver must run behind sluis's socket, peer credentials and command line, a thin host in `drivers/store/` registers atoom's library there; the hosts for `file`, `credential` and `rqlite` are built. The AWS-backed drivers are Go libraries of atoom that a Lambda function mounts in process.
- **Each port has one contract and one conformance kit.** A driver passes its kit or it is not a driver; the in-memory driver passes it too. A driver sees items only and never knows what a kind means.
- **The platform starts a driver; nobody execs it.** On boards it is a systemd service beside the store's unit, on Lambda an extension of the function, and on Kubernetes a native sidecar container in the pod, with its socket on an emptyDir and the cloud token mounted only into the driver container ([0003](/decisions/0003-units-transports-and-fusion/)). Only store@ can open a driver's socket, and a work actor never sees one (principle 11).
- **An installation selects the driver of each class by what the class must be.** A durable kind must survive a crash and a failover, so sluis refuses a `lossy` driver for the durable class. Every driver sluis selects today declares `strict`, for the temp class too: a temp kind holds rotation state, leases and replay guards, and a lost acknowledged write would bring back a rotated token. `sluis config check` refuses a driver that does not fit the runtime, such as `rqlite` on serverless.
- **Each port has one driver per installation shape** until a second real installation needs another; that driver arrives with that installation.

### Engines

| Store | Where | Notes |
|---|---|---|
| **DynamoDB** | AWS: serverless | A table per installation, reachable by store@'s role only; DynamoDB TTL; needs no node or disk |
| **PostgreSQL** | Kubernetes, through CloudNativePG | One cluster per installation, with the store and the audit index in separate databases and roles, so the credential of one does not open the other. Planned: atoom's `store/postgres` |
| **rqlite** | Boards, and the local installation with one node | Replicated SQLite on Raft; temp data is replicated too, so any board serves any request |
| **In-memory** | Tests only | Passes the conformance kit |

- **Kubernetes stores in PostgreSQL through CloudNativePG,** so the Kubernetes installation does not depend on one cloud for its store. The `postgres` driver implements the item contract, and its change log is the part to get right, because watch and every exporter and reader depend on it. It passes the same kit as the others. Keys do not change: the key driver on Kubernetes is `aws-kms`, so the tested installation still needs AWS KMS for its keys; a key driver for a cluster without AWS is later and not part of the core. The requirements are [REQ-DATA-130](/requirements/data/#REQ-DATA-130), [REQ-DATA-131](/requirements/data/#REQ-DATA-131) and [REQ-DEPLOY-053](/requirements/deploy/#REQ-DEPLOY-053); the roadmap says when ([the roadmap](/roadmap/)). Serverless keeps DynamoDB, and the boards and the local installation keep rqlite ([0011](/decisions/0011-runtimes-and-installations/)).
- **A store serves a boundary at L3 or above.** A boundary below L3 is served under its address prefix by its parent's store ([0008](/decisions/0008-boundaries-and-tenants/)).
- **rqlite runs beside sluis as its own service,** one cluster of 1, 3 or 5 nodes: an even number adds a node without adding fault tolerance, and 3 or more survive a failure. It is a platform piece, not an actor of any family ([0002](/decisions/0002-families-of-actors/)). How the driver reaches it, the read levels and the sweeper are in atoom's [rqlite design](https://github.com/truvity/atoom/blob/master/store/rqlite/docs/design.md).
- **Raft is the only port a board opens to another board,** with mutual TLS and a certificate per cluster, so a node of one cluster cannot join another. The firewall admits it from the cluster's other nodes only ([0003](/decisions/0003-units-transports-and-fusion/)). Membership is automatic: a new node joins by discovery, an unreachable member is reaped after minutes, and a replacement node enrols as any host ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **A snapshot copy of a database is not the backup.** The backup is a logical archive read through the store, the same on every store ([0010](/decisions/0010-backup-and-recovery/)).

### Configuration loading

Config reaches the store as signed documents, one per config kind, loaded as a whole snapshot. The loading mechanics are atoom's. sluis supplies these parts:

- **The publisher keys are pinned in the bootstrap file.** The store accepts a signature only from a pinned key, so config cannot choose its own publisher. That the commit was reviewed is the branch protection of the repository, not the signature.
- **A regulated party's policy carries two signatures,** the publisher's and the party's. Which kinds need a second signature is declared in the bootstrap file, as a `co_signs` list on the pinned key of the party, so that config cannot waive it.
- **The format of a payload depends on the kind.** A policy kind is Cedar text; every other kind is YAML or JSON read under the strict profile of the bootstrap file, with durations written as [0004](/decisions/0004-the-data-model/) says.
- **The boundary declares the config kinds that its modules read.** A declared kind with no document is a failure, and a document for an undeclared kind is refused. Installation-wide documents have the same content in every boundary's source, and the store reads its own boundary's only ([0008](/decisions/0008-boundaries-and-tenants/)).
- **Audit's documents are config kinds like the rest.** `AuditLog`, `AuditWitnesses`, `AuditCatalogue` and `Meter` are loaded, verified and swapped in the same snapshot as sluis's own, and the audit pieces receive a changed document when the snapshot swaps ([audit 0015](https://github.com/truvity/audit/blob/master/docs/decisions/0015-configuration-kinds.md)).
- **A second verifier is possible.** A reader that must not rely on store@ for what a document says asks it for the signed bytes (`GetSignedConfig`) and verifies them itself, under the publisher keys of its own copy of the bootstrap file. key@ does so for the `KeyPlan` ([0006](/decisions/0006-keys-and-sealing/)). A compromised store@ can serve any decoded item, but it cannot sign one.
- **A source driver signals a change** in the way its backend allows: the `file` driver watches the directory, `appconfig` polls, and a Lambda layer cannot change under a running function.
- **A cold start refuses on any failure.** An unreachable source, an unsigned, badly signed, malformed or invalid document, and a snapshot that fails validation each stop the start with an error that names the document. Nothing is remembered between starts: no cached copy, no default in place of a missing document. A check that cannot run is reported as not run, never as passed (principle 4).
- **A failed reload keeps the snapshot in force.** The new snapshot is all or nothing, and a refusal is reported once per refused digest.
- **A cold start takes the latest validly signed document the source gives it,** whatever its sequence number; the sequence number protects a running store only. The residual is stated plainly: a source that serves an older document that the publisher key signed is believed until it serves a newer one, and the audit fact of the start is how it is found. The attacker needs the right to change what the source serves, not the publisher key, so it can roll back and cannot write. A revocation that cannot wait is a tightening in the operator layer, which is durable data, takes effect at once and needs no config ([0004](/decisions/0004-the-data-model/)).
- **Each start and each reload is an audit fact** that names, for each document, its kind, its `seq` and the SHA-384 of its payload, so a reviewer matches it to `sha384sum` of the file at the commit.
- **`sluis config check` runs the same validation offline,** as CI does before a document is published; `sluis config explain` shows the effective value of a setting with its provenance.

### The bootstrap file

- **The bootstrap file is the one local file store@ reads at start.** It names the boundary, one destination per storage class, the driver sockets, and the public keys of the publishers, each shown with its SPKI hash, which is the pin, and each with an optional `co_signs` list. On a host there is one file for each boundary, in `/etc/sluis/bootstrap.d/<boundary>.yaml`, and the systemd generator creates the instances from that directory. It is the only setting whose change needs a restart; a change of a publisher key is a restart of store@.
- **It holds no credential.** The credentials of store@ (the store login, access to the secret source) are provided by the platform: a systemd credential, an IAM role or a service account ([0004](/decisions/0004-the-data-model/)).
- **It is not a kind,** and its schema is generated from Protobuf like every external config. Its keys and checks are on the [bootstrap file](/reference/bootstrap-file/) page. store@ refuses a file that another uid can write.
- **The pin is the trust anchor of config.** It needs no signed document to verify the first one.

### Secret sources

- **A secret source is read-only,** a systemd credential on boards and AWS SSM elsewhere, written by IaC. The store serves a secret kind to its listed readers only ([0004](/decisions/0004-the-data-model/)).
- **A secret kind is linked to the config kind it serves** (`UpstreamSecret` to `Upstream`), and config names where the secret is in the source.
- **Values are fetched on first use and held in memory.** Nothing is written to disk, and a secret never appears in an audit fact, a log line or a span.
- **The store sees secret-source values in the clear.** A secret source holds them in the clear for whoever may read it, so they pass through store@ in memory. The design accepts this and the threat model states it: a compromised store@ yields secret-source values, and no sealed data, because the store holds nothing that opens a sealed value.
- **Secret kinds are not backed up.** IaC owns them, so a restore does not carry them.

## Consequences

- A compromised work actor can write only the kinds its rules allow and read only what they list, checked by the store, and it holds no store credential. The cost is a hop: every data call goes through store@, and a sealed value also goes through key@. On Lambda the hop is an invoke ([0003](/decisions/0003-units-transports-and-fusion/)).
- store@ is the one place that holds the store credentials and sees secret-source values. It is never fused with a work actor ([0003](/decisions/0003-units-transports-and-fusion/)). A compromised store@ yields ciphertext, hashes, plain kinds and secret-source values; it can also change or delete items in its store, and the audit log and the backup are the control against that.
- Replacing a store is a change of installation, not of code: a driver passes the same kit for the same contract.
- The boards need no external service: three boards, a disk each and the Raft ports. The cost is that a loss of 2 of 3 nodes stops writes until one returns, and that every write on a board is a Raft commit. A reap timeout shorter than a planned outage removes a healthy node, so it is set against rolling updates, and after a full power loss a cluster returns only with a quorum. The write budget has headroom; the figures and their dates are on the [bare-metal](/runtimes/bare-metal/) page, and [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/) re-measures the writers.
- rqlite is a single point of project risk: one main maintainer (checked 2026-10-26), SQLite compiled in through cgo, no embedding as a library, and three security advisories in October 2026, so each release is a rolling upgrade of one cluster. The driver is thin so that a replacement is a driver, and an own Raft is the fallback.
- A cold start needs the config source and every document: an AppConfig outage stops a start, and so does a document that is missing or broken. On Lambda this adds a dependency to the start of the store's function.
- An audit piece cannot cold-start without store@, which also loads its documents. It already depends on the store through key@, whose key wraps are kinds, so a loader of its own would buy no independence.
- A signed document is not fresh at a cold start; the residual is stated under [Configuration loading](/decisions/0005-the-store/#configuration-loading).
- A secret-source value is visible to store@: accepted, and stated in the threat model.
- The pages that follow are [Store API](/reference/store-api/), [Bootstrap file](/reference/bootstrap-file/), [Signed config](/reference/signed-config/) and [Data](/concepts/data/).

## Roads not taken

- **A store of sluis's own, beside atoom's `state` block.** audit and session would build the same addresses, kind registry and signed-config checks again. One block that each product extends keeps them in one mechanism.
- **Embedded etcd.** A second store with a privileged control plane and no SQL for the declared indexes.
- **Own Raft on `hashicorp/raft`.** We would build snapshots, membership and conditional updates ourselves; it stays the fallback if rqlite's risk grows.
- **A query language or SQL for actors.** Any actor could ask any question, every store would have to run it, and the rules could not be generated from the registry; a new question is a new declared index.
- **One generated service per kind.** A method and generated code per kind on the wire multiply with the registry; one generic service and typed clients give the same types without it. <!-- terms-allow: service-per-kind -->
- **The store seals and opens.** A key in store@ would let a compromise of the one holder of every store credential read every sealed value; sealing at key@ keeps plaintext in the actor that uses it (principle 18).
- **A store and a driver for each actor, each driver holding its actor's credential.** It puts a store credential in every work actor against principle 16, and a store that cannot tell callers apart needs a cluster for each owning actor, with its ports, certificates and upgrades on a 2 GB board.
- **NATS JetStream key-value for clusters.** It is replicated with revisions, but v1 removed it from its State contract because no deployment ran it, it is a second service with its own flush interval, and it adds nothing to the declared indexes.
- **A cache with asynchronous replication.** An acknowledged write can be lost on failover, signing everyone out and reviving rotated tokens.
- **Kubernetes objects as the store.** Session-rate writes load the API server, items have no lifetime, and it fits one runtime.
- **DynamoDB everywhere.** Every installation, the local one and the boards included, would need an AWS account; a local emulator is not the store.
- **DynamoDB as the store of Kubernetes.** It kept the Kubernetes installation tied to AWS for its store; PostgreSQL through CloudNativePG does not.
- **Several config sources composed by path ownership, or a later source that overrides an earlier one.** A document in a service could override a reviewed file with no trace in git, and the effective value would depend on the order of a list; one signed document per kind, loaded as a snapshot, has no order.
- **Trusting the config service's own access control for authority.** The right to write to AppConfig would become the right to grant, against principle 4.
- **Publisher keys that are themselves signed by a root, with the roots pinned in the bootstrap file.** Publisher keys would rotate without a restart, but every change would need a root signature, which is a ceremony. It can follow when publisher keys rotate often enough to matter.
- **A signed last known good copy for a cold start when the source is down.** It is the oldest authority the installation has: a grant revoked since would come back at the moment the operator cannot publish, and it starts the process without the check that failed.

## Follow-ups

- Secret-source values on AWS, tightened: the actor opens at key@ a value that store@ fetched without decrypting it. [#1182](https://github.com/truvity/sluis/issues/1182).
- Delete, list and watch in the store service, and the conformance suite of the drivers: [#1009](https://github.com/truvity/sluis/issues/1009).
- The config loader, the `file` and `appconfig` drivers, the check and the signed-document vectors: [#1157](https://github.com/truvity/sluis/issues/1157).
- Reloading the snapshot of a running store on every runtime: [#977](https://github.com/truvity/sluis/issues/977).
- Who issues the certificate of the Raft port: [#1584](https://github.com/truvity/sluis/issues/1584).
- The `postgres` driver, with the design of its change log and the kit run on it: [#1238](https://github.com/truvity/sluis/issues/1238).
- Open: the `state` block moves into atoom at step B7 of its [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md). Its records take over the mechanics that this record only points to (watch and the resume point, leases, time to live, the loading of signed config); sluis has no issue for its host change yet.
- Open: whether sluis ever binds a `lossy` driver to a temp kind. It binds none today; the question waits for the durability field of atoom's store port ([atoom 0012](https://github.com/truvity/atoom/blob/master/docs/decisions/0012-store-durability.md)).
