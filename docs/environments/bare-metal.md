---
title: Bare-metal
description: The bare-metal environment runs sluis as systemd units on physical hosts or virtual machines such as EC2, or in Docker locally, with one node or several.
status: target
sidebar:
  order: 1
---

Bare-metal is the environment where sluis owns its hosts. It covers physical machines (single-board computers, servers), virtual machines (EC2 or any other provider) and local Docker on a workstation. Processes are long-running, the node has a persistent disk, actors and drivers talk over Unix sockets on the node, and a supervisor starts everything: systemd on a host or a virtual machine, a container runtime for local Docker. See [what each environment provides](/environments/#what-each-environment-provides).

The number of nodes is an attribute of the installation, not of the environment: bare-metal runs with one node or with N. The named installations on bare-metal are the presets [T0 to T4](/environments/presets/).

## What runs where

- Each [actor](/concepts/actors/) is its own OS user and its own systemd unit. Drivers are systemd units too, started by the platform. Nothing execs a driver.
- The packages install under `/usr/libexec/sluis/{roles,drivers,integrations,node,vendor}/<short-name>`. The directory is still called `roles`, as the package layout is agreed; whether to rename it to `actors` is an open follow-up.
- The node agent (`sluis-node`) runs on the node. It issues each actor a short-lived workload JWT and, on a cloud VM, is the only process that holds the instance role and brokers per-actor credentials.
- The permission manifest generates the units: `User=`, a private state directory, `LoadCredential=` (or `LoadCredentialEncrypted=` where the systemd floor allows it) for the secrets an actor may read, and socket modes and groups for the callers the manifest allows.
- The shared drivers (`key-holder` for keys, `state-owner` for state) check each caller by peer credentials against the ACL generated from the manifest.
- Egress is closed by default for actors that need no network (`policy` and `signer`).
- Integration actors run only when the installation uses them.

## Supported distributions and the systemd floor

The systemd floor is 252. Ubuntu 26.04 LTS, Rocky Linux 10.x and Amazon Linux 2023 meet it, so the units use `LoadCredentialEncrypted=` where they need it. The distributions and architectures are listed on [Platforms](/distribution/platforms/), and the packages (`.deb` and `.rpm` from the same binaries) on [Packages](/distribution/packages/). Mixed distributions in one cluster share one package set, at the price of a wider test surface, because the systemd, kernel and library versions differ.

### Board OS exception

Single-board computers run the vendor's Ubuntu 22.04 for now. This is a declared exception to the supported-distribution list.

- systemd 249 has `ExecPaths=` but no `LoadCredentialEncrypted=`. The generated units use plain `LoadCredential=`, and the credential files are protected by file mode and by the dedicated state directory. They are not encrypted at rest on the board. Key material never is, because it stays in the YubiKey.
- The kernel is 5.10. `sluis doctor` warns on the randomness checklist, because VM-clone reseeding needs a kernel from 5.18. Amazon Linux 2023 runs a newer kernel, so that check passes on EC2.
- Ubuntu 26.04 and Rocky Linux 10.2 board images are a backlog task, and the images will live in a separate public repository. Mixing the two on boards starts once they exist.

## One node

A single node is one failure domain. Losing it stops sign-in until the node is rebuilt and its state restored.

- **State:** an embedded single-node store behind the state port, no Raft, or DynamoDB on an AWS VM.
- **Audit:** every success is audited. Records go to a local spool, the audit writer seals batches into the blob store and the `notary` signs the batch heads. Records still in the spool when a node dies are lost, so keep the spool small and drained. Keep the blob store off the node if the audit trail has to outlive it.
- **Index:** `sqlite`. It lags the chain, and verification never trusts it or the state, so a damaged index cannot hide a missing record.
- Recovery needs no Kubernetes and no cloud: a hardware-token challenge or SRP. A recovery spools its audit record locally and delivers it later.

## N nodes

With three nodes the installation survives the loss of one.

- **State:** rqlite across the nodes. It is the `state-owner` shared driver on every node, checks each caller by peer credentials and enforces one writer per record kind. The nodes need a quorum of two of three. A node that is down is replaced or repaired without losing state.
- **Raft over mTLS.** Raft peer traffic uses the state driver's own TCP port, protected by mutual TLS with a certificate from the node identity. It is the only TCP port between the nodes (agreed, [OD-4](/open-decisions/#od-4)), and the network rule allows it only between the peers. No actor is called across nodes.
- **Audit:** the transport is a local spool on each node, and each node's writer drains its own spool. The `notary` co-signs batch heads 2 of 3 across the nodes, with no separate notary key. A batch head carries the signatures of two nodes, so one compromised node cannot extend the chain alone.
- **Identity:** each node is its own issuer for workload identity: `https://<issuer>/nodes/<node>/`. A cloud role trusts only the issuers of the nodes that run it, so a compromised node can assume only its own roles.
- Raft tolerates the loss of one node of three. Losing two stops writes, and sign-in stops with them, because state is needed for sessions and grants. Three nodes usually share a site, a network and a power feed, so this is not a multi-site design.

## Virtual machines (EC2)

An EC2 installation is bare-metal on virtual machines. The units are the same as on a board.

- Instances boot from the Amazon Linux 2023 image and install the signed packages from the dnf repository. The node agent verifies what it installs. No machine image is baked.
- The Auto Scaling group (ASG) replaces a lost instance, and the replacement joins the Raft cluster:
  - **Join.** A new instance joins through rqlite discovery.
  - **Reaping.** A member that stays unreachable is removed after a timeout of minutes (rqlite reaping).
  - **Leave.** An ASG lifecycle hook makes an instance leave the cluster cleanly when the group scales it in.
  - **Enrolment.** A replacement instance enrols its identity through the node agent.
- Spread the group over availability zones, so that one zone outage costs one instance.
- Each instance publishes its workload-identity discovery document and JWKS, public keys only, to the distribution bucket under `nodes/<node>/`.
- A `t4g.nano` has little memory. The actors, the drivers and rqlite have to fit in it. The memory measurement on this instance type also bounds the Rust cloud drivers.
- Egress to AWS endpoints is by IP, not by name, in a plain VPC. A per-actor allow-list by host name needs a proxy.

:::note[Proposed]
The node agent (`sluis-node`) holds the instance role, brokers per-actor AWS credentials over a Unix socket, closes IMDS to every uid but its own, and verifies what it installs. The per-actor IAM trust policies carry labels. These are proposed controls against mislabelling and lateral movement, and host root defeats them. The research is on [Deployment](/architecture/deployment/).
:::

## Local Docker

Docker on a workstation is the development and CI form of bare-metal, one node, preset [T0](/environments/presets/#t0-local). It is not for production, and the code refuses a software key in production.

- All actors run in one container on one node. Each is still its own OS user and its own process, started by a supervisor inside the image, so the call graph and the socket ACLs are the ones the other presets enforce.
- Drivers speak ConnectRPC over Unix sockets under `/run/sluis/`.
- The host's YubiKey is optional. The `key-holder` runs natively on the host, talks to the host's `pcscd`, and the container mounts only the `key-holder`'s Unix socket (agreed, [OD-8](/open-decisions/#od-8)). The fallback is to mount the host's `pcscd` socket; then the ACL does not apply, and the pcsc-lite protocol versions of the image and the host must match.

## Keys

- **YubiKey (PIV):** the key mode on boards. A YubiKey has no symmetric keys, so sealing uses ECDH plus HKDF (HPKE-style) with a P-384 key in a slot. Slot plan: `9c` node intermediate and signing, `9d` ECDH sealing and ring wrap, `82` SSH CA, `83` to `95` retired sealing keys. Slot `9e` is avoided. Only the `key-holder` talks to the YubiKey, and it audits every use. One YubiKey per node: a node's key is its own, so losing a board loses one key, not the install's.
- **AWS KMS:** the key mode on AWS VMs. One symmetric key (`ENCRYPT_DECRYPT`) with encryption contexts for every sealing need, and one `ECC_NIST_P384` `SIGN_VERIFY` key per authority purpose (token signing, CA, grant). KMS allows one usage per key and `Sign` takes no encryption context, so purposes are separated by key, not by context. Each key policy names one actor, and the instance role is denied by every key policy. In a cluster, all instances use the same KMS keys for tokens, the CA and grants; KMS, not a local device, is the root of trust, so a replaced instance needs no key ceremony. Each node has its own notary key, because a shared key cannot support co-signing.
- **Software key:** local development only. The code refuses it in production.
- Algorithm: EC P-384, no RSA. A cloud-federation key may use the algorithm its cloud requires, and an audience that cannot take EC may get RS256 from a separate RSA key.
- Tokens are signed directly with the non-extractable key (agreed, [OD-3](/open-decisions/#od-3)). A YubiKey signs P-384 at about 120 ms, about 8 per second. A production install measured about 25,000 tokens in seven days and a peak of 0.8 per second, so direct signing fits. EUCLEAK is fixed from YubiKey firmware 5.7; use 5.7 or newer. TPM 2.0 support is a backlog feature; when it ships it is an addition to the YubiKey, not a replacement.

## Identity and edge

- An actor's identity on a node is its uid, read through peer credentials (`SO_PEERCRED`) on the Unix socket. On a cloud VM, calls to AWS use the actor's own IAM role, so CloudTrail names the actor, the instance and the session.
- `entry`'s public API listens on TCP, bound to `127.0.0.1` (the port is configuration; `8080` is an example). Every other actor and every driver listens on a Unix socket under `/run/sluis/<role>/`. See [Listeners](/architecture/listeners/).
- `cloudflared` is the edge connector on boards. It connects to `entry`'s loopback port and makes outbound connections only, so the node opens no inbound port for sluis. It holds no key and has no access to any other actor's socket. With several nodes, each runs its own `cloudflared`, so the edge has one connector per node and survives the loss of one.

:::note[Proposed]
`cloudflared` as the edge connector on the EC2 presets is a proposal, not a decision. The listener rule itself (only `entry`'s public API is reachable from outside, on `127.0.0.1`) is agreed; the choice of connector is not final.
:::

## Limits and residual risk

- The YubiKey is physical. Whoever holds the board and the key, and knows the PIN, can sign. Set a PIN, a touch policy where the workflow allows it, and keep the board in a controlled place.
- eMMC wears under heavy writes. The spool and the state are small, and a blob store off the board keeps the audit chain.
- A root-level compromise of a node defeats host-asserted identities (uid, cgroup). KMS key policies and CloudTrail limit and record what the stolen identity can do. The instance role is the host's root of trust for AWS and is the instance's blast radius, so keep the role small.
- KMS is a regional service. A regional outage stops signing; there is no software fallback key, because the code refuses one in production.
- Signing speed on a YubiKey is limited to about 8 tokens per second. That is far above the measured load, but a burst beyond it queues.
