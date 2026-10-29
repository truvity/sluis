---
title: Ports and drivers
description: "Ports and drivers: port interfaces, what each driver requires, which drivers fit which environment, and how sluis config check validates it."
status: target
sidebar:
  order: 2
---

An [actor](/concepts/actors/) needs things from outside itself: keys to sign with, a place to keep records, a place to put audit archives. It asks for each through a **port**. A port is an abstract interface, with capability flags where the technologies behind it differ. The technology that answers it is a **driver**, and a driver declares what it requires: whether it needs a long-running process, a local disk, a number of nodes, or a **dependency** such as an AWS account.

The ports are **keys**, **state**, **lease**, **blob**, **transport**, **index** and **cloud**, plus **witness**, the only port that audit adds. Audit shares the other port definitions with sluis; see [Audit](/concepts/audit/).

An actor sees one contract per port, whatever sits behind it. `signer` calls "sign with the token key" on the keys port; whether that lands on AWS KMS, on a YubiKey or on a software key is installation configuration, not actor code.

## Port interfaces

| Port | Interface | Capability flags |
|---|---|---|
| `keys` | `sign`, `public-key` | |
| `state` | `get`, `put`, `cas` | `cas`: compare-and-set, needed wherever a record may be changed by more than one caller |
| `lease` | `acquire`, `renew` | |
| `blob` | `put-once`, `get`, `list`, plus a lock mode | lock mode: `none`, `removable` or `compliance` |
| `transport` | `put` / `receive` / `ack`, or `stream` | |
| `index` | `write`, `query` | |
| `witness` | `cosign` | |
| `cloud` | `credentials(cloud, role)` | |

The `transport` port replaces what earlier drafts called the queue port and the outbox: it is how an emitter hands an audit record to the writer, either directly (`stream`) or through a queue (`put` / `receive` / `ack`). See [Audit](/concepts/audit/).

## Drivers are services, and nobody execs them

A driver is a platform-started service that speaks ConnectRPC over a Unix socket. **No actor starts a driver.** The platform does:

| Environment | The platform starts a driver as |
|---|---|
| Bare-metal | A systemd service (and socket); under local Docker, a container runtime service |
| Serverless | A Lambda extension |
| Kubernetes (proposed) | A native sidecar container in the same pod as the actor |

