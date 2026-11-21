---
title: Runtimes
description: The three runtimes sluis runs on, what each provides, which drivers sluis runs on which, and the four tested installations (local, boards, lambda, kubernetes).
status: target
sidebar:
  order: 0
---

A **runtime** is the kind of place sluis runs: how processes are started and what they can rely on. There are three ([0011](/decisions/0011-runtimes-and-installations/#runtime-kinds)). The word "environment" keeps its usual meaning: development, staging or production.

- **[bare-metal](/runtimes/bare-metal/)** covers physical hosts, virtual machines such as EC2, and local Docker. systemd, or a container runtime for local Docker, starts the processes.
- **[serverless](/runtimes/serverless/)** covers AWS Lambda and similar platforms. The platform runs a function per event, and every function runs behind the Lambda Web Adapter. Lambda's own term is written "Lambda runtime" in these pages, to keep the two apart.
- **[kubernetes](/runtimes/kubernetes/)** runs sluis as pods, supervised by the kubelet.

The runtime is not the node count and not the cloud account: both are attributes of an installation. Nor is it the [boundary](/concepts/boundaries/), the environment or customer that an installation keeps apart from the others.

Every runtime runs the same actors: the foundation (store@, key@ and exporter@) and the layers the installation needs ([0002](/decisions/0002-families-of-actors/#the-foundation-and-the-layers), [0044](/decisions/0044-sluis-is-a-core-and-add-ons/)). The actor registry says where each one runs. The runtime decides how an actor becomes a unit and how a call travels from one unit to the next ([0003](/decisions/0003-units-transports-and-fusion/#units)):

| | bare-metal | serverless | kubernetes |
|---|---|---|---|
| The unit | A systemd unit with its own uid | A function with its own role, one per actor and per holder | A pod with its own service account, one per actor and per holder |
| A call travels as | A Unix socket call | A Lambda invoke | In-cluster gRPC |
| The caller is known by | The peer uid | The caller's role and alias | A service-account token with one audience per callee |
| The call graph is enforced again by | The socket's group and the caller's sandbox | The callee's resource policy | A NetworkPolicy per callee |
| `entry` listens on | A Unix socket, behind `cloudflared` | `127.0.0.1` inside the sandbox, behind the Lambda Web Adapter | A Unix socket, behind a key-less edge relay container |
| The Cloudflare edge | A tunnel to `cloudflared` | Cloudflare proxied to an API Gateway custom domain with mutual TLS | The cluster's own Cloudflare tunnel and gateway |
| A holder's drivers are | systemd units beside the holder | Linked in the function, or extensions in one merged drivers layer | Native sidecar containers in the holder's pod |
| Packaging | `.deb` and `.rpm` packages, and a tarball | An app zip per executable and a config layer | One OCI image per process, and one Helm chart |

The steps of a request and the checks on it are the same on all three ([principle 19](/principles/#19-one-model-many-transports)). Packaging is on [Packages](/distribution/packages/#packaging-per-runtime); there are no build profiles, and `dev` is not a build.

## What each runtime provides

| Capability | bare-metal | kubernetes | serverless |
|---|---|---|---|
| Long-running processes | Yes | Yes | No (runs per event) |
| Persistent local disk | Yes | Only with a per-pod volume | No |
| Same-host Unix sockets | Yes, on the node | Yes, within a pod | Yes, within one function (extensions) |
| Inbound connections between processes | Yes (between nodes, only the engine's Raft port) | Yes (Service) | No (only triggers or a gateway) |
| Hardware devices | Yes | Partly (a pod pinned to its node) | No |
| Nodes | 1 or N | 1 or N | Elastic (no node identity) |
| Supervisor | systemd, or a container runtime for local Docker | kubelet | The platform |

Serverless always comes with a provider, AWS Lambda today, so for serverless the provider is part of the runtime.

## Installations, nodes and dependencies

- **Nodes.** One, N, or elastic on serverless. A bare-metal installation with one node and one with three have the same runtime.
- **Dependency.** Something outside sluis that a [driver](/decisions/0005-the-store/#drivers) needs and declares: an AWS account, a Cloudflare account, a YubiKey, a NATS service.
- **Installation.** One concrete deployment: a runtime, a node count, the available dependencies, the engine behind store@, the key store behind key@ and its fusion choices ([0003](/decisions/0003-units-transports-and-fusion/#fusion)), for example "bare-metal, 3 nodes, AWS account".
- **Tested installation.** One of the four that the documentation, the tutorials and the test matrix use: local, boards, lambda and kubernetes. Any other combination is a possible installation, allowed by the rules but not tested.

"Installation" is not a configuration key. The [bootstrap file](/reference/bootstrap-file/) and the [signed config documents](/reference/signed-config/) are the installation's configuration, and the four tested installations ship as example bootstrap files.

`sluis config check --runtime` validates an installation against what its drivers declare: a driver that needs a long-running process is refused on serverless, a driver that needs exactly one node is refused on a cluster, and a driver whose dependency is missing is refused everywhere.

A job that must run once at a time takes a lease, which is a temp kind of the store ([0005](/decisions/0005-the-store/)). There is no lock outside the store on any installation.

## Which drivers fit which runtime

The drivers are [atoom's](https://github.com/truvity/atoom#status): sluis runs them and writes none. atoom's [driver table](https://github.com/truvity/atoom/blob/master/docs/packages.md) lists every driver with its package and its Lambda form, and says which has none. This table is what sluis runs where, narrowed on Kubernetes by [0011](/decisions/0011-runtimes-and-installations/#kubernetes). A tick means the driver fits, a cross means `sluis config check` refuses it, and a note is the condition.

| Role | Driver (atoom's) | Bare-metal, 1 node | Bare-metal, cluster | Serverless | Kubernetes |
|---|---|---|---|---|---|
| key@ | `software` (`key/software`) | not in production | not in production | ✗ | the `kubernetes-dev` preset only |
| | `yubikey-piv` (`key/yubikeypiv`) | ✓ | ✓ | ✗ | ✗ |
| | `aws-kms` (`key/awskms`) | ✓, with AWS credentials | ✓, with AWS credentials | ✓ | ✓ |
| store@: durable and temp | `rqlite` (`store/rqlite`) | ✓, one node | ✓ | ✗ | ✗ |
| | `dynamodb` (`store/dynamodb`) | ✓, with AWS credentials | ✓, with AWS credentials | ✓ | ✓, with AWS credentials; not the default |
| | `postgres` (`store/postgres`, planned) | ✗ | ✗ | ✗ | ✓, the default, on CloudNativePG |
| | in-memory (`store/memory`) | tests only | tests only | tests only | tests only |
| store@: config source | `file` (`source/file`) | ✓ | ✓ | ✓, from a layer | ✓, a mounted ConfigMap |
| | `appconfig` (`source/appconfig`, planned) | with AWS credentials | with AWS credentials | ✓ | ✓ |
| store@: secret source | `credential` (`source/credential`) | ✓, systemd credentials | ✓ | ✗ | ✗ |
| | `ssm` (`source/ssm`, planned) | with AWS credentials | with AWS credentials | ✓ | ✓ |

The rows of store@'s four ports are also in code (`actors/store/src/install.rs`), which `sluis config check --runtime` reads; the other ports join as a file names their drivers. A lease needs no row: it is a temp kind in the engine. The blob, transport, index and witness drivers of the audit pieces are audit's, chosen by [audit's distribution record](https://github.com/truvity/audit/blob/master/docs/decisions/0016-distribution-and-the-lambda-shape.md).

## Configuration sources

Every installation offers the `file` source driver: it needs nothing, and on Lambda a layer is a file and on Kubernetes a mounted ConfigMap is a file. An installation offers `appconfig` when it has AWS, because the driver needs AWS credentials. Of the four tested installations, lambda and kubernetes offer it, and local and boards do not. `sluis config check` refuses a source whose driver the installation does not offer, with the same check as for any other driver.

Which documents may come from which source is [0004](/decisions/0004-the-data-model/#storage-classes): config is read-only and signed, and store@ verifies each document before it parses it. The loading mechanics and the signed format are [atoom's](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md); the rules and the test vectors are on [Signed config](/reference/signed-config/).

## The four tested installations

The tested installations choose the default drivers of their runtime. Pick by the failure you have to survive and the key custody you accept; the order in which they are built is on the [Roadmap](/roadmap/).

| Installation | Runtime and nodes | key@ driver | store@: engine, config source, secret source |
|---|---|---|---|
| [local](#local) | bare-metal, 1 node | `software` (a development card is opt-in) | `rqlite` with one node; `file`; credential files |
| [boards](#boards) | bare-metal, 3 nodes | `yubikey-piv`, one card per board | `rqlite`, one cluster over the three boards; `file`; systemd credentials |
| [lambda](#lambda) | serverless, elastic | `aws-kms` | `dynamodb`; `appconfig` or `file` from a layer; SSM |
| [kubernetes](#kubernetes) | kubernetes, N nodes | `aws-kms` | `postgres` on CloudNativePG; a mounted ConfigMap or `appconfig`; SSM |

Every production installation sits behind the Cloudflare edge, and each has its own engine and key driver. The key mode follows from the installation, not from a switch: KMS on lambda and kubernetes, a YubiKey on boards, software keys on local. The code carries the floor: the `software` driver of key@ refuses to start in production and warns in staging, and configuration picks only among the modes the floor allows ([0006](/decisions/0006-keys-and-sealing/#floors-in-code)).

An installation serves one [boundary](/concepts/boundaries/) at L4, or the front of a federation with the boundaries below L4 inside it. A test installation serves no real user, trusts nothing, and lives outside the sluis host names. An installation runs in one region of one cloud provider, or on one set of boards; a second region is a second installation ([0011](/decisions/0011-runtimes-and-installations/#installations)).

```mermaid
flowchart TD
  start["What do you need?"] --> dev{"Developing or<br/>testing sluis?"}
  dev -- yes --> local["local"]
  dev -- no --> cloud{"Run on AWS?"}
  cloud -- no --> boards["boards"]
  cloud -- yes --> k8s{"Already run<br/>Kubernetes?"}
  k8s -- yes --> kube["kubernetes"]
  k8s -- no --> lam["lambda"]
```

Anything else is a possible installation: start from the nearest tested one and change drivers within the rules.

**The reference installations** that the design is checked against are three: a three-board installation, which is the self-hosted one ([boards](#boards)); an internal installation for a company's own staff, on serverless ([lambda](#lambda)); and a Kubernetes installation on AWS (EKS) ([kubernetes](#kubernetes)). The [local](#local) installation is for development and serves no real user ([0011](/decisions/0011-runtimes-and-installations/#installations)).

The state of each: local is built, and its smoke tests run in CI on every pull request. The boards installation has its deploy and smoke test built, and its hardening and rehearsals planned. Lambda and kubernetes are planned: no deploy code exists yet ([0011](/decisions/0011-runtimes-and-installations/#installations)).

## The edge and the engines of each installation

Every production installation sits behind the Cloudflare edge, and each has its own way in. sluis's own deploy code sets its side of it, and each installation's infrastructure code applies the Cloudflare records, rules and rate limits; sluis holds no Cloudflare credential in the core ([0011](/decisions/0011-runtimes-and-installations/#the-cloudflare-edge)). The key set and the discovery document are cacheable for less than the key pre-publish time.

```mermaid
flowchart LR
  cf["Cloudflare edge"]
  cf -->|"tunnel"| cfd["cloudflared"] --> eb["entry<br/>boards"]
  cf -->|"proxied,<br/>mutual TLS"| gw["API Gateway<br/>custom domain"] --> lwa["Web Adapter"] --> el["entry<br/>Lambda"]
  cf -->|"cluster's tunnel<br/>and gateway"| rel["edge relay"] --> ek["entry<br/>Kubernetes"]
```

The local installation has no Cloudflare: its forwarder ends TLS for a developer or a test harness. On Lambda the API's default `execute-api` endpoint is disabled, so the custom domain is the only way in. On Kubernetes a `cloudflared` sidecar in `entry`'s pod is an option for a cluster without its own tunnel.

The store engine follows the installation: `rqlite` on local and boards, `dynamodb` on Lambda, `postgres` on CloudNativePG on Kubernetes ([0005](/decisions/0005-the-store/#engines)). On Kubernetes the store and the audit index are separate databases with separate roles of one cluster, so the credential of one does not open the other.

## local

Docker Compose on one workstation, one container per actor and per driver, for development, continuous integration and the integration tests ([0011](/decisions/0011-runtimes-and-installations/#the-local-installation)). It runs the release's published images, so a local run tests the images Kubernetes runs. It is not for production. Runtime: [bare-metal](/runtimes/bare-metal/#local-docker), one node. Its containers, host names, TLS and first start are on [The local installation](/runtimes/local/). The same page walks through a first install.

- **Engine:** `rqlite` with one node, the engine of the boards with a cluster of one, so local exercises the replicated engine path.
- **Keys:** `software` by default, which refuses to start in production. A **development card** is opt-in: the key@ container, and only it, reaches a dedicated card through the host's smart-card service, and key@ has the card to itself ([0006](/decisions/0006-keys-and-sealing/#exclusive-card-access)).
- **Sign-in:** through the generic OpenID Connect upstream against a real identity provider in a container, never a hand-written fake. sluis holds no accounts, even in tests.
- **Edge:** `entry`'s container has no network. A developer or a test harness reaches its Unix socket through the forwarder, a generic reverse proxy in the Compose project that ends TLS, mounts the socket and publishes a port on the loopback address.
- **Limits:** a software key can be read by anyone who can read its volume, so it guards development data only. One node proves behaviour, not availability, and local does not exercise the platform controls of the other installations: IAM and KMS key policies, systemd sandboxing, a replicated store, a compliance lock.

## boards

Three single-board computers (arm64, 2 GB RAM, eMMC), each with its own YubiKey, behind a Cloudflare tunnel to `sluis.example.com`. A board runs a systemd unit and a uid for each actor and each holder, and rqlite replicates the data over Raft. Runtime: [bare-metal](/runtimes/bare-metal/#n-nodes), three nodes. The units, the call graph and the card are on [Bare-metal](/runtimes/bare-metal/); the memory and write budget is in [Board capacity](/runtimes/bare-metal/#board-capacity).

```mermaid
flowchart LR
  edge["Cloudflare edge"] -- "tunnel" --> cfd
  subgraph board["Each board"]
    cfd["cloudflared"] -- "Unix socket" --> entry["entry"]
    entry --> work["work actors"]
    work --> store["store@"] --> rq["rqlite node"]
    work --> key["key@"] --> card["YubiKey"]
  end
  rq <-- "Raft, mutual TLS" --> peers["rqlite nodes of<br/>the other boards"]
```

- **Drivers:** `yubikey-piv` behind key@; `rqlite` behind store@, one cluster over the three boards with the data on eMMC and the temp data replicated too, so any board serves any request ([0005](/decisions/0005-the-store/#engines)); `file` for config and systemd credentials for secrets. Audit's blob is `fs` on each board's disk; object storage for the logs belongs to the Cloudflare add-on ([0011](/decisions/0011-runtimes-and-installations/#boards-and-the-bare-metal-runtime)).
- **Keys:** a board's keys are its own, born on its card. No key is shared between boards, so losing a board loses its keys, not the installation's. Data keys are wrapped for each board's card, so any board opens them ([0006](/decisions/0006-keys-and-sealing/#keys-per-board)). The slots are on [Bare-metal](/runtimes/bare-metal/#the-card-and-its-slots).
- **Audit:** each board keeps its own log, and the other two boards are its peer witnesses (planned; [audit 0009](https://github.com/truvity/audit/blob/master/docs/decisions/0009-witnesses-and-verification.md)). The log lives on the board's own disk for now, so a reinstalled board starts a fresh log ([A reinstalled board](/runtimes/bare-metal/#a-reinstalled-board)).
- **Identity and edge:** each board runs its own `cloudflared`, which forwards to `entry`'s Unix socket. The boundary's issuer issues workload tokens, with one key per board card in its key set ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **Limits:** Raft tolerates the loss of one board of three. Physical access to a board with its card and PIN allows signing as that board; peer witnessing limits that for the audit logs but not for tokens.

## lambda

One Lambda function per actor and per holder behind API Gateway, each behind the Web Adapter, with KMS keys and DynamoDB, and S3, SQS and an Aurora index for the audit pieces. Cloudflare proxies to an API Gateway custom domain with mutual TLS, and the API's default endpoint is disabled. A work function holds no credential; only the holders' roles reach storage and keys, and every hop between functions is an invoke. An invoke is fast enough for this layout, so no function links store@ into a work actor ([0003](/decisions/0003-units-transports-and-fusion/#fusion); the figures of the warm sign-in are in [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/#the-warm-sign-in-on-lambda)). The request path and the limits are on [Serverless](/runtimes/serverless/). Runtime: serverless, elastic.

## kubernetes

A pod per actor and per holder on Amazon EKS, each with its own service account, installed from one Helm chart. A driver is a native sidecar container in its holder's pod, and a key-less edge relay container in `entry`'s pod forwards to its socket. Its store is PostgreSQL through CloudNativePG, the keys are AWS KMS and the blob is S3, so a cluster without AWS is not in 2.0. The edge is the cluster's own Cloudflare tunnel and gateway, with an optional `cloudflared` sidecar. A holder's cloud role comes from web identity through the cluster's IAM OIDC provider, not from EKS Pod Identity. It runs the same smoke tests as the other installations, plus a smoke test on kind with the `kubernetes-dev` preset and integration tests on a development EKS cluster. The details are on [Kubernetes](/runtimes/kubernetes/). Runtime: kubernetes, N nodes.

## Other installations

These are possible installations: allowed by the rules, not tested.

- **One board.** The boards installation reduced to one node, with `rqlite` on one node and the `fs` blob.
- **EC2.** One instance or an Auto Scaling group is a bare-metal installation on virtual machines ([Bare-metal](/runtimes/bare-metal/#virtual-machines-ec2)). EC2 is not a delivery target.

For each, `sluis config check` applies the same rules as for a tested installation. What the tests do not give is evidence that the combination keeps working from release to release.

## Pages in this section

- [Bare-metal](/runtimes/bare-metal/): physical hosts, virtual machines and local Docker, the units, the call graph, the card and the board capacity.
- [Serverless](/runtimes/serverless/): AWS Lambda, a function per actor and per holder.
- [Kubernetes](/runtimes/kubernetes/): a pod per actor, drivers as sidecars, one chart.
- [The local installation](/runtimes/local/): Compose, host names, TLS, the first start and sign-in.
