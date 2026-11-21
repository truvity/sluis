---
title: Conformance
description: "What sluis proves in its own CI: the OpenID test plans v2 targets, the deny matrix, isolation vectors, driver host smoke tests and catalogue coverage."
status: target
sidebar:
  order: 1
---

A claim of conformance is only worth what the test behind it is worth. sluis claims a profile only when a published test suite passes it, keeps the proof in the repository, and runs the suite often enough that the claim stays true. The standards themselves are in the [register](/standards/). The umbrella issue is [#850](https://github.com/truvity/sluis/issues/850).

This page is sluis's conformance: what the product proves in its own CI. atoom proves the contract of each driver and block, and sluis proves everything built on them ([Conformance beyond OpenID](#conformance-beyond-openid)).

## Which plans v2 targets

The plan names are the OpenID Foundation's; the exact plan identifiers are confirmed against the current suite release before the work on each starts. v2 keeps parity with v1 on the first four.

| Plan | What it covers | Status |
|---|---|---|
| Config OP | The discovery document and the key set | First wave |
| Basic OP | The code flow with PKCE (or the `nonce` for a confidential client that is set to it, [0021](/decisions/0021-clients/#the-client-declaration)), the ID token, UserInfo and refresh, for static client registration with discovery | First wave |
| RP-Initiated Logout OP | `end_session`, `id_token_hint`, post-logout redirects; every logout submission must include it | First wave |
| Back-Channel Logout OP | The signed logout token, its two-minute expiry and no `nonce` | First wave |
| Form Post OP | `response_mode=form_post` for the code flow; the auto-submit page has a strict CSP that allows its one script by hash, with a `form-action` of the redirect URI's origin | Runs as a public client, in the comprehensive plan ([below](#what-runs-today)); the certification plan waits for confidential clients ([#979](https://github.com/truvity/sluis/issues/979)) |
| WebAuthn, with a browser virtual authenticator and a software authenticator library | The passkey flow. sluis claims no FIDO conformance: the FIDO Alliance's conformance tools are not run, and no row relies on them (decided 2026-11-07) | With passkeys ([#885](https://github.com/truvity/sluis/issues/885)) |
| Relying-party plans (Basic RP, Config RP and the logout RP plans that apply) | sluis as a client of an upstream provider, through the [generic OIDC upstream](/integrations/oidc-upstream/) | Later ([#930](https://github.com/truvity/sluis/issues/930)) |
| FAPI 2.0 Security Profile (Final) OP | The sender-constrained variant sluis supports in the core, DPoP (mutual TLS binding comes later); needs PAR, `private_key_jwt`, DPoP, RFC 9207 and, for some variants, JAR | In the core, beside the Basic plans ([#929](https://github.com/truvity/sluis/issues/929)); not yet run in CI |
| FAPI 2.0 Message Signing OP | Signed requests and responses, on top of the Security Profile | With its first relying party ([#929](https://github.com/truvity/sluis/issues/929)) |
| CIBA, poll mode | Approval with a passkey on the approving device | Later: after the parity milestone, once a real consumer exists ([#980](https://github.com/truvity/sluis/issues/980)) |

The optional plans follow [ADR 0017](/decisions/0017-identity-and-sign-in/).

### The plans sluis does not claim

Users and auditors should not infer support from silence. These are not planned unless a decision changes them:

| Plan | Why not |
|---|---|
| Implicit OP and Hybrid OP | The implicit and hybrid response types are not served; code with PKCE replaces them. |
| Dynamic OP | Dynamic client registration is refused; Client ID Metadata Documents admit self-describing clients from allow-listed origins. |
| Session Management OP | A browser iframe mechanism. |
| Front-Channel Logout OP | Hidden iframes depend on third-party cookies, which browsers block. Back-channel logout is the path, and discovery does not advertise front-channel logout. |
| 3rd Party-Initiated Login OP | A sign-in starts at the client. Revisited if the console gets an app launcher. |
| FAPI 1.0 Advanced | Superseded by FAPI 2.0. |

A suite module may skip because the discovery document says a feature is not served. That is the intended outcome: **discovery is truthful**, and a skip is acceptable only when discovery explains it.

## How the tests run in CI

Conformance is a pipeline, not a ritual, because a run done by hand rots. The pipeline is [#931](https://github.com/truvity/sluis/issues/931), part of the core.

- **The suite** is a self-hosted instance of the open-source OpenID Foundation conformance suite.
- **Against the installations.** Each plan runs against the [local](/runtimes/#local) installation and the [lambda](/runtimes/#lambda) installation, through the [installation test harness](https://github.com/truvity/sluis/issues/877). The installation on [Kubernetes](/runtimes/#kubernetes) runs the same set, and FAPI 2.0 runs against the core issuer beside the Basic plans ([0038](/decisions/0038-fapi-2-profile/#the-profile)). Today only the local installation runs the suite; the others follow with their installations. The cloud installations run only in a restricted private runner group with no pull-request trigger ([#891](https://github.com/truvity/sluis/issues/891)).
- **When.** On pull requests that touch protocol code, against the local installation; nightly and on every release, the full set against the lambda and Kubernetes installations. A change that breaks a claimed plan fails CI.
- **The inputs.** sluis holds no accounts, even in tests. The harness signs people in through a real identity provider, Keycloak in a container on every pull request and nightly against two free cloud tenants ([ADR 0011](/decisions/0011-runtimes-and-installations/#the-local-installation)); the harness clients and that provider's realm are declared in a reviewed file. Passkeys are tested with a browser's virtual authenticator and a software authenticator library. The product carries no test code: the server under test is the released build, and only configuration differs.
- **The outputs.** Logs and result bundles are archived per release. Result exports hold client secrets, so what is published is the plan, the variant, the version and the counts.
- **The attended steps.** Some modules end at a page the suite cannot judge, such as an error page the issuer must show. The pipeline either compares the page automatically or hands it to a reviewer, as [#931](https://github.com/truvity/sluis/issues/931) settles.

## What runs today

The core runs the suite in CI against the local installation, as a **public client** (`client_auth_type=none`). **That proves flows, not a certification profile, and the core cannot claim to be OpenID certified.** The FAPI 2.0 plans are not run in CI yet. The Basic and Form Post certification plans fix a confidential client, and the confidential clients of the core are not in the run yet; so the run uses the suite's comprehensive plan `oidcc-test-plan` with `client_registration=static_client`, `response_type=code` and the two response modes, and the Config plan, which has no client variant.

- **The suite** is release v5.3.4 of the open-source suite, self-hosted: the Java application, MongoDB and the suite's nginx, in containers, every image pinned by digest (`tests/conformance/compose.yaml`). No account, no secret, no call outside the runner except the image pulls. A relay on the installation's Compose network gives the suite the issuer's name; the suite is a relying party of the released build, and nothing in the product knows about it.
- **The browser step.** The suite needs a person to sign in. A runner (`tests/conformance/runner`) takes the addresses the suite asks a person to visit and signs a test user in at the identity provider's forms, in one browser per plan, then delivers the redirect to the suite. The modules that end on a page of sluis's own run in a second plan whose scripted browser reads the page for its heading; their result is REVIEW, the best the suite gives a page.
- **The table.** `tests/conformance/expected.yaml` lists every module of every plan with the result it must end in. The runner fails on any difference, in either direction: a pass that fails, a fail that passes, a skip that runs, a pass that skips, a module the table lacks or the suite lacks. A module expected to fail or to skip carries the words of its failure or the suite's reason, and `cleared_by` says which planned work should make it pass. **An unexpected skip never reads as a pass.** A change that moves a result edits the table in the same pull request, with the reason.
- **The runner's probes.** The suite sends a valid PKCE request or none, so the runner asks for what the suite does not, as the same public client: no challenge, the method `plain`, an unsupported `response_type` and none at the authorization endpoint, and at the token endpoint a wrong verifier, none, and the challenge used as the verifier. Each must be refused as it should; a probe that fails fails the run.
- **The gate.** The Go tests in `tests/conformance/runner/gate_test.go` carry the requirement tags. Each says which conditions of which module the suite must have judged a success, and which probes must have passed; they skip without a run, and `run.sh` fails when one skips or does not pass.
- **The rows.** Seven rows are proved by the gate: REQ-AUTHN-002, 005, 030, 058 and REQ-TOKEN-010, 013, 030. **They stay `partial` for now** (`external-suite-not-run`), because `Conformance` is not a required check, so a red run cannot stop a merge, and a row is `done` only where a failing test would. The flip is part of the change that joins the job to the shared `checks` gate.
- **What the suite shows (measured 2026-11-07, 73 module runs in three plans; the table is the current statement).** Four modules pass in the default and form_post plans together, six end in REVIEW, thirteen skip for a reason the table states, and forty-nine fail in the two comprehensive plans plus one in the Config plan, each for a stated reason:
  - **PKCE (forty-two).** The suite's OpenID Connect Core modules send no `code_challenge`, and sluis requires PKCE with S256 on every code request (REQ-AUTHN-002). OpenID Connect Core does not require PKCE, so a certified Basic OP would have to accept these requests. The decision is that a confidential client may skip PKCE when its request carries a nonce, by a per-client setting that is off by default; a public client always needs PKCE. Once that client exists, the certification Basic and Form Post plans run with it.
  - **RS256 (two).** `oidcc-discovery-endpoint-verification` fails because OpenID Connect Discovery requires RS256 among the ID token signing algorithms, and sluis lists ES384 only ([0006](/decisions/0006-keys-and-sealing/#named-exceptions)). The per-audience RS256 exception is built, but discovery lists RS256 only while an RS256 key is active, and the suite's installation has none.
  - **No rotation (two), a prompt module (two) and form post with request objects (one),** each for a cause the table names.
- **When and what it costs.** The workflow `Conformance` (`.github/workflows/conformance.yaml`) runs on a push to `main` and on a pull request that touches `entry`, `signin`, `signer`, the upstream adapter, the protocol files, the installation's configuration or the runner. It is a workflow of its own, not a job of `Checks`, because a path-filtered job cannot be a required check. The run itself took 1 minute 38 seconds on a development machine with the images already pulled (2026-11-07), because almost every module ends at the authorization endpoint. **Who reads it:** the maintainer who merged the change, as for any run on `main` ([After the merge](/contributing/workflow/#after-the-merge)); a red run on `main` is looked at and reported before the next change starts. When the confidential client exists, the job joins the shared `checks` gate.

## What certification means, and when

Passing the suite on your own and **certification** are different things. In the OpenID Foundation's certification programme an implementer submits the suite's results for a **specific version** and **profile**, with a statement, and the Foundation lists the implementation as certified for it. A logout submission must include RP-Initiated Logout plus one other logout profile.

Certification is not a security audit, not a FIPS validation (sluis v2.0 has no FIPS build; see the [register](/standards/#crypto-and-compliance)), and not a promise about the next release. An external penetration test and a certification are conditions for calling a customer-identity profile production-ready.

The submission package and the self-certification statement are assembled by a script from a release's archived results, so a submission is a by-product of a release, not a separate project.

:::note[Proposed]
Certification comes after v2 passes the first-wave plans in CI on a tagged release, and not before v2 reaches parity with v1; there is no date ([#931](https://github.com/truvity/sluis/issues/931)). FAPI 2.0 certification follows when PAR, DPoP and `private_key_jwt` ship ([#929](https://github.com/truvity/sluis/issues/929)).
:::

## Conformance beyond OpenID

OpenID plans cover the protocol surface. These cover what sits underneath, and each runs in sluis's own CI:

- **The deny matrix.** The kind registry and the actor registry generate tests that prove an actor cannot do what they do not say: every pair of actors is tried against both layers of the call graph ([Architecture](/architecture/), [0003](/decisions/0003-units-transports-and-fusion/#the-call-graph)).
- **The isolation vectors** ([#950](https://github.com/truvity/sluis/issues/950)). Every control of the [threat model](/security/threat-model/) has vectors in the [isolation area](/requirements/iso/) of the requirements catalogue. Each is an attempt that must fail at a named layer while the harness switches the other layers off. A vector that is refused even with every layer off is void, so a passing run proves each layer on its own.
- **The smoke test of each atoom driver host.** `just atoom-smoke` runs one test for each host that sluis keeps in `drivers/`: the host starts atoom's driver behind its guarded socket and one round trip comes back. It proves sluis's host, not the driver.
- **The vectors of the standards and of sluis's formats.** The signed-config documents run as one set of vectors through the Rust and the Go verifier ([Signed config](/reference/signed-config/#test-keys-and-vectors)); the bootstrap file and the YAML profile have schema vectors that both readers must agree on; the policy example has Cedar test vectors ([Policy](/reference/policy/#test-vectors)); the canonical-JSON corpus belongs to audit.
- **The kits' conformance vectors** ([#861](https://github.com/truvity/sluis/issues/861)). The two kits, Go and Rust, pass one conformance suite ([0012](/decisions/0012-the-language-follows-the-executable/#the-rules), R9): Connect over Unix sockets with peer credentials, the typed store client, configuration from the generated JSON Schema, the CLI conventions and OpenTelemetry.
- **Policy tests** ([#852](https://github.com/truvity/sluis/issues/852)) for the grant naming, the matchers and the lifetimes, and a shared test for every credential-minter integration: create a credential, use it, let it expire, and sweep.

### What atoom proves

atoom proves the contract of each driver and block with the conformance kit of its port, run through the driver's socket, and with the driver's own integration tests; it proves nothing about a product built on top ([atoom 0014](https://github.com/truvity/atoom/blob/master/docs/decisions/0014-what-atoom-proves.md)). The kits are [`store/conformance`](https://github.com/truvity/atoom/blob/master/store/conformance/docs/suite.md), [`source/conformance`](https://github.com/truvity/atoom/blob/master/source/conformance/docs/suite.md), `key/conformance`, `blob/conformance` and the kits of the other ports, each next to its port. A driver passes its suite or it is not a driver, and the in-memory driver passes too.

A requirement of sluis whose behaviour is atoom's is marked `gap: proved-in-atoom` in the catalogue. The mark says that the proof is atoom's kit in atoom's CI and that sluis runs only the smoke test of its host; it is not a failing test of this repository. Each time sluis takes a new atoom (a new pin, then a new release), it runs its own conformance again, because atoom's tests cannot see what sluis builds on its drivers.

## How much of the catalogue an external suite checks

The [requirements catalogue](/requirements/) says, for each requirement, how it is verified. A requirement is **externally checked** when its verification includes a published conformance suite or a set of test vectors, rather than only a test of sluis's own or a manual review. The site build computes the figures below from the catalogue, so they are never out of date.

[[coverage-table]]

## Where to follow it

[#850](https://github.com/truvity/sluis/issues/850), OpenID conformance; [#929](https://github.com/truvity/sluis/issues/929), FAPI 2.0; [#930](https://github.com/truvity/sluis/issues/930), the relying-party plans; [#931](https://github.com/truvity/sluis/issues/931), the pipeline; [#950](https://github.com/truvity/sluis/issues/950), the isolation vectors.
