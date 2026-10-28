---
title: Kubernetes (T6)
description: "T6 runs sluis as a node pod and an audit pod, with a container and a token audience per role, KMS keys on EKS and a Helm chart pinned by digest."
status: target
sidebar:
  order: 7
---

Kubernetes (T6) is in design and test scope now (see [OD-7](/open-decisions/#od-7)). Everything on this page is the design as proposed, except the points that [agreed decisions](/open-decisions/) already fix: sluis writes no Kubernetes objects, a driver talks to its role over a Unix socket, and the audit path is the one in [Audit](/concepts/audit/).

:::note[Proposed]
In T6 a role is a container with its own user id and its own cloud identity. The containers of one node form a pod, and the audit path forms a second pod. The chart renders everything from the permission manifest, and sluis itself calls no Kubernetes API. The maintainer has not yet agreed this. The first release supports Amazon EKS with AWS KMS. Other clusters are specified where a decision is open, and a cluster with no key mode that the floor allows refuses to start.
:::

## Topology

### The node pod

```mermaid
flowchart LR
  gw["Gateway<br/>or Ingress"] --> relay["edge relay<br/>no key"]
  subgraph node["Node pod"]
    relay -- "127.0.0.1" --> entry["entry"]
    entry -- "Unix sockets" --> core["signer, policy, ca,<br/>quota, integrations"]
    core --> drv["drivers<br/>(sidecars)"]
  end
  drv --> keys["Keys<br/>(KMS on EKS)"]
  drv --> state["State"]
```

The node pod runs two or three replicas. A request enters through the edge relay and never leaves the pod: the roles and their drivers call each other over Unix sockets.

### The audit pod

```mermaid
flowchart LR
  node["Node pod"] -. "audit records" .-> q["Queue"]
  q --> aw
  subgraph audit["Audit pod"]
    aw["audit-writer"] --> nt["notary"]
    follower["index follower"]
  end
  aw --> blob["Blob store"]
  follower --> idx["postgres<br/>index"]
```

The audit pod runs two replicas, and one of them writes. The queue is the only link between the two pods. `audit-read`, in the node pod, reads the same index.

## What runs where

- **Sockets stay Unix sockets.** Roles and drivers talk over Unix sockets in shared volumes, and the callee reads the caller's user id with `SO_PEERCRED` and checks the call graph, default deny.
- Helm charts are OCI artifacts with images pinned by digest. They are built from the release output; see [Packages](/distribution/packages/). One release means one set of images, so the roles inside a pod can never be at different versions.
- sluis does not write Kubernetes objects. It exports, and clusters pull. cert-manager and trust-manager stay as clients of sluis. The chart renders no Role, RoleBinding, ClusterRole or ClusterRoleBinding, because sluis needs no access to the Kubernetes API.

:::note[Proposed]
- **A role is a container.** Each role and each driver runs as its own container with a unique user id, a read-only root file system, no capabilities, no privilege escalation and the runtime default seccomp profile. Drivers are native sidecars, so the platform starts them before the roles that call them.
- **Two pod kinds, cut along the audit queue.**
  - The **node pod** holds every role on the synchronous call graph: `entry`, `signer`, `policy`, `ca`, `quota`, `audit-read` and the integrations, with their drivers and the edge container. It runs as a Deployment of two or three replicas spread over zones. Replicas exist for availability, not throughput.
  - The **audit pod** holds `audit-writer`, `notary` and the index follower. One replica writes at a time, chosen by a lease with a fencing token. The only link between the two pods is the audit queue, so no call crosses a pod boundary.
- **The mount is the first lock.** Each callee has its own volume for its socket directory, and that volume is mounted read-only only into the containers that the call graph allows. On Kubernetes this replaces the socket group of the hosts, because a container cannot have its own supplementary groups. The callee still checks the peer's user id.
- **A driver is never a pod of its own,** because a socket cannot cross a pod boundary.
:::

:::caution[Open decision OD-17: Pod layout on Kubernetes]
Options: (a) one pod per node, with every role a container; (b) one pod per role, with mutual TLS between pods, which gives every role a TCP listener and needs a certificate for every pod from `ca`; (c) a node pod plus an audit pod, cut at the audit queue. The proposal is (c), built as (a) inside each pod.
:::

:::caution[Open decision OD-18: What a unit is in the fusion rule on Kubernetes]
The [fusion rule](/architecture/processes/) says no unit holds a key next to an unauthenticated listener. Options: the pod, or the container as the unit of credentials. The proposal is the container, with a generated check that no container that can use a key also has a listener.
:::

:::caution[Open decision OD-29: Placement of integrations on Kubernetes]
Options: integrations as containers of the node pod; each as a pod of its own, called over HTTPS; a per-integration switch in the manifest. The proposal is containers now, and a switch for an integration that needs its own egress rules or its own lifecycle.
:::

## Keys

- A software key is refused on a production shape.
- A cluster with no key mode that the floor allows does not start. `sluis config check` and the process both refuse.

:::note[Proposed]
On AWS (EKS) the keys are AWS KMS keys with the same layout as the other AWS shapes: one symmetric key with encryption contexts for sealing, and one `ECC_NIST_P384` key per authority purpose. Each key policy names one role.
:::

:::caution[Open decision OD-20: Key custody on clusters outside AWS]
Options: cloud KMS drivers for Google Cloud and Azure; a remote `key-holder` on a machine with a hardware key, reached over the network; a PKCS#11 hardware security module behind the existing `key-holder`; or no support yet. The proposal is EKS only for now. When a second mode is wanted, PKCS#11 through `key-holder` comes first, then cloud KMS drivers. A remote `key-holder` is not proposed, because it puts a key beside a network listener.
:::

## Identity

The identity of a role is the token mounted into its container.

:::note[Proposed]
The pod defines one projected ServiceAccount token volume for each role, with an audience for that role, and mounts it only into the role's container and its driver. The role's cloud driver exchanges the token with the cloud: `AssumeRoleWithWebIdentity` on AWS, Workload Identity Federation on Google Cloud, a federated credential on Azure. It is the same driver code path as on the EC2 shapes. Only the source of the token differs: the kubelet and the API server issue it, and not `sluis-node`.

The pod's default ServiceAccount token is not mounted. The cloud role of each sluis role trusts the cluster's issuer, the audience of that role, and the namespace and ServiceAccount of the install. Nothing in the cluster is created for this: the infrastructure code that the manifest generates registers the issuer and the roles.

The workload tokens are the cluster's, signed by the cluster's key. They are not sluis keys, so the P-384 rule of [Keys](/concepts/keys/) does not cover them.

EKS Pod Identity is used for a pod that holds a single role, because its credentials belong to the pod and not to a container. The node agent `sluis-node` is not used on Kubernetes.
:::

:::caution[Open decision OD-19: Cloud identity for pods]
Options: EKS Pod Identity per pod; projected tokens per container with an audience per role; per-role tokens signed by `sluis-node` in the pod; a mix. Pod Identity alone cannot separate the roles of a pod, because every container of the pod can use the pod's credentials. The proposal is projected tokens per container as the default.
:::

On a cluster outside AWS the cluster's issuer must be reachable by the cloud, or its public keys must be published as static documents, as in [Identity](/concepts/identity/). Setting the API server's issuer is an operator step on the cluster.

Pods also must not reach the node's instance metadata service. Every key policy denies the node's role.

## State

- The chart refuses more than one replica on any state store that is not shared.

:::note[Proposed]
**State port:** DynamoDB on EKS, with conditional writes, transactions for the atomic batch, and IAM conditions that allow exactly one writer role for each record kind.
:::

:::caution[Open decision OD-21: State driver on Kubernetes]
Options: DynamoDB; rqlite as a StatefulSet behind `state-owner`; a Postgres state driver, for example on CNPG, with a database role for each sluis role. The proposal is DynamoDB on EKS and a Postgres driver as the portable follow-up. rqlite on Kubernetes is not proposed, because it would add a second replication layer and a shared driver inside a cluster that already offers replicated databases.
:::

## Audit

- **Index:** `postgres`, with any connection string (for example CNPG).
- The sealed batches go to the blob store, and the `notary` signs batch heads into the hash chain, as in every shape.
- A separate audit install, which holds `audit-read` and the index for an install that has no index of its own, such as [AWS Lambda](/shapes/aws-lambda/), runs from the same chart with a different profile.

:::note[Proposed]
**Outbox:** an SQS queue on EKS, as on Lambda. Producers may only send, and the writer receives. Blocking on the archive is not used, because a request must never wait for the writer, the archive or the index.

**Notary:** a dedicated notary with its own key, in the audit pod, with a ServiceAccount and a key policy of its own. There is no co-signing across replicas.
:::

:::caution[Open decision OD-22: Audit outbox on Kubernetes]
Options: a queue (SQS on AWS); a spool on a persistent volume, with a writer in every replica; blocking. The proposal is SQS on EKS, a volume spool for clusters outside AWS, and no blocking.
:::

:::caution[Open decision OD-23: Notary on Kubernetes]
Options: batch heads co-signed across replicas, which needs a stable identity and a key for every replica; a dedicated notary with its own key. The proposal is the dedicated notary.
:::

:::caution[Open decision OD-26: Reaching a separate audit install]
A console reaches `audit-read` over a Unix socket in its own install. For an audit install in another place, options: the audit install runs its own `entry` in front of `audit-read`; or the console calls it as a remote plugin over HTTPS with a token. The proposal is the first.
:::

## Edge

`entry` listens on `127.0.0.1` on every shape. A gateway cannot route to a loopback address in another pod, and the kubelet's HTTP and TCP probes connect to the pod address, so they cannot reach it either.

:::note[Proposed]
`entry` keeps its loopback listener. A container in the same pod bridges it, in one of two modes:

- **Edge relay (the default).** A small container that holds no key listens on the pod address and forwards to `entry` on `127.0.0.1`. A Service targets its port, so any Gateway API implementation or Ingress controller can route to it. The relay serves a health endpoint for the gateway, passes the client address in `X-Forwarded-For`, and removes that header from requests unless they come from a configured trusted proxy. A certificate can be configured when the hop from the gateway must be encrypted.
- **`cloudflared`.** A sidecar that connects out to the edge and to `entry`'s loopback port, as on the board and EC2 shapes. The pod accepts no inbound connection at all.

The kubelet probes `entry` with an `exec` probe, which runs inside the container's network namespace and reaches the loopback port. `entry` is not bound to the pod address.
:::

:::caution[Open decision OD-24: Edge on Kubernetes]
Options: a `cloudflared` sidecar; a key-less edge relay on the pod address; `entry` bound to the pod address. The relay is a second TCP listener, so the rule in [Listeners](/architecture/listeners/) that only `entry`'s API listens on TCP would gain the relay as an exception. The proposal is the relay as the default and `cloudflared` as a supported mode. Binding the pod address in `entry` is not proposed.
:::

## Packaging

- The Helm chart, `oci://ghcr.io/truvity/charts/sluis`, is built from the release output by `ocictl helmctl`, with every image pinned by digest. Whether one chart covers the audit install too is open (OD-30).
- The values schema is generated from the Pkl configuration schema, so the chart, `sluis config check` and the runtime validate against one source. The install document is passed whole. The chart fails at render time only on what spans the document and the deployment values, for example more than one replica on a state store that is not shared, or a key driver the floor refuses.

:::note[Proposed]
The permission manifest generates:

- one ServiceAccount for each pod kind, with the automatic token mount off;
- for each container, a unique `runAsUser`, `runAsNonRoot`, a read-only root file system, dropped capabilities, `allowPrivilegeEscalation: false` and the runtime default seccomp profile;
- for each pod, `shareProcessNamespace: false`;
- the socket volumes and the token volumes described above, each mounted only where the call graph allows;
- a NetworkPolicy for each pod kind that allows the gateway to reach the edge port and nothing else inbound, and egress to DNS and the allowed cloud endpoints;
- optionally a CiliumNetworkPolicy, because a plain NetworkPolicy cannot match host names, and Cilium can express egress by FQDN with default deny;
- a PodDisruptionBudget, zone spread and a rolling update that never takes a replica away before its replacement is ready;
- the infrastructure code for the cloud side: the issuer registration, the roles, the key policies, the tables and the queue.

The namespace carries the Pod Security label `restricted`. The render is tested against it.
:::

:::caution[Open decision OD-30: One chart or two]
Options: one chart with an install profile and an audit-install profile; a chart for the install and another for the audit install. The proposal is one chart, so that one install can never run a writer and a reader from different releases.
:::

:::caution[Open decision OD-28: Kubernetes baseline]
Which Kubernetes version and features the chart requires: native sidecars, the `restricted` Pod Security level, projected tokens, and a CNI that enforces NetworkPolicy. Whether OpenShift, which assigns user ids by namespace range, and sandboxed runtimes are supported. The proposal is native sidecars and `restricted` required, Cilium optional, and OpenShift and sandboxed runtimes outside the first release.
:::

## Testing

:::note[Proposed]
The shape is tested in three parts.

- **Smoke test on kind, on every pull request.** The chart installs under the `restricted` level. A first test confirms that `SO_PEERCRED` identifies the peer by user id across the containers of one pod. The rest checks the socket reachability matrix, the NetworkPolicy deny matrix, the edge, the probes, a rolling upgrade, and that the render contains no RBAC objects. It runs without cloud credentials, with a development profile.
- **A real EKS cluster in the development account,** as a job of the integration-test workflow that runs on the private runner, nightly and on release candidates, never on a pull request. It covers what kind cannot: the cluster's issuer into the cloud, key policies per role, IAM conditions on the state, and the queue.
- **A cluster outside AWS** joins the matrix when a key mode for it exists.
:::

:::caution[Open decision OD-25: Development profile for the Kubernetes smoke test]
A smoke test on kind has no KMS, and the floor refuses a software key on a production shape. Options: a `kubernetes-dev` shape profile that allows a software key, on the terms of the local shape; an emulated KMS; no signer in the smoke test. The proposal is the profile.
:::

:::caution[Open decision OD-27: Reaching a development EKS cluster from the integration job]
Options: a public API endpoint limited to the private runner's address; an ephemeral runner inside the development network against a private endpoint. The proposal is the first, if the runner's address is stable.
:::

## Limits and residual risk

- Identity is as strong as the cluster. A cluster administrator, a compromised node or a permissive admission policy can mint a token for any audience of a ServiceAccount, or open a shell in a container. A cluster is one trust domain.
- A network policy and the cloud's view of the pod are per pod. All containers of a pod share its egress allow-list, so the allow-list is the union of what every container needs. An integration with a long list of hosts is the candidate for its own pod (OD-29).
- Separation between containers depends on unique user ids, on no container being able to change its user id, and on `shareProcessNamespace` being false. The generator and the tests enforce all three, and `SO_PEERCRED` behaviour has to be confirmed on every container runtime that is claimed.
- A pod has many containers, one for each role and each driver. Memory and start-up time are measured, not assumed.
- Do not run the install that signs in to a cluster on that same cluster without the [recovery](/concepts/audit/) path, which needs no Kubernetes.
- A `postgres` index adds a stateful dependency.
