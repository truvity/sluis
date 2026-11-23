---
title: Roadmap
description: The order in which sluis v2 is delivered, and what each milestone is done when, from the core to parity with v1 and the PKI, SSH and database add-on.
status: page
---

This page holds the order of delivery and what each milestone is done when, and nothing else. The reasoning is in [ADR 0044](/decisions/0044-sluis-is-a-core-and-add-ons/) (the layers) and [ADR 0011](/decisions/0011-runtimes-and-installations/) (the installations). The work is tracked in the `Product milestone` field of the public [sluis v2 board](https://github.com/orgs/truvity/projects/9), and [Project status](/status/) shows how far each milestone has got.

v1 keeps its own roadmap on the [sluis v1 board](https://github.com/orgs/truvity/projects/10), on `master`. New feature requests target v2 ([ADR 0001](/decisions/0001-v1-and-v2-lines/)).

## The layers

sluis is a **core** and **add-ons** ([ADR 0044](/decisions/0044-sluis-is-a-core-and-add-ons/)). The core is what every installation runs; no core actor calls an add-on. There is no customer identity layer: customers sign in at the group's identity provider, and products use its tokens ([ADR 0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)). The layers say what a part is. The milestones below say when it is delivered, and they are not layers.

The ports, drivers and blocks that the core hosts are [atoom](https://github.com/truvity/atoom)'s, and the audit log is [audit](https://github.com/truvity/audit)'s. Their plans are theirs: [atoom's plan](https://github.com/truvity/atoom/blob/master/docs/plan.md) holds the blocks, the driver packages and the Lambda extension zips. [audit's records](https://github.com/truvity/audit/tree/master/docs/decisions) hold the log, the index and the writer.

## What runs today

`v2.0.0-alpha.1` was tagged on 2026-11-08. The decision records, the requirements and the design pages are ahead of the code. What the code proves was read on 2026-11-18 from the CI workflows and the CHANGELOG. Each line is a proof you can run: `devbox run -- just <recipe>`, described on [The local installation](/runtimes/local/#recipes) and [Bare metal](/runtimes/bare-metal/#deploy-and-prove-on-three-boards).

| Proof | State | Recipe |
|---|---|---|
| **The spine** | On the local installation, a client asks `policy` for a decision and `signer` for a token, the token verifies against the key set that `entry` serves, the run's records are in the audit log, and `audit verify` checks the log | `local-up`, then `local-smoke` |
| **The boards** | On an additional, external testing area of three boards over one three-node rqlite cluster, the same spine runs against each board in turn, and each later board finds the earlier boards' signing key | `build-arm64`, `boards-deploy`, then `boards-smoke` |
| **The person** | `signin` seals the facts of a sign-in session chain, `signin` and `exchange` alone open them ([ADR 0006](/decisions/0006-keys-and-sealing/#data-keys)), `sluis login` signs a person in through an upstream identity provider, and `sluis token` exchanges the token for an audience (RFC 8693) | `local-up`, then `local-person-smoke` |
| **Passkey registration** | In a real headless Chromium with a virtual authenticator | `local-passkey-smoke` |
| **Packages and the release candidate** | `.deb` and `.rpm` packages build, a throwaway apt and dnf repository installs from them, and the `Candidate` workflow builds the release once, so the tag run publishes the candidate's bytes and builds nothing | `release-packages`, `release-repo-smoke` |

The deny matrix of the local installation also runs (`local-deny`): every caller against every socket, at the socket and at the guard. CI runs it and the smokes of the local installation on every pull request. There is no Lambda installation and no Kubernetes installation yet.

## Delivery order

Each milestone opens when the one before it is done, except where it says otherwise. The order is:

1. **Core.** Everything an installation needs, completed and polished on every installation.
2. **Customer identity and quota.** The exchange for resource clients, for every subject alike, and the limit check. Right after the core. The name is the one the board option carries; the layer of that name is withdrawn.
3. **Parity with v1,** with the console.
4. **The PKI, SSH and database add-on,** after parity.

The cutover from v1 runs in parallel with these milestones, as [ADR 0001](/decisions/0001-v1-and-v2-lines/#cutover-from-v1) says: no relying party moves until v2 covers every required parity row.

The milestone slugs of `docs/requirements/index.yaml` are `core`, `customer-identity`, `quota`, `parity`, `pki-ssh-db` and `backlog`. A requirement record names its milestone, so that `just check-requirements` can compare a record with the milestone of its issues ([Requirements](/contributing/decisions/#requirements)).

### Core

The core is the first working version, completed and polished on every installation. Its steps, in order:

1. **Re-baseline the trackers.** The board gets a `Core` milestone, and the issues and requirement rows that belong to it move in. No other step starts on a stale board.
2. **Finish the core features,** including the hardening below. Then cut `v2.0.0-alpha.2`, before KV and the installations.
3. **Build KV** ([ADR 0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)), now and before parity, on the local installation first, without the part that serves External Secrets: stores, policy by path, a data key per store and a blocking audit fact ([ADR 0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/)). SSM stays the only secret store for now, and sluis KV serving External Secrets is a later option, decided when v2 reaches parity ([ADR 0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/)).
4. **Bring up the installations:** local, boards, Lambda and Kubernetes. Local comes first, because the others reuse its smoke tests; the order of the rest is open ([ADR 0011](/decisions/0011-runtimes-and-installations/)).
5. **Close release management** on every installation.

#### The core is done when

Every part below is done. The proof of each part is a command or a CI job, and the same smoke tests run on every installation.

| Part | Done when |
|---|---|
| **Core features** | Every requirement row of the core milestone is done (the counts are on [Project status](/status/)), including the parts of v1's contract that live in the core: group strings and per-audience narrowing of the groups claim, `sub` formats, per-audience lifetimes, RS256 per audience where a relying party needs it, confidential and public clients with PKCE, and RP-initiated logout. It includes device login (RFC 8628) for a person on a machine without a browser, behind the normal sign-in with multi-factor authentication and off for a client unless its configuration turns it on ([#855](https://github.com/truvity/sluis/issues/855)). |
| **Hardening** | Each of these is proved by a test or a CI job. The FAPI 2.0 profile that a client declares ([0038](/decisions/0038-fapi-2-profile/#the-profile)), with the FAPI 2.0 conformance suite in CI beside the Basic suite ([#912](https://github.com/truvity/sluis/issues/912), [#929](https://github.com/truvity/sluis/issues/929)); a relying party that cannot do PAR or DPoP does not have to use it. Refresh-token reuse detection beyond the last 16 rotations ([#1639](https://github.com/truvity/sluis/issues/1639)). RP-initiated and back-channel logout, and ending a browser sign-in stops silent re-admission ([#779](https://github.com/truvity/sluis/issues/779), [#780](https://github.com/truvity/sluis/issues/780), [#1625](https://github.com/truvity/sluis/issues/1625)). Sign-in sessions listed and revoked by the person and by an operator ([#793](https://github.com/truvity/sluis/issues/793)). Passkey tiers and step-up, enforcement off by default ([#795](https://github.com/truvity/sluis/issues/795), [#885](https://github.com/truvity/sluis/issues/885)). A compromised key revoked, then replaced ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)). Rate and abuse limits in `entry` ([0036](/decisions/0036-volumetric-abuse/)). |
| **KV** | The `kv` actor serves versioned static secrets in stores that no tenant owns ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)): `sluis kv` for staff, a data key per store, and a blocking audit fact for every successful access. KV is a resource client, so a token for it comes by exchange ([0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/)). It is built before parity. Not in this part, and a later option decided at parity ([0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/)): the Vault KV v2 subset that External Secrets reads, the slice of workload exchange built for its login (the `k8s-sa-token` verifier and the declared tenant of a workload), the moving of generated secrets into KV and the External Secrets job in CI. |
| **Local** | One command brings up a full local installation, from source and from released images, and the smoke tests pass on it. A contributor guide says how. |
| **Boards** | [#1584](https://github.com/truvity/sluis/issues/1584) is closed: mutual TLS on Raft, flood handling and a firewall that fails closed. A rebuild and a lost-board rehearsal pass. `cloudflared` is deployed by the scripts, and atoom's `yubikey-piv` driver is installed from its package. The audit blob is `fs`; a bucket driver belongs to the Cloudflare add-on. |
| **Lambda** | Every function runs behind the vendored, pinned Lambda Web Adapter: an app zip and a config layer, plus one merged drivers layer only where a driver's language differs from the function's ([0011](/decisions/0011-runtimes-and-installations/)). The drivers are atoom's: `store/dynamodb` and `key/awskms` are built; the extension zips and the `source/ssm`, `source/appconfig`, `blob/s3` and `transport/sqs` drivers are planned in atoom's plan. exporter@ is woken by DynamoDB Streams and a daily run ([0009](/decisions/0009-export/#on-lambda)). The installation sits behind the Cloudflare edge ([0011](/decisions/0011-runtimes-and-installations/#the-cloudflare-edge)), runs with test clients only, and passes the same smoke tests as local. Audit's pieces run as audit's distribution record says. |
| **Kubernetes (EKS)** | The Helm chart ([#844](https://github.com/truvity/sluis/issues/844), [#876](https://github.com/truvity/sluis/issues/876)) installs on a test cluster and passes the same smoke tests. Its store is atoom's `postgres` store driver on CloudNativePG (planned in atoom's plan), the key driver is `aws-kms`, the blob is S3, and the audit transport is NATS JetStream. The edge is the cluster's own Cloudflare tunnel and gateway. A key driver for a cluster without AWS is later. |
| **Release management** | One release carries the packages, the images, the chart and the Lambda zips at one version, and a release candidate is built before each tag ([0013](/decisions/0013-release-train-packaging-and-distribution/)). Upgrade and rollback are tested on every installation. The CHANGELOG is kept. |

The core runs without an intermediate certificate: the token certificate is off, the key set carries no `x5c` chain, and a board needs no slot 9c ([0044](/decisions/0044-sluis-is-a-core-and-add-ons/), [0006](/decisions/0006-keys-and-sealing/#the-token-certificate)). Root custody reaches an installation through the hand-off to [ritueel](https://ritueel.id); the core does not wait for it.

Out of the core, and still out: sluis KV serving External Secrets (a later option, [ADR 0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/)), a key driver for a cluster without AWS, workload identity beyond what KV needs ([#870](https://github.com/truvity/sluis/issues/870)), federation, the proof verifiers, the signing API ([#890](https://github.com/truvity/sluis/issues/890)), the console (it is core and is built with Parity), `ca` beyond the token certificate, the Cloudflare and GitHub add-ons, and the mixed shape of [ADR 0011](/decisions/0011-runtimes-and-installations/) ([#926](https://github.com/truvity/sluis/issues/926)).

### Customer identity and quota

This milestone opens when the core is done. It has two parts.

1. **The exchange for resource clients** ([0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/), [0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/), [0032](/decisions/0032-customer-identity-installations/)). sluis has two kinds of client. A token for a resource client (an AWS role, Kubernetes, a database, SSH, a vendor minter, KV) is obtained by exchange, and a token for a sign-in client (a staff web tool, the console) by sign-in only. `exchange` issues tokens only for resource clients, whatever the subject token is: sluis's own sign-in, a token of the installation's trusted context realm (RFC 8693), or a workload's token. Who gets which resource, and with what scope, is a Cedar policy decision on facts of the subject token: its issuer and realm, its subject, and the customer and the product. Only staff and operators sign in to sluis, and a customer's token reaches it only as the subject of an exchange. The token carries none of `customer`, `org` and `product`: products use the group's identity provider's tokens and never a sluis token. The `ExchangeTrust` config kind declares the realm, and the central realm is never trusted. Boundaries stay the unit of keys, policy and audit for a customer's infrastructure, and tenant clients are resource clients only. Issues: [#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961).
2. **Quota,** which is the limit check of [0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/) and nothing more. A sluis operation emits its usage record to [audit](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md), which never drops it, and the operation fails when the record is not accepted. The check compares a counter with a limit that the product writes. Balances, plans and prices live in the product. The counter is keyed by the customer and the product of the subject token ([0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/)), or by the tenant and the client when it names neither; the organisation is left out of the limit key and of the policy facts, and sluis looks it up nowhere ([0053](/decisions/0053-the-limit-counter-is-keyed-by-customer-and-product-and-sluis-never-uses-the-organisation/)). Issue: [#841](https://github.com/truvity/sluis/issues/841).

**Done when** a sluis sign-in token, a token of the trusted realm and a workload token are each exchanged for a sluis token for a resource client, with none of the three claims, on the local installation, and a token of another realm, or a request for a sign-in client, is refused; and the limit check refuses at the limit, and an operation that produces a usage record fails when the record cannot be stored.

### Parity

v2 covers every v1 parity row that is required. The rows are the issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity), listed on the [v1 parity checklist](/features/); the rest of the group is Backlog. Which rows are required is decided when parity is planned in detail, which waits until the exchange for resource clients and the limit check are done. At parity the maintainer also decides whether sluis KV serves External Secrets ([0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/)). Open: that list.

The console is part of the core layer and is built in this milestone ([0027](/decisions/0027-the-console/)).

**Done when** every required parity row is done and has its evidence, and the console is built. The cutover from v1 starts here: [ADR 0001](/decisions/0001-v1-and-v2-lines/#cutover-from-v1) says how.

### The PKI, SSH and database add-on

After parity. The add-on replaces OpenBao.

- **The add-on.** Leaf profiles and the revocation list ([#913](https://github.com/truvity/sluis/issues/913)); `ssh-ca`, which turns a sluis token into a short-lived SSH user certificate and issues host certificates under the SSH CA that ritueel lists ([#925](https://github.com/truvity/sluis/issues/925), [0023](/decisions/0023-certificate-authorities/)); PostgreSQL client certificates for CloudNativePG ([#926](https://github.com/truvity/sluis/issues/926)). The token certificate and the `x5c` chain come on with it.
- **Entry conditions.** A ritueel release candidate with a software root exists, and a cloud root is adopted. Until then the add-on runs on the test harness's root.

**Done when** an operator gets an SSH certificate and a PostgreSQL client certificate from a sluis token.

The Cloudflare and GitHub add-ons have no milestone: each is built when a consumer needs it ([ADR 0044](/decisions/0044-sluis-is-a-core-and-add-ons/#the-add-ons)).

## Cancelled

These are not planned. Their records are superseded, and their requirement rows are withdrawn.

- **A customer-facing broker:** customer pages, customer connections, the end-user lifecycle and pairwise subjects. Customers sign in through the upstream identity provider ([0032](/decisions/0032-customer-identity-installations/); the superseded records are in the [archive](/decisions/archive/0031-ciam-is-a-broker/)).
- **A sluis sign-in session for customers.** Only staff and operators sign in to sluis, and a customer's token reaches it only as the subject of an exchange ([0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/)).
- **A sluis token for products.** A sluis token carrying a customer, an organisation and a product, and services calling each other with it. Products use the group's identity provider's tokens ([0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)).
- **Moving External Secrets to sluis KV as planned work.** It is a later option, decided at parity ([0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/)).
- **An allowance and balance engine inside sluis.** Plans and balances live in the product; sluis keeps the limit check ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)).
- **OpenBao as a driver.** The PKI, SSH and database add-on replaces it instead.

## Later

**The backlog** holds the plans that nothing above needs: the v1 parity rows that are not required, the isolation harness and generators, the extra board rows, the extras of the Cloudflare and GitHub add-ons, the reconcilers, and libraries in languages other than Go and Rust. It also holds CIBA in poll mode with a passkey on the approving device, once a real consumer exists ([#980](https://github.com/truvity/sluis/issues/980)); ACME for host certificates ([#978](https://github.com/truvity/sluis/issues/978)); a FIPS build variant when a customer needs one ([#868](https://github.com/truvity/sluis/issues/868)); and other clouds and stores.

**Staff keys** are Backlog: card binding per employee, and a company's own OpenPGP keys for its staff synced to their GitHub accounts ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/), [0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/)). Binding is for staff and operators only: a customer has no sluis session ([0052](/decisions/0052-a-customer-has-no-sluis-session-so-0043-binds-staff-keys-only/)). No milestone waits for them. They need the hardware-attested sign-in, the directory and `adapter-github` first.

**Signing of release artifacts** is Backlog: a release carries checksums and SBOMs, and its tag is signed by hand ([0013](/decisions/0013-release-train-packaging-and-distribution/#signing-and-checksums)). The signing API ([#890](https://github.com/truvity/sluis/issues/890)) is a separate thing and is in parity.

## Where the work is tracked

The `Product milestone` field of the [board](https://github.com/orgs/truvity/projects/9) carries the milestones, under the names in `docs/requirements/index.yaml`. The v1 features that v2 must match are the issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity). Spike results are in the records they settled, such as [ADR 0012](/decisions/0012-the-language-follows-the-executable/#the-spikes-are-gates), and on the issue of each spike.
