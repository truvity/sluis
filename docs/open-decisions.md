---
title: Open decisions
description: The points of the sluis v2 design that are still open, with their options and lean, and the points the maintainer has decided. Pages link here by id.
status: target
---

The maintainer has decided some points of the v2 design and not others. This page lists both. A page that depends on a point that is **open** carries an "Open decision" note with the same id. A point that is **decided** is written on the pages as plain fact and leaves the notes; its row moves to the second table, with the decision. A decision that changes the architecture also becomes an [architecture decision record](/decisions/).

## Open

No point is open at the moment. The last open points, [OD-24](#od-24) and [OD-26](#od-26) to [OD-31](#od-31), were decided on 2026-10-30, and their rows are in the table below. A new open point gets the next id and a row here, with its options and the **current lean**, which is where the design points and is not a decision.

## Decided

Each row says what was decided. The anchors stay, so links to a decided point still land on its row.

| Id | Question | Decision |
|---|---|---|
| <a id="od-1"></a>OD-1 | Name of the process that speaks the OIDC protocol | `entry` |
| <a id="od-2"></a>OD-2 | Name of the node agent | `sluis-node` |
| <a id="od-3"></a>OD-3 | Derived signing keys on hardware installations | (a) always sign directly with the non-extractable key |
| <a id="od-4"></a>OD-4 | Which TCP ports exist between nodes | Raft driver port only. Since 2026-10-30 that is one Raft port per rqlite cluster, one cluster per (boundary, owning actor), each over mTLS |
| <a id="od-5"></a>OD-5 | Audit transport per installation | Read as installation defaults: a local spool on the bare-metal installations (local and boards; co-signed by peer witnesses in cluster mode), SQS on serverless; SQS on Kubernetes for now, see [OD-22](#od-22). The remaining part was settled by [OD-34](#od-34) |
| <a id="od-6"></a>OD-6 | Diagram tool | Mermaid now; LikeC4 generated from the model once the permission manifest exists |
| <a id="od-7"></a>OD-7 | Whether the test matrix includes Kubernetes now | Kubernetes is in design and test scope now (the kubernetes installation); it is built after local, in parallel with boards and lambda |
| <a id="od-8"></a>OD-8 | Key for the local installation when a YubiKey runs in Docker | (a) The container's `key-holder` uses the host's `pcscd` socket, mounted into it. Fallback: (c) `key-holder` runs natively on the host and talks to the host's `pcscd`; the containers mount only its Unix socket |
| <a id="od-9"></a>OD-9 | How CI reaches the hardware boards | Design agreed, work in the CI backlog |
| <a id="od-10"></a>OD-10 | Policy language | Cedar; the `policy` actor in Rust with `cedar-policy` |
| <a id="od-11"></a>OD-11 | Role of TPM 2.0 | TPM is a backlog feature |
| <a id="od-12"></a>OD-12 | Index host on Lambda | Revised: serverless may run the indexer and the reader with a `postgres` index (RDS or Aurora through RDS Proxy, proposed). The earlier answer, (a) no index inside serverless, is withdrawn. The lambda installation's default index stays `none`; that default is proposed. Where Search runs in general is derived from the port and driver rules, see [OD-32](#od-32) |
| <a id="od-13"></a>OD-13 | Derived key for bursts on YubiKey installations | Closed by OD-3, no burst key |
| <a id="od-14"></a>OD-14 | Rust for the AWS, GCP and Azure drivers | Rust for the cloud drivers |
| <a id="od-15"></a>OD-15 | RS256 for specific audiences (verifiers that can't take EC) | (a) per-audience RS256 exception with its own RSA key |
| <a id="od-16"></a>OD-16 | Name of the v2 command-line tool | (a) one `sluis` binary. The ceremonies are `sluis ceremony …` subcommands; their OpenPGP-card part is a Rust helper under `/usr/libexec/sluis/`, shipped inside the `sluis` package, so there is no second tool (decided 2026-10-30; [ADR 0060](/decisions/0060-the-language-follows-the-executable/)) |
| <a id="od-17"></a>OD-17 | Pod layout on Kubernetes | (c) a node pod plus a separate audit side; the audit side is a separate audit installation ([OD-31](#od-31), decided 2026-10-30) |
| <a id="od-20"></a>OD-20 | Key custody on clusters outside AWS | Read as an installation default: for now the kubernetes runtime supports AWS KMS only (EKS) |
| <a id="od-21"></a>OD-21 | State driver on Kubernetes | For now the kubernetes runtime supports DynamoDB only for state |
| <a id="od-22"></a>OD-22 | Audit transport on Kubernetes (the remaining part of OD-5) | Read as an installation default: for now SQS is the audit transport on Kubernetes |
| <a id="od-23"></a>OD-23 | Notary on Kubernetes | Closed by [OD-39](#od-39): there is no notary actor, so nothing is placed. On Kubernetes the writer asks `key-holder` to sign checkpoints, as elsewhere |
| <a id="od-32"></a>OD-32 | Where Search runs, and where every actor runs | (a) Where an actor runs is derived from the port and driver rules, for Search (the indexer and the reader) and for every sluis actor. There are no per-installation placement decisions: `sluis config check` enforces the rules and each installation only names its default drivers. For example, with a `sqlite` index on a cluster the reader runs on the indexer's node. See [Ports and drivers](/concepts/drivers/) |
| <a id="od-33"></a>OD-33 | Quota: enforcement or metering | (c) Metering by default, with enforcement per policy rule for chosen subjects or audiences. Enforcement needs `state` with compare-and-set on the mint path. The earlier decision that strict quota comes only after the k6 load test still applies to turning enforcement on in production |
| <a id="od-34"></a>OD-34 | Transport per installation | (a) The defaults stay as they were: the spool on the bare-metal installations, SQS on serverless and Kubernetes. The `direct` and `nats-jetstream` drivers are supported and are the default of no installation |
| <a id="od-35"></a>OD-35 | Direct mode when the writer is down | (b) The emitter falls back to a local spool. Consequence: direct mode needs a disk, so it is not available on serverless, which fits the compatibility table on [Ports and drivers](/concepts/drivers/) |
| <a id="od-36"></a>OD-36 | Witness optional or required | (c) Optional everywhere; `sluis config check` warns when an installation has none. Documented risk, together with [OD-39](#od-39): with no witness and no notary, a compromised writer can get a rewritten checkpoint signed. A compliance lock on s3 still prevents overwriting objects, but not a split view |
| <a id="od-37"></a>OD-37 | Audit's driver instances, together with OD-38 | Audit is four separate pieces: the **emitter** (a library inside each end application, configured by that application, not a process), the **log** (writer, blob and witness), **Search** (indexer, index and reader) and **Quota** (quota engine and state). Transport is a port, not a piece. Each piece can be its own installation. When pieces are combined in one installation, even with sluis, they share driver instances. This replaces the earlier lean of separate instances |
| <a id="od-38"></a>OD-38 | Quota: a separate product, or an actor, together with OD-37 | Resolved by the four pieces of [OD-37](#od-37): Quota is a separate piece, combinable with sluis in one installation, where it shares driver instances |
| <a id="od-39"></a>OD-39 | Audit actors | (a) No notary actor. The writer asks `key-holder` to sign checkpoints, and the `key-holder` access list lets only the writer use the log key. The audit actors are the writer, the witness, the indexer and the reader. A cluster keeps one log per node: each node has its own writer, spool and log key, the other nodes are its peer witnesses, and readers merge the logs by time. The rule is exactly one writer per log, not one per installation |
| <a id="od-18"></a>OD-18 | What "unit" means in the fusion rule on Kubernetes | The container, with a generated check that no container holding a key credential exposes a listener (decided 2026-10-30; recorded in ADR 0046) |
| <a id="od-19"></a>OD-19 | Cloud identity for pods | Projected ServiceAccount tokens per container by default, EKS Pod Identity for single-actor pods, `sluis-node` only as a fallback (decided 2026-10-30; recorded in ADR 0046) |
| <a id="od-24"></a>OD-24 | Edge on Kubernetes | (a) A key-less edge relay container in the pod, on the pod address, forwards to `entry`'s Unix socket. A `cloudflared` sidecar is a supported mode. `entry` is not bound to the pod address (decided 2026-10-30) |
| <a id="od-25"></a>OD-25 | Development profile for the Kubernetes smoke test | The chart's `kubernetes-dev` preset, which allows a software key on the same terms as the local installation (decided 2026-10-30 with the one chart, [OD-30](#od-30); [ADR 0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/); confirmed 2026-10-30) |
| <a id="od-26"></a>OD-26 | How a console reaches the reader in a separate audit install | (a) The audit installation runs its own `entry`, and the console calls it with a short-lived token. Audit gets the same split as sluis: a slim router `entry`, the API (`reader`) and its own console. The details go to the audit records (decided 2026-10-30) |
| <a id="od-27"></a>OD-27 | How the integration-test job reaches a development EKS cluster | (b) A short-lived runner inside the development VPC, against a private API endpoint (decided 2026-10-30) |
| <a id="od-28"></a>OD-28 | Kubernetes baseline | (a) Native sidecars, the `restricted` Pod Security level and a CNI that enforces NetworkPolicy are required. Cilium is optional, for egress by FQDN. OpenShift and sandboxed runtimes (gVisor, Kata) are not in the first cut (decided 2026-10-30) |
| <a id="od-29"></a>OD-29 | Placement of integrations on Kubernetes | (b) A pod per integration, with its own egress NetworkPolicy (decided 2026-10-30) |
| <a id="od-31"></a>OD-31 | Audit as a separate installation: the co-located profile | Audit is its own installation, and sluis emits over TCP or a queue through the transport port. The points [OD-32](#od-32) to [OD-39](#od-39) refine it, and an emitter is authenticated by the transport driver's own layer ([ADR 0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). (a) The co-located profile for a small install shares the installation's driver instances, as [OD-37](#od-37) says for combined pieces; the defence-in-depth layers stay per actor, unchanged (decided 2026-10-30) |
| <a id="od-30"></a>OD-30 | One chart or two | One `sluis` chart with a piece selector and the presets `install`, `audit-install`, `quota-install` and `kubernetes-dev`, and no subcharts. The cert-manager issuer has a chart of its own. The chart's version is the release version (decided 2026-10-30; [ADR 0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)) |
