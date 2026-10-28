---
title: Open decisions
description: The points of the sluis v2 design that are not settled yet, each with its options and the current lean. Pages link here by id.
status: target
---

These are the points of the v2 design that the maintainer has not decided. A page that depends on one carries an "Open decision" note with the same id, and a decision that is made moves into an [architecture decision record](/decisions/) and leaves this table.

The **current lean** is where the design points today. It is not a decision.

| Id | Question | Options | Current lean |
|---|---|---|---|
| <a id="od-1"></a>OD-1 | Name of the central process | coordinator, central, hub, and others | A placeholder, `central` |
| <a id="od-2"></a>OD-2 | Name of the node agent | Not yet listed | A placeholder |
| <a id="od-3"></a>OD-3 | Derived signing keys on hardware shapes | (a) always direct; (b) always derived; (c) direct on KMS, derived only for bursts on YubiKey | (c), pending benchmarks |
| <a id="od-4"></a>OD-4 | Which TCP ports exist between nodes | Only the Raft driver port over mTLS; or that plus a role port | Raft driver only |
| <a id="od-5"></a>OD-5 | Audit outbox port per shape | Spool; queue; blocking | Spool for self-hosted shapes, queue for Lambda |
| <a id="od-6"></a>OD-6 | Diagram tool | LikeC4 generated from the model; or Mermaid only | Open |
| <a id="od-7"></a>OD-7 | Whether the test matrix includes Kubernetes now | Yes; or later | Later |
| <a id="od-8"></a>OD-8 | Key for the local shape when a YubiKey runs in Docker | The host `pcscd` socket; or software only | The `pcscd` socket |
| <a id="od-9"></a>OD-9 | How CI reaches the hardware boards | A private network (VPN); or a self-hosted runner on a board | A private network |
| <a id="od-10"></a>OD-10 | Policy language | Cedar; or an own file format | Open |
| <a id="od-11"></a>OD-11 | Role of TPM 2.0 | An optional add-on; or not supported | An optional add-on |
| <a id="od-12"></a>OD-12 | Index host on Lambda | Aurora Serverless v2; or DynamoDB only | Aurora, to be verified |
| <a id="od-13"></a>OD-13 | Derived key for bursts on YubiKey shapes | See OD-3 | None yet |
| <a id="od-14"></a>OD-14 | Rust for the AWS, GCP and Azure drivers | Rust; or Go | A memory measurement decides |
| <a id="od-15"></a>OD-15 | RS256 for specific audiences (verifiers that can't take EC) | (a) per-audience exception with a separate RSA key; (b) no RS256, verifiers must take ES256/ES384 | Open |
| <a id="od-16"></a>OD-16 | Name of the v2 command-line tool | (a) one `sluis` binary with subcommands for operators and users; (b) `sluis` for operators and `sluisctl` for users | Open |
