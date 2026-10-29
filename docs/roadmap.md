---
title: Roadmap
description: Where sluis is going. The roadmap is two public GitHub Project boards, one for v2 and one for v1; this page links to both and lists the v2 technical backlog.
status: shipped
---

The roadmap lives in two public GitHub Projects under the `truvity` organization:

- **sluis v2**: [github.com/orgs/truvity/projects/9](https://github.com/orgs/truvity/projects/9). The line built on `main`.
- **sluis v1**: [github.com/orgs/truvity/projects/10](https://github.com/orgs/truvity/projects/10). The agreed v1 roadmap (v1.80 to v1.85) and fixes, on `master`.

## Goal views of the v2 board

The v2 board has a multi-select project field `Goal`, and its views are filters on that field. Items are being tagged, so a view fills in as the tagging proceeds.

- [Roadmap](https://github.com/orgs/truvity/projects/9/views/1): every item on the board, in the order planned.
- [Replace v1](https://github.com/orgs/truvity/projects/9/views/2): what v2 needs to take over from v1, feature for feature.
- [Replace v1 + SSH & DB](https://github.com/orgs/truvity/projects/9/views/3): the same, plus SSH access and database access.
- [Customer identity (CIAM)](https://github.com/orgs/truvity/projects/9/views/4): the items for using sluis as the sign-in service of a product's customers.
- [Self-hosted on boards](https://github.com/orgs/truvity/projects/9/views/5): the items for running sluis on single-board computers.
- [Conformance](https://github.com/orgs/truvity/projects/9/views/6): the items for standards conformance and the tests that prove it.
- [Foundation](https://github.com/orgs/truvity/projects/9/views/7): the technical backlog underneath all of it (build, patterns, kits, configuration, keys, packaging).

v1 keeps its own roadmap while v2 is built alongside it. New feature requests target v2. An issue about v1 carries the label `line:v1`; an issue without it is about v2.
The reasoning is in [ADR 0001](https://github.com/truvity/sluis/blob/main/docs/decisions/0001-v1-and-v2-lines.md).

The [features page](/features/) lists what v1 does and where each capability stands in v2. Those rows are tracked as issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity).

Planned work is also tracked as [issues](https://github.com/truvity/sluis/issues) and milestones in the repository.

## Technical backlog

Parity says what v2 must do. The technical backlog says what has to exist underneath it: the build, the process and driver patterns, the shared kits, configuration, keys and identity, packaging and distribution. All of it carries the label [`track:foundation`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Atrack%3Afoundation) and sits on the [v2 board](https://github.com/orgs/truvity/projects/9).

Items that decide an open question (a benchmark or a spike) carry `type:decision`; the open questions are listed on the open decisions page.

### Phase 0: patterns

The shape of the repository, the build, and the patterns every component follows.

| Issue | Item |
|---|---|
| [#859](https://github.com/truvity/sluis/issues/859) | Source layout scaffold |
| [#860](https://github.com/truvity/sluis/issues/860) | Build system: one repository version, GoReleaser v2, per-architecture jobs |
| [#861](https://github.com/truvity/sluis/issues/861) | Shared kits for Go, Rust and TypeScript with a conformance suite |
| [#862](https://github.com/truvity/sluis/issues/862) | Driver IPC pattern with platform-started services |
| [#863](https://github.com/truvity/sluis/issues/863) | Permission manifest format and generators |
| [#864](https://github.com/truvity/sluis/issues/864) | Configuration from Pkl to JSON Schema with strict validation |
| [#866](https://github.com/truvity/sluis/issues/866) | systemd unit hardening baseline |
| [#877](https://github.com/truvity/sluis/issues/877) | Test presets harness |

### Phase 1: keys, identity and configuration

Key custody, platform checks, workload identity, and the measurements that settle the open key questions.

| Issue | Item |
|---|---|
| [#865](https://github.com/truvity/sluis/issues/865) | Pkl to JSON Schema generator as a separate open-source tool |
| [#867](https://github.com/truvity/sluis/issues/867) | `sluis doctor` platform checks |
| [#868](https://github.com/truvity/sluis/issues/868) | FIPS builds with the Go FIPS 140-3 module |
| [#869](https://github.com/truvity/sluis/issues/869) | JOSE package and per-language verification demos |
| [#870](https://github.com/truvity/sluis/issues/870) | Workload identity with a per-node issuer and AWS exchange |
| [#878](https://github.com/truvity/sluis/issues/878) | Benchmark: AWS KMS Sign latency for ECC P-384 |
| [#879](https://github.com/truvity/sluis/issues/879) | Benchmark: YubiKey firmware 5.8 P-384 sign and ECDH on an arm64 board |
| [#880](https://github.com/truvity/sluis/issues/880) | Spike: memory footprint of Go and Rust cloud drivers on t4g.nano |

### Phase 2: release and distribution

How a release is built, signed, published and installed.

| Issue | Item |
|---|---|
| [#873](https://github.com/truvity/sluis/issues/873) | Distribution site on R2 |
| [#874](https://github.com/truvity/sluis/issues/874) | Release signing and CI upload credentials |
| [#875](https://github.com/truvity/sluis/issues/875) | Lambda packaging as zip per actor |
| [#876](https://github.com/truvity/sluis/issues/876) | OCI images and Helm charts with digest-pinned images |
| [#881](https://github.com/truvity/sluis/issues/881) | Board OS images for Ubuntu 26.04 and Rocky Linux 10.2 |

### Later

| Issue | Item |
|---|---|
| [#871](https://github.com/truvity/sluis/issues/871) | GCP Workload Identity Federation exchange |
| [#872](https://github.com/truvity/sluis/issues/872) | Azure Entra federated credential exchange |
| [#882](https://github.com/truvity/sluis/issues/882) | Site explorers read the permission manifest |