:::note[Proposed]
On Kubernetes a driver is never a pod of its own, because a Unix socket cannot cross a pod boundary. It is a native sidecar, which the platform starts before the actors that call it. This replaces "a sidecar container, or a pod of its own". See [Kubernetes](/environments/kubernetes/#what-runs-where).
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
| `key-holder` | keys | The keys driver for HSMs, YubiKeys and software keys | An access list of caller actor × key purpose × operation, generated from the manifests. The device node is closed to everyone else, and keys are created non-extractable and sign-only. |
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
| rqlite | state, lease | ✓ | ✓ | 1, 3 or 5 (3+ to survive a failure) | – |
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
| postgres | index | – | – | any | a Postgres service: self-run; on AWS: RDS or Aurora; on Kubernetes: CloudNativePG (CNPG) |
| none (scan) | index | – | – | any | – |
| none | witness | – | – | any | – |
| peer | witness | ✓ | – | 2 or more | – |
| external | witness | – | – | any | an external witness |
| web identity | cloud | – | – | any | the node agent and a public issuer URL |
| platform role | cloud | – | – | any | a cloud VM, function or pod identity |
| gcp / azure federation | cloud | – | – | any | GCP or Azure |

Neither the nodes nor the dependencies belong to an [environment](/environments/). The number of nodes is an attribute of an installation, and a provider is a dependency that a driver declares, such as an AWS account, a Cloudflare account, a YubiKey or a NATS service. The one exception is serverless: it always comes with a provider (AWS Lambda today), so there the provider is part of the environment.

:::note[Proposed]
The witness port, the transport drivers other than `spool` and `sqs`, and the `index` and `witness` drivers belong to the audit redesign. Their per-preset choices are open: [OD-34](/open-decisions/#od-34) (transport per preset), [OD-36](/open-decisions/#od-36) (whether a witness is optional or required) and [OD-37](/open-decisions/#od-37) (audit's own driver instances).
:::

## Which drivers fit which environment

This table is **derived**: it follows from the requirements above and from what each [environment](/environments/) provides. A long-running driver is impossible on serverless, a driver that needs a disk is possible on Kubernetes only with a per-pod volume, and so on.

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
| | direct-mtls | ✓ | ✓ | ~⁵ | ✓ |
| | spool | ✓ | ✓ | ✗ | ~² |
| | sqs | ✓ | ✓ | ✓ | ✓ |
| | nats-jetstream | ✓ | ✓ | ~⁵ | ✓ |
| index | sqlite | ✓ | ~⁶ | ✗ | ~² |
| | postgres | ✓ | ✓ | ~⁷ | ✓ |
| | none (scan) | ✓ | ✓ | ✓ | ✓ |
| witness | none | ✓ | ✓ | ✓ | ✓ |
| | peer | ✗ | ✓ | ✗ | ~⁸ |
| | external | ✓ | ✓ | ✓ | ✓ |
| cloud | web identity | ✓ | ✓ | ~⁹ | ✓ |
| | platform role | ~¹⁰ | ~¹⁰ | ✓ | ✓ |
| | gcp / azure | ✓ | ✓ | ~⁹ | ✓ |

1. USB passthrough, with the pod pinned to its node.
2. Needs a persistent volume (a StatefulSet).
3. No lock: tamper evidence only with witnesses.
4. The writer runs in the same pod.
5. Producers can only send from serverless; the writer runs outside it.
6. On one node only.
7. Only in a Search install outside the functions.
8. Not independent within the same cluster.
9. There is no node agent, so the JWT has to come from the signer.
10. Only on a cloud VM.

## Capability flags

Two ports carry flags that an actor can require, so that "any driver for this port" is not always enough.

**Blob lock mode.** A blob driver declares how strongly it can lock an object once written: `none`, `removable` or `compliance`.

| Driver | Lock mode | What it means |
|---|---|---|
| `fs` | `none` | Nothing stops a local administrator from changing a file. The audit trail is tamper-evident only if witnesses co-sign its heads. |
| `r2` | `removable` | An object lock exists, but an account administrator can remove it. |
| `s3` | `compliance` | Object Lock in compliance mode: not even the account's root can shorten the retention. |

An actor that stores a record which must not be changed, such as the audit log store, requires a lock mode. The installation's driver must meet it, or the installation states that it accepts the weaker mode and relies on witnesses. The audit log store is a `blob` with a lock mode; see [Audit](/concepts/audit/).

**State `cas`.** The `state` port has compare-and-set. An actor that changes a record that more than one caller can reach requires it. Quota state is `state` with compare-and-set, and quota needs live state to enforce a cap ([OD-33](/open-decisions/#od-33) asks whether quota enforces or only meters). The contract has no unconditional put: see [Data](/concepts/data/).

## How `sluis config check` validates an installation

An **installation** is one concrete deployment: an [environment](/environments/), a number of nodes, the dependencies that are available, and one driver per port. For example, "bare-metal, 3 nodes, an AWS account". A named, tested installation is a [preset](/environments/presets/).

`sluis config check` is the check that an installation can work. This is the design; the command itself is agreed. It reads the installation and, for each actor in the installation, follows the ports it requires to the driver chosen for that port, and refuses a combination that cannot work:

- **A port with no driver.** Every port that an actor requires must have a driver.
- **The environment cannot run the driver.** A driver that needs a long-running process is refused on serverless; a driver that needs a disk is refused where there is no persistent disk. This is the derived table above, applied to the installation.
- **The node count does not fit.** `embedded` with three nodes, or `rqlite` with two, is refused.
- **A dependency is missing.** `aws-kms` without an AWS account, `yubikey-piv` on a node with no YubiKey, `nats-jetstream` without a NATS service.
- **A capability flag is too weak.** A port flag that an actor requires, such as a blob lock mode, that the chosen driver cannot give.
- **An actor with instances: exactly 1 and no way to keep it so.** When nodes are more than one or elastic, the installation needs the lease port.
- **The code floor.** A software key in a production installation.

Each refusal names the actor, the port, the driver and the thing that is missing, so that an operator can fix the file. For example (illustrative):

```text
$ sluis config check
error  ports.keys: driver "yubikey-piv" needs a long-running process; environment "serverless" has none
error  ports.state: driver "embedded" needs exactly 1 node; the installation has 3
error  ports.blob: audit log store needs lock mode "removable" or stronger; driver "fs" gives "none"
```

The check is the only place where driver and environment meet, which is why a driver's requirements are declared once, in a table, and not in each preset.

## Ports and drivers by preset

The same actors run in every environment. The drivers behind their ports are what changes. The table shows the drivers of each [preset](/environments/presets/).

| Port | T0 Local | T1 Single board | T2 Board cluster | T3 EC2 single | T4 EC2 cluster | T5 AWS Lambda | T6 Kubernetes (EKS) |
|---|---|---|---|---|---|---|---|
| keys | software (or the host's YubiKey) | yubikey-piv, shared | yubikey-piv per node, shared | aws-kms, per-actor | aws-kms, a key per node, per-actor | aws-kms, per-actor | aws-kms (OD-20) |
| state | embedded | embedded | rqlite on Raft, behind `state-owner` | dynamodb | rqlite on Raft, behind `state-owner` | dynamodb | dynamodb |
| lease | local lock | local lock | rqlite (Raft leader lease) | dynamodb conditional write | rqlite (Raft leader lease) | dynamodb conditional write, or the platform's concurrency limit of 1 | Kubernetes Lease |
| blob | fs | r2 | r2 | s3 | s3 | s3 | s3 |
| transport | spool | spool | spool | spool | spool | sqs | sqs (OD-22) |
| index | sqlite | sqlite | sqlite | sqlite | sqlite | none: a separate install holds it (OD-12 a) | postgres |
| witness | none | none | peer (proposed) | none | none | none | none |
| cloud | web identity (node agent); aws, gcp, azure as configured | web identity, as configured | web identity, as configured | web identity | web identity | platform role | web identity or pod identity (OD-19) |

The keys, state and index choices follow the agreed presets. T0 may use the host's YubiKey: the `key-holder` runs natively on the host and talks to the host's `pcscd`, and the container mounts only the `key-holder`'s Unix socket, so the host keeps using the key. The fallback is to mount the host's `pcscd` socket into the container, with the risk that the pcsc-lite protocol versions must match.

Each preset's driver choice is the reading of an older decision: [OD-5](/open-decisions/#od-5) put the spool on bare-metal presets and SQS on serverless, [OD-22](/open-decisions/#od-22) put SQS on Kubernetes, [OD-20](/open-decisions/#od-20) put KMS on Kubernetes, and [OD-12](/open-decisions/#od-12) (a) keeps the index out of serverless. The key mode follows the same pattern: KMS on AWS presets, a YubiKey on board presets, a software key only on T0.

:::note[Proposed]
The blob, transport, lease, witness and cloud rows are the working design and are not settled preset by preset. The transport per preset is [OD-34](/open-decisions/#od-34); where the search index runs is [OD-32](/open-decisions/#od-32).
:::

The only inter-node traffic is the state driver's own Raft port, over mutual TLS. It is the only TCP port between nodes.

## On-disk layout

Internal programs are not on `PATH`. The only command for humans is `/usr/bin/sluis`. Everything a platform starts lives under `/usr/libexec/sluis/`, in one directory per kind:

```text
/usr/bin/sluis                         the only command for humans
/usr/libexec/sluis/
  roles/                               entry, signer, policy, ca, notary, audit-writer, audit-read, quota
  drivers/                             one directory entry per driver
  integrations/                        one per integration
  node/                                sluis-node, and other node services
  vendor/                              third-party binaries, such as the state store
```

Short names are used because the directory is the namespace (Postfix does the same with `/usr/libexec/postfix/smtpd`), and because the kernel truncates a process name at 15 characters, so `sluis-audit-writer` would otherwise show as `sluis-audit-wri`. Units, users and the CLI keep the `sluis-` prefix, so `ps`, the journal and `systemctl` still say sluis.

The directory is still named `roles/`. The package layout is agreed with that name, and whether to rename it to `actors/` to match the vocabulary is an open follow-up.

## Why separate services

Compiling every driver into every actor would make fat binaries, noisy scanner results for code an actor never uses, and a rebuild to add a backend. An actor that spawned its own driver would need exec rights and would put supervision in our code. A single AWS driver shared by all actors would hold every actor's credentials and lose IAM's per-actor enforcement. Separate services cost a socket to protect, contract versioning across images and some memory, and one Unix-socket hop is a fraction of a millisecond against the several milliseconds a KMS or DynamoDB call takes.

The aws, gcp and azure drivers are written in Rust.

Where the sockets live and which of them ever listen on TCP is on [Listeners](/architecture/listeners/).
