---
title: Roadmap
description: Where sluis is going. The four v2 product milestones in priority order, the two public boards, the delivery order and the v2 technical backlog.
status: shipped
---

The [project status](/status/) page shows where v2 stands, milestone by milestone: requirements, decision records and issues, grouped by the board's `Product milestone` field and rebuilt with the site.

The roadmap lives in two public GitHub Projects under the `truvity` organization:

- **sluis v2**: [github.com/orgs/truvity/projects/9](https://github.com/orgs/truvity/projects/9). The line built on `main`.
- **sluis v1**: [github.com/orgs/truvity/projects/10](https://github.com/orgs/truvity/projects/10). The agreed v1 roadmap (v1.80 to v1.85) and fixes, on `master`.

## Product milestones

v2 is delivered in four product milestones, in this priority order. They are the options of the `Product milestone` field on the v2 board (checked 2026-10-31), and the [project status](https://sluis.id/status/) page groups requirements, decision records and issues under them. Within milestone 2, the boards, lambda and kubernetes installations are built in parallel after local ([delivery order](#delivery-order)).

1. **First working version.** The minimal local installation: Docker Compose on a workstation, one container per actor, built from the published images, with the minimal drivers (embedded SQLite state, a SQLite index, a files blob, a local lock, a spool or direct transport) and software keys in CI or the host's YubiKey on a workstation ([ADR 0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)). It is where the basic functionality is developed and tested, and what the integration tests run on.
   - **Done when** the example install file for local starts under Docker Compose from the published images, and the integration tests run against it in CI.
   - Live numbers: [project status](https://sluis.id/status/).
2. **v1 parity.** The boards, lambda and kubernetes installations, and everything v1 does. Parity means that the workloads that use v1 today can be wired to the new sluis, possibly in a different way or with a different configuration, not that behaviour is identical ([ADR 0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/)).
   - **Done when** every row of the [features page](/features/) is `Done` or `Changed` (with its decision), and the boards, lambda and kubernetes installations are built and tested ([installations](/runtimes/installations/)).
   - Live numbers: [project status](https://sluis.id/status/).
3. **CIAM upstream.** A customer-identity product signs in a product's customers through sluis. sluis stays a broker and does not become the customer-identity product: the CIAM product is one more OpenID Connect upstream ([OIDC upstream](/integrations/oidc-upstream/)).
   - **Done when** the generic OIDC upstream ([#916](https://github.com/truvity/sluis/issues/916)) does claim mapping per upstream, `acr` and `amr` passthrough, step-up (RFC 9470), end-user subjects, back-channel logout and rate limits per connection.
   - Live numbers: [project status](https://sluis.id/status/).
4. **Quota.** `quota` as a front for installations: applications ask it to admit work before it runs, confirm or cancel the usage after, and push top-ups. It runs beside an installation or on its own ([ADR 0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/), [ADR 0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
   - **Done when** quota meters by default and enforces per policy rule for chosen subjects or audiences, a request counts once however often it is retried, and under strict enforcement the admitted total never exceeds the limit under concurrent requests (REQ-QUOTA-001 and REQ-QUOTA-002; work item [#915](https://github.com/truvity/sluis/issues/915)).
   - Live numbers: [project status](https://sluis.id/status/).

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
| [S5](https://github.com/truvity/sluis/issues/943) | The memory budget on the boards: each actor and driver under load, in each language, against the 2 GB budget. **Done** (2026-10-31): the memory fits with Go or with Rust actors (about 985 or 680 MiB of a 1380 MiB ceiling on a tier-0 board), rqlite costs about 9 MiB per cluster, and the CPU, not the eMMC, limits writes first, at about 400 commits/s per board. The maintainer decided its open points on 2026-10-31: no thermal action, the write rate estimated from the actor design (about 7 commits/s needed at the measured peak), the estimated rows kept, `GOMEMLIMIT` as a ceiling, and Rust on tier-0 boards unless a serious trade-off |
| [S6](https://github.com/truvity/sluis/issues/944) | OpenTelemetry from Rust: one trace across a Go and Rust hop. **Done** (2026-10-31): passed with conditions |
| [S7](https://github.com/truvity/sluis/issues/945) | Configuration in Rust: the same conformance vectors as the Go structs. **Done** (2026-10-31): passed with conditions |
| [S8](https://github.com/truvity/ritueel/issues/26) | The ceremony with OpenPGP cards. **Moved to ritueel** on 2026-11-01 with the rest of the root ceremonies; the 2026-10-31 result stays in that issue. sluis keeps only the back-signature that `key-holder` makes on the card ([ADR 0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)) |
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
| [#947](https://github.com/truvity/sluis/issues/947) | Spike S10: GoReleaser and a Cargo workspace |

### Phase 1: keys, identity, the core actors and boundaries

Key custody, the port library and its generator, the slim `entry` and the other core actors, issuers per boundary, SPIFFE identities, and the platform checks.

| Issue | Item |
|---|---|
| [#865](https://github.com/truvity/sluis/issues/865) | Pkl to JSON Schema generator as a separate open-source tool |
| [#867](https://github.com/truvity/sluis/issues/867) | `sluis doctor` platform checks |
| [#869](https://github.com/truvity/sluis/issues/869) | JOSE package and per-language verification demos |
| [#892](https://github.com/truvity/sluis/issues/892) | Policy actor in Rust with Cedar (cedar-policy crate) |
| [#913](https://github.com/truvity/sluis/issues/913) | The ca actor: the X.509 certificate authority |
| [#914](https://github.com/truvity/sluis/issues/914) | `sluis key`: the card slot map and node PIN unlock; the ceremony commands moved to ritueel |
| [#1066](https://github.com/truvity/sluis/issues/1066) | Hand-off to root custody: `sluis key request`, `install` and `status` |
| [#1067](https://github.com/truvity/sluis/issues/1067) | `key-holder`: the OpenPGP back-signature for a node subkey |
| [#1068](https://github.com/truvity/sluis/issues/1068) | Expiry warning for intermediates |
| [#1070](https://github.com/truvity/sluis/issues/1070) | Hand-off conformance fixtures shared with ritueel |
| [#1071](https://github.com/truvity/sluis/issues/1071) | Tracker: the first version needs ritueel's software-root mode |
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
| [#1069](https://github.com/truvity/sluis/issues/1069) | KMS proof of key location for the hand-off |
| [#1072](https://github.com/truvity/sluis/issues/1072) | SSH CA keys as anchors: the signed list of SSH CA keys on hosts |
| [#1074](https://github.com/truvity/sluis/issues/1074) | Signing API: apt `Release.gpg` and `InRelease`, and rpm `repomd.xml.asc` |
| [#874](https://github.com/truvity/sluis/issues/874) | Release signing and CI upload credentials |
| [#875](https://github.com/truvity/sluis/issues/875) | Lambda packaging as zip per actor |
| [#876](https://github.com/truvity/sluis/issues/876) | OCI images and Helm charts with digest-pinned images |
| [#878](https://github.com/truvity/sluis/issues/878) | Benchmark AWS KMS Sign latency for ECC P-384 from Lambda (EKS later) |
| [#879](https://github.com/truvity/sluis/issues/879) | Benchmark YubiKey firmware 5.8 P-384 sign and ECDH on an arm64 board |
| [#881](https://github.com/truvity/sluis/issues/881) | Board OS images for Ubuntu 26.04 and Rocky Linux 10.2 |
| [#885](https://github.com/truvity/sluis/issues/885) | Passkeys and step-up for high-privilege grants |
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
| [#1075](https://github.com/truvity/sluis/issues/1075) | Signing API, later: per-package rpm signatures and a `gpg.program` stand-in for git |
| [#1076](https://github.com/truvity/sluis/issues/1076) | Planned later: online OpenPGP certification of directory people |
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
