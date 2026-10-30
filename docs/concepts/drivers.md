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
| `lease` | `acquire`, `renew` | |
| `blob` | `put-once`, `get`, `list`, plus a lock mode | lock mode: `none`, `removable` or `compliance` |
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
| `per-actor` | The backend can tell callers apart: AWS IAM, an HSM with a user per actor, a database with a role per sluis actor | Each instance carries that one actor's own credential, and the backend enforces access |
| `shared` | The backend has one credential or one device: a YubiKey and its PIN, a TPM, software keys, the state store | One process holds the credential. It authenticates every caller by peer credentials (`SO_PEERCRED`) and checks an access list generated from the manifest |

### The uid rule

A **per-actor driver runs as the actor's own uid.** Its socket is owned by that uid and no other uid can connect to it. A **shared driver runs under its own uid**, and its socket accepts any caller that the access list then checks.

For example, the AWS driver for `signer` is a unit that runs as the `signer` user (the paths are illustrative):

```text
/run/sluis/signer/api.sock     actor's own API, group-readable by its clients
/run/sluis/signer/aws.sock     the signer's AWS driver, mode 0600, owned by the signer uid
```

Another actor cannot open `aws.sock`, and AWS does the rest: the KMS key policy names the signer's role and no other.

## The two mediating drivers

Some resources cannot refuse a caller by themselves. A YubiKey has one PIN; a state store has one login. For these, a **shared driver** owns the resource and every other process goes through it. There are two, and both audit every use.

| Mediating driver | Port | Fronts | What it enforces |
|---|---|---|---|
| `key-holder` | keys | The keys driver for HSMs, YubiKeys and software keys | An access list of caller actor × key purpose × operation, generated from the manifests. The device node is closed to everyone else, and keys are created non-extractable and sign-only. The access list lets only the audit `writer` use the log key, which is what replaces a separate notary. |
| `state-owner` | state | The replicated state store (rqlite on Raft) | An access list of caller actor × record kind × operation, with exactly one actor allowed to write each record kind. The store's own listener is closed to everyone else. See [Data](/concepts/data/). |

Both have no internet egress, load no plugins, and are started before their callers.

## What each driver requires

Every driver declares what it needs from the installation. The columns are: whether it needs a long-running process, whether it needs a persistent local disk, how many nodes it works with, and which dependency it needs from outside sluis. A dash means the driver does not need it.

| Driver | Port | Long-running | Disk | Nodes | Dependency |
|---|---|---|---|---|---|
| software | keys | – | – | any | – (refused in production: code floor) |
| yubikey-piv | keys | ✓ | – | any | a YubiKey on each node |
| aws-kms | keys | – | – | any | AWS |
| embedded | state | ✓ | ✓ | exactly 1 | – |
| rqlite | state, lease, index (index proposed) | ✓ | ✓ | 1, 3 or 5 (3+ to survive a failure) | – |
| dynamodb | state, lease | – | – | any | AWS |
| local lock | lease | ✓ | – | exactly 1 | – |
| Kubernetes Lease | lease | ✓ | – | any | the Kubernetes API |
| fs | blob | – | ✓ | exactly 1 | – (no lock: tamper evidence only with witnesses) |
| r2 | blob | – | – | any | Cloudflare (lock removable by an account admin) |
| s3 | blob | – | – | any | AWS (Object Lock, compliance mode) |
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

Neither the nodes nor the dependencies belong to a [runtime](/runtimes/). The number of nodes is an attribute of an installation, and a provider is a dependency that a driver declares, such as an AWS account, a Cloudflare account, a YubiKey or a NATS service. The one exception is serverless: it always comes with a provider (AWS Lambda today), so there the provider is part of the runtime.

