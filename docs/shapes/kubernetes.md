---
title: Kubernetes
description: The Kubernetes shape runs each sluis role as a pod under its own ServiceAccount, with Helm charts that pin images by digest. It is supported in the model.
status: target
sidebar:
  order: 7
---

Kubernetes is a supported shape in the model: each process role is a pod, with its own ServiceAccount, and the chart renders the network policy from the permission manifest. Whether it is in the test matrix now is undecided.

:::caution[Open decision OD-7: Kubernetes in the test matrix]
Options: test Kubernetes now, or later. The lean is later. Until it is decided, the shape is specified here and validated by the generators, but it is not covered by integration tests the way T1 to T5 are.
:::

## Topology

```mermaid
flowchart LR
  user["Browser, CLI,<br/>relying parties"] --> gw["Gateway"]
  gw -- "route to pod port" --> central
  subgraph ns["Namespace sluis"]
    central["central pod"]
    signer["signer pod"]
    policy["policy pod"]
    others["ca, notary, audit-writer,<br/>audit-read, quota pods"]
    drv["driver sidecars"]
    central --> signer
    central --> policy
    central --> others
    signer --> drv
    others --> drv
  end
  drv --> keys["Keys (KMS on AWS)"]
  drv --> state["State"]
  others -. "outbox" .-> q["Queue"]
  others --> idx["postgres index"]
```

## What runs where

- One pod per process role, each with its own ServiceAccount. A driver runs as a sidecar of the role that uses it, or as its own pod when it is shared.
- A shared driver (the `key-holder` and `state-owner` mediators) authenticates each caller and checks an ACL generated from the manifest.
- Helm charts are OCI artifacts with images pinned by digest. They are built from the release output; see [Packages](/distribution/packages/).
- Roles run non-root with a read-only root filesystem, no capabilities and the runtime default seccomp profile.
- sluis does not write Kubernetes objects. It exports, and clusters pull. cert-manager and trust-manager stay as clients of sluis.

## Keys

- The key mode follows the platform. On AWS (EKS), the keys are AWS KMS keys, one per authority purpose, with the ServiceAccount's Pod Identity role named in each key policy.
- A software key is refused on a production shape.

:::note[Proposed]
Key custody for a Kubernetes cluster outside AWS is not yet decided. The shape documents the KMS case.
:::

## State and audit

- **Index:** `postgres`.
- **Audit outbox:** a queue, or blocking.
- Sealed batches are written to the blob store, and the `notary` signs batch heads. A dedicated notary can run as a Kubernetes Job.
- The state store is chosen by the platform; the state port, the one-writer-per-record-kind rule and the audit chain are the same as in every shape.

:::caution[Open decision OD-5: Audit outbox per shape]
Options: spool, queue, blocking. On Kubernetes the candidates are a queue, or blocking until the batch is written.
:::

## Identity and edge

- A role's identity is its ServiceAccount, with a projected token bound to the callee's audience. The callee's interceptor verifies the token and maps `sub` to the role before it applies the call graph.
- On AWS, EKS Pod Identity gives each ServiceAccount its own IAM role. This is the platform-native identity, used where it exists.
- NetworkPolicy or CiliumNetworkPolicy allows only the call-graph edges. A Cilium policy can also express egress by FQDN, with default deny.
- **Listener exception.** The pod port the gateway routes to is `central`'s TCP listener. No key-holding role and no driver has a TCP listener outside the pod.

## Limits and residual risk

- The shape is not in the test matrix yet (OD-7), so a regression here can go unnoticed longer than on T1 to T5.
- Identity is as strong as the cluster. A cluster admin, a compromised node or a permissive admission policy can impersonate a ServiceAccount.
- A `postgres` index adds a stateful dependency.
- The outbox choice decides the failure mode: a queue keeps serving when the writer lags, and blocking does not.
