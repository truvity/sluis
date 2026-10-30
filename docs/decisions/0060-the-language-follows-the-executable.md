# 0060 — The language follows the executable: Go by default, Rust at the edge, in the trusted base and in cloud drivers

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (the open points of [issue #910](https://github.com/truvity/sluis/issues/910) decided: fallbacks are candidates chosen when a spike fails, `cloud-cloudflare` is Go, `sluis-node` verifies OpenPGP signatures with Sigstore provenance as a second signal, and the ceremony is a `sluis` subcommand with a Rust helper inside the `sluis` package; OD-16 holds without exception).
Rewritten in place on 2026-10-31 (`cloud-garage` is Rust, by rule R5).
Rewritten in place on 2026-10-31 (no FIPS build in v2.0, after spike S2 ([issue #940](https://github.com/truvity/sluis/issues/940)): R8 is no longer FIPS parity, and the AWS drivers stay Rust).

## Context

sluis v2 is written in two languages already. The `policy` actor is Rust, because Cedar's engine is a Rust crate ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/), OD-10). The cloud drivers for AWS, GCP and Azure are Rust (OD-14). Everything else was proposed as Go. Two toolchains, a Rust kit for Connect over Unix sockets, configuration and the conformance suite are therefore a cost already paid. Moving one more component to Rust costs that component's code, not a second toolchain.

The unit of choice is the executable. Every component boundary is a ConnectRPC contract ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)), every driver is its own service on a Unix socket ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)), and the executable a unit starts is the only code in its process. A driver's language is therefore independent of its caller's, and a wrong choice can be reversed one component at a time.

The trigger was the slim `entry` (ADR 0007): the one actor that faces the internet, now holding no key, no secret and no state. Hardening it raised the question whether it should be in a memory-safe language with no garbage collector. The research of 2026-10-30 found:

- **Go is memory-safe with exceptions:** data races on multi-word values, `unsafe` and cgo. The Go memory model says such races can lead to arbitrary memory corruption.
- **The bug classes are the same in practice.** Since 2024 the Go standard library had 89 advisories and none was memory corruption: they were denial of service, panics and logic bugs, including name-constraint bypasses in crypto/x509 and request smuggling in net/http. Rust's TLS and certificate crates had the same classes in the same period, and aws-lc-sys, which is C, had PKCS#7 verification bypasses. hyper itself has had no advisory since 2022.
- **What Rust buys:** no memory corruption through data races, which matters most in the most concurrent process; deterministic destruction and zeroisation of secrets (Go's `runtime/secret` is still an experiment in Go 1.27.3); a smaller footprint and faster cold start; a cleaner record for the HTTP server. On lambda-perf (2026-10-30; arm64, zip, 128 MB, hello world), Go initialised in 46.6 ms using 17 MB and Rust in 18.0 ms using 13.3 MB.
- **What Rust does not buy:** freedom from parser logic bugs, allocation bombs or panics. Rust's cryptography is usually C underneath, while Go's is pure Go and validated in-tree.
- **The libraries decide many components.** Go clearly leads for an embeddable OpenID Connect provider, the X.509 CA and the static-ct-api issuance log, the C2SP transparency-log tools, PIV attestation, HPKE, Kubernetes controllers and the vendor SDKs (Google Directory, Cloudflare, GitHub, Slack). Rust clearly leads for the OpenPGP smart card (`openpgp-card` with rPGP, which was audited in 2024-12). Go has no maintained OpenPGP-card library.
- **The Rust side has known gaps.** Connect for Rust (`connectrpc` 0.9.1, 2026-09-21) is official but before 1.0, with one named maintainer. OpenTelemetry traces in Rust are Beta. The FIPS build of aws-lc-rs needs CMake, Go and a C toolchain, and the current aws-lc-rs binds a module that is still in process at the CMVP, so a certified build pins an older one. Spike S2 confirmed both, and found that the AWS SDK for Rust signs SigV4 requests and S3 checksums with RustCrypto, outside the module. GoReleaser's Rust builder warns that workspaces might not work.

One inconsistency had to be resolved. OD-14 says Rust for the AWS drivers, while the source-layout table put the AWS-backed drivers of the other ports (`aws-kms`, `dynamodb`, `sqs`, the object-store blob driver) in Go.

FIPS is [0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/): no FIPS build in v2.0, and every algorithm within the FIPS-approved set. The release train and GoReleaser are [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/). The delivery order is [0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/).

## Decision

### The rules

- **R1. One executable, one language.** An actor is never split across languages. A driver is its own executable and may differ from its caller. The choice is recorded per component in the source-layout table, with its status.
- **R2. Rust at the unauthenticated edge**, where a process is reachable by unauthenticated network input and needs no protocol library that is mature only in Go. Today that is the slim `entry`, and the key-less edge relay on Kubernetes ([OD-24](/open-decisions/#od-24)). An edge actor that needs an OpenID Connect provider stays Go until a Rust one exists.
- **R3. Rust for the trusted base:** processes that run as root or as a shared mediator, speak a fixed local protocol and hold a PIN, a session or key material in memory. These are `sluis-node`, `key-holder` and `sluis-host`.
- **R4. Go where the deciding library is Go-only or best in Go:** the X.509 CA and its issuance log, the C2SP log writer and witness, the OpenID Connect provider and token exchange, PIV attestation, HPKE, age with post-quantum recipients, TPM, Kubernetes controllers and the cert-manager issuer, and the vendor SDKs.
- **R5. Drivers follow their backend's best SDK, with one language per vendor SDK in the build.** AWS, GCP and Azure are Rust. Local backends default to Go. One named exception: `cloud-cloudflare` is Go (below).
- **R6. Go by default** for business logic, CRUD and integrations. The reasons are delivery speed, stable OpenTelemetry and the wider hiring pool.
- **R7. No Rust where the only library for a security function is marked experimental or unaudited.** Use a standard interface such as PKCS#11, or stay in Go. The `yubikey` crate is the case in point.
- **R8. No FIPS build in v2.0.** Rust components use current crates and rustls, with no FIPS pin, and keep to the FIPS-approved algorithms of ADR 0010. When a FIPS build variant is added, a Rust component that performs an approved operation uses aws-lc-rs in FIPS mode, and the AWS SDK gap (SigV4 signed outside the module) must be solved, or the AWS drivers are built in Go for that variant. The AWS drivers stay Rust (R5).
- **R9. Both kits pass one conformance suite.** Every component, in either language, uses its language's kit (Connect over Unix sockets with peer credentials, configuration from the JSON Schema, the CLI conventions, OpenTelemetry). A component may move language only once its language's kit passes the suite.
- **R10. Contracts are on the wire, so a choice is reversible.** A language change is a replacement of one component behind the same contract and suite, never a fork of the contract.

### The verdicts

| Component | Language | Rule | Gate |
|---|---|---|---|
| `policy` | Rust | R4 (`cedar-policy`) | None; decided by OD-10 and [0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/) |
| `cloud` port drivers: web identity, platform role, GCP and Azure federation | Rust | R5 | None; decided by OD-14 |
| AWS-backed drivers of the other ports: `aws-kms`, `dynamodb` (state and lease), `sqs`, `s3-compatible` blob | Rust | R5 | S1 |
| `entry` (slim) | Rust | R2 | S1 and S3 |
| `sluis-node` | Rust. It verifies OpenPGP signatures, with Sigstore provenance as a second signal | R3 | S1 |
| `key-holder` | Rust, through PKCS#11 (`cryptoki` with Yubico's `ykcs11`) | R3, R7 | S4 |
| `sluis-host` | Rust | R3 | S1 |
| The ceremony's OpenPGP-card helper, run by `sluis ceremony` | Rust (`openpgp-card` with rPGP) | The deciding library is Rust-only, the converse of R4 | S8 |
| `cloud-garage` | Rust; it shares no library with an integration, so R5 applies | R5 | None |
| `cloud-cloudflare` | Go, sharing the library of the integration `cloudflare@<company>` | R4: an exception to R5 | None |
| `session`, `exchange`, `console`, `quota`, `signer`, `ca`, `ssh-ca` | Go | R4, R6 | None |
| Audit actors: `writer`, `witness`, `indexer`, `reader` | Go | R4 | None |
| Integrations | Go | R4, R6 | None |
| The `sluis` CLI, `sluis agent`, the cert-manager issuer | Go | R4, R6 | None |
| Other drivers (local backends, indexes, witnesses, `nats-jetstream`, `age` export) | Go | R5, R6 (inferred: the decision does not list them one by one) | None |

- **The cloud and AWS-backed drivers are one group.** The inconsistency with OD-14 is resolved in its favour: every AWS-backed driver is Rust, so the build has one AWS SDK language. These drivers run as Lambda extensions and as sidecars, where start-up time adds to every cold start.
- **`exchange` stays Go.** It is the most parser-heavy actor, but nearly every verifier it needs is best in Go. Its HPKE private key stays behind the `keys` port, and the cookie-sealing key of `session` does too.
- **`key-holder` goes through PKCS#11**, not through the `yubikey` crate (R7). `cryptoki` loads the module at run time, so the YubiKey driver needs no C at build time and no native job per architecture. Attestation verification stays in the Go CLI (`piv.Verify`), at ceremony and node initialisation.
- **The ceremonies are `sluis ceremony …` subcommands of the one `sluis` CLI**, so OD-16 holds without exception. The part that drives the OpenPGP cards with `openpgp-card` and rPGP is a Rust helper executable under `/usr/libexec/sluis/`, which the CLI runs. The helper ships **inside the `sluis` package**; there is no separate ceremony package ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).
- **A ceremony runs in three steps:** prepare it online; run the same `sluis` with the same configuration on the offline live USB system, where the cards are; then complete and publish it online.
- **`cloud-cloudflare` is Go.** It shares the Cloudflare library with the integration `cloudflare@<company>`, and Cloudflare's best SDK is Go. This is an exception to R5, which would put a `cloud` port driver in Rust: one vendor library in one language weighs more here than one language for every `cloud` driver.
- **`sluis-node` verifies OpenPGP signatures** on signed configuration and binaries, by the release key certified under both roots. Sigstore provenance is checked as a second signal, not as the trust. OpenPGP is what the Rust stack handles well, and it is the format the roots already sign with ([Release signing](/distribution/release-signing/)). (the last reason is inferred)

### The spikes are gates

Each conditional verdict holds only once its spike passes. The languages are fixed from the spike results before step-1 code is written. For the trusted base on the boards, which step 2 delivers, building in Go first and swapping later is acceptable.

| Spike | What it shows | Gates |
|---|---|---|
| S1 | Connect for Rust over Unix sockets: `connectrpc` server and client with a peer-credential extension and the default-deny interceptor, against connect-go in both directions; the conformance runner; streaming. Its fallback candidates are in the table below | R2, R3, R5 |
| S2 | FIPS with aws-lc-rs: the `fips` feature for amd64 and arm64 under GoReleaser; the pin that maps to a certified module; the run-time self-test indicator; the AWS SDK over rustls in FIPS mode against FIPS endpoints. Done on 2026-10-30, passed with conditions; with no FIPS build in v2.0 it gates nothing, and its result is kept for a later FIPS variant | Nothing in v2.0 |
| S3 | A slim `entry` prototype in both languages, behind the tunnel client and behind the Lambda Web Adapter, with limits, timeouts and 24 hours of fuzzing; p99 latency, memory, cold start and binary size | `entry` |
| S4 | `key-holder` through `cryptoki` and `ykcs11`: P-384 signing, ECDH, PS256, Ed25519 on firmware 5.7, PIN once, concurrent callers, the release-subkey slot; then YubiHSM 2 and Nitrokey; a zeroisation review of the PIN paths | `key-holder` |
| S5 | The boards' memory budget: memory under load for every actor and driver, in Go and for the Rust candidates | Fusion defaults, the AWS-backed drivers, the trusted base |
| S6 | OpenTelemetry from Rust: one trace across a Go-to-Rust Connect hop | R9 |
| S7 | Configuration in Rust: types from the JSON Schema accept and reject the same conformance vectors as the Go structs | R9 |
| S8 | The ceremony with OpenPGP cards: two primaries generated on cards and one subkey bound under both, verified by the same tool matrix as the test of 2026-10-29, compared with a Go CLI driving scdaemon | The ceremony's card helper |
| S10 | GoReleaser with a Cargo workspace: every Rust binary for two architectures, with SBOM, signing and provenance like the Go artefacts, and Lambda zips within the layer limits | Every Rust component |

There is no spike for `exchange` in Rust, because it stays Go.

### Fallbacks are candidates, decided when a spike fails

A fallback is not chosen in advance. When a spike fails, the maintainer decides the fallback for that component from its candidates, with the spike's results in hand. The table lists the candidates; none is a commitment, including the two that were agreed earlier as the first candidate.

| Component | Candidate fallbacks |
|---|---|
| `entry` | Go first, Rust after v2 parity |
| `key-holder` | Go with `piv-go` |
| AWS-backed drivers | Go |
| `sluis-node`, `sluis-host` | Go, for the trusted base |
| The ceremony's card helper | A Go CLI driving gpg and scdaemon |
| Connect for Rust (S1) | The gRPC protocol through tonic, or a hand-written unary codec |

## Consequences

- **The public edge and the trusted base are memory-safe without a garbage collector**, and the processes that hold a PIN or a session can zeroise it. The cost is two languages on the request path and at the trust boundaries, so reviewers must read both.
- **Most of sluis stays Go**, where its deciding libraries are: the OpenID Connect provider, the CA, the transparency logs, the vendor SDKs and Kubernetes.
- **One AWS SDK language.** The AWS-backed drivers and the cloud drivers share a toolchain and a release path, and a later FIPS variant has one SDK gap to solve, not two.
- **The YubiKey driver loses its cgo job** if S4 passes, because `cryptoki` loads the PKCS#11 module at run time. The per-architecture native build of [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/) is then not needed for it. (inferred)
- **No Rust FIPS build in v2.0.** The Rust components build like any other Rust code. A FIPS variant would bring back the heavier build and a certified pin that lags the current `aws-lc-rs`, and it needs its own build design when it is added (ADR 0010).
- **The spikes are on the critical path** of the local installation. S1 to S3 are small, and the contracts are fixed first anyway.
- **Connect for Rust is young.** If it stalls, one of S1's fallback candidates carries the Rust components. tonic is in maintenance only, so the hand-written unary codec is the longer-lived fallback. (inferred)
- **Two OpenTelemetry stacks.** Rust traces are Beta, so S6 has to show one trace across both languages before the Rust components ship.
- **The `sluis` CLI stays the only executable people run.** The ceremony's card helper is run by `sluis ceremony`, never by hand, and it is in the `sluis` package, so the ceremony machine needs the same package as any other host.
- **A failed spike reopens one verdict, not the plan.** Its fallback is picked from the candidates when the result is known.
- **The source-layout page follows this record.** Its language table and its status column change, and the OD-14 row gains the AWS-backed drivers.

## Roads not taken

### Go for everything except `policy` and the cloud drivers

- Pros: one language for nearly all code, stable OpenTelemetry, connect-go at 2.x, a simple FIPS build.
- Cons: the public edge keeps net/http's steady advisory stream and Go's data-race class. The root and secret-holding processes keep their secrets in a garbage-collected heap. The AWS SDK would be in two languages, against OD-14.
- Why not: it ignores the places where Rust buys something real, and it leaves the OD-14 inconsistency.

### Rust for everything

- Pros: one language, memory-safe without a garbage collector throughout.
- Cons: Rust has no embeddable OpenID Connect provider, no maintained static-ct-api log or C2SP witness, no PIV-attestation library, no maintained Cloudflare or Google Directory client, and no cert-manager issuer library. Each would be written in-house.
- Why not: rewriting the deciding libraries is the largest risk on the list, and it buys little where the bug classes are the same.

### Group by piece: core in one language, audit in one, platform in one

- Pros: fewer seams between languages.
- Cons: it overrides the library-driven choices, for example by putting `exchange` with `entry` or `ca` with `policy`.
- Why not: the contracts already make each executable independent, so the language can follow what each one needs.

### `entry` in Go

- Pros: one language on the request path, stable OpenTelemetry, connect-go.
- Cons: the edge keeps net/http's advisory record, and the reason for asking the question is not addressed.
- Why not: a slim `entry` needs no protocol library, which is exactly where Rust pays. Go first and Rust after v2 parity is the first fallback candidate if S1 or S3 fails.

### `exchange` in Rust, or both `session` and `exchange`

- Pros: memory safety where attacker-supplied proofs are parsed.
- Cons: three of the six proof verifiers would be written in-house, the HPKE crate is unaudited, and both actors together would need an OpenID Connect provider and RFC 8693 written in-house.
- Why not: Go's verifiers are mature and FIPS-simple. Keeping the HPKE key and the cookie-sealing key behind the `keys` port takes the secret out of the process instead.

### `key-holder` on the `yubikey` crate

- Pros: direct access to PIV-specific features.
- Cons: its own notice says it has had no security audit and is experimental.
- Why not: R7. PKCS#11 is a standard interface and also serves the planned HSM drivers.

### The ceremony as a Go CLI driving gpg and scdaemon, or with in-house OpenPGP-card commands

- Pros: one `sluis` binary with no helper.
- Cons: driving gpg depends on the system's gpg and makes the transcript harder to reproduce. In-house card commands would be new security-critical code with no reference implementation.
- Why not: the maintained card stack is Rust. A helper inside the `sluis` package keeps the one CLI and uses that stack.

### A separate `sluis-ceremony` executable and package

- Pros: the ceremony code is apart from the everyday CLI. It was the first version of this record.
- Cons: a second tool for people to run, and it bent OD-16's one CLI.
- Why not: `sluis ceremony` is a subcommand, and the Rust part is a helper in the `sluis` package.

### Fallbacks fixed in advance

- Pros: no decision is left for the moment a spike fails.
- Cons: a fallback chosen before the spike ignores what the spike found. (inferred)
- Why not: the candidates are written down, and the choice is made with the results.

### `cloud-cloudflare` in Rust, like the other `cloud` drivers

- Pros: R5 without an exception, and one language for every `cloud` port driver.
- Cons: the Cloudflare library would exist twice, in Go for the integration and in Rust for the driver, and Cloudflare's maintained SDK is Go.
- Why not: one library shared with `cloudflare@<company>`.

### Sigstore as the signature `sluis-node` verifies

- Pros: transparency-logged signatures, best supported in Go.
- Cons: the roots sign with OpenPGP, so Sigstore would be a second chain of trust beside them. (inferred)
- Why not: OpenPGP under both roots is the trust, and Sigstore provenance is a second signal.

### FIPS parity now: aws-lc-rs in FIPS mode in every Rust build

- Pros: one FIPS story in both languages from the first release.
- Cons: S2 found that the only certified pin freezes rustls at 0.23.43, that the build needs CMake, Go, Perl and a pinned clang and cannot use cargo-zigbuild, and that the AWS SDK for Rust still signs SigV4 outside the module.
- Why not: no installation that v2.0 targets needs it. ADR 0010 keeps the algorithms within the approved set, so the variant stays possible.

### The AWS drivers in Go, to avoid the SigV4 gap

- Pros: the Go SDK has no such gap, so a FIPS variant would cover the AWS calls fully.
- Cons: it reverses R5 and splits the cloud drivers across two languages, for a variant that v2.0 does not ship.
- Why not: the AWS drivers stay Rust. Building them in Go is one of the two answers kept for when the variant is added.

### Build everything in Go first and swap in Rust later

- Pros: no spike on the critical path of the local installation.
- Cons: `entry` and the trusted base would be written twice.
- Why not: the spikes are small and the contracts are fixed first. The swap later is accepted only for the trusted base on the boards, which step 2 delivers.
