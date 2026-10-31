# 0060 — The language follows the executable: Go by default, Rust at the edge, in the trusted base and in cloud drivers

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (the open points of [issue #910](https://github.com/truvity/sluis/issues/910) decided: fallbacks are candidates chosen when a spike fails, `cloud-cloudflare` is Go, `sluis-node` verifies OpenPGP signatures with Sigstore provenance as a second signal, and the ceremony is a `sluis` subcommand with a Rust helper inside the `sluis` package; OD-16 holds without exception).
Rewritten in place on 2026-10-31 (`cloud-garage` is Rust, by rule R5).
Rewritten in place on 2026-10-31 (no FIPS build in v2.0, after spike S2 ([issue #940](https://github.com/truvity/sluis/issues/940)): R8 is no longer FIPS parity, and the AWS drivers stay Rust).
Rewritten in place on 2026-10-31 (spike S4 done ([issue #942](https://github.com/truvity/sluis/issues/942)): `key-holder` is Go with `piv-go`, a named exception to R3; PKCS#11 stays only for the later YubiHSM 2 and Nitrokey drivers; the YubiKey attestation verifier anchors on Yubico Attestation Root 1).
Rewritten in place on 2026-10-31 (spikes S6 ([issue #944](https://github.com/truvity/sluis/issues/944)) and S7 ([issue #945](https://github.com/truvity/sluis/issues/945)) done, both passed with conditions; `cloud-garage` is a later driver, as ADR 0058 now says; the points that earlier rewrites marked as inferred are confirmed by the maintainer, and their markers are removed).
Rewritten in place on 2026-10-31 (spikes S5 ([issue #943](https://github.com/truvity/sluis/issues/943)) and S8 ([issue #946](https://github.com/truvity/sluis/issues/946)) done: the boards' memory fits with Go or with Rust actors, and the CPU, not the eMMC, limits rqlite's writes first; the ceremony's card helper is the Rust OpenPGP-card helper, because `gpg` cannot bind an existing P-384 key as the shared signing subkey; the points both spikes leave open are listed for the maintainer, undecided).
Rewritten in place on 2026-10-31 (the maintainer decided the open points of spikes S5 and S8: no thermal action, the write rate estimated from the actor design, Rust on the smallest board tier unless a serious trade-off, the estimated memory rows kept, `GOMEMLIMIT` as a ceiling, the helper contract and a required OpenPGP attestation; the ceremony tooling, the source of a node-held subkey's back-signature and a remote second card holder are reopened for a redesign of the ceremony and root-card model).

## Context

sluis v2 is written in two languages already. The `policy` actor is Rust, because Cedar's engine is a Rust crate ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/), OD-10). The cloud drivers for AWS, GCP and Azure are Rust (OD-14). Everything else was proposed as Go. Two toolchains, a Rust kit for Connect over Unix sockets, configuration and the conformance suite are therefore a cost already paid. Moving one more component to Rust costs that component's code, not a second toolchain.

The unit of choice is the executable. Every component boundary is a ConnectRPC contract ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)), every driver is its own service on a Unix socket ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)), and the executable a unit starts is the only code in its process. A driver's language is therefore independent of its caller's, and a wrong choice can be reversed one component at a time.

The trigger was the slim `entry` (ADR 0007): the one actor that faces the internet, now holding no key, no secret and no state. Hardening it raised the question whether it should be in a memory-safe language with no garbage collector. The research of 2026-10-30 found:

- **Go is memory-safe with exceptions:** data races on multi-word values, `unsafe` and cgo. The Go memory model says such races can lead to arbitrary memory corruption.
- **The bug classes are the same in practice.** Since 2024 the Go standard library had 89 advisories and none was memory corruption: they were denial of service, panics and logic bugs, including name-constraint bypasses in crypto/x509 and request smuggling in net/http. Rust's TLS and certificate crates had the same classes in the same period, and aws-lc-sys, which is C, had PKCS#7 verification bypasses. hyper itself has had no advisory since 2022.
- **What Rust buys:** no memory corruption through data races, which matters most in the most concurrent process; deterministic destruction and zeroisation of secrets (Go's `runtime/secret` is still an experiment in Go 1.27.3); a smaller footprint and faster cold start; a cleaner record for the HTTP server. On lambda-perf (2026-10-30; arm64, zip, 128 MB, hello world), Go initialised in 46.6 ms using 17 MB and Rust in 18.0 ms using 13.3 MB.
- **What Rust does not buy:** freedom from parser logic bugs, allocation bombs or panics. Rust's cryptography is usually C underneath, while Go's is pure Go and validated in-tree.
- **The libraries decide many components.** Go clearly leads for an embeddable OpenID Connect provider, the X.509 CA and the static-ct-api issuance log, the C2SP transparency-log tools, PIV attestation, HPKE, Kubernetes controllers and the vendor SDKs (Google Directory, Cloudflare, GitHub, Slack). Rust clearly leads for the OpenPGP smart card (`openpgp-card` with rPGP, which was audited in 2024-12). Go has no maintained OpenPGP-card library.
- **Spike S4 measured the YubiKey path on an arm64 board (2026-10-30 and 2026-10-31; YubiKey firmware 5.8.0, `cryptoki` 0.12.1 over Yubico's `ykcs11` 2.7.3, and `piv-go` v2.6.0).** Every PIV operation in scope worked through both, within 1 to 3 ms of each other: the card is the cost, about 146 ms per P-384 signature. Three findings decided the language:
  - **`ykcs11` does not hold the card for itself.** When a second PC/SC client touched the card while a `ykcs11` holder was signing, the holder failed operations in 3 of 5 runs, and once it never recovered until it restarted: libykpiv's recovery reconnects with `SCARD_RESET_CARD`, which resets the card for every client.
  - **libykpiv caches the PIN in its own heap,** to re-verify after a reset, outside the reach of Rust's `zeroize`.
  - **`piv-go` connects exclusively** and holds one transaction for the handle's lifetime, so nothing else can reach the card while it runs. It queues callers fairly and reaches its first signature 0.17 s after start, against 1.8 to 2.0 s for `ykcs11`. Its resident memory under load was 9.1 to 9.8 MB, against 5.7 to 6.5 MB for the Rust holder.
  - **`piv-go`'s own `piv.Verify` rejects attestations from firmware 5.7 and newer,** because its built-in roots predate Yubico Attestation Root 1, so a verifier must supply that root and its intermediates. The full chain verified against Yubico's published PKI.
- **Spike S5 measured memory and writes on one tier-0 board (2026-10-31; arm64, 4 cores, 2 GB RAM with 1978 MiB usable, eMMC; rqlite 10.6.6).** Swap was off, so every figure is resident memory.
  - **The memory fits in either language.** A tier-0 board with 10 rqlite nodes (one per cluster), 30 actors, two entries, the card path, `cloudflared` and the services that share the board needs about **985 MiB with Go actors** and about **680 MiB with Rust actors**. Both are under a ceiling of 1380 MiB: a quarter of the RAM is kept for page cache, and 100 MiB for slab and bursts. About 335 MiB of the Go total is estimated, not measured: OpenBao, `cloudflared` with a live tunnel, a status page, alerting and DNS. The maintainer keeps these estimates (2026-10-31).
  - **rqlite costs about 9 MiB per cluster on a board** (proportional set size under write load, with small databases), not the 16 to 26 MB per node measured on a desktop. The binary's pages, about 22 MiB, are paid once per board. The cost grows with the database, which these runs kept small.
  - **The CPU, not the eMMC, is the first limit.** N rqlite clusters writing as fast as they can top out at about 430 commits/s in total, with the four cores 96% busy and the eMMC about half busy. The eMMC alone does 1,500 synchronous writes per second with one writer. The budget is **about 400 commits/s per board, summed over all its clusters**, and more with batching (635/s with more writers per cluster). The load generator shared the board's cores, so the CPU figure is an over-count.
  - **`GOMEMLIMIT` is a ceiling, not a way to shrink a process.** At 12 MiB the Go actor and entry stand-ins saved about 4 MiB each and lost 61 to 81% of their throughput to garbage collection. The maintainer decided (2026-10-31) a limit at 2 to 3 times each process's steady private memory, with the default `GOGC`.
  - **Per actor, Go costs about 10 MiB more than Rust,** about 300 MiB at 30 actors: the largest single difference in the budget.
- **Spike S8 ran the release-key ceremony on two OpenPGP cards (2026-10-31; two YubiKey 5 test cards, firmware 5.8.0, OpenPGP applet 3.4, on an arm64 board).**
  - **The Rust helper did every step:** an ECDSA P-384 primary generated on each card, one P-384 signing subkey bound under both primaries with its back-signatures, and signatures by the subkey and by each primary. It uses `openpgp-card` 0.7.0, `openpgp-card-rpgp` 0.8.1 and rPGP 0.20.0 over PC/SC. It refuses the wrong card by serial and fingerprint, never retries a PIN, and reports a missing touch as an error. A primary took 1.2 s, a binding 0.3 s and a card signature 0.27 s.
  - **`gpg` cannot bind the shared subkey.** `gpg` 2.2.27 and 2.4.7 both bind an existing P-384 key as an ECDH encryption subkey, and `--quick-add-key` refuses a keygrip. A Go CLI got there only by building the binding packets itself (ProtonMail go-crypto) and using `gpg-agent` as a card signer. Driving `gpg` also sent two wrong PINs to a card, because `gpg` 2.2 does not say which PIN it asks for, and its card key generation adds encryption and authentication subkeys.
  - **Every verifier accepted every signature,** with certificate A, B or both: `gpg` and `gpgv` 2.2.27, 2.2.40 and 2.4.7, `sqv` 1.1.0 and 1.3.0, apt 2.4.14, 2.6.1 and 3.0.3, and rpm 6.0.2 with rpm-sequoia 1.10.2. apt's `gpgv` and `sqv` paths and rpm-sequoia therefore accept ECDSA P-384 release signatures. apt needs an ASCII-armoured `Release.gpg`; a binary one fails with `NODATA`.
- **The Rust side has known gaps.** Connect for Rust (`connectrpc` 0.9.1, 2026-09-21) is official but before 1.0, with one named maintainer. OpenTelemetry traces in Rust are Beta. The FIPS build of aws-lc-rs needs CMake, Go and a C toolchain, and the current aws-lc-rs binds a module that is still in process at the CMVP, so a certified build pins an older one. Spike S2 confirmed both, and found that the AWS SDK for Rust signs SigV4 requests and S3 checksums with RustCrypto, outside the module. GoReleaser's Rust builder warns that workspaces might not work.

One inconsistency had to be resolved. OD-14 says Rust for the AWS drivers, while the source-layout table put the AWS-backed drivers of the other ports (`aws-kms`, `dynamodb`, `sqs`, the object-store blob driver) in Go.

FIPS is [0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/): no FIPS build in v2.0, and every algorithm within the FIPS-approved set. The release train and GoReleaser are [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/). The delivery order is [0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/).

## Decision

### The rules

- **R1. One executable, one language.** An actor is never split across languages. A driver is its own executable and may differ from its caller. The choice is recorded per component in the source-layout table, with its status.
- **R2. Rust at the unauthenticated edge**, where a process is reachable by unauthenticated network input and needs no protocol library that is mature only in Go. Today that is the slim `entry`, and the key-less edge relay on Kubernetes ([OD-24](/open-decisions/#od-24)). An edge actor that needs an OpenID Connect provider stays Go until a Rust one exists.
- **R3. Rust for the trusted base:** processes that run as root or as a shared mediator, speak a fixed local protocol and hold a PIN, a session or key material in memory. These are `sluis-node`, `key-holder` and `sluis-host`. One named exception: `key-holder` is Go, because the library that holds a YubiKey the way one `key-holder` per node needs is Go (below).
- **R4. Go where the deciding library is Go-only or best in Go:** the X.509 CA and its issuance log, the C2SP log writer and witness, the OpenID Connect provider and token exchange, PIV attestation, HPKE, age with post-quantum recipients, TPM, Kubernetes controllers and the cert-manager issuer, and the vendor SDKs.
- **R5. Drivers follow their backend's best SDK, with one language per vendor SDK in the build.** AWS, GCP and Azure are Rust. Local backends default to Go. One named exception: `cloud-cloudflare` is Go (below).
- **R6. Go by default** for business logic, CRUD and integrations, except on the smallest board tier (R11). The reasons are delivery speed, stable OpenTelemetry and the wider hiring pool.
- **R7. No Rust where the only library for a security function is marked experimental or unaudited.** Use a standard interface such as PKCS#11, or stay in Go. The `yubikey` crate is the case in point.
- **R8. No FIPS build in v2.0.** Rust components use current crates and rustls, with no FIPS pin, and keep to the FIPS-approved algorithms of ADR 0010. When a FIPS build variant is added, a Rust component that performs an approved operation uses aws-lc-rs in FIPS mode, and the AWS SDK gap (SigV4 signed outside the module) must be solved, or the AWS drivers are built in Go for that variant. The AWS drivers stay Rust (R5).
- **R9. Both kits pass one conformance suite.** Every component, in either language, uses its language's kit (Connect over Unix sockets with peer credentials, configuration from the JSON Schema, the CLI conventions, OpenTelemetry). A component may move language only once its language's kit passes the suite.
- **R10. Contracts are on the wire, so a choice is reversible.** A language change is a replacement of one component behind the same contract and suite, never a fork of the contract.
- **R11. Rust on the smallest board tier, unless a serious trade-off.** An actor that runs on a tier-0 board (a 2 GB board, as in spike S5) prefers Rust. Go is used there only where this record's rules for that component name a serious trade-off, for example a required library that exists only in Go. Spike S5 measured about 10 MiB of private memory per Go actor and about 1 MiB per Rust one, about 300 MiB at 30 actors. R1 gives an actor one language, so the choice holds on every runtime. R9 applies: a component moves to Rust only once the Rust kit passes the suite, and building in Go first and swapping later is acceptable for the trusted base (the spike gates, below). (decided 2026-10-31)

### The verdicts

| Component | Language | Rule | Gate |
|---|---|---|---|
| `policy` | Rust | R4 (`cedar-policy`) | None; decided by OD-10 and [0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/) |
| `cloud` port drivers: web identity, platform role, GCP and Azure federation | Rust | R5 | None; decided by OD-14 |
| AWS-backed drivers of the other ports: `aws-kms`, `dynamodb` (state and lease), `sqs`, `s3-compatible` blob | Rust | R5 | S1 |
| `entry` (slim) | Rust | R2 | S1 and S3 |
| `sluis-node` | Rust. It verifies OpenPGP signatures, with Sigstore provenance as a second signal | R3 | S1 |
| `key-holder` | Go, with `piv-go` for YubiKey PIV. PKCS#11 only for the later YubiHSM 2 and Nitrokey drivers | R4 (`piv-go`): an exception to R3 | S4, done on 2026-10-31 |
| `sluis-host` | Rust | R3 | S1 |
| The ceremony's OpenPGP-card helper, run by `sluis ceremony` | Rust (`openpgp-card` with rPGP) | The deciding library is Rust-only, the converse of R4 | S8, done on 2026-10-31 |
| `cloud-garage` (a later driver) | Rust; it shares no library with an integration, so R5 applies | R5 | None |
| `cloud-cloudflare` | Go, sharing the library of the integration `cloudflare@<company>` | R4: an exception to R5 | None |
| `session`, `exchange`, `console`, `quota`, `signer`, `ca`, `ssh-ca` | Go | R4, R6 | None |
| Audit actors: `writer`, `witness`, `indexer`, `reader` | Go | R4 | None |
| Integrations | Go | R4, R6 | None |
| The `sluis` CLI, `sluis agent`, the cert-manager issuer | Go | R4, R6 | None |
| Other drivers (local backends, indexes, witnesses, `nats-jetstream`, `age` export) | Go | R5, R6 (inferred: the decision does not list them one by one) | None |

- **The cloud and AWS-backed drivers are one group.** The inconsistency with OD-14 is resolved in its favour: every AWS-backed driver is Rust, so the build has one AWS SDK language. These drivers run as Lambda extensions and as sidecars, where start-up time adds to every cold start.
- **`exchange` stays Go.** It is the most parser-heavy actor, but nearly every verifier it needs is best in Go. Its HPKE private key stays behind the `keys` port, and the cookie-sealing key of `session` does too.
- **`key-holder` is Go with `piv-go`,** a named exception to R3. One `key-holder` owns each node's card, and `piv-go` enforces that by construction with its exclusive connection, while `ykcs11` resets the card when another client appears and keeps a copy of the PIN that `key-holder` cannot wipe. `key-holder` keeps the PIN in locked memory and zeroes its own copy; Go cannot promise that the runtime keeps no other, which the zeroisation review records. The Rust `yubikey` crate stays out (R7).
- **PKCS#11 stays, for the planned YubiHSM 2 and Nitrokey drivers only** ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/), ADR 0052). Those are drivers of the `keys` port built later; which language they are written in is decided with them.
- **Attestation verification is Go,** in the CLI at ceremony and node initialisation and in the `yubikey-attestation` verifier of `exchange` ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). Its trust anchor is Yubico's PIV attestation root, **Yubico Attestation Root 1**, with its intermediates, supplied by sluis rather than taken from `piv-go`'s built-in roots.
- **The ceremonies are `sluis ceremony …` subcommands of the one `sluis` CLI**, so OD-16 holds without exception. The part that drives the OpenPGP cards with `openpgp-card` and rPGP is a Rust helper executable under `/usr/libexec/sluis/`, which the CLI runs. The helper ships **inside the `sluis` package**; there is no separate ceremony package ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).
- **The ceremony is not built on `gpg`.** Spike S8 ran the helper end to end on two cards, and found that `gpg` cannot bind the shared signing subkey (above). The helper writes only the primary, its user ID and the signing subkeys, so the release certificate has no encryption subkey; the one that `gpg`'s card key generation adds makes rpm-sequoia print a policy warning on every install.
- **A ceremony runs in three steps:** prepare it online; run the same `sluis` with the same configuration on the offline live USB system, where the cards are; then complete and publish it online.
- **`cloud-cloudflare` is Go.** It shares the Cloudflare library with the integration `cloudflare@<company>`, and Cloudflare's best SDK is Go. This is an exception to R5, which would put a `cloud` port driver in Rust: one vendor library in one language weighs more here than one language for every `cloud` driver.
- **`sluis-node` verifies OpenPGP signatures** on signed configuration and binaries, by the release key certified under both roots. Sigstore provenance is checked as a second signal, not as the trust. OpenPGP is what the Rust stack handles well, and it is the format the roots already sign with ([Release signing](/distribution/release-signing/)). (the last reason is inferred)

### Which way each actor goes on a tier-0 board

Applying R11 to the verdicts above (2026-10-31). A component is listed as Go only where the rules above already name the trade-off; a component whose rules name none is open, and prefers Rust until a trade-off is named with its design.

| Component | On a tier-0 board | Reason |
|---|---|---|
| `entry`, `sluis-node`, `sluis-host`, `policy` | Rust | Already Rust (R2, R3, R4) |
| AWS-backed drivers and the `s3-compatible` blob driver | Rust | Already Rust (R5) |
| `key-holder` | Go | The named exception to R3: `piv-go` holds the card exclusively (spike S4) |
| `session` | Go | R4: the embeddable OpenID Connect provider is Go-only in practice |
| `exchange` | Go | R4: three of the six proof verifiers, the HPKE crate and the OpenID Connect provider would be written in-house (Roads not taken) |
| `ca` | Go | R4: the X.509 CA and its issuance log |
| `writer`, `witness` | Go | R4: the C2SP log writer and witness |
| Integrations and `cloud-cloudflare` | Go | R4: the vendor SDKs, and Cloudflare's best SDK is Go |
| `age` export driver | Go | R4: age with post-quantum recipients |
| `signer` | Open | No deciding library is named; tokens are standard JWS and JWT, and R6 was its only reason |
| `ssh-ca` | Open | R4 names the X.509 CA, not the SSH CA |
| `quota` | Open | R6 only |
| `console` (the actor; the single-page application stays TypeScript) | Open | R6 only |
| `indexer`, `reader` | Open | The verdicts table groups them under R4, but the libraries it names (the log writer and witness) do not obviously cover reading an index |
| Local drivers: rqlite state, lease and index, the spool, the local lock, the files blob, `sqlite` | Open | R5 and R6 only (the verdicts table says "inferred") |
| `sluis agent` | Open | Where it runs is not stated |
| The `sluis` CLI, the cert-manager issuer, the ceremony's card helper | Unchanged | Not resident on a tier-0 board: people run the CLI, the issuer runs on Kubernetes, and the helper is already Rust |

### The spikes are gates

Each conditional verdict holds only once its spike passes. The languages are fixed from the spike results before step-1 code is written. For the trusted base on the boards, which step 2 delivers, building in Go first and swapping later is acceptable.

| Spike | What it shows | Gates |
|---|---|---|
| S1 | Connect for Rust over Unix sockets: `connectrpc` server and client with a peer-credential extension and the default-deny interceptor, against connect-go in both directions; the conformance runner; streaming. Its fallback candidates are in the table below | R2, R3, R5 |
| S2 | FIPS with aws-lc-rs: the `fips` feature for amd64 and arm64 under GoReleaser; the pin that maps to a certified module; the run-time self-test indicator; the AWS SDK over rustls in FIPS mode against FIPS endpoints. Done on 2026-10-30, passed with conditions; with no FIPS build in v2.0 it gates nothing, and its result is kept for a later FIPS variant | Nothing in v2.0 |
| S3 | A slim `entry` prototype in both languages, behind the tunnel client and behind the Lambda Web Adapter, with limits, timeouts and 24 hours of fuzzing; p99 latency, memory, cold start and binary size | `entry` |
| S4 | `key-holder` through `cryptoki` and `ykcs11`, against `piv-go`: P-384 signing, ECDH, PS256, Ed25519 on firmware 5.7, PIN once, concurrent callers, the release-subkey slot; then YubiHSM 2 and Nitrokey; a zeroisation review of the PIN paths. Done on 2026-10-31 for YubiKey PIV: both paths work, and `piv-go` was chosen (above). The release-subkey slot, YubiHSM 2, Nitrokey and the zeroisation review are still to run | `key-holder`: decided, Go |
| S5 | The boards' memory budget: memory under load for every actor and driver, in Go and for the Rust candidates. Done on 2026-10-31: the memory fits with Go or with Rust actors (about 985 or 680 MiB of a 1380 MiB ceiling), rqlite costs about 9 MiB per cluster, and the CPU limits writes first, at about 400 commits/s per board. Its points are decided below | Fusion defaults, the AWS-backed drivers, the trusted base |
| S6 | OpenTelemetry from Rust: one trace across a Go-to-Rust Connect hop. Done on 2026-10-31, passed with conditions | R9 |
| S7 | Configuration in Rust: types from the JSON Schema accept and reject the same conformance vectors as the Go structs. Done on 2026-10-31, passed with conditions | R9 |
| S8 | The ceremony with OpenPGP cards: two primaries generated on cards and one subkey bound under both, verified by the same tool matrix as the test of 2026-10-29, compared with a Go CLI driving scdaemon. Done on 2026-10-31: the Rust helper did every step, `gpg` cannot bind the shared subkey, and every verifier accepted ECDSA P-384. Its decided and reopened points are below | The ceremony's card helper: decided, Rust |
| S10 | GoReleaser with a Cargo workspace: every Rust binary for two architectures, with SBOM, signing and provenance like the Go artefacts, and Lambda zips within the layer limits | Every Rust component |

There is no spike for `exchange` in Rust, because it stays Go.

### What S5 and S8 decided, and what is open

The maintainer decided these points on 2026-10-31.

- **The board's temperature (S5): accepted, no thermal action.** The reboot of an earlier run was caused by an accidental short circuit on a GPIO pin during handling, not by heat. The peak of 83.3 °C under sustained load, from 60 °C at idle, stays noted.
- **The write rate the design needs (S5): estimated from the actor design.** The estimate is on [Bare metal](/runtimes/bare-metal/#state-writes-on-a-board). At the previous line's measured peak the design needs about 7 commits/s on each board, about 30 at ten times that and about 69 at the signing ceiling of three cards, against a budget of about 400. The docs lack the per-action write counts of several actors, which the estimate states and marks as assumptions. The counts are measured when the actors exist.
- **The language on the smallest board tier (S5): Rust unless a serious trade-off.** This is R11, and the table above lists each actor.
- **The estimated rows of the board budget (S5): kept.** OpenBao and `cloudflared` with a live tunnel stay estimates, measured when they are built.
- **`GOMEMLIMIT` (S5): a ceiling at 2 to 3 times the steady anonymous memory,** with the default `GOGC`: 24 to 32 MiB for rqlite, and 24 MiB for each actor and entry. The budget uses the default-GC figures. A limit of 12 MiB is rejected: it cut throughput by 61 to 81% through garbage collection, while rqlite at 24 MiB with `GOGC=50` cost only 6%.
- **The helper's contract with `sluis ceremony` (S8): arguments and files.** The Rust helper prompts for the PIN itself on the terminal, and the PIN never travels in the environment or in arguments.
- **Attestation of the primaries (S8): required,** and checked against Yubico's OpenPGP attestation CA, as the PIV path is. Decided; the implementation is pending. The test with the two cards runs later, once the card is wired.

These points are reopened. The maintainer wants the ceremony and the root-card model revisited, so none of them has an answer in this record until the redesign is decided ([issue #946](https://github.com/truvity/sluis/issues/946)).

- **The ceremony tooling (S8).** Reopened, pending a redesign of the ceremony and root-card model.
- **Where the back-signature comes from when the subkey is on a node (S8).** Reopened. S<sub>i</sub> lives in a node's PIV slot behind `key-holder`, so the back-signature over each primary must come from `key-holder`, and that purpose is not on its list. The run used a software stand-in for S<sub>i</sub>.
- **The split for a card B held by a second person elsewhere (S8).** Reopened. One process cannot hold both cards, so the back-signatures, the binding by each card and the merge of the certificates become separate steps.

This point is still open and was not part of that decision.

- **The touch timeout (S8).** A missed touch comes back from the card after about 15 s as a generic security status, not as a touch timeout; whether the helper reports it as one.

### Fallbacks are candidates, decided when a spike fails

A fallback is not chosen in advance. When a spike fails, the maintainer decides the fallback for that component from its candidates, with the spike's results in hand. The table lists the candidates; none is a commitment, including the two that were agreed earlier as the first candidate.

| Component | Candidate fallbacks |
|---|---|
| `entry` | Go first, Rust after v2 parity |
| `key-holder` | Rust through PKCS#11 (`cryptoki` with `ykcs11`), carrying the S4 constraints: one owner of the card, restart on a removed device, serialised ECDH |
| AWS-backed drivers | Go |
| `sluis-node`, `sluis-host` | Go, for the trusted base |
| The ceremony's card helper | A Go CLI driving gpg and scdaemon. S8 found that `gpg` alone cannot bind the shared subkey, so this candidate needs the binding packets built in Go, with `gpg-agent` only as the card signer |
| Connect for Rust (S1) | The gRPC protocol through tonic, or a hand-written unary codec |

## Consequences

- **More of sluis moves to Rust on the boards.** Under R11 an actor with no Go-only deciding library prefers Rust, so reviewers read more Rust and the Rust gates (S1, S6, S7, S10) cover more components. Each move waits for the Rust kit to pass the conformance suite (R9), and the open rows of the tier-0 table are decided with their components.
- **The public edge and the trusted base, apart from `key-holder`, are memory-safe without a garbage collector**, and those processes can zeroise what they hold. The cost is two languages on the request path and at the trust boundaries, so reviewers must read both.
- **Most of sluis stays Go**, where its deciding libraries are: the OpenID Connect provider, the CA, the transparency logs, the vendor SDKs and Kubernetes.
- **One AWS SDK language.** The AWS-backed drivers and the cloud drivers share a toolchain and a release path, and a later FIPS variant has one SDK gap to solve, not two.
- **The YubiKey driver keeps its cgo job.** `piv-go` reaches pcsc-lite through cgo on Linux, so the per-architecture native build of [0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/) stays for it.
- **`key-holder` is the one trusted-base process in Go.** Its PIN sits in a garbage-collected process, and it uses a few megabytes more than the Rust holder did. In return the card has exactly one owner on every node, enforced by the library rather than by deployment rules, and a crash of another card client cannot reset it.
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
- Why not: R7. The YubiKey driver is `piv-go`, and PKCS#11, a standard interface, serves the planned HSM drivers.

### `key-holder` in Rust through PKCS#11 (`cryptoki` with `ykcs11`)

- Pros: the trusted base stays in Rust as R3 says, with `zeroize` for the PIN; a smaller resident footprint; one interface for the YubiKey and the planned HSMs. It was the first verdict of this record.
- Cons: spike S4 found that `ykcs11` shares the card and resets it for every client when a second one appears, and one holder never recovered; libykpiv keeps a copy of the PIN that Rust cannot wipe; ECDH results pass through one shared token object; the first session takes about 1.6 s to open with eight keys.
- Why not: the maintainer chose `piv-go`, whose exclusive connection is what one `key-holder` per node needs. This path is kept as the fallback candidate.

### The ceremony as a Go CLI driving gpg and scdaemon, or with in-house OpenPGP-card commands

- Pros: one `sluis` binary with no helper.
- Cons: driving gpg depends on the system's gpg and makes the transcript harder to reproduce. In-house card commands would be new security-critical code with no reference implementation. Spike S8 (2026-10-31) found that `gpg` 2.2.27 and 2.4.7 cannot bind an existing P-384 key as a signing subkey, that `gpg` 2.2 does not say which PIN it prompts for (two wrong PINs reached a test card), that `gpg` 2.2 silently ignores the touch-policy command, and that its card key generation adds encryption and authentication subkeys.
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

### Go by default on a tier-0 board (R6 unchanged)

- Pros: the fastest delivery, one language for most actors, stable OpenTelemetry.
- Cons: about 10 MiB more private memory per actor than Rust, about 300 MiB at 30 actors on a 2 GB board, which is the largest single difference in the budget (spike S5, 2026-10-31).
- Why not: the maintainer prefers Rust where it costs no serious trade-off.

### Rust for every actor on a tier-0 board, with no exception

- Pros: the smallest footprint and one language on the board.
- Cons: the same rewrite of the deciding libraries as "Rust for everything" above: the OpenID Connect provider, the CA, the transparency-log tools and the vendor SDKs.
- Why not: the maintainer allows Go where a serious trade-off is named.

### No `GOMEMLIMIT`, or a limit of 12 MiB

- Pros: no limit leaves the garbage collector alone; 12 MiB saved about 4 MiB for each actor and entry stand-in.
- Cons: no limit lets a process grow to the node's ceiling (inferred); 12 MiB cut throughput by 61 to 81% through garbage collection.
- Why not: a ceiling at 2 to 3 times the steady private memory keeps the throughput and still bounds the process.

### The helper takes JSON on its standard streams, or the PIN from the environment

- Pros: JSON on standard streams is structured and easy to script; an environment variable needs no prompt.
- Cons: a PIN in the environment or in arguments is visible to other processes and children, and standard streams that carry data leave no clean place for a prompt on the terminal (inferred).
- Why not: arguments and files are enough for the ceremony, and the helper prompts for the PIN itself.

### An optional OpenPGP attestation of the primaries

- Pros: no new check in the ceremony, and it works with a card that cannot attest.
- Cons: a ceremony could not show that a primary was generated on the card and never left it.
- Why not: the PIV path already requires attestation, and the maintainer wants the OpenPGP path to match it.
