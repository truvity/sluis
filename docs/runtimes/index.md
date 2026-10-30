---
title: Runtimes
description: The three runtimes sluis runs on (bare-metal, serverless, kubernetes), what each provides, and the four tested installations that name its default drivers.
status: target
sidebar:
  order: 0
---

A **runtime** is the kind of place sluis runs: how processes are started and what they can rely on. There are three, and the choice is agreed. The word "environment" is kept for development, staging and production.

- **[bare-metal](/runtimes/bare-metal/)** covers physical hosts, virtual machines such as EC2, and local Docker. A supervisor (systemd, or a container runtime for local Docker) starts the processes.
- **[serverless](/runtimes/serverless/)** covers AWS Lambda and similar platforms. The platform runs a function per event. In these pages Lambda's own term is always written "Lambda runtime", to keep the two apart.
- **[kubernetes](/runtimes/kubernetes/)** runs sluis as pods. The kubelet is the supervisor.

The runtime is not the node count and not the cloud account. Both are attributes of an installation, described below. Nor is it the [boundary](/runtimes/boundaries/): an environment, a company or a product, kept apart on an isolation ladder from separate keys up to a separate runtime. The [permission manifest](/architecture/deployment/) of every [actor](/concepts/actors/) is the same on every runtime. The runtime decides how it is turned into units, policies and ACLs, and sluis enforces its least-privilege rules on each one with the means that platform offers.

Each runtime has its own packaging, which is the build output for it: bare-metal takes `.deb` and `.rpm` packages split by function and a tarball, serverless takes a zip per executable and a function per actor, and kubernetes takes one OCI image per process and one Helm chart with presets. There are no build profiles, and `dev` is not a build; see [Packages](/distribution/packages/).

## What each runtime provides

| Capability | bare-metal | kubernetes | serverless |
|---|---|---|---|
| Long-running processes | Yes | Yes | No (runs per event) |
| Persistent local disk | Yes | Only with a per-pod volume (StatefulSet) | No |
| Same-host Unix sockets | Yes, on the node | Yes, within a pod | Yes, within one function (extensions) |
| Inbound connections between processes | Yes | Yes (Service) | No (only triggers or a gateway) |
| Hardware devices | Yes | Partly (a pod pinned to its node) | No |
| Nodes | 1 or N | 1 or N | Elastic (no node identity) |
| Supervisor | systemd, or a container runtime for local Docker | kubelet | The platform |

**Serverless always comes with a provider** (AWS Lambda today), so for serverless the provider is part of the runtime.

## Nodes and dependencies are not part of a runtime

The words below are the vocabulary of an installation. It is agreed.

- **Nodes.** How many nodes an installation has: one, N, or elastic (serverless). A bare-metal installation can have one node or three, and the runtime is the same.
- **Dependency.** Something outside sluis that a driver needs: an AWS account, a Cloudflare account, a YubiKey, a NATS service, a Postgres service. A [driver](/concepts/drivers/) declares its dependencies.
- **Installation.** One concrete deployment: a runtime, a node count, the available dependencies and one driver per [port](/concepts/drivers/). For example, "bare-metal, 3 nodes, AWS account".
- **Tested installation.** A named installation used in the documentation, the tutorials and the test matrix: local, boards, lambda and kubernetes. See [Installations](/runtimes/installations/). Any other combination is a possible installation, allowed by the rules but not tested.

In the design, `sluis config check` validates an installation against the requirements its drivers declare: a driver that needs a long-running process is refused on serverless, a driver that needs exactly one node is refused on a cluster, and a driver whose dependency is missing is refused everywhere. The requirements and the resulting driver and runtime compatibility table are on [Ports and drivers](/concepts/drivers/).

Some actors declare **instances: exactly 1**: the audit writer (exactly one writer per log, not one per installation), the FactsSource reconciler and the indexer. When there is more than one node, or the nodes are elastic, such an actor needs the lease port. On serverless, the platform's concurrency limit of 1 may stand in for it.

## Choosing an installation

The four tested installations name the default drivers for a runtime. They are delivered in this order: local first, then boards, lambda and kubernetes in parallel. Pick by the failure you have to survive and the key custody you accept.

- **[local](/runtimes/installations/#local):** Docker on a workstation, with the host's YubiKey. For learning and development.
- **[boards](/runtimes/installations/#boards):** three single-board computers, a YubiKey in each, with rqlite state. On-premises, and it survives the loss of a node.
- **[lambda](/runtimes/installations/#lambda):** AWS Lambda, a function per actor. No hosts to patch.
- **[kubernetes](/runtimes/installations/#kubernetes):** a node pod and an audit pod on EKS (proposed). For a team that already runs a cluster.

A key mode follows from the installation, not from a switch: KMS on lambda and kubernetes, a YubiKey on local and boards, and a software key only for development.

:::note[Proposed]
The code carries the floor (it refuses a software key in production) and configuration picks among the modes the floor allows. See [Keys](/concepts/keys/).
:::

Integration tests and the [tutorials](/tutorials/) run on the tested installations. Everything else is a possible installation, which the rules allow and the tests do not cover.

## Pages in this section

- [Bare-metal](/runtimes/bare-metal/): physical hosts, virtual machines and local Docker, with one node or N.
- [Serverless](/runtimes/serverless/): AWS Lambda, a function per actor.
- [Kubernetes](/runtimes/kubernetes/): a node pod and an audit pod (proposed).
- [Installations](/runtimes/installations/): the four tested installations, with the default drivers each one picks.
- [Boundaries and federation](/runtimes/boundaries/): the kinds of boundary, the isolation ladder L0 to L4, a front installation plus a boundary installation per L4 boundary, and the issuers.
