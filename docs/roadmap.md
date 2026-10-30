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

## Delivery order

The maintainer agreed this order for the tested [installations](/runtimes/installations/) on 2026-10-29. It replaces the earlier plans that made an EC2 cluster, and then boards, the first build target.

1. **local first.** Docker Compose on a workstation, one container per actor, with the minimal drivers: embedded SQLite state, a SQLite index, a files blob, a local lock, and a spool or direct transport. Keys are software keys in CI and the host's YubiKey on a workstation. This step runs until v2 can replace v1, and it includes the enablers of the target architecture.
2. **Then boards, lambda and kubernetes in parallel,** built and tested in parallel.
   - **boards.** Three single-board computers behind a Cloudflare tunnel, a YubiKey in each node, state and index on rqlite (the rqlite index is a proposed new driver), audit records in a Cloudflare R2 bucket, the audit pieces on the same nodes, and one log per node with the other nodes as peer witnesses.
   - **lambda.** AWS Lambda.
   - **kubernetes.** The one tested Kubernetes installation.

EC2 is no longer a delivery target. A virtual machine is still bare-metal, so an EC2 installation stays possible, under the same rules, but it is not tested. Installations other than these four are possible and untested.

Packaging follows the runtime, not a build profile: `.deb` and `.rpm` packages split by function and a tarball for bare-metal, a function per actor and a zip per executable for serverless, and one OCI image per process with one Helm chart for kubernetes. `dev` is not a build.

## Spikes before code

The language of each executable is fixed from a set of **spikes**, run before the first code of step 1 (agreed 2026-10-30, [ADR 0060](/decisions/0060-the-language-follows-the-executable/)). Every Rust choice on [Source layout](/architecture/source-layout/#languages) is gated by its spike, and falls back to Go if the spike fails.

| Spike | What it settles |
|---|---|
| S1 | Connect for Rust over Unix sockets: interoperation with connect-go in both directions, peer credentials, the default-deny interceptor, streaming |
| S2 | FIPS with `aws-lc-rs`: builds for amd64 and arm64, a certified module pin, the AWS SDK for Rust in FIPS mode |
| S3 | A slim `entry` prototype in both languages: latency, memory, Lambda cold start and size, and a day of fuzzing |
| S4 | `key-holder` through PKCS#11 (`cryptoki` with `ykcs11`): every algorithm and slot on [Keys](/concepts/keys/), concurrent callers, PIN handling |
| S5 | The memory budget on the boards: each actor and driver under load, in each language, against the 2 GB budget |
| S6 | OpenTelemetry from Rust: one trace across a Go and Rust hop |
| S7 | Configuration in Rust: the same conformance vectors as the Go structs |
| S8 | The ceremony with OpenPGP cards: two primaries and a shared subkey, verified with the tools tested on 2026-10-29 |
| S10 | One release train: GoReleaser with a Cargo workspace, for two architectures, with SBOM, signing and provenance, and Lambda zips within their limits |

## Architecture decision records

The first batch of decision records is accepted: ADRs 0001 to 0011 are accepted. Batch 2 is in progress. Batches of more than five records are split into two pull requests. The list is on [Decisions](/decisions/).

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
| [#877](https://github.com/truvity/sluis/issues/877) | Installation test harness (local, boards, lambda, kubernetes) |
| [#891](https://github.com/truvity/sluis/issues/891) | CI: integration tests on a private runner group (boards and AWS test installations) |
| [#892](https://github.com/truvity/sluis/issues/892) | Policy actor in Rust with Cedar (cedar-policy crate) |

### Phase 1: keys, identity and configuration

Key custody, platform checks, workload identity, and the measurements that settle the open key questions.

| Issue | Item |
|---|---|
| [#865](https://github.com/truvity/sluis/issues/865) | Pkl to JSON Schema generator as a separate open-source tool |
| [#867](https://github.com/truvity/sluis/issues/867) | `sluis doctor` platform checks |
| [#868](https://github.com/truvity/sluis/issues/868) | FIPS builds with the Go FIPS 140-3 module |
| [#913](https://github.com/truvity/sluis/issues/913) | The `ca` actor: X.509 and SSH certificate authorities |
| [#914](https://github.com/truvity/sluis/issues/914) | `sluis ceremony`: semi-automatic key ceremonies, card slot map and node PIN unlock |
| [#869](https://github.com/truvity/sluis/issues/869) | JOSE package and per-language verification demos |
| [#870](https://github.com/truvity/sluis/issues/870) | Workload identity with a per-node issuer and AWS exchange |
| [#878](https://github.com/truvity/sluis/issues/878) | Benchmark: AWS KMS Sign latency for ECC P-384 |
| [#879](https://github.com/truvity/sluis/issues/879) | Benchmark: YubiKey firmware 5.8 P-384 sign and ECDH on an arm64 board |
| [#880](https://github.com/truvity/sluis/issues/880) | Spike: memory footprint of Go and Rust cloud drivers on t4g.nano (closed: Rust for the cloud drivers is decided; the board budget is spike S5) |

### Phase 1b: features beyond parity

New v2 features that sit on the foundation. They carry the label `type:feature` and sit on the [v2 board](https://github.com/orgs/truvity/projects/9).

| Issue | Item |
|---|---|
| [#885](https://github.com/truvity/sluis/issues/885) | Passkeys: a generic WebAuthn relying party, and step-up for high-privilege grants |
| [#912](https://github.com/truvity/sluis/issues/912) | FAPI 2.0 security profile: PAR, DPoP and `private_key_jwt` client authentication |
| [#915](https://github.com/truvity/sluis/issues/915) | Quota: engine, quota state, metering and enforcement, Idempotency-Key |
| [#916](https://github.com/truvity/sluis/issues/916) | Generic OIDC upstream integration with `acr`/`amr` passthrough and step-up (RFC 9470) |
| [#917](https://github.com/truvity/sluis/issues/917) | Connect client library for TypeScript (connect-es) for the public API |

### Phase 2: release and distribution

How a release is built, signed, published and installed.

| Issue | Item |
|---|---|
| [#873](https://github.com/truvity/sluis/issues/873) | Distribution site on R2 |
| [#874](https://github.com/truvity/sluis/issues/874) | Release signing and CI upload credentials |
| [#875](https://github.com/truvity/sluis/issues/875) | Serverless packaging: a zip per actor |
| [#876](https://github.com/truvity/sluis/issues/876) | Kubernetes packaging: OCI images and Helm charts with digest-pinned images |
| [#881](https://github.com/truvity/sluis/issues/881) | Board OS images for Ubuntu 26.04 and Rocky Linux 10.2 (boards installation) |
| [#888](https://github.com/truvity/sluis/issues/888) | Release OpenPGP key ceremony on two YubiKeys (guide and tests) |
| [#889](https://github.com/truvity/sluis/issues/889) | Research: publishing and verifying release keys (keyservers, WKD, DNS OPENPGPKEY, verifier tools) |
| [#890](https://github.com/truvity/sluis/issues/890) | Artifact signing service: OpenPGP subkeys and short-lived X.509 signing certificates behind federated identity (needs a decision) |

### Later

| Issue | Item |
|---|---|
| [#871](https://github.com/truvity/sluis/issues/871) | GCP Workload Identity Federation exchange |
| [#872](https://github.com/truvity/sluis/issues/872) | Azure Entra federated credential exchange |
| [#882](https://github.com/truvity/sluis/issues/882) | Site explorers read the permission manifest |
| [#918](https://github.com/truvity/sluis/issues/918) | SAML 2.0 identity provider and SCIM provisioning for AWS IAM Identity Center |
