# 0039 — Delivery goes local first, then boards, lambda and kubernetes in parallel

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). This record replaces the earlier order ("local, then boards, then lambda, with kubernetes later"), which [0003](/decisions/0003-runtimes-are-runtime-kinds/) is edited in place to match. The roadmap and installations pages are fixed with it.

## Context

v2 is built alongside v1 and has to take its place ([0001](/decisions/0001-v1-and-v2-lines/)). Four installations are tested ([0003](/decisions/0003-runtimes-are-runtime-kinds/)): local, boards, lambda and kubernetes. The order in which they are built decides what is proved first, and how much of the team's time waits.

The order changed twice. The first plan made a virtual-machine cluster on EC2 the first build target. On 2026-10-29 the maintainer replaced it with a sequence: local, then boards, then lambda, with kubernetes later. The reason was to put the hardest dependencies first, the key hardware and the replicated state, before the cloud, which is easy to provision. The maintainer then changed the sequence again: local first, and once it is done the other three start together and are built and tested in parallel.

Two things stay unclear unless they are written down.

- **What "until v2 can replace v1" means.** Local is not a production installation, so work on local does not mean that production runs on local. It means that functionality is developed and tested there until it is enough.
- **What "parity" means.** v1 is the measure of what v2 has to cover. Identical behaviour is not required. What is required is that the workloads that use v1 today can be wired to the new sluis, possibly in a different way or with a different configuration.

Foundations come first in any order. The build, the process and driver patterns, the shared kits, configuration, key custody and packaging are the technical backlog on the roadmap, and every installation stands on them.

## Decision

- **Step 1: local, until v2 can replace v1, plus the enablers of the target architecture.**
  - Local is the Docker Compose installation of [0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/), with minimal drivers.
  - "Can replace v1" is measured by parity. Parity means that existing workloads can be wired to the new sluis, possibly in a different way or with a different configuration. It does not mean identical behaviour. The parity issues on the v2 board track it.
  - The enablers are the foundations the target architecture needs: the build, the process and driver patterns, the shared kits, configuration, keys and packaging. For example, the release train of [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/), the contracts and the permission manifest of [0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/), the driver pattern of [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) and key custody of [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/). They are built while local grows, so that the other installations are not blocked when step 2 starts.
- **Step 2: boards, lambda and kubernetes, built and tested in parallel.** They start when step 1 ends. None of them waits for another. The test matrix grows to all four.
- **The four tested installations, and what each needs.** Everything else is a possible installation under the rules, allowed by `sluis config check` and not tested.

  | Installation | Runtime and nodes | Needs |
  |---|---|---|
  | **local** | bare-metal, 1 node | A workstation with Docker. A YubiKey and the host's smart-card service for the card path; none for continuous integration, which uses a software key |
  | **boards** | bare-metal, 3 nodes | Three single-board computers (arm64, 2 GB RAM, eMMC), each with a YubiKey. A Cloudflare account for the tunnel to the public hostname (`sluis.example.com` in docs) and for the off-site R2 copy of the audit log; the boards' blob store is Garage. The vendor's Ubuntu 22.04, a declared exception until newer images exist ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)) |
  | **lambda** | serverless, elastic | An AWS account with Lambda, KMS, DynamoDB, SQS and S3. The writer runs with reserved concurrency of 1 on a standard SQS queue, fenced by create-if-absent on the bucket |
  | **kubernetes** | kubernetes, N nodes | A Kubernetes cluster and an AWS account with KMS, DynamoDB, SQS and S3. The Kubernetes Lease for the lease port |

  - **local:** `yubikey-piv` or `software` keys, `embedded` state, local lock, `fs` blob, `spool` or `direct` transport, `sqlite` index.
  - **boards:** `yubikey-piv` with one key per node, `rqlite` for state and lease, Garage as the blob store through the `s3-compatible` driver (with an off-site R2 copy of the audit log, after a spike), `spool` transport. The audit pieces run on the same nodes and share driver instances. Each node keeps its own log with the other nodes as peer witnesses ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
  - **lambda:** `aws-kms`, `dynamodb`, `sqs`, `s3`.
  - **kubernetes:** `aws-kms`, `dynamodb`, `sqs`, `s3` and the Kubernetes Lease.

  The list names the drivers decided here for each port except the index. The index driver of each installation is decided in ADR 0022, and the Kubernetes pod layout and chart in ADRs 0046 and 0047.
