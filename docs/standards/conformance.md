---
title: Conformance
description: "How sluis proves the standards it claims: the OpenID Foundation test plans v2 targets, how they run in CI per installation, and when certification is planned."
status: target
sidebar:
  order: 1
---

A claim of conformance is only worth what the test behind it is worth. sluis's rule is to claim a profile only when a published test suite passes it, to keep the proof in the repository, and to run it often enough that the claim stays true. This page is the programme. The standards themselves are in the [register](/standards/).

You can follow the work on the project's [Conformance view](https://github.com/orgs/truvity/projects/9/views/6). The umbrella issue is [#850](https://github.com/truvity/sluis/issues/850), and the port suite is [#851](https://github.com/truvity/sluis/issues/851).

## What v1 has done

v1 runs the OpenID Foundation conformance suite for four profiles: Config, Basic OP, RP-Initiated Logout and Back-Channel Logout. The runs so far are self-runs, not certifications. No module failed in any of them, and every warning is accepted with a reason. The details, the findings and the fixes they led to are in v1's documentation:

- [What did the OpenID conformance run find?](/v1/concepts/sluis/conformance-findings/)
- [Run the OpenID conformance suite](/v1/guides/sluis/run-conformance/)
- [What is not served?](/v1/concepts/sluis/not-served/)

v1's runs are attended: a headless-browser driver signs in through the recovery account and hands the screenshots of the review steps to a person. v2 keeps the parity: it must pass the same four plans ([#850](https://github.com/truvity/sluis/issues/850)).

## Which plans v2 targets

The plan names are the OpenID Foundation's. The exact plan identifiers in the suite are confirmed against the current suite release before the work on each starts.

