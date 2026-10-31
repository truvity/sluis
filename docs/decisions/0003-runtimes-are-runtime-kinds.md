# 0003 — Runtimes are runtime kinds; installations add nodes and dependencies; four installations are tested

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #902](https://github.com/truvity/sluis/issues/902). Rewritten in place on 2026-10-29 during the v2 design phase (see "Design phase" in the decisions README). The earlier slug was `0003-environments-are-runtime-kinds`. The delivery-order bullet was edited in place on 2026-10-30 to follow ADR 0039.
Rewritten in place on 2026-10-30 (realignment pass).
Rewritten in place on 2026-10-31 (the boards' blob store is R2 in the first release; Garage is a later backend, blocked on conditional writes and prefix scoping upstream, after the spike in [issue #982](https://github.com/truvity/sluis/issues/982)).

## Context

sluis v1 and the early v2 design described "deployment shapes": local, single node, cluster, single virtual machine, virtual machine cluster, serverless function and Kubernetes. A shape mixed three things: the runtime kind, the number of nodes, and the outside services the deployment needs (a cloud account, a hardware key). Each new platform added shapes, and a name such as "board + cluster" hid which part actually differed.

The runtime kind decides what a process can rely on: whether it runs for a long time, whether it has a persistent disk, whether processes can connect to each other. The number of nodes and the outside services do not change that. They change which drivers fit.

Two words need fixing. "Environment" is the common word for development, staging and production, so it keeps that meaning and does not name the runtime kind. A serverless provider also has its own term, for example the "Lambda runtime", which is a language runtime and not what this record means. Docs write the provider's term in full.

Seven named installations were also too many to build and test. The maintainer chose a delivery order that needs fewer, and a list of tested installations that follows it.

## Decision

- **A runtime is only the runtime kind.** There are three:
  - **bare-metal**: includes virtual machines (EC2 and similar) and local Docker. Supervised by systemd, or by a container runtime for local Docker.
  - **serverless**: AWS Lambda and similar. It runs per event, so there are no long-running processes, no local disk and no inbound connections between processes. It always comes with a provider, so for serverless the provider is part of the runtime.
  - **kubernetes**: supervised by the kubelet. A persistent disk exists only with a per-pod volume.
- **An environment is development, staging or production.** It says where an installation is used. It is not a runtime and it does not change what a driver needs. An installation per boundary is a separate question.
- **Nodes and dependencies are not part of a runtime.** The node count (1, N, or elastic on serverless) is an attribute of an installation. A provider need, such as a cloud account, a hardware key or a message service, is a dependency that a driver declares ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).
- **An installation is one concrete deployment:** runtime, nodes, available dependencies and one driver per port. For example: "bare-metal, 3 nodes, AWS account".
- **Four installations are tested.** They are the ones we build, document and keep in the test matrix:
  - **local**: bare-metal, 1 node. Docker on a workstation, using the host's hardware key through the host's smart-card service. Not a software key.
  - **boards**: bare-metal, 3 nodes. Three single-board computers (arm64, 2 GB RAM, eMMC), a hardware key in each node and a tunnel to the public hostname (`sluis.example.com` in docs). State and lease on rqlite. The index driver is decided in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/). Blob on R2 through the S3-compatible driver in the first release (Garage, with an off-site R2 copy of the audit log, is a later backend), and spool transport. The audit pieces run on the same nodes and share driver instances. Each node keeps its own log with the other nodes as peer witnesses ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
  - **lambda**: serverless on AWS. KMS for keys, DynamoDB for state and lease, SQS for transport, S3 for blob. The index driver is decided in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/). The writer runs with a reserved concurrency of 1 on a standard SQS queue; correctness comes from the blob fence (create-if-absent).
  - **kubernetes**: KMS, DynamoDB, SQS, S3 and the Kubernetes Lease. The index driver is decided in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/); the pod layout and chart in ADRs 0046 and 0047.

  Any other combination is a possible installation: the rules allow it and `sluis config check` accepts it, but we do not test it.
- **There are no presets.** The earlier ids T0 to T6 are retired. Visible text uses the four names above.
- **Delivery order.** Minimal local first, then boards, lambda and kubernetes in parallel ([0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/)).
- **Packaging follows the runtime.** Bare-metal ships a deb, an rpm and a tarball. Serverless ships a zip per actor. Kubernetes ships an OCI image and a Helm chart. `dev` is an installation, not a build. The details are in [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/).
- **Labels follow.** `runtime:bare-metal`, `runtime:serverless` and `runtime:kubernetes` mark the runtime. `install:local`, `install:boards`, `install:lambda` and `install:kubernetes` mark a tested installation.
- **"Installation" is not a configuration key.** The install file is the installation, and the tested installations ship as example install files. The runtime is a configuration layer and replaces the "shape profile"; the order of the layers is in [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).

## Consequences

- Adding a platform adds one runtime at most, and often none. A new driver changes which installations are possible, not which runtimes exist.
- Which installations work is derived from driver requirements, and `sluis config check` reports it. A tested installation is a point in that space, not the whole of it.
- Docs get a clear split: one page per runtime, and one page for the four tested installations with the rule that others are possible but untested.
- The old shape and environment paths on the site redirect to the new pages, and labels and issues are renamed. This is a one-time cost.
- The test matrix is four installations wide, not seven. Combinations outside it can break without a test noticing, and the docs say so.
- Local development uses a real hardware key, so contributors need one for the full local installation.
- Older driver choices read as "this installation's default": spool on boards, SQS on lambda and kubernetes, a cloud key service on AWS, a hardware key on boards. Their reasoning is unchanged.
- Local Docker and virtual machines share a runtime with physical nodes. Where they differ, the difference is a driver requirement, not a new runtime.
- The index driver of each tested installation is decided in [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/).


## Roads not taken

### Seven fixed shapes

- Pros: simple to name and to test. Every shape is a known point.
- Cons: each new platform multiplies the shapes. Shapes with the same runtime repeat the same facts.
- Why not: the runtime kind is the part that rarely changes. Names for tested points still exist, as installations.

### Keep seven presets (T0 to T6) as the tested set

- Pros: a wide test matrix. Familiar ids.
- Cons: three of the seven are not on the near-term path. Maintaining them slows the four that are. The ids said nothing about what they contained.
- Why not: the tested set follows the delivery order. Everything else is allowed by the rules and untested.

### Keep "environment" for the runtime kind

- Pros: no second rename. Matches the first draft of this record.
- Cons: "environment" already means development, staging or production to nearly every reader. The two meanings meet in one sentence whenever a page says where an installation is used.
- Why not: the runtime kind gets its own word, and "environment" keeps its common meaning. "Stage", which briefly replaced it, is withdrawn.

### Combined names such as "board + cluster"

- Pros: short, familiar labels for common setups.
- Cons: they hide what differs. Nodes and dependencies are attributes of an installation, not of a platform. Every combination needs a new name.
- Why not: an installation states its runtime, nodes and dependencies separately, and four names cover the ones we test.

### Start on a virtual machine cluster

- Pros: the cloud is easy to provision and tear down.
- Cons: the first build would depend on a cloud account and would not exercise the hardware key path, which is the hardest part to get right.
- Why not: starting local and then on boards puts the hardest dependencies first. The cloud installations follow.

### Placement decided per installation

- Pros: each installation can be tuned by hand.
- Cons: the placement rules repeat in every installation and drift apart.
- Why not: placement is derived from port and driver rules ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). An installation only names default drivers.