- **EC2 is not a delivery target.** A virtual machine is bare-metal, so an EC2 installation is a possible installation, allowed by the rules and untested.
- **The order is reviewed when each installation ships.** A change of order is a new record.

## Consequences

- Until the first installation of step 2 ships, v2 has no production installation, because local is not one. The checks that need a cloud account, a cluster or the board hardware wait for step 2.
- Step 2 puts three installations in flight at once. It needs enough hands to keep them moving, and a test harness that can run all four ([0003](/decisions/0003-runtimes-are-runtime-kinds/)).
- A defect in a shared piece, found on one installation, can block the other two. The shared foundations in step 1 are the guard against that, and anything that every installation needs is built before step 2.
- The boards installation depends on the index driver that ADR 0022 chooses. A full-text test on a three-node rqlite cluster (2026-10-30) worked. It has not been run on arm64 board hardware, and the cost of writes on eMMC is not measured. Both are inputs to ADR 0022.
- The kubernetes installation was "later" and is now in step 2. Its pod layout, edge and chart are not decided yet (ADRs 0046 and 0047), so it may take longer than the other two. The order allows that; it does not wait for it.
- The sluis v1 line continues while v2 grows. v1 is supported until the maintainers' production deployments run v2 ([0001](/decisions/0001-v1-and-v2-lines/)), which is a fact and not a date, so this record sets no date either.
- Every place that names the older order is corrected: the roadmap page, the installations page and the delivery-order bullet in [0003](/decisions/0003-runtimes-are-runtime-kinds/), which was edited in place for this.
- This record is date-sensitive. It is reviewed when each installation ships, and a newly shipped installation changes what "needs" means for it.

## Roads not taken

### Local, then boards, then lambda, with kubernetes later

- Pros: one thing at a time. The hardest dependencies come first. Each installation inherits the lessons of the one before it.
- Cons: the three later installations use different runtimes and drivers, so most of the work in each does not depend on the others. A serial order leaves capacity idle once local is done. (inferred: the maintainer replaced the order and gave no reason beyond the change itself)
- Why not: after local, nothing in one installation blocks another, so they are built and tested in parallel.

### A virtual-machine cluster on EC2 first

- Pros: the cloud is easy to provision and tear down. It would have been the first test of a cluster.
- Cons: the first build would depend on a cloud account and would not exercise the key hardware, which is the hardest part to get right.
- Why not: starting local puts the hardest dependencies first ([0003](/decisions/0003-runtimes-are-runtime-kinds/)), and an EC2 installation stays possible without being a target.

### All four installations at once from the start

- Pros: the fastest way to find a mistake that shows only on one runtime.
- Cons: the foundations and the basic functionality would change under four moving installations at the same time. (inferred)
- Why not: local is the fastest loop. It settles the functionality and the foundations first, and the others then stand on a stable base.

### Parity as identical behaviour

- Pros: no work for existing workloads when they move. A simple test: same input, same output.
- Cons: v1's mistakes, and every accident of its configuration, would be carried forward. It would constrain v2's design to the shape of v1. (inferred)
- Why not: the maintainer defined parity as the ability to wire existing workloads to the new sluis, possibly in a different way. The target is the new architecture, and the bridge is the wiring.

### Replace v1 only when all four installations are ready

- Pros: v2 is complete before anyone moves.
- Cons: nothing is replaced until the slowest installation ships, and the kubernetes pod layout and chart are not decided yet. (inferred)
- Why not: step 1 ends when v2 can replace v1 and the enablers exist. The installations that production needs are then built in parallel, and each one is usable when it ships.