:::note[Proposed]
The `rqlite` index driver is proposed: rqlite is a replicated SQLite, so with it the reader can run on any node of a cluster. Whether it supports the full-text search (FTS5) that the indexer needs has to be verified before it is relied on. The `secrets` and `export` drivers are proposed too. The witness and transport drivers follow the decisions in [Audit](/concepts/audit/): the installation defaults are the spool on bare-metal and SQS on serverless and Kubernetes ([OD-34](/open-decisions/#od-34)), and a witness is optional ([OD-36](/open-decisions/#od-36)).
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
| | Kubernetes Lease | ✗ | ✗ | ✗ | ✓ |
| blob | fs | ~³ | ~³ | ✗ | ~² ³ |
| | r2 | ✓ | ✓ | ✓ | ✓ |
| | s3 | ✓ | ✓ | ✓ | ✓ |
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

It is not yet known whether rqlite supports the full-text search (FTS5) that the indexer needs; that has to be verified before the driver is relied on. If it does not, boards fall back to `sqlite` on the indexer's node, with the reader beside it.
:::

## Capability flags

Two ports carry flags that an actor can require, so that "any driver for this port" is not always enough.

**Blob lock mode.** A blob driver declares how strongly it can lock an object once written: `none`, `removable` or `compliance`.

| Driver | Lock mode | What it means |
|---|---|---|
| `fs` | `none` | Nothing stops a local administrator from changing a file. The audit trail is tamper-evident only if witnesses co-sign its heads. |
| `r2` | `removable` | An object lock exists, but an account administrator can remove it. |
| `s3` | `compliance` | Object Lock in compliance mode: not even the account's root can shorten the retention. |

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
| keys | software, or the host's YubiKey | yubikey-piv per node, shared | aws-kms, per-actor | aws-kms ([OD-20](/open-decisions/#od-20)) |
| state | embedded | rqlite on Raft, behind `state-owner` | dynamodb | dynamodb |
| lease | local lock | rqlite (Raft leader lease) | dynamodb conditional write, or the platform's concurrency limit of 1 | Kubernetes Lease |
| blob | fs | r2 | s3 | s3 |
| transport | spool | spool | sqs | sqs ([OD-22](/open-decisions/#od-22)) |
| index | sqlite | rqlite (proposed) | none (proposed default); postgres through RDS Proxy is allowed ([OD-12](/open-decisions/#od-12)) | postgres on cnpg (proposed) |
| witness | none | peer, each node witnessing the others | none | none |
| cloud | web identity (node agent), as configured | web identity, as configured | platform role | web identity or pod identity ([OD-19](/open-decisions/#od-19)) |
| secrets (proposed) | a mounted file, or sealed-state | sealed-state | a parameter store | a parameter store, or a mounted file |

The keys, state and index choices follow the agreed installations. The **local** installation may use the host's YubiKey: the `key-holder` container uses the host's `pcscd` socket, which is mounted into it, and the pcsc-lite protocol versions of the image and the host must match. The fallback is to run the `key-holder` natively on the host, so that the containers mount only its Unix socket and the host keeps using the key.

On **boards** the log is per node: each node's writer, spool and log key are its own, and the other two nodes are its peer witnesses. See [Audit](/concepts/audit/#exactly-one-writer-per-log).

Each driver choice is the reading of an older decision. [OD-5](/open-decisions/#od-5) and [OD-34](/open-decisions/#od-34) put the spool on bare-metal installations and SQS on serverless, [OD-22](/open-decisions/#od-22) put SQS on Kubernetes, [OD-20](/open-decisions/#od-20) put KMS on Kubernetes, and [OD-12](/open-decisions/#od-12), as revised, lets serverless run the indexer and the reader with a `postgres` index while the lambda default stays `none`. The key mode follows the same pattern: KMS on AWS, a YubiKey on boards, a software key only in the local installation.

:::note[Proposed]
The index defaults of lambda (`none`) and kubernetes (`postgres` on `cnpg`) are proposals, and so is the `rqlite` index of boards. The secrets row is a proposal.
:::

The only inter-node traffic is the state driver's own Raft port, over mutual TLS. It is the only TCP port between nodes.

## On-disk layout

Internal programs are not on `PATH`. The only command for humans is `/usr/bin/sluis`. Everything a platform starts lives under `/usr/libexec/sluis/`, in one directory per kind:

```text
/usr/bin/sluis                         the only command for humans
/usr/libexec/sluis/
  actors/                              entry, signer, policy, ca, quota, writer, witness, indexer, reader
  drivers/                             one directory entry per driver
  integrations/                        one per integration
  node/                                sluis-node, and other node services
  vendor/                              third-party binaries, such as the state store
```

Short names are used because the directory is the namespace (Postfix does the same with `/usr/libexec/postfix/smtpd`), and because the kernel truncates a process name at 15 characters, so a long `sluis-<actor>` name would show cut off. Units, users and the CLI keep the `sluis-` prefix, so `ps`, the journal and `systemctl` still say sluis.

The directory is named `actors/`, to match the vocabulary (agreed; it amends the earlier package layout, which called it `roles/`).

## Why separate services

Compiling every driver into every actor would make fat binaries, noisy scanner results for code an actor never uses, and a rebuild to add a backend. An actor that spawned its own driver would need exec rights and would put supervision in our code. A single AWS driver shared by all actors would hold every actor's credentials and lose IAM's per-actor enforcement. Separate services cost a socket to protect, contract versioning across images and some memory, and one Unix-socket hop is a fraction of a millisecond against the several milliseconds a KMS or DynamoDB call takes.

The aws, gcp and azure drivers are written in Rust.

Where the sockets live and which of them ever listen on TCP is on [Listeners](/architecture/listeners/).
