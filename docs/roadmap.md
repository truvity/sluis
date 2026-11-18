---
title: Roadmap
description: Where sluis v2 is going: the core first, a re-plan, then parity, the switch and the PKI, SSH and database add-on.
status: page
---

The roadmap lives in the `Product milestone` field of the public [sluis v2 board](https://github.com/orgs/truvity/projects/9), and the [project status](/status/) page shows how far each milestone has got. This page says what each phase is for and when it is done; the reasoning is in [ADR 0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/) and, for the order of installations, [ADR 0011](/decisions/0011-runtimes-and-installations/#delivery-order).

v1 keeps its own roadmap on the [sluis v1 board](https://github.com/orgs/truvity/projects/10), on `master`. New feature requests target v2 ([ADR 0001](/decisions/0001-v1-and-v2-lines/)).

## Where v2 stands

As of 2026-11-18, the core is being built and polished, and `v2.0.0-alpha.1` was tagged on 2026-11-08. The design is still ahead of the code in the add-ons and the later blocks. What exists are the [decision records](/decisions/), the [requirements](/requirements/), the design pages, and the part of the code listed in [What runs today](#what-runs-today). Root custody lives in [ritueel](https://ritueel.id), a separate project that owns the roots, the root list, the CA bundle, the revocation lists, the keyring, what it knows about its cards, and the SSH CA.

On 2026-11-18 the maintainer re-planned again (decision 1 below). The frame is: first fully complete and polish the core, including the boards, Lambda, Kubernetes, local development and release management; then re-plan. Nothing after the core is scheduled until that re-plan has been made, so this page names the later phases and their order, and not their contents.

## The maintainer's decisions of 2026-11-18

These decisions are quoted with the codes the maintainer gave them. The phases below, the [Core table](#the-core-is-done-when) and the amendments to ADR [0006](/decisions/0006-keys-and-sealing/#amendment-of-2026-11-18-the-token-certificate-is-optional-and-off-in-the-core), [0009](/decisions/0009-export/#amendment-of-2026-11-18-the-exporter-on-lambda-is-woken-by-the-stream-of-the-table), [0011](/decisions/0011-runtimes-and-installations/#amendment-of-2026-11-18-the-core-comes-first-and-the-mixed-shape-is-designed-in-it), [0028](/decisions/0028-audit-pieces/#amendment-of-2026-11-18-the-index-of-a-lambda-installation-runs-beside-it-on-kubernetes) and [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-core-comes-first-and-the-token-certificate-is-off) follow from them.

| Code | Decision |
|---|---|
| 1 | Re-plan frame: "first fully complete and polish our core on sluis v2, including boards, lambda and k8s, local development, and release management and after that replan again". Parity planning waits for that re-plan. The PKI, SSH and database add-on, which replaces OpenBao, comes after parity. |
| C1 (agree) | The definition of done of the core, part by part: the [Core table](#the-core-is-done-when). The trackers are re-baselined first, with a "Core" milestone. |
| C2 (yes) | The core includes the parts of v1's contract that live in the core: group strings and per-audience narrowing of the groups claim, `sub` formats, per-audience lifetimes, RS256 per audience where a relying party needs it, confidential and public clients with PKCE, and logout (`end_session`, RP-initiated). |
| V5 (yes) | `v2.0.0-alpha.2` is cut when the core features are done, before the shapes. |
| X1 A | The token certificate is optional per installation and off in the core. The built `ca` v0 code stays; the key set carries no `x5c` until the PKI add-on and a ritueel root exist. The core needs no intermediate and no test certifier, and the boards need no slot 9c in the core. The PKI phase starts when the ritueel release candidate (software root) exists and a cloud root is adopted. |
| L1 A | The audit index of a Lambda installation: the indexer and the reader run on a Kubernetes cluster beside it, on the `postgres` index in the `cnpg` mode (the mixed shape). There is no DynamoDB index driver. The `aurora` mode stays documented for an AWS-only installation. |
| L2 | The exporter on Lambda is woken by DynamoDB Streams on the store's table when an exportable item changes, plus a daily scheduled run that rebuilds the documents and removes what is gone. |
| K1 | The Kubernetes audit transport stays `nats-jetstream`, as documented. |
| 9 | The mixed shape ([ADR 0011](/decisions/0011-runtimes-and-installations/), D9 of 2026-11-12, [#926](https://github.com/truvity/sluis/issues/926)) is designed in the core phase. Its first use is the audit search pieces beside a Lambda installation (L1). |
| V6 A | The switch rule, recorded here only as the plan for the platform installation: see [The switch](#the-switch). |

## What runs today

The three demos were measured on 2026-11-04 and are run in CI (the first two on every pull request); the other lines were read on 2026-11-18 from the CI workflows and the CHANGELOG. Each line is a proof you can run; the commands are `devbox run -- just <recipe>`, and the recipes are described on [The local installation](/runtimes/local/#recipes) and on [Bare metal](/runtimes/bare-metal/#deploy-and-prove-on-three-boards).

| Milestone | State | Demo |
|---|---|---|
| **M1, the spine** | Passes on the local installation: a client asks `policy` for a decision and `signer` for a token, the token verifies against the key set that `entry` serves, the records of the run are in the audit log, and `sluis audit verify` checks the log | `local-up`, then `local-smoke` |
| **M3, the boards** | Passes on three boards over one shared three-node rqlite cluster: the same spine runs against each board in turn, and from the second board on it finds the earlier boards' signing key, which shows the shared store | `build-arm64`, `boards-deploy`, then `boards-smoke`; `boards-undeploy` removes it again |
| **M2, the person** | Passes on the local installation: `session` seals the facts of a session chain under the tenant's `chain` key, and `session` and `exchange` alone open them ([0006](/decisions/0006-keys-and-sealing/#data-keys)); `sluis login` signs a person in through the upstream identity provider, and `sluis token` exchanges the token for an audience (RFC 8693) | `local-up`, then `local-person-smoke` |
| **Passkey registration** | Passes on the local installation in a real headless Chromium with a virtual authenticator | `local-passkey-smoke` (the `smokes` job of CI) |
| **Packages and the release candidate** | The release tooling builds the `.deb` and `.rpm` packages, a throwaway apt and dnf repository installs from them, an install tier proves the units, and the `Candidate` workflow builds the release once, so that the tag run publishes the candidate's bytes and builds nothing | `release-packages`, `release-repo-smoke`, CI jobs `install` and `Candidate` |

The deny matrix of the local installation also runs (`local-deny`): every caller against every socket, at the socket and at the guard. CI runs the deny matrix and the smokes of the local installation on every pull request. There is no Lambda installation and no Kubernetes installation yet; both are targets of the core ([The core is done when](#the-core-is-done-when)).

## A core, building blocks and add-ons

On 2026-11-12 the maintainer re-planned v2. It is no longer a core with two later layers (CIAM and quota). It is a core, a few building blocks and optional add-ons, delivered in the phases below ([ADR 0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/)).

- **The core** is what every installation needs: install and run, the commands of the [first-version command table](/reference/cli/#the-first-working-versions-commands), `session`, `exchange`, `signer` and `entry`, and the built `ca` v0, which can issue a token certificate and is off in the core (decision X1 A of 2026-11-18). The core installs and runs without any add-on, and without an intermediate.
- **The building blocks** are modules that a product may reuse beyond sluis, each behind an interface:
  - **store:** the generic store with its drivers ([0005](/decisions/0005-the-store/)). The driver holds bytes; the kinds are compiled in today. The rule until the blocks are extracted is that no domain type is coupled into a driver; new storage needs are kinds.
  - **key:** key custody ([0006](/decisions/0006-keys-and-sealing/)). It holds no root. ritueel owns the roots; sluis owns the intermediates and the request and install side ([0024](/decisions/0024-root-custody-and-the-signing-api/)).
  - **policy:** Cedar with a fixed schema ([0007](/decisions/0007-policy-and-sealed-grants/)).
  - **audit, with usage and limits:** the audit log, plus a usage class of records that is never dropped and the limit check on the request path ([0029](/decisions/0029-the-audit-log/), [0046](/decisions/0046-usage-records-are-never-dropped-and-balances-live-in-the-product/)). It serves the product as well as sluis.
- **The add-ons** are optional, each with a scope of its own, and the core does not depend on any of them:
  1. **PKI, SSH and database access,** the most important add-on and the first one, after parity (decision 1 of 2026-11-18): leaf certificate profiles and revocation lists, short-lived SSH certificates from a sluis token under ritueel's SSH CA, and client certificates for PostgreSQL. It replaces OpenBao. With the KV add-on it completes the retirement of the v1 secrets store: until then the key-value secrets go to a cloud parameter store, and PKI and SSH come here. It also turns the token certificate on (decision X1 A).
  2. **KV secrets:** versioned static secrets in stores that no tenant owns, with policy by path, a data key per store, a blocking audit fact for every successful access, and a Vault KV v2 subset that External Secrets reads ([0047](/decisions/0047-kv-is-an-add-on-of-stores-that-no-tenant-owns/)). It needs the slice of workload exchange for Kubernetes ServiceAccount tokens, pulled forward from parity.
  3. **Cloudflare:** the exporter's bucket driver, the edge purge and the R2 credentials.
  4. **GitHub:** GitHub Actions OpenID Connect as a workload proof in `exchange` and `policy`, and `adapter-github`.

An installation serves one boundary ([0008](/decisions/0008-boundaries-and-tenants/)). Its staff and operators sign in with sluis's own passkeys. Everyone else signs in through the upstream identity provider, and `exchange` turns the upstream token into a sluis token that carries the customer, the organisation and the product. Realms follow boundaries: a regulated boundary has a realm of its own, a product has a generic realm, and a central realm holds the end-user accounts, so the chain is end user, then the boundary's realm, then the central realm ([0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/)).

An installation may also run the foundation with the audit module only, to keep the audit trail of a product that does not use sluis for sign-in ([Audit](/concepts/audit/#audit-alone)). The foundation (store@, exporter@ and key@) and the modules are explained on [Families](/concepts/families/#the-foundation-and-the-modules).

## Phases

The phases are an order. Each opens when the one before it is done, except where it says otherwise. The order is: Core, then a re-plan, then Parity, then the switch of the platform installation, then the PKI, SSH and database add-on. Block extraction stays last and unscheduled.

Older records and pages name the phases P0 to P4. They map to the phases below as follows: P0 is [Records](#records), P1 is [Core](#core), P2 (the identity and audit blocks) is a candidate part of [Parity](#parity) that the re-plan places, P3 is split into [The switch](#the-switch) and [The PKI, SSH and database add-on](#the-pki-ssh-and-database-add-on), and P4 is [Block extraction](#block-extraction). The records are corrected when they are next amended.

### Records

This page, the decision records and the requirement rows say the plan. The board's `Product milestone` options and the issues change separately, by a script that the maintainer runs. As of 2026-11-18 the board still has the options First working version, v1 parity, CIAM upstream, Quota and Backlog; re-baselining them with a Core milestone is the first step of the core ([Core](#core)).

### Core

The core is the first working version, completed and polished on every installation. It has four steps, in this order:

1. **Re-baseline the trackers.** The board gets a "Core" milestone, and the issues and requirement rows that belong to the core move into it. The rows of today's First working version are the starting set ([Where the work is tracked](#where-the-work-is-tracked)). No other step starts on a stale board.
2. **Finish the core features,** including the parts of v1's contract that live in the core (decision C2). Then cut `v2.0.0-alpha.2` (decision V5), before the shapes.
3. **Bring up the shapes:** local development, the three boards, Lambda and Kubernetes. The mixed shape is designed in this step (decision 9); its first use is the audit search pieces beside a Lambda installation (decision L1 A). The order inside this step is open.
4. **Close release management** on every shape: one release with one version for everything, and upgrade and rollback proven on each shape.

#### The core is done when

Decision C1 (agree), 2026-11-18. The core is done when every part below is done. The proof of each part is a command or a CI job, and the same smoke tests run on every shape.

| Part | Done when |
|---|---|
| **Core features** | Every requirement row of the first version is done (on 2026-11-18, of 681 rows, 494 are done, 138 partial and 49 planned), plus the parts of v1's contract that live in the core (decision C2): group strings and per-audience narrowing of the groups claim, `sub` formats, per-audience lifetimes, RS256 per audience where a relying party needs it, confidential and public clients with PKCE, and logout (`end_session`, RP-initiated). |
| **Local development** | One command brings up a full local install, from source and from released images. The smoke tests pass on it. A contributor guide says how. |
| **Boards** | Gate [#1584](https://github.com/truvity/sluis/issues/1584) is closed: mutual TLS on Raft, flood handling and a firewall that fails closed. The rebuild and lost-board rehearsals pass. `cloudflared` is deployed by the scripts. The YubiKey driver is packaged. The boards need no slot 9c in the core (decision X1 A). |
| **Lambda** | The drivers `dynamodb`, `aws-kms`, `ssm`, `appconfig`, `s3-compatible` and `sqs` exist, with one extension per holder. The Lambda runtime exists: one zip per executable, with the Web Adapter in front of `entry`. A Lambda installation runs with test clients only, with the same smoke tests as local. Its audit index runs on a Kubernetes cluster beside it (decision L1 A), and its exporter is woken by DynamoDB Streams (decision L2). |
| **Kubernetes (EKS)** | The Helm chart ([#844](https://github.com/truvity/sluis/issues/844), [#876](https://github.com/truvity/sluis/issues/876)) reuses the AWS drivers, with the `nats-jetstream` transport (decision K1) and the `postgres` index in the `cnpg` mode. It installs on a test cluster, with the same smoke tests. |
| **Release management** | One release carries the packages, the images, the chart and the Lambda zips, all at one version. A release candidate is built before each tag. Upgrade and rollback are tested on every shape. The CHANGELOG is kept. |
| **Trackers** | Re-baselined first, with a Core milestone. |

What the core does not need, by decision X1 A: an intermediate certificate, a test certifier, the `x5c` chain in the key set, and slot 9c on a board. The built `ca` v0 stays in the code and is off.

The earlier plan kept these inside the first version too, and they stay in it: the YubiKey PIV key driver on the cards, the PIN released from slot 87 to a locked `key@`, the mainline Ubuntu 26.04 and Rocky 10 images, the three-node rqlite cluster ([Bare metal](/runtimes/bare-metal/)), the local installation under Docker Compose from the published images ([#1123](https://github.com/truvity/sluis/issues/1123)), sign-in through the generic OpenID Connect upstream ([#1106](https://github.com/truvity/sluis/issues/1106)), the audit log with Search ([#1120](https://github.com/truvity/sluis/issues/1120), [#1121](https://github.com/truvity/sluis/issues/1121), [#1122](https://github.com/truvity/sluis/issues/1122)), passkeys and step-up ([#885](https://github.com/truvity/sluis/issues/885)), exporter@ with the directory driver ([#1453](https://github.com/truvity/sluis/issues/1453)), the published images with checksums and SBOMs and no signature ([ADR 0013](/decisions/0013-release-train-packaging-and-distribution/#signing-and-checksums), [#1124](https://github.com/truvity/sluis/issues/1124)), and a test in CI of every command of the [first-version command table](/reference/cli/#the-first-working-versions-commands) against the local installation ([#1100](https://github.com/truvity/sluis/issues/1100)). The maintainer signs each tag by hand.

Out of the core since 2026-11-07 and 2026-11-12, and still out: workload identity ([#870](https://github.com/truvity/sluis/issues/870)), federation, the proof verifiers, the signing API ([#890](https://github.com/truvity/sluis/issues/890)), the console, `ca` beyond the token certificate, the exporter's bucket driver, the edge purge and the R2 token (the Cloudflare add-on, [0009](/decisions/0009-export/#publishing-public-material)), and the `github-oidc` verifier (the GitHub add-on). A revoked key leaves the key set that the directory driver publishes. The boards' rqlite listens on the board address behind a firewall; gate #1584 above closes that limit before any real PIN or real data goes on a board ([REQ-DEPLOY-047](/requirements/deploy/#REQ-DEPLOY-047)).

**Root custody** reaches an installation through the hand-off to ritueel ([#1066](https://github.com/truvity/sluis/issues/1066), [#1071](https://github.com/truvity/sluis/issues/1071)); the core runs without it (decision X1 A).

### Re-plan

When the core is done, the maintainer re-plans (decision 1). The re-plan decides which v1 parity rows are required, what the parity phase holds (the identity and audit blocks of the earlier P2 are candidates: person exchange from the upstream token, [#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961), [0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/), and usage and limits, [0046](/decisions/0046-usage-records-are-never-dropped-and-balances-live-in-the-product/), [#841](https://github.com/truvity/sluis/issues/841)), and where the platform installation runs. Parity is not planned in more detail before then.

### Parity

v2 covers every v1 parity row that the re-plan marks as required. The rows are the issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity), listed on the [v1 parity checklist](/features/). The rest of the parity group is Backlog.

**Done when** every required parity row is done and has its evidence.

### The switch

Decision V6 A, 2026-11-18, as the plan for the platform installation. It is recorded here and nowhere else.

1. v2 runs as a new issuer, `sluis.truvity.xyz` (planned for the Truvity platform installation), beside v1. It never takes over the host of the v1 platform installation ([ADR 0015](/decisions/0015-evolution-and-compatibility/) stands).
2. No relying party moves until v2 covers every required v1 parity row.
3. Then the relying parties move in waves.
4. v1 is switched off two weeks after the last wave.

Before the first relying party moves, the cutover inventory lists every relying party of the v1 issuer and every consumer that keys on its `sub` ([REQ-OPS-030](/requirements/ops/#REQ-OPS-030)). The earlier plan (decision D5 A of 2026-11-12) put the platform's cutover on the three boards, with the audit log on R2 buckets and so with the Cloudflare add-on. That placement is part of the re-plan and is not relied on until it is made.

**Done when** the last relying party has moved and v1 has been off for two weeks.

### The PKI, SSH and database add-on

After parity (decision 1). The add-on is the replacement of OpenBao.

- **The add-on.** Leaf profiles and the revocation list ([#913](https://github.com/truvity/sluis/issues/913)); `ssh-ca`, which turns a sluis token into a short-lived SSH user certificate and issues host certificates, under the SSH CA that ritueel lists ([#925](https://github.com/truvity/sluis/issues/925), [0023](/decisions/0023-certificate-authorities/#ssh)); PostgreSQL client certificates for CloudNativePG ([#926](https://github.com/truvity/sluis/issues/926)); and the KV add-on, with its slice of workload exchange ([0047](/decisions/0047-kv-is-an-add-on-of-stores-that-no-tenant-owns/), [#798](https://github.com/truvity/sluis/issues/798)). The token certificate and the `x5c` chain come on with it (decision X1 A).
- **Entry conditions** (decision X1 A): the ritueel release candidate with a software root exists, and a cloud root is adopted. Until ritueel has the SSH CA key list, the add-on runs on the harness's test root.
- **The v1 secrets store** is retired in waves: PKI and SSH go to the add-on, and External Secrets switches from the parameter store to sluis KV app by app, with a rollback ([0047](/decisions/0047-kv-is-an-add-on-of-stores-that-no-tenant-owns/)).

**Done when** an operator gets an SSH certificate and a PostgreSQL client certificate from a sluis token.

### Block extraction

A runtime registry for store, a schema extension for policy and a key library, so that a product can reuse a block. Until then no new coupling of domain types into a block. Not scheduled.

## Cancelled

By the maintainer's decisions of 2026-11-12 (D1 B, D4 A, D7 A) the following are no longer planned. Their records are superseded, and their requirement rows are withdrawn.

- **The CIAM upstream milestone:** the customer-facing sign-in profile (FAPI 2.0, PAR, DPoP and private-key client authentication), customer pages, customer connections, the end-user lifecycle, pairwise subjects per sector and the login abuse limits ([0031](/decisions/0031-ciam-is-a-broker/), [0033](/decisions/0033-customer-connections/), [0034](/decisions/0034-end-users-and-upstream-signals/), [0035](/decisions/0035-customer-pages/), [0038](/decisions/0038-fapi-2-profile/)). Customers sign in through the upstream identity provider ([0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/)).
- **The Quota milestone:** the allowance and its balance inside sluis ([0039](/decisions/0039-quota-allowance/)). Plans and balances live on the product side; sluis keeps usage and a limit check ([0046](/decisions/0046-usage-records-are-never-dropped-and-balances-live-in-the-product/)).
- **OpenBao as a driver.**

## Delivery order

The tested [installations](/runtimes/) are all part of the core. Their order inside the core is open, except that the local installation comes first because the others reuse its smoke tests:

1. **local,** from source and from released images, with a contributor guide.
2. **The three boards,** with the hardware base they need and gate [#1584](https://github.com/truvity/sluis/issues/1584) closed ([Bare metal](/runtimes/bare-metal/)).
3. **Lambda,** with test clients only. Its audit index is not on Aurora by default: the indexer and the reader run on a Kubernetes cluster beside it (decision L1 A, [Serverless](/runtimes/serverless/#data-and-audit)).
4. **Kubernetes on EKS,** from one Helm chart.

A mixed shape, where some sluis parts run on Kubernetes beside another installation, is designed in the core phase, and its first use is the audit search pieces beside a Lambda installation (decision 9; [ADR 0011](/decisions/0011-runtimes-and-installations/#delivery-order), [#926](https://github.com/truvity/sluis/issues/926)).

EC2 is a possible bare-metal installation, not a delivery target, and not tested. Other installations are possible and untested. The order is reviewed when each installation ships, and ADR 0011 records any change.

## Later

**The backlog** holds the plans that the re-plan of 2026-11-12 does not serve: the v1 parity group except the rows that the re-plan marks as required, the isolation harness and generators, the extra board rows, the extras of the Cloudflare and GitHub add-ons, the audit read API and front, the console, the reconcilers, and libraries in languages other than Go and Rust. The board and the requirement rows follow this page when the maintainer's script has moved the issues.

Work planned after that is marked Later on the board. It includes CIBA, in poll mode with a passkey on the approving device, once a real consumer exists ([#980](https://github.com/truvity/sluis/issues/980)); ACME for host certificates ([#978](https://github.com/truvity/sluis/issues/978)); device login ([#855](https://github.com/truvity/sluis/issues/855)); a FIPS build variant when a customer needs one ([#868](https://github.com/truvity/sluis/issues/868)); and other clouds and stores. Garage as the boards' blob store is backlog, because it ignores conditional writes ([#982](https://github.com/truvity/sluis/issues/982)).

**Staff keys** are in Backlog too: card binding per employee, the company's own OpenPGP keys for its staff (certified by sluis, listed in a staff keyring, and synced to staff GitHub accounts), and the same binding for a customer's JWK ([ADR 0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/), [ADR 0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/), [#1719](https://github.com/truvity/sluis/issues/1719), [#1718](https://github.com/truvity/sluis/issues/1718), [#1076](https://github.com/truvity/sluis/issues/1076)). The maintainer decided on 2026-11-07 to move "card binding per employee and GPG key management" out of the first working version, and to keep it in Backlog. It is not scheduled and no phase waits for it. The ritueel half of ADR 0043 is superseded ([ADR 0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#staff-keys-and-ritueel)): ritueel dropped staff keys, so there is no ritueel card helper, no shared staff bundle and no trust-signature ceremony. What it needs first: the hardware-attested sign-in, the directory and `adapter-github` (the GitHub add-on), and, before the build, the measurements against GitHub and the conformance test on a real card that the two records list under Follow-ups. The OpenPGP subkeys of the boards' nodes and the passkey sign-in tiers are not part of it and stay where they are.

**Signing of release artifacts** is in Backlog as well. The maintainer decided on 2026-11-07 that neither the keyless signing (cosign signatures, build-provenance and SBOM attestations) nor the OpenPGP signing of releases belongs to the first working version or to v1 parity. A release carries checksums and SBOMs, and the maintainer signs each tag by hand. The requirements REQ-SUPPLY-001, 002, 007, 008, 017, 018 and 043 are in Backlog, and the work waits with the staff key and GPG work ([ADR 0013](/decisions/0013-release-train-packaging-and-distribution/#signing-and-checksums), [#874](https://github.com/truvity/sluis/issues/874), [#1013](https://github.com/truvity/sluis/issues/1013), [#1074](https://github.com/truvity/sluis/issues/1074)). The signing API ([#890](https://github.com/truvity/sluis/issues/890)) is a separate thing and is in v1 parity.

## Where the work is tracked

The `Product milestone` field of the [board](https://github.com/orgs/truvity/projects/9) carries the phases. The requirement catalogue names the same milestone in each record's `milestone` key, declared in `docs/requirements/index.yaml`; until the maintainer's script has created the Core option on the board and moved the issues, the catalogue keeps `first-version` as the milestone of the core rows, so that `just check-requirements` can still compare a record with the milestone of its issues ([Requirements](/contributing/decisions/#requirements)).

The v1 features that v2 must match are issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity), listed on the [v1 parity checklist](/features/). Spike results are in the decision records they settled, such as [ADR 0012](/decisions/0012-the-language-follows-the-executable/#the-spikes-are-gates), and on the issue of each spike.
