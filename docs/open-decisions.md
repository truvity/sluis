---
title: Open decisions
description: The points of the sluis v2 design that are still open, with their options and lean, and the points the maintainer has decided. Pages link here by id.
status: target
---

The maintainer has decided some points of the v2 design and not others. This page lists both. A page that depends on a point that is **open** carries an "Open decision" note with the same id. A point that is **decided** is written on the pages as plain fact and leaves the notes; its row moves to the second table, with the decision. A decision that changes the architecture also becomes an [architecture decision record](/decisions/).

## Open

The **current lean** is where the design points today. It is not a decision.

| Id | Question | Options | Current lean |
|---|---|---|---|
| <a id="od-18"></a>OD-18 | What "unit" means in the fusion rule on Kubernetes | The pod; the container (credential scope) | The container, with a generated check that no container with a key credential exposes a listener |
| <a id="od-19"></a>OD-19 | Cloud identity for pods | EKS Pod Identity per pod; per-container projected tokens with one audience per actor; `sluis-node` per-actor JWTs in the pod; a mix | Projected tokens per container (default), Pod Identity for single-role pods, `sluis-node` only as a fallback |
| <a id="od-23"></a>OD-23 | Notary on Kubernetes | Co-signing across replicas; a dedicated notary with its own key | Dedicated notary in the audit pod |
| <a id="od-24"></a>OD-24 | Edge on Kubernetes | `cloudflared` sidecar; a key-less edge relay sidecar on the pod IP; `entry` binds the pod IP | Edge relay as the default, `cloudflared` as a supported mode; `entry` stays on 127.0.0.1 |
| <a id="od-25"></a>OD-25 | Development profile for the Kubernetes smoke test | A `kubernetes-dev` preset with a software key; an emulated KMS; no smoke test of the signer | A `kubernetes-dev` preset on the same terms as T0 |
| <a id="od-26"></a>OD-26 | How a console reaches `audit-read` in a separate audit install | The audit install has its own `entry` with `audit-read`; a remote plugin call with a token | An `entry` in the audit install |
| <a id="od-27"></a>OD-27 | How the integration-test job reaches a development EKS cluster | Public API endpoint limited to the runner's address; an ephemeral runner in the development VPC against a private endpoint | The first if the runner's address is stable |
| <a id="od-28"></a>OD-28 | Kubernetes baseline | Minimum version and features required (native sidecars, `restricted` PodSecurity, projected tokens, a NetworkPolicy-enforcing CNI); optional Cilium for FQDN; OpenShift and sandboxed runtimes supported or not | Native sidecars and `restricted` required; Cilium optional; OpenShift and gVisor/Kata not in the first cut |
| <a id="od-29"></a>OD-29 | Placement of integrations on Kubernetes | Containers of the node pod; separate pods per integration; a manifest switch per integration | Containers now; a `placement` switch designed, built when an integration's egress or lifecycle needs it |
| <a id="od-30"></a>OD-30 | One chart or two | One chart with an `install` and an `audit-install` profile; a chart for sluis and a chart for the audit install | One chart |
| <a id="od-31"></a>OD-31 | Audit as a separate installation: actors, transport from emitters, emitter authentication, co-located profile | Being designed; refined by [OD-32](#od-32) to [OD-39](#od-39) | Audit is its own installation; sluis emits over TCP or a queue through the transport port |
| <a id="od-32"></a>OD-32 | Where Search runs | On the log host; a separate host or Deployment; nowhere (the scan instead). It is decided that it does not run inside serverless | No lean yet |
| <a id="od-33"></a>OD-33 | Quota: enforcement or metering | Enforcement needs live state with compare-and-set. Metering can be a view derived from the log | No lean yet |
| <a id="od-34"></a>OD-34 | Transport per preset | Direct or queue, and where NATS JetStream fits (clusters, or Kubernetes instead of SQS) | No lean yet |
| <a id="od-35"></a>OD-35 | Direct mode when the writer is down | (a) fail the operation; (b) fall back to a local spool; (c) an in-memory buffer, which is unsafe | No lean yet |
| <a id="od-36"></a>OD-36 | Witness optional or required | Optional; required | No lean yet |
| <a id="od-37"></a>OD-37 | Audit's driver instances | Separate instances under audit's own identities (proposed) | Separate instances under audit's own identities |
| <a id="od-38"></a>OD-38 | Quota: a separate product, or an actor | A separate product; an actor | No lean yet |
| <a id="od-39"></a>OD-39 | Audit actors | The writer, indexer, reader and witness replace the notary, audit-writer and audit-read (proposed). The notary is folded into the `key-holder` ACL, or kept as a thin checker | The writer, indexer, reader and witness |

## Decided

Each row says what was decided. The anchors stay, so links to a decided point still land on its row.

| Id | Question | Decision |
|---|---|---|
| <a id="od-1"></a>OD-1 | Name of the process that speaks the OIDC protocol | `entry` |
| <a id="od-2"></a>OD-2 | Name of the node agent | `sluis-node` |
| <a id="od-3"></a>OD-3 | Derived signing keys on hardware presets | (a) always sign directly with the non-extractable key |
| <a id="od-4"></a>OD-4 | Which TCP ports exist between nodes | Raft driver port only |
| <a id="od-5"></a>OD-5 | Audit transport per preset | Local spool on the bare-metal presets (co-signed in cluster mode), SQS on serverless; SQS on Kubernetes for now, see [OD-22](#od-22). The remaining part is [OD-34](#od-34) |
| <a id="od-6"></a>OD-6 | Diagram tool | Mermaid now; LikeC4 generated from the model once the permission manifest exists |
| <a id="od-7"></a>OD-7 | Whether the test matrix includes Kubernetes now | Kubernetes is in design and test scope now (T6) |
| <a id="od-8"></a>OD-8 | Key for the local preset (T0) when a YubiKey runs in Docker | (c) `key-holder` runs natively on the host and talks to the host's `pcscd`; the container mounts only its Unix socket. Fallback: (a) mount the host `pcscd` socket |
| <a id="od-9"></a>OD-9 | How CI reaches the hardware boards | Design agreed, work in the CI backlog |
| <a id="od-10"></a>OD-10 | Policy language | Cedar; the `policy` actor in Rust with `cedar-policy` |
| <a id="od-11"></a>OD-11 | Role of TPM 2.0 | TPM is a backlog feature |
| <a id="od-12"></a>OD-12 | Index host on Lambda | (a) T5 ships no audit index; `audit-read` and its index run in a separate audit install (Kubernetes with Postgres, or a server with sqlite) that reads the same bucket. Where Search runs in general is [OD-32](#od-32) |
| <a id="od-13"></a>OD-13 | Derived key for bursts on YubiKey presets | Closed by OD-3, no burst key |
| <a id="od-14"></a>OD-14 | Rust for the AWS, GCP and Azure drivers | Rust for the cloud drivers |
| <a id="od-15"></a>OD-15 | RS256 for specific audiences (verifiers that can't take EC) | (a) per-audience RS256 exception with its own RSA key |
| <a id="od-16"></a>OD-16 | Name of the v2 command-line tool | (a) one `sluis` binary |
| <a id="od-17"></a>OD-17 | Pod layout on Kubernetes | (c) a node pod plus a separate audit side; the audit side is being redesigned as a separate audit installation (see the open audit-installation question, [OD-31](#od-31)) |
| <a id="od-20"></a>OD-20 | Key custody on clusters outside AWS | For now the kubernetes environment supports AWS KMS only (EKS) |
| <a id="od-21"></a>OD-21 | State driver on Kubernetes | For now the kubernetes environment supports DynamoDB only for state |
| <a id="od-22"></a>OD-22 | Audit transport on Kubernetes (the remaining part of OD-5) | For now SQS is the audit transport on Kubernetes |