| Plan | What it covers | Status |
|---|---|---|
| Config OP | The discovery document and the key set | Planned, first |
| Basic OP | The code flow with PKCE, the ID token, UserInfo and refresh, for static client registration with discovery | Planned, first |
| RP-Initiated Logout OP | `end_session`, `id_token_hint`, post-logout redirects. Every logout submission must include it | Planned, first |
| Back-Channel Logout OP | The signed logout token, its two-minute expiry and no `nonce` | Planned, first. v1 passes it, so v2 keeps it |
| FAPI 2.0 Security Profile (Final) OP | The sender-constrained variants sluis supports (DPoP; mutual TLS only if added) | Planned, later. Needs PAR, `private_key_jwt`, DPoP, RFC 9207 and, for some variants, JAR |
| FAPI 2.0 Message Signing OP | Signed requests and responses | Planned, later. Builds on the Security Profile |
| Relying-party plans (Basic RP, Config RP and the logout RP plans that apply) | sluis acting as a client of an upstream provider | Planned, later. Runs against the [generic OIDC upstream](/integrations/oidc-upstream/) |
| Form Post OP | `response_mode=form_post` | Decision pending |
| 3rd Party-Initiated Login OP | `initiate_login_uri` | Decision pending |
| Front-Channel Logout OP | Front-channel logout | Decision pending |
| FAPI-CIBA OP | Client-initiated backchannel authentication (poll mode required, ping optional) | Decision pending |
| WebAuthn conformance tool | The passkey ceremony ([#885](https://github.com/truvity/sluis/issues/885)) | Planned with passkeys |

### The decisions

:::note[Proposed]
Four optional plans are decisions, not plans. Each would be recorded in an ADR, and the plan run only if the answer is yes.

- **Form post.** v1 serves the query response mode only. Some security profiles prefer form post. Options: do not support it and document that; or support it for the code flow and run the plan for Basic.
- **Front-channel logout.** It depends on browser behaviour that is weakening, and back-channel logout is the supported path. Options: not supported; or supported alongside back-channel.
- **Third-party-initiated login.** It lets a client be started from the provider's own portal, which the console may want. Options: not supported; or supported for registered clients only.
- **CIBA.** It lets a service ask for a person's approval on another device. Device login ([#855](https://github.com/truvity/sluis/issues/855)) covers part of the need for the command line. Options: not supported; poll mode only; poll and ping.
:::

### The plans sluis does not claim

Users and auditors should not infer support from silence. These are not planned unless a decision changes them:

| Plan | Why not |
|---|---|
| Implicit OP and Hybrid OP | The implicit and hybrid response types are not served; code with PKCE replaces them. |
| Dynamic OP | Dynamic client registration is refused (an endpoint that mints trust). Client ID Metadata Documents admit self-describing clients from allow-listed origins. |
| Session Management OP | A browser iframe mechanism. |
| FAPI 1.0 Advanced | Superseded by FAPI 2.0. |

A suite module may skip because the discovery document says a feature is not served. That is the intended outcome: **discovery is truthful**, and a skip is acceptable only when discovery explains it.

## How the tests run in CI

:::note[Proposed]
The design is that conformance is a pipeline, not a ritual, because a run done by hand rots.

- **The suite** is the open-source OpenID Foundation conformance suite, a self-hosted instance.
- **Against the installations.** Each plan runs against a v2 installation, on the [local](/runtimes/installations/#local) installation and on one cloud installation ([lambda](/runtimes/installations/#lambda)), using the [installation test harness](https://github.com/truvity/sluis/issues/877). The cloud installation runs only in a restricted private runner group ([#891](https://github.com/truvity/sluis/issues/891)) that has no pull-request trigger.
- **When.** On pull requests that touch protocol code, against the local installation; nightly and on every release, for the full set and the cloud installation. A change that breaks a claimed plan fails CI.
- **The inputs.** The harness clients and a conformance sign-in account are declared in a reviewed file. They exist only in the test installation.
- **The outputs.** Logs and result bundles are archived per release. Result exports hold client secrets, so what is published is the plan, the variant, the version and the counts.
- **The attended steps.** Some modules end at a page the suite cannot judge, such as an error page the issuer must show. v1 hands those screenshots to a person. Whether v2 keeps a person in that loop, or compares the page automatically, is not decided.
:::

## What certification means, and when

Passing the suite on your own and **certification** are different things. The OpenID Foundation runs a certification programme in which an implementer submits the suite's results for a **specific version** and **profile**, with a statement, and the Foundation lists the implementation as certified for it. A logout submission must include RP-Initiated Logout plus one other logout profile.

What certification does not mean: it is not a security audit, not a FIPS validation (that is a separate programme, see the [register](/standards/#crypto-and-compliance)), and not a promise about the next release. An external penetration test and a certification are listed as conditions for calling a customer-identity profile production-ready.

:::note[Proposed]
Certification is planned after v2 passes the first-wave plans in CI on a tagged release, and not before v2 reaches parity with v1. There is no date. The submission package and the self-certification statement are assembled by a script from the archived results, so the submission is a by-product of a release, not a separate project. FAPI 2.0 certification follows when PAR, DPoP and `private_key_jwt` ship.
:::

## Conformance beyond OpenID

OpenID plans cover the protocol surface. Two other suites cover what sits underneath.

**The port conformance suite** ([#851](https://github.com/truvity/sluis/issues/851)). Every driver of every [port](/concepts/drivers/) (keys, state, lease, blob, transport, index, witness, cloud, secrets, export) must pass one shared behavioural suite. In v1 the suite ran against memory, the legacy store and DynamoDB; in v2 it lives in `kits/conformance` and runs per driver and per kit language (Go, Rust and TypeScript). A new driver is accepted when it passes; a fork that adds one runs the same suite.

**The kits' conformance vectors** ([#861](https://github.com/truvity/sluis/issues/861)). The shared kits exist in parallel in three languages: the ConnectRPC server and client over Unix sockets with peer credentials, the configuration loader and validator, and the command-line shape. One shared set of vectors runs against all three, so they agree. For configuration, that includes which documents are valid under the generated JSON Schema ([Configuration](/concepts/configuration/)).

**Minters.** A shared test for every `CredentialMinter` integration: create a credential, use it, let it expire, and sweep. A new minter is accepted when it passes.

**Policy tests** ([#852](https://github.com/truvity/sluis/issues/852)). The policy has its own tests for the grant naming, the matchers and the lifetimes.

**The isolation vectors.** Every control of the [threat model](/security/threat-model/) has test vectors in the [isolation area](/requirements/iso/) of the requirements catalogue. Each vector is an attempt that must fail at a named layer (the port library, the driver's or backend's own check, or physical separation) while the harness switches the other layers off ([#877](https://github.com/truvity/sluis/issues/877)). A vector that is refused even with every layer off is void, so a passing run proves each layer on its own.

**The deny matrix.** The permission manifest generates tests that prove an actor cannot do what its manifest does not say ([Architecture](/architecture/)).

## Where to follow it

- The [Conformance view](https://github.com/orgs/truvity/projects/9/views/6) of the project board.
- [#850](https://github.com/truvity/sluis/issues/850), OpenID conformance and standards coverage; [#851](https://github.com/truvity/sluis/issues/851), the port conformance suite.
- The [standards register](/standards/), for the status of each standard.
- [Open decisions](/open-decisions/), for decided and open design points.
