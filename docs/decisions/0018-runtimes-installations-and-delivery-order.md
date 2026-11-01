# 0018 — Runtimes are three kinds, four installations are tested, and delivery goes local first, then boards, Lambda and Kubernetes in parallel

**Status:** Accepted
**Date:** 2026-11-01

Proposal: [issue #902](https://github.com/truvity/sluis/issues/902).

## Context

v1 and the early v2 design described "deployment shapes": local, single node, cluster, a single virtual machine, a virtual-machine cluster, a serverless function and Kubernetes. A shape mixed three things: the runtime kind, the number of nodes, and the outside services a deployment needs. Each new platform added shapes, and a name such as "board + cluster" hid which part differed.

The runtime kind decides what a process can rely on: whether it runs for a long time, whether it has a persistent disk, whether processes can connect to each other. The node count and the outside services do not change that; they change which drivers fit. "Environment" already means development, staging or production to nearly every reader, so it keeps that meaning and does not name the runtime kind.

Seven installations are too many to build and test, and the order in which the tested ones are built decides what is proved first. v2 has to take the place of v1 ([0001](/decisions/0001-v1-and-v2-lines/)), and v1 is the measure of what it must cover.

## Decision

### Runtime kinds

- **A runtime is only the runtime kind.** There are three:
  - **bare-metal**: physical hosts, virtual machines (EC2 and similar) and local Docker, supervised by systemd or by a container runtime;
  - **serverless**: AWS Lambda and similar. It runs per event: no long-running processes, no local disk, no inbound connections between processes. It always comes with a provider, so the provider is part of the runtime;
  - **kubernetes**: supervised by the kubelet. A persistent disk exists only with a per-pod volume.
- **An environment is development, staging or production.** It says where an installation is used, not what a driver needs.
- **Nodes and dependencies are attributes of an installation.** The node count (1, N, or elastic on serverless) belongs to the installation. A provider need, such as a cloud account, a hardware key or a message service, is a dependency that a driver declares ([0002](/decisions/0002-the-design-model-actors-ports-drivers-and-contracts/#ports-and-drivers)).
- **An installation is one concrete deployment:** a runtime, nodes, the available dependencies and one driver per port, for example "bare-metal, 3 nodes, AWS account". "Installation" is not a configuration key: the install file is the installation, the tested installations ship as example install files, and the runtime is a configuration layer ([0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema)).
- **There are no presets.** The earlier ids T0 to T6 are retired. Labels follow the names: `runtime:bare-metal`, `runtime:serverless`, `runtime:kubernetes` for the runtime, and `install:local`, `install:boards`, `install:lambda`, `install:kubernetes` for a tested installation.

### Installations

Four installations are tested: built, documented and kept in the test matrix. Any other combination is a possible installation: the rules allow it and `sluis config check` accepts it, but it is not tested. EC2 is not a delivery target; an EC2 installation is a possible bare-metal installation.

| Installation | Runtime and nodes | Needs | keys | state and lease | blob | transport | index |
|---|---|---|---|---|---|---|---|
| **local** | bare-metal, 1 node | A workstation with Docker | `software` (a development card is opt-in) | `embedded`; a local lock | `fs` | `spool` or `direct` | `sqlite` |
| **boards** | bare-metal, 3 nodes | Three single-board computers (arm64, 2 GB RAM, eMMC), a YubiKey in each, a Cloudflare account for the tunnel to the public hostname (`sluis.example.com` in docs) and for R2 | `yubikey-piv`, one key per node | `rqlite` | R2 through `s3-compatible` | `spool` | `rqlite` |
| **lambda** | serverless, elastic | An AWS account with Lambda, KMS, DynamoDB, SQS and S3 | `aws-kms` | `dynamodb` | `s3` | `sqs` | `postgres` (`aurora` mode) |
| **kubernetes** | kubernetes, N nodes | A cluster and an AWS account with KMS, DynamoDB, SQS and S3 | `aws-kms` | `dynamodb` | `s3` | `sqs` | `postgres` (`cnpg` mode) |

- **On the boards the audit pieces run on the same nodes** and share driver instances. Each node keeps its own log, and the other nodes are its peer witnesses ([0016](/decisions/0016-the-audit-log/#witnesses)). Garage is backlog: it ignores conditional writes.
- **On Lambda the audit writer runs with a reserved concurrency of 1 on a standard SQS queue.** Correctness does not come from the limit but from the write-once fence on the bucket ([0016](/decisions/0016-the-audit-log/#the-write-once-fence)); the cost of the throttling is measured on the Lambda installation.
- **Leases live on the state store everywhere** ([0011](/decisions/0011-state-is-one-port-with-compare-and-set/#leases)): `rqlite` on the boards, `dynamodb` on Lambda and Kubernetes. There is no Kubernetes Lease driver.
- The index drivers and their provisioning modes are [0015](/decisions/0015-audit-pieces-transport-search-and-front/#search-and-the-index).

### The local installation

- **Docker Compose, with one container per actor and per driver.** The platform starts every service and nobody execs a driver ([0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/#drivers-are-platform-started-services)); Compose is that platform. The runtime is bare-metal with one node. It is for development, continuous integration and the integration tests, and it proves behaviour, not availability.
- **The same socket layout and identities as everywhere.** Unix sockets live on shared volumes under `/run/sluis/<actor>/`; each container mounts only the socket directories its manifest allows and runs as its actor's uid, so the peer-credential checks work as on a host. `key-holder` gives each caller its own socket, mounted only into that caller's container ([0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/#key-holder-is-the-one-mediator)).
- **The published images.** Local runs the release's images, one per process, which carry the packages' binaries byte for byte ([0019](/decisions/0019-release-train-packaging-and-distribution/#packaging-per-runtime)). A local run tests the images Kubernetes runs, not a development build.
- **Software keys by default.** The `software` keys driver needs no hardware; it refuses to start in production and warns in staging ([0012](/decisions/0012-cryptography-and-authority-keys/#floors-in-code)). A dedicated development card is opt-in: the `key-holder` container, and only it, reaches the card through the host's smart-card service, and `key-holder` has the card to itself. Sharing a card with other card software such as gpg is not set up by the installation; it was tried on 2026-11-01 and turned off, because gpg then asked for the PIN on every operation.
- **People sign in through a small fake upstream identity provider**, run from configuration. sluis holds no accounts, even in tests. Passkeys are tested with a browser's virtual authenticator and a software authenticator library; the product carries no test code.
- **`entry`'s container has no network.** Its public API is a Unix socket on a shared volume, as on any bare-metal installation ([0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/#listeners)). A developer or a test harness reaches it through a Compose service, the edge-connector image or a forwarder, that mounts the socket and publishes a port. A development installation differs from the others by configuration only.
- **Direct transport falls back to a local spool** when the writer is down ([0015](/decisions/0015-audit-pieces-transport-search-and-front/#transport)); local has a disk, so both `direct` and `spool` work. There is no witness, and `sluis config check` warns about it as for any installation without one.

### Kubernetes

Decided on 2026-10-30.

- **The fusion unit is the container** ([0003](/decisions/0003-core-actors-with-one-responsibility-each/#the-fusion-rule)). A generated check proves that no container holding a key credential exposes a listener.
- **Cloud identity: a projected ServiceAccount token per container** by default, with one audience per role. EKS Pod Identity is used for single-actor pods, and `sluis-node` only as a fallback ([0007](/decisions/0007-workload-identity-and-host-enrolment/#proofs-and-the-exchange)).
- **Drivers:** AWS KMS only for keys, DynamoDB only for state and leases, SQS for the audit transport, S3 for blobs.
- **The edge is a key-less edge relay container in the pod.** It listens on the pod address and forwards to `entry`'s Unix socket; `entry` stays socket-only. A `cloudflared` sidecar is a supported mode.
- **A pod per integration**, each with its own egress NetworkPolicy to its vendor.
- **The audit side is a separate audit installation** with its own front ([0015](/decisions/0015-audit-pieces-transport-search-and-front/#the-audit-front)), from the chart's `audit-install` preset.
- **Baseline:** native sidecars, the `restricted` Pod Security level and a CNI that enforces NetworkPolicy are required. Cilium is optional, for egress by FQDN. OpenShift and sandboxed runtimes (gVisor, Kata) are not in the first cut.
- **One chart** with the presets `install`, `audit-install`, `quota-install` and `kubernetes-dev`, and no subcharts ([0019](/decisions/0019-release-train-packaging-and-distribution/#packaging-per-runtime)). The `kubernetes-dev` preset allows software keys on the same terms as the local installation, for the smoke test on a kind cluster. The KMS driver is tested against real KMS by the Lambda installation's tests.
- **sluis never writes Kubernetes objects.** Cluster tools such as cert-manager, trust-manager and External Secrets stay its clients and distributors; sluis serves APIs and exports. The chart renders no Role for leases.
- **Continuous integration reaches the development EKS API through a short-lived runner inside the VPC**, against a private endpoint. The cluster has no public API endpoint.

### Delivery order

The product milestones, in order:

1. **First working version: the local installation**, plus the foundations every installation stands on: the build and release train ([0019](/decisions/0019-release-train-packaging-and-distribution/)), the contracts and the permission manifest ([0002](/decisions/0002-the-design-model-actors-ports-drivers-and-contracts/#the-permission-manifest)), the driver pattern ([0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/)), configuration and key custody ([0012](/decisions/0012-cryptography-and-authority-keys/)).
2. **v1 parity: boards, Lambda and Kubernetes, built and tested in parallel.** They start when the first working version is done, and none waits for another. Parity means that the workloads that use v1 today can be wired to v2, possibly in a different way or with a different configuration. It does not mean identical behaviour. The parity issues on the v2 board track it.
3. **CIAM upstream**, broker only: sluis hosts no customer accounts.
4. **Quota.**

The order is reviewed when each installation ships, and a change of order is recorded here.

## Consequences

- Adding a platform adds one runtime at most, and often none. A new driver changes which installations are possible, not which runtimes exist; `sluis config check` reports which ones work.
- The test matrix is four installations wide. Combinations outside it can break without a test noticing, and the docs say so.
- The local installation exercises the same call graph, socket layout and per-actor identity as the boards, so a socket or access-list mistake is found on a workstation. Several containers instead of one cost start-up time and memory, accepted for fidelity.
- No hardware is needed to build or test. The card path is exercised on the boards and by anyone who opts into a development card; the platform controls (IAM and key policies, systemd sandboxing, a replicated store, a compliance lock) are exercised only by boards, Lambda and Kubernetes.
- The Compose file is the local counterpart of the systemd units and must be generated from the permission manifest like them, or the two drift. The uids are pinned, the same in Compose and in the chart, because the peer-uid check relies on them.
- A software key can be read by anyone who can read its volume. It is for development data only.
- On a card, PIV signing is about 7 signatures a second on a YubiKey 5 Nano and about 21 for three boards (measured 2026-10-31), so the cards limit token signing before the state store limits writes ([Bare metal](/runtimes/bare-metal/)). Load tests run on installations with cloud keys.
- Until the parity milestone ships an installation, v2 has no production installation, because local is not one. Checks that need a cloud account, a cluster or the board hardware wait for it.
- The parity milestone puts three installations in flight at once. A defect in a shared piece found on one can block the others; the foundations built in the first working version are the guard.
- Kubernetes depends on more cluster features than the other runtimes, and an operator's cluster must meet the baseline before the chart installs.

## Roads not taken

- **Seven fixed shapes, or the presets T0 to T6, as the tested set.** Each new platform multiplied them, three were not on the near-term path, and the ids said nothing about what they contained.
- **"Environment" for the runtime kind.** The word already means development, staging or production, and the two meanings would meet in one sentence.
- **A virtual-machine cluster on EC2 first.** It would depend on a cloud account and leave the key hardware, the hardest part, untested.
- **Local, then boards, then Lambda, with Kubernetes later.** After local, nothing in one installation blocks another, so a serial order would leave capacity idle.
- **Parity as identical behaviour.** It would carry v1's mistakes forward and bind v2 to v1's shape; the bridge is the wiring.
- **One local container with every actor under a supervisor.** The supervisor puts back the right to start processes, and local would no longer be shaped like the other installations.
- **A hardware key for every local run.** It would make a card a precondition for building and testing; the card path is proved on the boards and on an opt-in development card.
- **A Kubernetes Lease driver.** Leases already live on the state store with fencing tokens, and a second lease backend would need Kubernetes RBAC and objects that sluis does not write.
- **Integrations as containers of the node pod.** A pod per integration gives each one its own egress rule and lifecycle.
- **A public EKS API endpoint limited to the runner's address.** A runner inside the VPC needs no public endpoint at all.

## Follow-ups

- The test installations harness, including the fake upstream identity provider and the generated Compose file: [#877](https://github.com/truvity/sluis/issues/877).
- Integration tests on a private runner group for the boards and the AWS installations: [#891](https://github.com/truvity/sluis/issues/891).
- The Helm chart for the parity milestone: [#844](https://github.com/truvity/sluis/issues/844).
- AWS KMS signing latency from Lambda, for the load-test plan: [#878](https://github.com/truvity/sluis/issues/878).
