---
title: Bare-metal
description: The bare-metal runtime runs sluis as systemd units on physical hosts or virtual machines such as EC2, or in Docker locally, with one node or several.
status: target
sidebar:
  order: 1
---

Bare-metal is the runtime where sluis owns its hosts. It covers physical machines (single-board computers, servers), virtual machines (EC2 or any other provider) and local Docker on a workstation. Processes are long-running, the node has a persistent disk, actors and drivers talk over Unix sockets on the node, and a supervisor starts everything: systemd on a host or a virtual machine, a container runtime for local Docker. See [what each runtime provides](/runtimes/#what-each-runtime-provides).

The number of nodes is an attribute of the installation, not of the runtime: bare-metal runs with one node or with N. The tested installations on bare-metal are [local](/runtimes/installations/#local) and [boards](/runtimes/installations/#boards). A virtual machine, such as an EC2 instance, is a possible installation under the same rules, but it is not tested.

## What runs where

- Each [actor](/concepts/actors/) instance, `<actor>@<boundary>`, is its own OS user and its own instance of a systemd template unit. Drivers are systemd units too, started by the platform. Nothing execs a driver.
- **Templates and a generator** (agreed, SY-1). The packages ship template units only: `sluis-<actor>@.service` and `sluis-<actor>@.socket`, with `User=sluis-<actor>-%i`, `RuntimeDirectory=sluis/<actor>/%i`, `LoadCredentialEncrypted=` per instance and `ListenStream=/run/sluis/<actor>/%i.sock`. A **systemd generator** shipped in the package reads `/etc/sluis/install.yaml` and creates the instances and their `sysusers.d` entries, so a second [boundary](/runtimes/boundaries/) on the same host is a second set of instances, not a second package.
- The packages, split by function ([Packages](/distribution/packages/#packages-by-function)), install under `/usr/libexec/sluis/{actors,drivers,integrations,node,vendor}/<short-name>`. The directory is called `actors`; it was `roles`, and the rename amends the earlier package layout.
- The node agent (`sluis-node`) runs on the node. It issues each actor a short-lived workload JWT and, on a cloud VM, is the only process that holds the instance role and brokers per-actor credentials.
- The permission manifest generates the units: `User=`, a private state directory, `LoadCredential=` (or `LoadCredentialEncrypted=` where the systemd floor allows it) for the secrets an actor may read, and socket modes and groups for the callers the manifest allows.
- **Hardening per unit:** `ProtectSystem=strict`, `NoNewPrivileges=`, an empty `CapabilityBoundingSet=` and `SystemCallFilter=@system-service`. Every actor that needs no network, `entry`, `policy` and `signer` among them, gets `PrivateNetwork=yes` and `RestrictAddressFamilies=AF_UNIX`. Integrations reach their providers through the egress proxy.
- `key-holder` fronts the node's YubiKey with one socket per caller, a peer-uid check and content-aware signing ([The mediating driver](/concepts/drivers/#the-mediating-driver)). State has no mediator: each owning actor has its own rqlite cluster. How an actor in a private network namespace reaches its cluster's TCP client port is still open ([ADR 0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/#not-decided-here)).
- Integration actors run only when the installation uses them.

## Supported distributions and the systemd floor

The systemd floor is 252. Ubuntu 26.04 LTS, Rocky Linux 10.x and Amazon Linux 2023 meet it, so the units use `LoadCredentialEncrypted=` where they need it. The distributions and architectures are listed on [Platforms](/distribution/platforms/), and the packages (`.deb`, `.rpm` and a tarball from the same binaries) on [Packages](/distribution/packages/). Bare-metal has no build profiles: the packaging is the build, and `dev` is not a build. Mixed distributions in one cluster share one package set, at the price of a wider test surface, because the systemd, kernel and library versions differ.

### Board OS exception

Single-board computers run the vendor's Ubuntu 22.04 for now. This is a declared exception to the supported-distribution list.

- systemd 249 has `ExecPaths=` but no `LoadCredentialEncrypted=`. The generated units use plain `LoadCredential=`, and the credential files are protected by file mode and by the dedicated state directory. They are not encrypted at rest on the board. Key material never is, because it stays in the YubiKey.
- The kernel is 5.10. `sluis doctor` warns on the randomness checklist, because VM-clone reseeding needs a kernel from 5.18. Amazon Linux 2023 runs a newer kernel, so that check passes on EC2.
- Ubuntu 26.04 and Rocky Linux 10.2 board images are a backlog task, and the images will live in a separate public repository. Mixing the two on boards starts once they exist.

## One node

A single node is one failure domain. Losing it stops sign-in until the node is rebuilt and its state restored.

- **State:** an embedded single-node store behind the state port, no Raft, or DynamoDB on an AWS VM.
- **Audit:** every success is audited. Records go to a local spool, the writer seals batches into the blob store and asks the `key-holder` to sign checkpoints; there is no notary actor. Records still in the spool when a node dies are lost, so keep the spool small and drained. Keep the blob store off the node if the audit trail has to outlive it.
- **Index:** `sqlite`. It lags the chain, and verification never trusts it or the state, so a damaged index cannot hide a missing record.
- Recovery needs no Kubernetes and no cloud: a hardware-token challenge or SRP. A recovery spools its audit record locally and delivers it later.

## N nodes

With three nodes the installation survives the loss of one.

- **State:** rqlite across the nodes, **one cluster per (boundary, owning actor)**, each a systemd template instance `sluis-rqlite@<boundary>-<actor>` with its own port and certificate. The owning actor has the only rqlite user that may `execute`; a declared reader has a `query`-only user. Each cluster's unit is sandboxed so that it writes its data directory only. rqlite is light: about 7 to 9 MB idle and 16 to 26 MB loaded per node and cluster, plus about 30 MB shared per host; the budget is measured again on the boards. Each cluster needs a quorum of two of three. A node that is down is replaced or repaired without losing state.
- **Raft over mTLS.** Raft peer traffic uses each cluster's own TCP port, protected by mutual TLS with a certificate per cluster, between nodes and to clients. These are the only TCP ports between the nodes (agreed, [OD-4](/open-decisions/#od-4)), and the network rule allows them only between the peers. No actor is called across nodes.
- **Audit:** the transport is a local spool on each node. A cluster keeps **one log per node**: each node has its own writer, spool and log key, and the other nodes are its peer witnesses (the agreed 2-of-3 co-signing). The rule is exactly one writer per log, not one per installation. Readers merge the logs by time. A checkpoint carries the signatures of two nodes, so one compromised node cannot extend a log alone. The witness is optional, and `sluis config check` warns when there is none.
- **Identity:** each node is its own issuer for workload identity: `https://<issuer>/nodes/<node>/`. A cloud role trusts only the issuers of the nodes that run it, so a compromised node can assume only its own roles.
- Raft tolerates the loss of one node of three. Losing two stops writes, and sign-in stops with them, because state is needed for sessions and grants. Three nodes usually share a site, a network and a power feed, so this is not a multi-site design.

## Virtual machines (EC2)

An EC2 installation is bare-metal on virtual machines. The units are the same as on a board. An EC2 installation is a possible installation, allowed by the rules but not tested; the tested bare-metal installations are local and boards.

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

Docker on a workstation is the development and CI form of bare-metal, one node: the [local](/runtimes/installations/#local) installation. It uses the host's YubiKey on a workstation and a software key in CI, and it is delivered first. It is not for production, and the code refuses a software key in production.

- It runs as **Docker Compose, one container per actor** (agreed), on one node. Each actor and each driver is its own container user and its own process, so the call graph and the socket ACLs are the ones the other installations enforce.
- Actors and drivers speak ConnectRPC over Unix sockets on a shared volume under `/run/sluis/`.
- The host's YubiKey is the default key on a workstation. The `key-holder` container uses the host's `pcscd` socket, mounted into it (agreed, [OD-8](/open-decisions/#od-8)); the pcsc-lite protocol versions of the image and the host must match. The fallback is to run the `key-holder` natively on the host, so that the containers mount only its Unix socket. CI uses a software key.

## Keys

- **YubiKey (PIV):** the key mode on boards. A YubiKey has no symmetric keys, so sealing uses ECDH plus HKDF (HPKE-style) with a P-384 key in a slot. Slot plan (proposed): `9c` node intermediate and signing, `9d` ECDH sealing and ring wrap, `82` SSH user CA and `83` SSH host CA (both for `ssh-ca`), `84` release-signing subkey, `85` to `95` retired sealing keys. Slot `9e` is avoided. Only the `key-holder` talks to the YubiKey, through one socket per caller, and it audits every use. One YubiKey per node: a node's key is its own, so losing a board loses one key, not the install's.
- **AWS KMS:** the key mode on AWS VMs. One symmetric key (`ENCRYPT_DECRYPT`) with encryption contexts for every sealing need, and one `ECC_NIST_P384` `SIGN_VERIFY` key per authority purpose (token signing, X.509 CA, SSH CA, grant), in each environment. KMS allows one usage per key and `Sign` takes no encryption context, so purposes are separated by key, not by context. Each key policy names one actor, and the instance role is denied by every key policy. In a cluster, all instances use the same KMS keys for tokens, the CA and grants; KMS, not a local device, is the root of trust, so a replaced instance needs no key ceremony. Each node has its own log key, because a shared key cannot support co-signing.
- **Software key:** local development only. The code refuses it in production.
- Algorithm: ES384 (P-384) is sluis's internal recommendation and default. sluis supports the FAPI 2.0 set, PS256, ES256 and EdDSA (Ed25519), as well; PS256 is RSA, so it is another exception to the no-RSA rule. Driver support differs by algorithm: PS256 on a YubiKey takes about 2.3 s per signature, and PIV Ed25519 needs firmware 5.7 or newer. A cloud-federation key may use the algorithm its cloud requires, and an audience that cannot take EC may get RS256 from a separate RSA key.
- Tokens are signed directly with the non-extractable key (agreed, [OD-3](/open-decisions/#od-3)). A YubiKey signs P-384 at about 120 ms, about 8 per second. A production install measured about 25,000 tokens in seven days and a peak of 0.8 per second, so direct signing fits. EUCLEAK is fixed from YubiKey firmware 5.7; use 5.7 or newer. TPM 2.0 support is a backlog feature; when it ships it is an addition to the YubiKey, not a replacement.

## Identity and edge

- An actor's identity on a node is its uid, read through peer credentials (`SO_PEERCRED`) on the Unix socket. On a cloud VM, calls to AWS use the actor's own IAM role, so CloudTrail names the actor, the instance and the session.
- `entry`'s public API is a Unix socket, and `entry` runs with `PrivateNetwork=yes`, seccomp and Landlock, so it has no network at all. Every other actor and every driver listens on a Unix socket under `/run/sluis/<actor>/`. See [Listeners](/architecture/listeners/).
- `cloudflared` is the edge connector on the boards installation. It forwards to `entry`'s Unix socket as a Unix-socket origin and makes outbound connections only, so the node opens no inbound port for sluis. It holds no key and has no access to any other actor's socket. With several nodes, each runs its own `cloudflared`, so the edge has one connector per node and survives the loss of one.

- **The console.** `entry` serves the console's single-page application from a directory on the node, on the same origin as its API. The bundle is a separate artifact of the release ([Packages](/distribution/packages/)).

:::note[Proposed]
`cloudflared` as the edge connector on EC2 installations is a proposal, not a decision. The listener rule itself (only `entry`'s public API faces the edge, on a Unix socket on bare-metal) is agreed; the choice of connector is not final.
:::

## Limits and residual risk

- The YubiKey is physical. Whoever holds the board and the key, and knows the PIN, can sign. Set a PIN, a touch policy where the workflow allows it, and keep the board in a controlled place.
- eMMC wears under heavy writes. The spool and the state are small, and a blob store off the board keeps the audit chain.
- A root-level compromise of a node defeats host-asserted identities (uid, cgroup). KMS key policies and CloudTrail limit and record what the stolen identity can do. The instance role is the host's root of trust for AWS and is the instance's blast radius, so keep the role small.
- KMS is a regional service. A regional outage stops signing; there is no software fallback key, because the code refuses one in production.
- Signing speed on a YubiKey is limited to about 8 tokens per second. That is far above the measured load, but a burst beyond it queues.
