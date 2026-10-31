---
title: Roadmap
description: Where sluis is going. The roadmap is two public GitHub Project boards, one for v2 and one for v1; this page links to both and lists the v2 technical backlog.
status: shipped
---

The [project status](/status/) page shows where v2 stands, milestone by milestone: requirements, decision records and issues, grouped by the board's `Product milestone` field and rebuilt with the site.

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
   - **boards.** Three single-board computers behind a Cloudflare tunnel, a YubiKey in each node, state and index on rqlite ([ADR 0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)), audit records on R2 in the first release (Garage, an S3-compatible store on the boards, is a later backend, blocked on conditional writes and prefix scoping upstream: [#982](https://github.com/truvity/sluis/issues/982)), the audit pieces on the same nodes, and one log per node with the other nodes as peer witnesses.
   - **lambda.** AWS Lambda.
   - **kubernetes.** The one tested Kubernetes installation.

EC2 is no longer a delivery target. A virtual machine is still bare-metal, so an EC2 installation stays possible, under the same rules, but it is not tested. Installations other than these four are possible and untested.

Packaging follows the runtime, not a build profile: `.deb` and `.rpm` packages split by function, with a package per driver executable and meta-packages per installation, and a tarball for bare-metal, a function per actor and a zip per executable for serverless, and one OCI image per process with one Helm chart for kubernetes. `dev` is not a build.

## Spikes before code

The language of each executable is fixed from a set of **spikes**, run before the first code of step 1 (agreed 2026-10-30, [ADR 0060](/decisions/0060-the-language-follows-the-executable/)). Every Rust choice on [Source layout](/architecture/source-layout/#languages) is gated by its spike. When a spike fails, the maintainer decides the fallback for that executable at that time; the fallbacks listed in ADR 0060 are candidates, not commitments (decided 2026-10-30).

| Spike | What it settles |
|---|---|
| [S1](https://github.com/truvity/sluis/issues/939) | Connect for Rust over Unix sockets: interoperation with connect-go in both directions, peer credentials, the default-deny interceptor, streaming |
| [S2](https://github.com/truvity/sluis/issues/940) | FIPS with `aws-lc-rs`: builds for amd64 and arm64, a certified module pin, the AWS SDK for Rust in FIPS mode. **Done** (2026-10-30): passed with conditions. With no FIPS build in v2.0 (decided 2026-10-31), its result is kept for a later FIPS variant |
| [S3](https://github.com/truvity/sluis/issues/941) | A slim `entry` prototype in both languages: latency, memory, Lambda cold start and size, and a day of fuzzing |
| [S4](https://github.com/truvity/sluis/issues/942) | `key-holder` through PKCS#11 (`cryptoki` with `ykcs11`) against `piv-go`: every algorithm and slot on [Keys](/concepts/keys/), concurrent callers, PIN handling. **Done** for YubiKey PIV (2026-10-31): `key-holder` is Go with `piv-go`, which holds the card exclusively. The release-subkey slot, YubiHSM 2, Nitrokey and the zeroisation review are still to run |
| [S5](https://github.com/truvity/sluis/issues/943) | The memory budget on the boards: each actor and driver under load, in each language, against the 2 GB budget. **Done** (2026-10-31): the memory fits with Go or with Rust actors (about 985 or 680 MiB of a 1380 MiB ceiling on a tier-0 board), rqlite costs about 9 MiB per cluster, and the CPU, not the eMMC, limits writes first, at about 400 commits/s per board. The temperature, the estimated rows and the needed write rate are still open |
| [S6](https://github.com/truvity/sluis/issues/944) | OpenTelemetry from Rust: one trace across a Go and Rust hop. **Done** (2026-10-31): passed with conditions |
| [S7](https://github.com/truvity/sluis/issues/945) | Configuration in Rust: the same conformance vectors as the Go structs. **Done** (2026-10-31): passed with conditions |
| [S8](https://github.com/truvity/sluis/issues/946) | The ceremony with OpenPGP cards: two primaries and a shared subkey, verified with the tools tested on 2026-10-29. It decides the Rust card helper that `sluis ceremony` runs. **Done** (2026-10-31): the Rust helper (`openpgp-card` and rPGP) did every step on two cards, `gpg` cannot bind the shared subkey, and every verifier accepted ECDSA P-384. Its open points are in [ADR 0060](/decisions/0060-the-language-follows-the-executable/) |
| [S10](https://github.com/truvity/sluis/issues/947) | One release train: GoReleaser with a Cargo workspace, for two architectures, with SBOM, signing and provenance, and Lambda zips within their limits |

One more spike was run for the boards' blob store, not before step 1: **Garage on the boards** ([#982](https://github.com/truvity/sluis/issues/982)). **Done** (2026-10-31): Garage v2.5.0 has no conditional writes and no prefix-scoped keys, both open upstream, so the boards use R2 for blobs in the first release and Garage moves to Later.

## Architecture decision records

The records are on [Decisions](/decisions/), with their status. During the design phase the maintainer may rewrite them in place; on 2026-10-30 every record was realigned to one set of decisions. The audit records followed on 2026-10-31: the log ([ADR 0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/), Accepted since its record format was decided the same day: plain OCSF events, with a parallel usage log in CloudEvents for quota and billing), search ([ADR 0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)), quota ([ADR 0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)) and the audit installation's front ([ADR 0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/), built in [#985](https://github.com/truvity/sluis/issues/985)).

## Technical backlog

Parity says what v2 must do. The technical backlog says what has to exist underneath it: the build, the process and driver patterns, the shared kits, configuration, keys and identity, packaging and distribution. The foundation items carry the label [`track:foundation`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Atrack%3Afoundation), and the features beyond parity sit beside them. The tables below follow the `Phase` field of the [v2 board](https://github.com/orgs/truvity/projects/9) (checked 2026-10-30); the parity items are on the [features page](/features/).

Items that decide an open question (a benchmark or a spike) carry `type:decision`; the open questions are listed on the open decisions page.

### Phase 0: patterns and spikes

The shape of the repository, the build, the patterns every component follows, and the spikes that fix the language of each executable before the first code of step 1.

| Issue | Item |
|---|---|
| [#859](https://github.com/truvity/sluis/issues/859) | Source layout scaffold |
| [#860](https://github.com/truvity/sluis/issues/860) | Build system with one repository version, GoReleaser v2 and per-architecture jobs |
| [#861](https://github.com/truvity/sluis/issues/861) | Shared kits for Go, Rust and TypeScript with a conformance suite |
| [#862](https://github.com/truvity/sluis/issues/862) | Driver IPC pattern with platform-started services |
| [#863](https://github.com/truvity/sluis/issues/863) | Permission manifest format and generators |
| [#864](https://github.com/truvity/sluis/issues/864) | Configuration from Pkl to JSON Schema with strict validation |
| [#877](https://github.com/truvity/sluis/issues/877) | Test installations harness |
| [#939](https://github.com/truvity/sluis/issues/939) | Spike S1: Connect for Rust over Unix sockets |
| [#940](https://github.com/truvity/sluis/issues/940) | Spike S2: FIPS with aws-lc-rs |
| [#941](https://github.com/truvity/sluis/issues/941) | Spike S3: a slim entry prototype in Go and in Rust |
| [#942](https://github.com/truvity/sluis/issues/942) | Spike S4: key-holder through cryptoki and ykcs11 (done for YubiKey PIV: Go with piv-go) |
| [#943](https://github.com/truvity/sluis/issues/943) | Spike S5: board memory budget |
| [#944](https://github.com/truvity/sluis/issues/944) | Spike S6: OpenTelemetry from Rust |
| [#945](https://github.com/truvity/sluis/issues/945) | Spike S7: configuration in Rust |
| [#946](https://github.com/truvity/sluis/issues/946) | Spike S8: ceremony with OpenPGP cards in Rust |
| [#947](https://github.com/truvity/sluis/issues/947) | Spike S10: GoReleaser and a Cargo workspace |

### Phase 1: keys, identity, the core actors and boundaries

Key custody, the port library and its generator, the slim `entry` and the other core actors, issuers per boundary, SPIFFE identities, and the platform checks.

| Issue | Item |
|---|---|
| [#865](https://github.com/truvity/sluis/issues/865) | Pkl to JSON Schema generator as a separate open-source tool |
| [#867](https://github.com/truvity/sluis/issues/867) | `sluis doctor` platform checks |
| [#869](https://github.com/truvity/sluis/issues/869) | JOSE package and per-language verification demos |
| [#888](https://github.com/truvity/sluis/issues/888) | Release OpenPGP key ceremony on two YubiKeys (guide and tests) |
| [#892](https://github.com/truvity/sluis/issues/892) | Policy actor in Rust with Cedar (cedar-policy crate) |
| [#913](https://github.com/truvity/sluis/issues/913) | The ca actor: the X.509 certificate authority |
| [#914](https://github.com/truvity/sluis/issues/914) | Sluis ceremony: semi-automatic key ceremonies, card slot map and node PIN unlock |
| [#948](https://github.com/truvity/sluis/issues/948) | Port library: every state value encrypted, scope from the actor's own identity |
| [#949](https://github.com/truvity/sluis/issues/949) | Generator and drift test: scopes, ACLs, cloud policies, units, sockets and service accounts from one manifest |
| [#950](https://github.com/truvity/sluis/issues/950) | Isolation test vectors per layer, with the other layers switched off |
| [#951](https://github.com/truvity/sluis/issues/951) | Key-holder: one socket per caller, peer-uid check, content-aware signing |
| [#956](https://github.com/truvity/sluis/issues/956) | Leases on the state store with fencing tokens |
| [#957](https://github.com/truvity/sluis/issues/957) | Decision: sealed state for secrets and per-company data |
| [#958](https://github.com/truvity/sluis/issues/958) | The slim entry actor: parse, validate, route, and nothing else |
| [#959](https://github.com/truvity/sluis/issues/959) | The session actor: session records, the cookie and per-boundary freshness |
| [#960](https://github.com/truvity/sluis/issues/960) | The console actor: the operator API (backend for the console) |
| [#961](https://github.com/truvity/sluis/issues/961) | The exchange actor: token exchange, enrolment and proof verifiers |
| [#962](https://github.com/truvity/sluis/issues/962) | Issuers per boundary with static discovery and JWKS |
| [#964](https://github.com/truvity/sluis/issues/964) | SPIFFE IDs, X.509-SVIDs and JWT-SVIDs, with a trust domain per boundary |
| [#973](https://github.com/truvity/sluis/issues/973) | Runbook: promoting a company to its own installation |

### Phase 2: drivers, hosts, conformance and distribution

The board drivers, federation, host enrolment and SSH, the conformance pipeline and form post, and how a release is built, signed, published and installed.

| Issue | Item |
|---|---|
| [#866](https://github.com/truvity/sluis/issues/866) | Systemd unit hardening baseline |
| [#870](https://github.com/truvity/sluis/issues/870) | Workload identity with the boundary issuer and AWS exchange |
| [#873](https://github.com/truvity/sluis/issues/873) | Distribution site on R2 |
| [#874](https://github.com/truvity/sluis/issues/874) | Release signing and CI upload credentials |
| [#875](https://github.com/truvity/sluis/issues/875) | Lambda packaging as zip per actor |
| [#876](https://github.com/truvity/sluis/issues/876) | OCI images and Helm charts with digest-pinned images |
| [#878](https://github.com/truvity/sluis/issues/878) | Benchmark AWS KMS Sign latency for ECC P-384 from Lambda (EKS later) |
| [#879](https://github.com/truvity/sluis/issues/879) | Benchmark YubiKey firmware 5.8 P-384 sign and ECDH on an arm64 board |
| [#881](https://github.com/truvity/sluis/issues/881) | Board OS images for Ubuntu 26.04 and Rocky Linux 10.2 |
| [#885](https://github.com/truvity/sluis/issues/885) | Passkeys and step-up for high-privilege grants |
| [#889](https://github.com/truvity/sluis/issues/889) | Research: publishing and verifying release keys (keyservers, WKD, DNS OPENPGPKEY, verifier tools) |
| [#890](https://github.com/truvity/sluis/issues/890) | Artifact signing service (OpenPGP subkeys and short-lived X.509 signing certificates behind federated identity) |
| [#891](https://github.com/truvity/sluis/issues/891) | CI: integration tests on a private runner group (boards and AWS test installations) |
| [#915](https://github.com/truvity/sluis/issues/915) | Quota: engine, quota state, metering and enforcement, Idempotency-Key |
| [#916](https://github.com/truvity/sluis/issues/916) | Generic OIDC upstream integration with acr/amr passthrough and step-up (RFC 9470) |
| [#917](https://github.com/truvity/sluis/issues/917) | Connect client library for TypeScript (connect-es) for the public API |
| [#925](https://github.com/truvity/sluis/issues/925) | SSH user and host certificates from the ssh-ca actor, with principals from the sluis-host service |
| [#926](https://github.com/truvity/sluis/issues/926) | Database access: short-lived client certificates for self-run PostgreSQL and CloudNativePG |
| [#931](https://github.com/truvity/sluis/issues/931) | Conformance pipeline: suite runs on the local and lambda installations, archived results, certification package |
| [#952](https://github.com/truvity/sluis/issues/952) | Rqlite state and index drivers: one cluster per (boundary, owning actor) |
| [#953](https://github.com/truvity/sluis/issues/953) | Systemd template units and the instance generator |
| [#954](https://github.com/truvity/sluis/issues/954) | S3-compatible blob driver and the cloud-cloudflare credential driver |
| [#955](https://github.com/truvity/sluis/issues/955) | Peer witness driver |
| [#963](https://github.com/truvity/sluis/issues/963) | Federation: boundary installations joined by declared trust |
| [#965](https://github.com/truvity/sluis/issues/965) | Verifier drivers for workload and host proofs |
| [#966](https://github.com/truvity/sluis/issues/966) | Host enrolment API with end-to-end encryption and token proof |
| [#967](https://github.com/truvity/sluis/issues/967) | Host enrolment client and console pages |
| [#968](https://github.com/truvity/sluis/issues/968) | Sluis trust sync: trust bundles onto hosts through a generic package |
| [#969](https://github.com/truvity/sluis/issues/969) | Ca issuance log on C2SP static-ct-api |
| [#970](https://github.com/truvity/sluis/issues/970) | Decision: re-test public API streaming on Lambda |
| [#971](https://github.com/truvity/sluis/issues/971) | Sluis-host: the root host service for SSH |
| [#972](https://github.com/truvity/sluis/issues/972) | Cert-manager issuer backed by sluis |
| [#979](https://github.com/truvity/sluis/issues/979) | Form post response mode with its conformance plan |
| [#983](https://github.com/truvity/sluis/issues/983) | Garage backend and the cloud-garage credential driver (waits on [#982](https://github.com/truvity/sluis/issues/982)) |
| [#984](https://github.com/truvity/sluis/issues/984) | Off-site copy of the audit log from Garage to R2 on the boards (waits on [#982](https://github.com/truvity/sluis/issues/982)) |
| [#985](https://github.com/truvity/sluis/issues/985) | Audit installation front: slim entry router, reader API and its own console |
| [#986](https://github.com/truvity/sluis/issues/986) | Per-driver deb/rpm packages and installation meta-packages |

### Later

After the first release or after v2 parity: FAPI 2.0, CIBA, ACME, device login (postponed), a FIPS build variant, Garage on the boards, other clouds and other stores.

| Issue | Item |
|---|---|
| [#855](https://github.com/truvity/sluis/issues/855) | Device login for sluisctl (RFC 8628 device authorization grant) |
| [#868](https://github.com/truvity/sluis/issues/868) | A FIPS build variant, when a customer needs it; v2.0 has no FIPS build (decided 2026-10-31) |
| [#871](https://github.com/truvity/sluis/issues/871) | GCP Workload Identity Federation exchange |
| [#872](https://github.com/truvity/sluis/issues/872) | Azure Entra federated credential exchange |
| [#882](https://github.com/truvity/sluis/issues/882) | Site explorers read the permission manifest |
| [#884](https://github.com/truvity/sluis/issues/884) | Optional TPM 2.0 key support (sealing and node identity) |
| [#912](https://github.com/truvity/sluis/issues/912) | FAPI 2.0 security profile: PAR, DPoP and private_key_jwt client authentication |
| [#918](https://github.com/truvity/sluis/issues/918) | SAML 2.0 identity provider and SCIM provisioning for AWS IAM Identity Center |
| [#927](https://github.com/truvity/sluis/issues/927) | CIAM backlog: customer sign-in features not yet scheduled |
| [#929](https://github.com/truvity/sluis/issues/929) | FAPI 2.0 conformance: Security Profile and Message Signing OP plans |
| [#930](https://github.com/truvity/sluis/issues/930) | OpenID relying-party plans for the generic OIDC upstream |
| [#974](https://github.com/truvity/sluis/issues/974) | NATS credentials from sluis: auth callout and user-JWT minter |
| [#975](https://github.com/truvity/sluis/issues/975) | Postgres state and index drivers with row-level isolation |
| [#976](https://github.com/truvity/sluis/issues/976) | Kubernetes Lease driver |
| [#977](https://github.com/truvity/sluis/issues/977) | Live reload of declared configuration |
| [#978](https://github.com/truvity/sluis/issues/978) | Ca: ACME (RFC 8555) for host TLS certificates, after the first release |
| [#980](https://github.com/truvity/sluis/issues/980) | CIBA in poll mode with a passkey on the approving device (after v2 parity) |
| [#982](https://github.com/truvity/sluis/issues/982) | Garage as the boards' blob store (blocked on conditional writes); spike done 2026-10-31 |
