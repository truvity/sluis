---
title: Ports and drivers
description: "Ports and drivers: port interfaces, what each driver requires, which drivers fit which runtime, and how sluis config check validates it."
status: target
sidebar:
  order: 2
---

An [actor](/concepts/actors/) needs things from outside itself: keys to sign with, a place to keep records, a place to put audit archives. It asks for each through a **port**. A port is an abstract interface, with capability flags where the technologies behind it differ. The technology that answers it is a **driver**, and a driver declares what it requires: whether it needs a long-running process, a local disk, a number of nodes, or a **dependency** such as an AWS account.

The ports are **keys**, **state**, **lease**, **blob**, **transport**, **index** and **cloud**, plus **witness**, the only port that audit adds. Audit shares the other port definitions with sluis; see [Audit](/concepts/audit/). Two more ports, **secrets** and **export**, are proposed for the values sluis keeps and publishes; see [Secrets and export](/concepts/secrets/).

An actor sees one contract per port, whatever sits behind it. `signer` calls "sign with the token key" on the keys port; whether that lands on AWS KMS, on a YubiKey or on a software key is installation configuration, not actor code.

## Port interfaces

| Port | Interface | Capability flags |
|---|---|---|
| `keys` | `sign`, `public-key`, `wrap`, `unwrap` | `wrap` and `unwrap` (or an `ecdh` operation) let a data key be protected without the key ever leaving the device or the service. A driver also declares which signing algorithms it supports: see [Keys](/concepts/keys/#algorithms) |
| `state` | `get`, `put`, `cas` | `cas`: compare-and-set, needed wherever a record may be changed by more than one caller |
| `lease` | `acquire`, `renew` | Built on the state store's compare-and-set; every acquire returns a fencing token |
| `blob` | `put-once`, `get`, `list`, plus a lock mode | lock mode: `none`, `removable` or `compliance`. `put-once` is create-if-absent (`If-None-Match: *`) |
| `transport` | `put` / `receive` / `ack`, or `stream` | |
| `index` | `write`, `query` | |
| `witness` | `cosign` | |
| `cloud` | `credentials(cloud, role)` | |
| `secrets` (proposed) | `get`, `put` against a revision, `delete`, `list` (names only) | `writable`, `encryption`, `versioned`; see [Secrets and export](/concepts/secrets/) |
| `export` (proposed) | `put`, `delete`, `status` of a typed document | whether the target keeps it encrypted; see [Secrets and export](/concepts/secrets/) |

The `transport` port replaces what earlier drafts called the queue port and the outbox: it is how an emitter hands an audit record to the writer, either directly (`stream`) or through a queue (`put` / `receive` / `ack`). See [Audit](/concepts/audit/).

## Drivers are services, and nobody execs them

A driver is a platform-started service that speaks ConnectRPC over a Unix socket. **No actor starts a driver.** The platform does:

| Runtime | The platform starts a driver as |
|---|---|
| Bare-metal | A systemd service (and socket); under local Docker, a container runtime service |
| Serverless | A Lambda extension |
| Kubernetes (proposed) | A native sidecar container in the same pod as the actor |

:::note[Proposed]
On Kubernetes a driver is never a pod of its own, because a Unix socket cannot cross a pod boundary. It is a native sidecar, which the platform starts before the actors that call it. This replaces "a sidecar container, or a pod of its own". See [Kubernetes](/runtimes/kubernetes/#what-runs-where).
:::

This has consequences that the rest of the design leans on:

- An actor's unit has no exec rights at all. On systemd it carries `NoExecPaths=/` with an `ExecPaths=` naming its own binary only, so a compromised actor cannot start a shell, a helper or a driver of its own.
- The platform supervises the driver, restarts it and orders it before its callers. Our code does not.
- Backends combine at deploy time without a rebuild: add a unit, a layer or a sidecar.

Plugins follow the same rule. They always run out of process: over a Unix socket with peer credentials locally, as a Lambda invoke on AWS, or over HTTPS with mutual TLS when remote. There are no Go `.so` plugins and no WebAssembly.

## Per-actor and shared instances

Whether the backend can tell callers apart decides how a driver runs. Each driver service declares `instance: per-actor | shared`.

| Instance | Use it when | How access is enforced |
|---|---|---|
| `per-actor` | The backend can tell callers apart: AWS IAM, an HSM with a user per actor, a database with a role per sluis actor, an rqlite cluster with a user per actor | Each instance carries that one actor's own credential, and the backend enforces access |
| `shared` | The backend has one credential or one device: a YubiKey and its PIN on a node | One process holds the credential. It authenticates every caller and checks an access list generated from the manifest |

**A backend that can authorise each caller is used with per-actor credentials and no mediator** (agreed, MD-1). A mediator exists only for a backend that cannot tell its callers apart, and the one left is `key-holder` for a node's card. sluis brokers **credentials** instead of mediating the data path:

- `sluis-node` issues a token per uid on a node;
- the cloud drivers exchange that token for the actor's own cloud credentials;
- `ca` issues database client certificates ([PostgreSQL](/integrations/postgresql/));
- sluis issues NATS credentials (see [Audit](/concepts/audit/#the-transport)).

### The uid rule

A **per-actor driver runs as the actor's own uid.** Its socket is owned by that uid and no other uid can connect to it. A **shared driver runs under its own uid**, and gives each caller a socket of its own (see below).

For example, the AWS driver for `signer@prod` is a unit that runs as the `sluis-signer-prod` user (the paths are illustrative):

```text
/run/sluis/signer/prod.sock        actor's own API, reachable by its callers
/run/sluis/signer/prod/aws.sock    the signer's AWS driver, mode 0600, owned by the signer's uid
```

Another actor cannot open `aws.sock`, and AWS does the rest: the KMS key policy names the signer's role and no other.

## The mediating driver

A YubiKey or another card on a node has one PIN and cannot refuse a caller by itself. **`key-holder`** owns it, and every other process goes through it (agreed, YK-1):

- **One `key-holder` process, one Unix socket per caller.** Each socket sits in a directory that only that caller can open: systemd `.socket` units with `SocketUser=` and `FileDescriptorName=` on a host, and only that one socket mounted into a container.
- **A peer-uid check as a second factor.** `key-holder` reads the caller's uid from the connection and checks it against the socket it arrived on and an access list of caller actor × key purpose × operation, generated from the manifests.
- **Content-aware signing per purpose.** The `ca` purpose signs only a parseable TBSCertificate within its constraints; the `ssh-ca` purposes sign only an OpenSSH certificate body; the token purposes sign only a JWS signing input. A caller that has a socket still cannot get arbitrary bytes signed.
- **Hardened.** `key-holder` is written in Rust, has no network, runs under seccomp, keeps the PIN in mlocked memory (unlocked by a peer or by `sluis node unlock`), rate-limits signatures and audits each one. The access list lets only the audit `writer` use the log key, which is what replaces a separate notary.

More cards per node are an option for later. The OpenPGP cards of the roots are used directly by the ceremony tool, with no mediator ([Keys](/concepts/keys/#openpgp-release-signing)).

**There is no `state-owner`.** rqlite is light enough to run **one cluster per (boundary, owning actor)** (measured at about 7 to 9 MB idle and 16 to 26 MB loaded per node, plus about 30 MB shared per host). The owning actor gets an rqlite user with `execute`, and each declared reader a user with `query` only. Each cluster runs as a systemd template instance, `sluis-rqlite@<boundary>-<actor>`, with its own port from a port scheme and its own certificate, mutual TLS between nodes and to clients, and a sandbox that lets it write its data directory only (which also contains SQLite's `ATTACH`-creates-files hole). The footprint is measured again on the boards.

**How an actor reaches its own cluster** (decided 2026-10-30). rqlite's client API is a TCP port, so the unit of a `per-actor` rqlite driver does not run with `PrivateNetwork=yes`. It gets a narrow rule instead: `IPAddressDeny=any`, `IPAddressAllow=` for its own cluster's addresses only, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, plus a client certificate per cluster. Actors that need no network keep `PrivateNetwork=yes`. If rqlite adds a Unix-socket listener (proposed upstream, not merged), the driver moves to it ([ADR 0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/#how-a-per-actor-rqlite-driver-reaches-its-cluster)).

## Defence in depth

Every port is protected in **three layers**, and all three are generated from one manifest (agreed, DD-1). The record is [ADR 0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/). The threats these layers answer (T-8 to T-14 and T-24 among them), the attacker positions and what is left after each control are on the [threat model](/security/threat-model/), and each control's test vectors are in the [isolation requirements](/requirements/iso/).

| Layer | Where | What it does |
|---|---|---|
| **1. The port library in the actor** | Linked into every actor | Scopes every call to `<boundary>/<kind>/…`, taken from the actor's own identity and never from the request. Encrypts every state value. Names keys by purpose. Writes blobs create-if-absent |
| **2. The driver's or backend's own check** | The backend | Checks the caller's platform identity: the peer uid, the IAM role, the ServiceAccount token or the client certificate |
| **3. Physical separation** | The installation | At [L3 and L4](/runtimes/boundaries/#the-isolation-ladder): a store, a bucket, a queue, an account or hosts of the boundary's own |

- **Every state value is encrypted in the port library** (EC-1), with AES-256-GCM, under a data key per (boundary, record kind); see [Data](/concepts/data/#every-value-is-encrypted).
- **One generator** writes the library scopes, the access lists, the IAM and KMS policy documents (as output for the infrastructure code), the units, the sockets and the ServiceAccounts, and a **drift test** compares them. `sluis config check` refuses an installation whose parts disagree.
- **Isolation is tested per layer, with the other layers switched off**, so that no layer passes only because another one caught the case.
- **A driver whose backend cannot check each caller** is allowed at L3 and above only together with its layer-3 separation, and `sluis config check` enforces it.

| Port | Driver | Layer 2 | Layer 3 and notes |
|---|---|---|---|
| keys | software | `key-holder` | Refused in production |
| keys | yubikey-piv | `key-holder` (one socket per caller) | |
| keys | aws-kms | Key policy plus an encryption-context condition | |
| keys | pkcs11, YubiHSM (planned) | `key-holder` plus the HSM's authentication keys and domains | |
| keys | pkcs11, Nitrokey (planned) | `key-holder` plus the PIN | |
| state | embedded | A file per uid, per actor | |
| state | rqlite | A cluster per owning actor, plus rqlite users | |
| state | dynamodb | IAM `dynamodb:LeadingKeys` per actor role | A table per boundary; at L4 in the boundary's account |
| state | postgres | Roles, grants and row-level security (below) | |
| lease | all | On the state store, independent of the Raft leader | |
| blob | fs | A directory per uid, mode 0700 | |
| blob | s3-compatible | IAM prefix conditions on AWS; R2 temporary credentials scoped to a prefix, from `cloud-cloudflare` | A bucket per boundary; Object Lock on S3 |
| blob | s3-compatible on Garage | A key per bucket, per (boundary, actor), with an expiry, from `cloud-garage` | A bucket per actor; a Garage cluster per boundary; lock mode `none` |
| transport | direct-unix | The peer uid at the receiver | |
| transport | direct-mtls | The SPIFFE ID in the client certificate | |
| transport | spool | A directory per producer uid | |
| transport | sqs | A queue policy per sender role | A queue per boundary |
| transport | nats-jetstream | NATS accounts plus permission templates | See [Audit](/concepts/audit/#the-transport) |
| index | sqlite | A file per indexer uid; the reader opens it read-only | |
| index | rqlite | As for state | |
| index | postgres | As for state | |
| witness | peer | The node identity, over mutual TLS | |
| witness | external | The witness's own key | |
| secrets | sealed-state | On the state and keys ports | |
| export | age | Encrypted to the recipient's key | |
| cloud | web identity | `sluis-node` tokens per uid; the trust policy pins `sub` to `<actor>@<boundary>` | |
| cloud | platform role | The platform's identity | |
| cloud | gcp / azure | The cloud's federation | |
| cloud | cloudflare | R2 temporary credentials, scoped to a bucket and a prefix | |
| cloud | garage | A scoped admin token that creates a key per (boundary, actor) with an expiry | A Garage cluster per boundary |

### Postgres drivers

The `postgres` state and index drivers follow one model (agreed, PG-1):

- a fixed schema per **actor kind**, with grants between kinds;
- **row-level security between instances of the same kind** (boundaries and tenants), by group-role membership, `pg_has_role(current_user, 'boundary_' || boundary, 'member')`, with `FORCE ROW LEVEL SECURITY`;
- actors never own tables: a migration role owns them;
- policies never trust session variables; views are `security_invoker`, and nothing is `SECURITY DEFINER`;
- the tenant column is part of every unique key;
- a connection pool per role;
- **PostgreSQL 15 or newer**;
- at L3 and above, a database per boundary, with row-level security still on;
- client certificates from `ca` (the common name is the role), or IAM authentication on RDS and Aurora.

## What each driver requires

Every driver declares what it needs from the installation. The columns are: whether it needs a long-running process, whether it needs a persistent local disk, how many nodes it works with, and which dependency it needs from outside sluis. A dash means the driver does not need it.

| Driver | Port | Long-running | Disk | Nodes | Dependency |
|---|---|---|---|---|---|
| software | keys | – | – | any | – (refused in production: code floor) |
| yubikey-piv | keys | ✓ | – | any | a YubiKey on each node |
| aws-kms | keys | – | – | any | AWS |
| embedded | state | ✓ | ✓ | exactly 1 | – |
| rqlite | state, lease, index (index proposed) | ✓ | ✓ | 1, 3 or 5 (3+ to survive a failure) | – (one cluster per boundary and owning actor) |
| dynamodb | state, lease | – | – | any | AWS |
| local lock | lease | ✓ | – | exactly 1 | – |
| fs | blob | – | ✓ | exactly 1 | – (no lock: tamper evidence only with witnesses) |
| s3-compatible | blob | – | – | any | An S3 API: AWS S3 (Object Lock, compliance mode) or Cloudflare R2 (lock removable by an account admin). Credentials come from the `cloud` port |
| direct-unix | transport | ✓ receiver | – | same host | – |
| direct-mtls | transport | ✓ receiver | – | any | – |
| spool | transport | ✓ | ✓ | any | – |
| sqs | transport | – | – | any | AWS |
| nats-jetstream | transport | ✓ | – | any | a NATS service (3 nodes to replicate) |
| sqlite | index | ✓ | ✓ | one writer | – |
| postgres | index | – | – | any | a Postgres service, by provisioning mode: `rds` or `aurora` on AWS, `cnpg` on Kubernetes, or `external` (a connection string only). See [The postgres index driver](#the-postgres-index-driver) |
| none (scan) | index | – | – | any | – |
| none | witness | – | – | any | – |
| peer | witness | ✓ | – | 2 or more | – |
| external | witness | – | – | any | an external witness |
| sealed-state (proposed) | secrets | – | – | any | a `state` driver and a `keys` driver that can wrap |
| age drop, file or bucket (proposed) | export | – | file: ✓ | any | the consumer's age recipient (a public key) |
| web identity | cloud | – | – | any | the node agent and a public issuer URL |
| platform role | cloud | – | – | any | a cloud VM, function or pod identity |
| gcp / azure federation | cloud | – | – | any | GCP or Azure |
| cloudflare | cloud | – | – | any | A Cloudflare account, and a parent token per boundary, stored sealed |

Neither the nodes nor the dependencies belong to a [runtime](/runtimes/). The number of nodes is an attribute of an installation, and a provider is a dependency that a driver declares, such as an AWS account, a Cloudflare account, a YubiKey or a NATS service. The one exception is serverless: it always comes with a provider (AWS Lambda today), so there the provider is part of the runtime.

:::note[Proposed]
The `rqlite` index driver is proposed: rqlite is a replicated SQLite, so with it the reader can run on any node of a cluster. Its full-text search (FTS5) was tested on 2026-10-30 and works on a three-node cluster. The `secrets` and `export` drivers are proposed too. The witness and transport drivers follow the decisions in [Audit](/concepts/audit/): the installation defaults are the spool on bare-metal and SQS on serverless and Kubernetes ([OD-34](/open-decisions/#od-34)), and a witness is optional ([OD-36](/open-decisions/#od-36)).
:::

## Which drivers fit which runtime

This table is **derived**: it follows from the requirements above and from what each [runtime](/runtimes/) provides. A long-running driver is impossible on serverless, a driver that needs a disk is possible on Kubernetes only with a per-pod volume, and so on.

Legend: ✓ possible, ~ possible with the condition in the footnote, ✗ impossible.

| Port | Driver | Bare metal, 1 node | Bare metal, cluster | Serverless | Kubernetes |
|---|---|---|---|---|---|
| keys | software | ✓ | ✓ | ✓ | ✓ |
| | yubikey-piv | ✓ | ✓ | ✗ | ~¹ |
| | aws-kms | ✓ | ✓ | ✓ | ✓ |
| state | embedded | ✓ | ✗ | ✗ | ~² |
| | rqlite | ✓ | ✓ | ✗ | ✓ |
| | dynamodb | ✓ | ✓ | ✓ | ✓ |
| lease | local lock | ✓ | ✗ | ✗ | ✗ |
| | rqlite | ✓ | ✓ | ✗ | ✓ |
| | dynamodb | ✓ | ✓ | ✓ | ✓ |
| blob | fs | ~³ | ~³ | ✗ | ~² ³ |
| | s3-compatible | ✓ | ✓ | ✓ | ✓ |
| transport | direct-unix | ✓ | ✓ | ✗ | ~⁴ |
| | direct-mtls | ✓ | ✓ | ✗⁵ | ✓ |
| | spool | ✓ | ✓ | ✗ | ~² |
| | sqs | ✓ | ✓ | ✓ | ✓ |
| | nats-jetstream | ✓ | ✓ | ~⁵ | ✓ |
| index | sqlite | ✓ | ~⁶ | ✗ | ~² |
| | rqlite (proposed) | ✓ | ✓ | ✗ | ✓ |
| | postgres | ✓ | ✓ | ~⁷ | ✓ |
| | none (scan) | ✓ | ✓ | ✓ | ✓ |
| witness | none | ✓ | ✓ | ✓ | ✓ |
| | peer | ✗ | ✓ | ✗ | ~⁸ |
| | external | ✓ | ✓ | ✓ | ✓ |
| secrets (proposed) | sealed-state | ✓ | ✓ | ✓ | ✓ |
| export (proposed) | file drop | ✓ | ✓ | ✗ | ~² |
| | bucket drop | ✓ | ✓ | ✓ | ✓ |
| cloud | web identity | ✓ | ✓ | ~⁹ | ✓ |
| | platform role | ~¹⁰ | ~¹⁰ | ✓ | ✓ |
| | gcp / azure | ✓ | ✓ | ~⁹ | ✓ |
| | cloudflare | ✓ | ✓ | ✓ | ✓ |

1. USB passthrough, with the pod pinned to its node.
2. Needs a persistent volume (a StatefulSet).
3. No lock: tamper evidence only with witnesses.
4. The writer runs in the same pod.
5. `direct-mtls` falls back to a local spool when the writer is down, so it needs a disk, which serverless does not have ([OD-35](/open-decisions/#od-35)). A function sends through a queue. For `nats-jetstream`, producers can send from serverless, and the writer runs outside it.
6. On one node only.
7. Allowed: serverless may run the indexer and the reader with a `postgres` index ([OD-12](/open-decisions/#od-12), revised). On AWS the functions reach it through RDS Proxy (proposed), and they need network access to the database.
8. Not independent within the same cluster.
9. There is no node agent, so the JWT has to come from the signer.
10. Only on a cloud VM.

The lease rows are the state stores: a lease lives on the state store, with compare-and-set and a fencing token ([Data](/concepts/data/#leases-and-fencing-tokens)). A Kubernetes Lease driver is in the backlog, so the kubernetes runtime takes its lease from DynamoDB like its state.

## The postgres index driver

The `index` port has one Postgres driver, `postgres`, and the driver has a **provisioning mode**. The mode says who creates the database; it does not change what the indexer and the reader do.

| Mode | Where | What sluis does |
|---|---|---|
| `rds` or `aurora` | AWS | Provisions the database. On [serverless](/runtimes/serverless/) the functions reach it through RDS Proxy (proposed), so that short-lived functions do not exhaust its connections |
| `cnpg` | [Kubernetes](/runtimes/kubernetes/) | Provisions a CloudNativePG (CNPG) cluster in the same cluster |
| `external` | Anywhere | Nothing. It takes a **connection string only** and provisions nothing |

`external` covers a database that someone else runs: a CNPG cluster in another Kubernetes cluster behind a load balancer, or a Postgres run on bare metal. Exposing CNPG through a load balancer is **not a driver of its own**; from sluis's side it is `external`.

Serverless may run the indexer and the reader with a `postgres` index ([OD-12](/open-decisions/#od-12), revised). The default index of the lambda installation stays `none`; that default is a proposal.

## The rqlite index driver (proposed)

:::note[Proposed]
The `index` port gains a second replicated driver, `rqlite`. rqlite is a replicated SQLite, and an installation that already runs it for `state` has the index on the same cluster with nothing more to run. Because every node holds a replica, the reader can run on **any node**, which a `sqlite` file on one node cannot offer. It is the proposed index of the [boards](/runtimes/installations/#boards) installation.

Its full-text search works: FTS5 was tested on 2026-10-30 with rqlite 10.6 on a three-node cluster, built with cgo and FTS5 and JSON1 compiled in (the arm64 build uses the same flags). The findings for the driver: followers serve reads only at the `none` level, so a reader uses a small freshness bound, and `weak` to read its own write; writes are batched; the search table is an external-content FTS table. Native arm64 hardware and the cost of fsync on eMMC are not yet tested. If either fails on the boards, they fall back to `sqlite` on the indexer's node, with the reader beside it.
:::

## One S3-compatible blob driver

There is **one blob driver for object stores, `s3-compatible`**, and the separate `r2` driver is removed (agreed, CF-1). The driver speaks the S3 API, and takes its credentials from the **`cloud` port**:

- on AWS, from STS through web identity or the platform role, scoped by IAM prefix conditions;
- on Cloudflare R2, from the **`cloudflare` cloud driver**, which runs as `cloud-cloudflare@<boundary>` and mints R2 **temporary credentials** scoped to the bucket and the prefix. They are minted locally, as an HS256 token signed with the parent token's secret. The parent token is per boundary and is stored sealed.

Long-lived R2 tokens can be scoped to a bucket only; a prefix scope exists only on temporary credentials, and R2 has no OIDC federation (checked 2026-09-26). The `cloudflare` cloud driver is separate from the company integration `cloudflare@<company>`, which mints Cloudflare tokens for people and jobs ([Cloudflare](/integrations/cloudflare/)); the two share a library, and both are written in Go ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)).

### Garage

[Garage](https://garagehq.deuxfleurs.fr/) is a **supported backend of the same `s3-compatible` driver** (decided 2026-10-30), and the primary blob store of the [boards](/runtimes/installations/#boards), which run it themselves. Per its documentation (read 2026-10-30) Garage has no Object Lock, versioning, bucket policies or ACLs; its permissions are per key and per bucket, with no prefixes; and its admin API v2 has scoped admin tokens. So:

- the **`garage` cloud driver**, `cloud-garage@<boundary>`, holds a scoped admin token and creates a **key per (boundary, actor) with an expiry**;
- each actor has **a bucket of its own**, because a key's rights are per bucket, and each boundary has its own Garage cluster;
- the lock mode is **`none`**, so the audit log's tamper evidence comes from the peer witnesses;
- on the boards the audit log has an **off-site copy on R2**.

A spike comes first: conditional writes (`If-None-Match: *`), Garage's memory on the boards, and key expiry. If conditional writes are missing, the writer relies on its per-node log plus a lease ([ADR 0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).

## Capability flags

Two ports carry flags that an actor can require, so that "any driver for this port" is not always enough.

**Blob lock mode.** A blob driver declares how strongly it can lock an object once written: `none`, `removable` or `compliance`.

| Driver | Lock mode | What it means |
|---|---|---|
| `fs` | `none` | Nothing stops a local administrator from changing a file. The audit trail is tamper-evident only if witnesses co-sign its heads. |
| `s3-compatible` on Garage | `none` | Garage has no object lock and no versioning. The audit trail is tamper-evident through the witnesses. |
| `s3-compatible` on Cloudflare R2 | `removable` | An object lock exists, but an account administrator can remove it. |
| `s3-compatible` on AWS S3 | `compliance` | Object Lock in compliance mode: not even the account's root can shorten the retention. |

An actor that stores a record which must not be changed, such as the audit log store, requires a lock mode. The installation's driver must meet it, or the installation states that it accepts the weaker mode and relies on witnesses. The audit log store is a `blob` with a lock mode; see [Audit](/concepts/audit/).

**State `cas`.** The `state` port has compare-and-set. An actor that changes a record that more than one caller can reach requires it. Quota state is `state` with compare-and-set. Quota meters by default and enforces per policy rule ([OD-33](/open-decisions/#od-33), decided); enforcement needs `state` with compare-and-set on the mint path. The contract has no unconditional put: see [Data](/concepts/data/).

## How `sluis config check` validates an installation

An **installation** is one concrete deployment: a [runtime](/runtimes/), a number of nodes, the dependencies that are available, and one driver per port. For example, "bare-metal, 3 nodes, an AWS account". Four installations are tested: [local, boards, lambda and kubernetes](/runtimes/installations/). Any other combination that the rules allow is possible, and untested.

`sluis config check` is the check that an installation can work. This is the design; the command itself is agreed. It reads the installation and, for each actor in the installation, follows the ports it requires to the driver chosen for that port, and refuses a combination that cannot work:

- **A port with no driver.** Every port that an actor requires must have a driver.
- **The runtime cannot run the driver.** A driver that needs a long-running process is refused on serverless; a driver that needs a disk is refused where there is no persistent disk. This is the derived table above, applied to the installation.
- **The node count does not fit.** `embedded` with three nodes, or `rqlite` with two, is refused.
- **A dependency is missing.** `aws-kms` without an AWS account, `yubikey-piv` on a node with no YubiKey, `nats-jetstream` without a NATS service.
- **A capability flag is too weak.** A port flag that an actor requires, such as a blob lock mode, that the chosen driver cannot give.
- **An actor with instances: exactly 1 and no way to keep it so.** When nodes are more than one or elastic, the installation needs the lease port.
- **The code floor.** A software key in a production installation.
- **A direct transport with no disk.** `direct-unix` and `direct-mtls` fall back to a local spool when the writer is down, so they are refused on serverless.
- **Generated parts that disagree.** The units, sockets, access lists and policy documents are generated from one manifest, and a drift test compares them with what is installed ([Defence in depth](#defence-in-depth)).
- **A driver with no per-caller check, without its separation.** At L3 and above such a driver needs its layer-3 separation, a store or bucket of the boundary's own.
- **A warning, not an error: no witness.** A witness is optional, so an installation without one passes the check with a warning that names the risk (see [Audit](/concepts/audit/#the-witness-is-optional)).

Each refusal names the actor, the port, the driver and the thing that is missing, so that an operator can fix the file. For example (illustrative):

```text
$ sluis config check
error  ports.keys: driver "yubikey-piv" needs a long-running process; runtime "serverless" has none
error  ports.state: driver "embedded" needs exactly 1 node; the installation has 3
error  ports.blob: audit log store needs lock mode "removable" or stronger; driver "fs" gives "none"
```

## Where an actor runs is derived

Where an actor runs is **derived from the port and driver rules**. This is agreed ([OD-32](/open-decisions/#od-32)), and it holds for Search (the indexer and the reader) and for every sluis actor. There are no per-installation placement decisions: `sluis config check` enforces the rules, and each [tested installation](/runtimes/installations/) only names its default drivers.

The rules come from the requirements table above. For example:

- An actor that declares **instances: exactly 1**, such as the indexer, runs on one node, and needs the lease port when there is more than one node or the nodes are elastic.
- With a `sqlite` index on a cluster there is one writer, so the reader runs on the indexer's node. With a `postgres` index the reader can run on any node.
- A driver that needs a long-running process or a disk puts its actor where such a process or disk exists. It is refused on serverless.
- An actor that requires a port reaches the driver of that port over its socket, so the two share a node, a pod or a function.

The check is the only place where driver and runtime meet, which is why a driver's requirements are declared once, in a table, and not in each installation.

## Ports and drivers by installation

The same actors run on every runtime. The drivers behind their ports are what changes. The table shows the drivers of the four [tested installations](/runtimes/installations/). Any other combination that the rules allow is a possible installation, and it is not tested.

| Port | [local](/runtimes/installations/#local) | [boards](/runtimes/installations/#boards) | [lambda](/runtimes/installations/#lambda) | [kubernetes](/runtimes/installations/#kubernetes) |
|---|---|---|---|---|
| keys | software, or the host's YubiKey | yubikey-piv per node, through `key-holder` | aws-kms, per-actor | aws-kms ([OD-20](/open-decisions/#od-20)) |
| state | embedded | rqlite, one cluster per owning actor | dynamodb | dynamodb |
| lease | local lock | rqlite, on the state store | dynamodb, on the state store | dynamodb, on the state store |
| blob | fs | s3-compatible on Garage, with keys from the `garage` cloud driver; an off-site R2 copy of the audit log | s3-compatible on S3 | s3-compatible on S3 |
| transport | spool | spool | sqs | sqs ([OD-22](/open-decisions/#od-22)) |
| index | sqlite | rqlite (proposed) | none (proposed default); postgres through RDS Proxy is allowed ([OD-12](/open-decisions/#od-12)) | postgres on cnpg (proposed) |
| witness | none | peer, each node witnessing the others | none | none |
| cloud | web identity (node agent), as configured | web identity; `cloudflare` for the bucket | platform role | web identity or pod identity ([OD-19](/open-decisions/#od-19)) |
| secrets (proposed) | a mounted file, or sealed-state | sealed-state | a parameter store | a parameter store, or a mounted file |

The keys, state and index choices follow the agreed installations. The **local** installation may use the host's YubiKey: the `key-holder` container uses the host's `pcscd` socket, which is mounted into it, and the pcsc-lite protocol versions of the image and the host must match. The fallback is to run the `key-holder` natively on the host, so that the containers mount only its Unix socket and the host keeps using the key.

On **boards** the log is per node: each node's writer, spool and log key are its own, and the other two nodes are its peer witnesses. See [Audit](/concepts/audit/#exactly-one-writer-per-log).

Each driver choice is the reading of an older decision. [OD-5](/open-decisions/#od-5) and [OD-34](/open-decisions/#od-34) put the spool on bare-metal installations and SQS on serverless, [OD-22](/open-decisions/#od-22) put SQS on Kubernetes, [OD-20](/open-decisions/#od-20) put KMS on Kubernetes, and [OD-12](/open-decisions/#od-12), as revised, lets serverless run the indexer and the reader with a `postgres` index while the lambda default stays `none`. The key mode follows the same pattern: KMS on AWS, a YubiKey on boards, a software key only in the local installation.

:::note[Proposed]
The index defaults of lambda (`none`) and kubernetes (`postgres` on `cnpg`) are proposals, and so is the `rqlite` index of boards. The secrets row is a proposal.
:::

The only inter-node traffic is the Raft traffic of the rqlite clusters, one port per cluster from the port scheme, over mutual TLS with a certificate per cluster. These are the only TCP ports between nodes.

## On-disk layout

Internal programs are not on `PATH`. The only command for humans is `/usr/bin/sluis`. Everything a platform starts lives under `/usr/libexec/sluis/`, in one directory per kind:

```text
/usr/bin/sluis                         the only command for humans
/usr/libexec/sluis/
  actors/                              entry, session, console, exchange, signer, policy, ca, ssh-ca, quota, writer, witness, indexer, reader
  drivers/                             one directory entry per driver
  integrations/                        one per integration
  node/                                sluis-node, and other node services
  vendor/                              third-party binaries, such as the state store
```

Short names are used because the directory is the namespace (Postfix does the same with `/usr/libexec/postfix/smtpd`), and because the kernel truncates a process name at 15 characters, so a long `sluis-<actor>` name would show cut off. Units, users and the CLI keep the `sluis-` prefix, so `ps`, the journal and `systemctl` still say sluis.

The directory is named `actors/`, to match the vocabulary (agreed; it amends the earlier package layout, which called it `roles/`).

## Why separate services

Compiling every driver into every actor would make fat binaries, noisy scanner results for code an actor never uses, and a rebuild to add a backend. An actor that spawned its own driver would need exec rights and would put supervision in our code. A single AWS driver shared by all actors would hold every actor's credentials and lose IAM's per-actor enforcement. Separate services cost a socket to protect, contract versioning across images and some memory, and one Unix-socket hop is a fraction of a millisecond against the several milliseconds a KMS or DynamoDB call takes.

The AWS, GCP, Azure and Cloudflare drivers are written in Rust ([Source layout](/architecture/source-layout/#languages)). On serverless one Rust `aws` executable implements every AWS port for its function, as one extension.

Where the sockets live and which of them ever listen on TCP is on [Listeners](/architecture/listeners/).
