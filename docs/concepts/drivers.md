---
title: Drivers
description: "Ports and drivers: platform-started services on Unix sockets that nobody execs, per-role and shared instances, the uid rule, shared drivers and the on-disk layout."
status: target
sidebar:
  order: 2
---

A role needs things from outside itself: keys to sign with, a place to keep records, a place to put audit archives. It asks for each through a **port**. A port is a contract. The technology that answers it is a **driver**.

The ports are **state**, **index**, **keys**, **blob**, **queue**, **lease** and **cloud**.

A role sees one contract per port, whatever sits behind it. `signer` calls "sign with the token key" on the keys port; whether that lands on AWS KMS, on a YubiKey or on a software key is install configuration, not role code.

## Drivers are services, and nobody execs them

A driver is a platform-started service that speaks ConnectRPC over a Unix socket. **No role starts a driver.** The platform does:

| Shape | The platform starts a driver as |
|---|---|
| Single node, cluster, EC2 | A systemd service (and socket) |
| Lambda | A Lambda extension |
| Kubernetes | A sidecar container, or a pod of its own |

This has consequences that the rest of the design leans on:

- A role's unit has no exec rights at all. On systemd it carries `NoExecPaths=/` with an `ExecPaths=` naming its own binary only, so a compromised role cannot start a shell, a helper or a driver of its own.
- The platform supervises the driver, restarts it and orders it before its callers. Our code does not.
- Backends combine at deploy time without a rebuild: add a unit, a layer or a sidecar.

Plugins follow the same rule. They always run out of process: over a Unix socket with peer credentials locally, as a Lambda invoke on AWS, or over HTTPS with mutual TLS when remote. There are no Go `.so` plugins and no WebAssembly.

## Per-role and shared instances

Whether the backend can tell callers apart decides how a driver runs. Each driver service declares `instance: per-role | shared`.

| Instance | Use it when | How access is enforced |
|---|---|---|
| `per-role` | The backend can tell callers apart: AWS IAM, an HSM with a user per role, a database with a role per sluis role | Each instance carries that one role's own credential, and the backend enforces access |
| `shared` | The backend has one credential or one device: a YubiKey and its PIN, a TPM, software keys, the state store | One process holds the credential. It authenticates every caller by peer credentials (`SO_PEERCRED`) and checks an access list generated from the manifest |

### The uid rule

A **per-role driver runs as the role's own uid.** Its socket is owned by that uid and no other uid can connect to it. A **shared driver runs under its own uid**, and its socket accepts any caller that the access list then checks.

For example, the AWS driver for `signer` is a unit that runs as the `signer` user (the paths are illustrative):

```text
/run/sluis/signer/api.sock     role's own API, group-readable by its clients
/run/sluis/signer/aws.sock     the signer's AWS driver, mode 0600, owned by the signer uid
```

Another role cannot open `aws.sock`, and AWS does the rest: the KMS key policy names the signer role and no other.

## The two shared drivers

Some resources cannot refuse a caller by themselves. A YubiKey has one PIN; a state store has one login. For these, a **shared driver** owns the resource and every other process goes through it. There are two, and both audit every use.

| Shared driver | Fronts | What it enforces |
|---|---|---|
| `key-holder` | The keys driver for HSMs, YubiKeys and software keys | An access list of caller role × key purpose × operation, generated from the manifests. The device node is closed to everyone else, and keys are created non-extractable and sign-only. |
| `state-owner` | The replicated state store (rqlite on Raft) | An access list of caller role × record kind × operation, with exactly one role allowed to write each record kind. The store's own listener is closed to everyone else. See [Data](/concepts/data/). |

Both have no internet egress, load no plugins, and are started before their callers.

## On-disk layout

Internal programs are not on `PATH`. The only command for humans is `/usr/bin/sluis`. Everything a platform starts lives under `/usr/libexec/sluis/`, in one directory per kind:

```text
/usr/bin/sluis                         the only command for humans
/usr/libexec/sluis/
  roles/                               central, signer, policy, ca, notary, audit-writer, audit-read, quota
  drivers/                             one directory entry per driver
  integrations/                        one per integration
  node/                                node-agent, and other node services
  vendor/                              third-party binaries, such as the state store
```

Short names are used because the directory is the namespace (Postfix does the same with `/usr/libexec/postfix/smtpd`), and because the kernel truncates a process name at 15 characters, so `sluis-audit-writer` would otherwise show as `sluis-audit-wri`. Units, users and the CLI keep the `sluis-` prefix, so `ps`, the journal and `systemctl` still say sluis.

## Ports and drivers by shape

The same roles run on every shape. The drivers behind their ports are what changes.

| Port | T0 Local | T1 Single node | T2 Cluster | T3 EC2 single | T4 EC2 cluster | T5 Lambda |
|---|---|---|---|---|---|---|
| keys | software (or the host's YubiKey) | YubiKey PIV, shared | YubiKey per node, shared | AWS KMS, per-role | AWS KMS, per-role | AWS KMS, per-role |
| state | embedded single-node store | single-node store, no Raft | rqlite on Raft, behind `state-owner` | DynamoDB | rqlite on Raft, behind `state-owner` | DynamoDB |
| index | sqlite | sqlite | sqlite | sqlite | sqlite | postgres |
| blob | S3-compatible store | S3-compatible store | S3-compatible store | S3 | S3 | S3 |
| queue (audit outbox) | local spool | local spool | local spool | local spool | local spool | queue |
| lease | state compare-and-set | state compare-and-set | Raft leader lease, others on state | DynamoDB conditional write | Raft leader lease, others on state | DynamoDB conditional write |
| cloud | aws, gcp, azure drivers as configured | as configured | as configured | aws | aws | aws |

The keys, state and index columns follow the agreed shapes. T0 may use the host's YubiKey through the host's `pcscd` socket, which is undecided.

:::caution[Open decision OD-8: YubiKey in Docker]
For the local shape the options are to use the host's `pcscd` socket, or to use software keys only. The current lean is the `pcscd` socket.
:::

:::caution[Open decision OD-12: Index host on Lambda]
The options are Aurora Serverless v2 (the lean, still to be verified) or DynamoDB only.
:::

:::note[Proposed]
The blob, queue, lease and cloud rows are the working design and are not settled shape by shape. The audit outbox per shape is open as OD-5 and is covered in [Audit](/concepts/audit/).
:::

:::caution[Open decision OD-4: Inter-node ports]
The only agreed inter-node traffic is the state driver's own Raft port, over mutual TLS. Whether any other port exists between nodes is open: the options are that port alone, or that plus a role port. The current lean is the Raft port alone.
:::

## Why separate services

Compiling every driver into every role would make fat binaries, noisy scanner results for code a role never uses, and a rebuild to add a backend. A role that spawned its own driver would need exec rights and would put supervision in our code. A single AWS driver shared by all roles would hold every role's credentials and lose IAM's per-role enforcement. Separate services cost a socket to protect, contract versioning across images and some memory, and one Unix-socket hop is a fraction of a millisecond against the several milliseconds a KMS or DynamoDB call takes.

:::caution[Open decision OD-14: Rust cloud drivers]
The aws, gcp and azure drivers are Go unless a memory measurement on the smallest EC2 instance shows Rust is worth a second toolchain.
:::

Where the sockets live and which of them ever listen on TCP is on [Listeners](/architecture/listeners/).
