# 0003 — Environments are runtime kinds; installations add nodes and dependencies; presets name tested installations

**Status:** Accepted
**Date:** 2026-10-29

Decided by the maintainer. Proposal: [issue #902](https://github.com/truvity/sluis/issues/902).

## Context

sluis v1 and the early v2 design described "deployment shapes": local, single node, cluster, single virtual machine, virtual machine cluster, serverless function and Kubernetes. A shape mixed three things: the runtime kind, the number of nodes, and the outside services the deployment needs (a cloud account, a hardware key). Each new platform added shapes, and a name such as "board + cluster" hid which part actually differed.

The runtime kind decides what a process can rely on: whether it runs for a long time, whether it has a persistent disk, whether processes can connect to each other. The number of nodes and the outside services do not change that. They change which drivers fit.

The word "environment" was also used for an install boundary such as development or production. That meaning is renamed **stage** in the glossary, so the word is free.

## Decision

- **An environment is only the runtime kind.** There are three:
  - **bare-metal**: includes virtual machines (EC2 and similar) and local Docker. Supervised by systemd, or by a container runtime for local Docker.
  - **serverless**: AWS Lambda and similar. It runs per event, so there are no long-running processes, no local disk and no inbound connections between processes. It always comes with a provider, so for serverless the provider is part of the environment.
  - **kubernetes**: supervised by the kubelet. A persistent disk exists only with a per-pod volume.
- **Nodes and dependencies are not part of an environment.** The node count (1, N, or elastic on serverless) is an attribute of an installation. A provider need, such as a cloud account, a hardware key or a message service, is a dependency that a driver declares ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).
- **An installation is one concrete deployment:** environment, nodes, available dependencies and one driver per port. For example: "bare-metal, 3 nodes, AWS account".
- **A preset is a named, tested installation.** Presets are used in docs, tutorials and the test matrix. The old shape ids survive as presets T0 to T6:
  - T0 Local, bare-metal, 1 node (Docker on a workstation).
  - T1 Single board, bare-metal, 1 node.
  - T2 Board cluster, bare-metal, 3 nodes.
  - T3 EC2 single, bare-metal (virtual machine), 1 node.
  - T4 EC2 cluster, bare-metal (virtual machine, autoscaling group), 3 nodes.
  - T5 AWS Lambda, serverless, elastic.
  - T6 Kubernetes, kubernetes, N nodes.
- **Visible text and labels follow.** "Shape" becomes **environment** when it means the runtime kind and **preset** when it means a named old shape. `shape/*` labels become `env/*` (bare-metal, serverless, kubernetes) plus `preset/*` (T0 to T6).
- **Proposed, not decided:** configuration layers are ordered defaults, environment, preset, install, stage overrides. This replaces the "shape profile".

## Consequences

- Adding a platform adds one environment at most, and often none. A new driver changes which installations are possible, not which environments exist.
- Which installations work is derived from driver requirements, and `sluis config check` reports it. A preset is a tested point in that space, not the whole of it.
- Docs get a clear split: pages per environment, and one page for the presets.
- The old shape paths on the site redirect to the new pages, and labels and issues are renamed. This is a one-time cost.
- Older decisions read as "this preset's driver choice": spool on bare-metal presets and SQS on serverless, SQS on Kubernetes, a cloud key service on Kubernetes, key modes by preset. Their reasoning is unchanged.
- Local Docker and virtual machines share an environment with physical nodes. Where they differ, the difference is a driver requirement, not a new environment.
- The layer order is a proposal. Config code and docs must mark it so until it is decided.

## Roads not taken

### Seven fixed shapes

- Pros: simple to name and to test. Every shape is a known point.
- Cons: each new platform multiplies the shapes. Shapes with the same runtime repeat the same facts.
- Why not: the runtime kind is the part that rarely changes. Names for tested points still exist, as presets.

### Combined names such as "board + cluster"

- Pros: short, familiar labels for common setups.
- Cons: they hide what differs. Nodes and dependencies are attributes of an installation, not of a platform. Every combination needs a new name.
- Why not: an installation states its environment, nodes and dependencies separately, and a preset names the ones we test.

### Placement decided per preset

- Pros: each preset can be tuned by hand.
- Cons: the placement rules repeat in every preset and drift apart.
- Why not: placement is derived from port and driver rules ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). A preset only names default drivers.
