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
| <a id="od-6"></a>OD-6 | Diagram tool | LikeC4 generated from the model; or Mermaid only | Open |

## Decided

Each row says what was decided. The anchors stay, so links to a decided point still land on its row.

| Id | Question | Decision |
|---|---|---|
| <a id="od-1"></a>OD-1 | Name of the process that speaks the OIDC protocol | `entry` |
| <a id="od-2"></a>OD-2 | Name of the node agent | `sluis-node` |
| <a id="od-3"></a>OD-3 | Derived signing keys on hardware shapes | (a) always sign directly with the non-extractable key |
| <a id="od-4"></a>OD-4 | Which TCP ports exist between nodes | Raft driver port only |
| <a id="od-5"></a>OD-5 | Audit outbox port per shape | Local spool on self-hosted shapes (co-signed in cluster mode), SQS on Lambda; Kubernetes later |
| <a id="od-7"></a>OD-7 | Whether the test matrix includes Kubernetes now | Later |
| <a id="od-8"></a>OD-8 | Key for the local shape when a YubiKey runs in Docker | (c) `key-holder` runs natively on the host and talks to the host's `pcscd`; the container mounts only its Unix socket. Fallback: (a) mount the host `pcscd` socket |
| <a id="od-9"></a>OD-9 | How CI reaches the hardware boards | Design agreed, work in the CI backlog |
| <a id="od-10"></a>OD-10 | Policy language | Cedar; the `policy` role in Rust with `cedar-policy` |
| <a id="od-11"></a>OD-11 | Role of TPM 2.0 | TPM is a backlog feature |
| <a id="od-12"></a>OD-12 | Index host on Lambda | (a) T5 ships no audit index; `audit-read` and its index run in a separate audit install (Kubernetes with Postgres, or a server with sqlite) that reads the same bucket |
| <a id="od-13"></a>OD-13 | Derived key for bursts on YubiKey shapes | Closed by OD-3, no burst key |
| <a id="od-14"></a>OD-14 | Rust for the AWS, GCP and Azure drivers | Rust for the cloud drivers |
| <a id="od-15"></a>OD-15 | RS256 for specific audiences (verifiers that can't take EC) | (a) per-audience RS256 exception with its own RSA key |
| <a id="od-16"></a>OD-16 | Name of the v2 command-line tool | (a) one `sluis` binary |
