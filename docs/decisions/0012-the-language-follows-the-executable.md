# 0012 — The language follows the executable: Go by default, Rust at the edge, in the trusted base and on the smallest boards, and the AWS-backed drivers are Go

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

sluis v2 is written in two languages. `policy` is Rust because Cedar's engine is a Rust crate ([0007](/decisions/0007-policy-and-sealed-grants/)). Two toolchains, a Rust kit for Connect over Unix sockets, schema validation and the conformance suite are a cost already paid, so moving one more component to Rust costs that component's code, not a second toolchain.

The unit of choice is the executable. Every component boundary is a ConnectRPC contract ([0002](/decisions/0002-families-of-actors/)), every driver is its own executable behind one port, and the executable a unit starts is the only code in its process. A driver's language is independent of its caller's, and a wrong choice can be reversed one component at a time. The drivers and the blocks are [atoom's](https://github.com/truvity/atoom/blob/master/docs/decisions/0002-ports-and-drivers-over-connectrpc.md): atoom chooses the language per driver, and sluis chooses it per actor.

The research found:

- **Go is memory-safe with exceptions:** data races on multi-word values, `unsafe` and cgo. Since 2024 the Go standard library had 89 advisories and none was memory corruption; Rust's TLS and certificate crates had the same classes (denial of service, panics, logic bugs) in the same period (research of 2026-10-30).
- **What Rust buys:** no memory corruption through data races in the most concurrent processes, deterministic destruction and zeroisation of secrets, a smaller footprint and faster cold start (lambda-perf, 2026-10-30, arm64, 128 MB, hello world: Go 46.6 ms and 17 MB, Rust 18.0 ms and 13.3 MB). It does not buy freedom from parser logic bugs, allocation bombs or panics.
- **The libraries decide many components.** Go clearly leads for an embeddable OpenID Connect provider, the X.509 CA and the static-ct-api issuance log, the C2SP transparency-log tools, PIV attestation, HPKE, Kubernetes controllers and the vendor SDKs.
- **The YubiKey path.** `piv-go` and `ykcs11` both worked, but `ykcs11` does not hold the card for itself and keeps a PIN copy that cannot be wiped, while `piv-go` connects exclusively and queues callers fairly. The research and the card driver are atoom's: [the spike](https://github.com/truvity/atoom/blob/master/key/yubikeypiv/docs/research/s4-piv-go-or-pkcs11.md) sits beside the driver, and the driver is Go with `piv-go`.
- **The small boards** (spike, three arm64 boards with 4 cores, 2 GB RAM and eMMC, one gigabit switch, real replication; measured 2026-11-02). At 30 actors under load a Rust actor holds about 0.2 MiB of private memory and a Go actor 8.0 to 8.7 MiB, so Go costs 235 to 255 MiB more, the largest single difference in the memory budget. Go does not give the memory back after the load stops. The board image's compressed swap must be counted when sizing memory.
- **The units of a whole board** (measured 2026-11-03, [#1180](https://github.com/truvity/sluis/issues/1180)): 30 Go units with every driver as a process of its own hold 727 to 836 MiB of resident memory idle and 856 MiB loaded. A typical loaded board, with the rqlite leader at 100 writes a second, uses 1,056 MiB of proportional memory (53 % of 1,978 MiB); the worst case measured 1,155 MiB (58 %), both under the ceiling of 1,380 MiB. The figures are for Go stand-ins of every unit, which over-count the Rust actors. The write budget is the saturation of the one engine cluster an installation has, about 275 commits a second (measured 2026-11-03, [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)); the design needs 7 to 73 (estimated 2026-10-31).
- **The Rust side has known gaps.** Connect for Rust (`connectrpc` 0.9.1) is official but before 1.0, with one named maintainer; OpenTelemetry traces in Rust are Beta; GoReleaser's Rust builder warns that workspaces might not work, and the build spike showed they do, with one build entry per binary using `--package` and `--bin`.

## Decision

### The rules

- **One executable, one language.** An actor is never split across languages, so its language holds on every runtime. A driver is its own executable and may differ from its caller. The per-actor table records the choice per component.
- **Rust at the unauthenticated edge,** where a process is reachable by unauthenticated network input and needs no protocol library that is mature only in Go: the slim `entry`, and the key-less edge relay on Kubernetes ([0011](/decisions/0011-runtimes-and-installations/#kubernetes)). An edge actor that needs an OpenID Connect provider stays Go until a Rust one exists.
- **Rust for the trusted base:** processes that run as root or hold a PIN, a session or key material in memory and speak a fixed local protocol: `sluis-node` and `sluis-host`. One named exception: key@ is Go (below).
- **Go where the deciding library is Go-only or best in Go:** the X.509 CA and its issuance log, the OpenID Connect provider and token exchange, PIV attestation, HPKE, TPM, Kubernetes controllers and the cert-manager issuer, and the vendor SDKs.
- **Drivers are atoom's and follow their backend's best SDK, one language per driver.**
  - **The AWS-backed drivers are written in Go,** on the AWS SDK for Go v2: the KMS key driver and the DynamoDB item driver exist in atoom, and the planned AWS drivers (AppConfig, SSM, S3, SQS) are Go too. There are no Rust AWS drivers. The reasons: the session authorizer is one Go Lambda function with no sidecars, so these drivers must be Go libraries linked into that function; the AWS SDK for Go is the more mature SDK, and for a key driver maturity counts for more than a smaller footprint (the AWS SDK for Rust signs requests and checksums with RustCrypto, outside any module, measured 2026-10-30); and ConnectRPC keeps the language free per driver, so a Rust twin can replace one behind the same contract if a Rust function ever needs AWS in process. The cost of a second SDK language falls on atoom's repository, which does not unify languages, and not on sluis's build.
  - **The engine and the local drivers are Rust:** `rqlite`, the `file` and credential sources, the `fs` blob, the spool, the `sqlite` index and the directory destination. They have no vendor SDK and run on the boards, so the smallest-board rule decides.
  - **The card driver is Go** with `piv-go`, and `cloud-cloudflare` and the Cloudflare minter share one Go library, because one vendor library in one language weighs more than one language for every driver.
  - **Further drivers** (PostgreSQL, NATS JetStream, witness and destination drivers) take the language atoom's plan names when each is built.
  - sluis runs atoom's drivers as atoom's packaged programs, each behind its socket, or links a Go driver into a Go function, so a driver's language does not matter to sluis.
- **Go by default** for business logic, CRUD and integrations, except on the smallest board tier: delivery speed, stable OpenTelemetry and the wider hiring pool.
- **No Rust where the only library for a security function is marked experimental or unaudited.** Use a standard interface such as PKCS#11, or stay in Go. The `yubikey` crate is the case in point.
- **No FIPS build in 2.0.** Rust components use current crates and rustls, with no FIPS pin, and keep to the approved algorithms of [0006](/decisions/0006-keys-and-sealing/). A FIPS build of the Rust side would need aws-lc-rs in FIPS mode and an answer for the AWS SDK for Rust (spike of 2026-10-30).
- **Both kits pass one conformance suite:** Connect over Unix sockets with peer credentials, the typed store client, configuration from the generated JSON Schema, the CLI conventions, OpenTelemetry. A component may move language only once its language's kit passes the suite.
- **Contracts are on the wire, so a choice is reversible.** A language change replaces one component behind the same contract and suite.
- **Rust on the smallest board tier, unless a serious trade-off is named.** An actor that runs on a 2 GB board prefers Rust. Go is used there only where a rule names a serious trade-off, such as a required library that exists only in Go. A later design that names a serious trade-off changes a row of the table. Building the trusted base in Go first and swapping later is acceptable, under the conformance rule.
- **`GOMEMLIMIT` is a ceiling** at 2 to 3 times each process's steady private memory, with the default `GOGC` (24 to 32 MiB for rqlite, 24 MiB for an actor; decided 2026-10-31). A limit of 12 MiB cut throughput by 61 to 81 % through garbage collection.

### Blocks follow their main host

atoom's blocks have one implementation each, in the language of the block's main host ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). A host in another language calls the block as a process over ConnectRPC. The hosts here are sluis's:

| Block | Language | Host in sluis |
|---|---|---|
| `seal` | Go | key@ (Go) |
| `state` | Rust | store@ (Rust) |
| `export` | Rust | exporter@ (Rust) |
| `token` | Go | `signer` (Rust today) |

None of the blocks is built yet. The hosts keep their languages now, and each becomes a thin host around its block when the block lands.

### Per-actor table

The table lists sluis's own executables. A Rust row keeps its gate (see [The spikes are gates](/decisions/0012-the-language-follows-the-executable/#the-spikes-are-gates)), and a failed gate moves it to a fallback candidate. A gate does not stop work: the component is built against its contract and the conformance suite meanwhile. Go rows have no gate. State marks what is built in the repository today; everything else is planned ([roadmap](/roadmap/)).

| Component | Language | Why | Gate | State |
|---|---|---|---|---|
| `entry` (slim) | Rust | Unauthenticated edge | S1, S3, S10 | Built |
| The Kubernetes edge relay | Rust | Unauthenticated edge | S1, S10 | Planned |
| store@ | Rust | Smallest board; it holds credentials and parses no unauthenticated input. The host of `state` | S1, S10 | Built |
| exporter@ | Rust | Smallest board. The host of `export` | S1, S10 | Built, with the directory driver |
| key@ | Go, with `piv-go` for the YubiKey in the card driver | Named exception to the trusted-base rule. The host of `seal` | None | Built |
| `sluis-node` | Rust; it verifies OpenPGP signatures on signed configuration and binaries by a release subkey that a root primary in the keyring has bound, and Sigstore provenance is a second signal, not the trust | Trusted base | S1, S10 | Planned |
| `sluis-host` | Rust | Trusted base | S1, S10 | Planned |
| `policy` | Rust | `cedar-policy`, the Cedar library | S1, S10 | Built |
| `signer` | Rust: it signs through key@ and needs no Go-only library | Smallest board | S1, S10 | Built |
| `signin` | Go | The embeddable OpenID Connect provider | None | Built |
| `exchange` | Go | The proof verifiers, HPKE and the OpenID Connect provider; its HPKE key and the cookie sealing stay behind key@ | None | Built |
| `console` (the single-page application is TypeScript) | Rust, unless a serious trade-off is named | Smallest board; Connect for Rust serves its streaming | S1, S10 | Planned |
| `kv` | Rust | Smallest board; it holds no credential at rest | S1, S10 | Planned |
| `ca` | Go | The X.509 CA and its issuance log | None | Built; off in the core |
| `ssh-ca` | Rust | Smallest board | S1, S10 | Planned |
| Adapters: `adapter-upstream-oidc`, `adapter-github`, `adapter-slack`, and every further `adapter-<vendor>`, one executable each | Go | The vendor SDKs, and the OpenID Connect client of `adapter-upstream-oidc` | None | `adapter-upstream-oidc` built; the others planned |
| The `sluis` command-line tool, the cert-manager issuer | Go | Go by default; not resident on a small board | None | The command-line tool is built; the issuer is planned |

The audit pieces are [audit's](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md): the emitter in Go and Rust, the writer and witness in Go, the indexer and reader in Rust. sluis links the emitter in each of its processes. The code of the writer, indexer and reader is still in this repository until it moves to audit.

Further decisions of the table:

- **key@ is Go with `piv-go`.** One key@ owns each node's card, and `piv-go` enforces that by construction with its exclusive connection. key@ keeps the PIN in locked memory and zeroes its own copy; Go cannot promise that the runtime keeps no other, which the zeroisation review records. PKCS#11 stays only for later YubiHSM 2 and Nitrokey drivers, whose language is chosen with them.
- **store@ and exporter@ are Rust.** They hold credentials, parse no unauthenticated input and speak the one store contract, so the smallest-board rule decides. Their code is the code to review hardest ([0002](/decisions/0002-families-of-actors/)).
- **Rust uses the `buffa` Protobuf runtime and validates with hand-written checks.** `connectrpc` 0.9.1 generates only `buffa` types, so `buffa` is the runtime of every Rust component and `prost` is not in the tree. `prost-protovalidate` cannot validate `buffa` messages. The kit therefore checks a decoded message with hand-written checks that follow the message's rules, and the protovalidate annotations in the `.proto` files stay as the documentation of those rules and as the source of the Go validator and the JSON Schema ([0004](/decisions/0004-the-data-model/)). The shared vectors of the conformance rule keep the two in step: a rule that the Go validator enforces and the Rust checks miss fails a vector.
- **The configuration check runs store@'s own rules.** `sluis config check` is in the Go CLI, and the rules it checks are store@'s, in Rust. The CLI does not copy them: it runs the store executable as a helper, `store check --validate DIR`, which reads the bootstrap file and the signed config documents with the code store@ loads them with, and the CLI maps the helper's checks to its own output and exit codes. So there is one implementation of the rules of a config set, and the check cannot pass a set that store@ refuses ([REQ-DATA-090](/requirements/data/#REQ-DATA-090)). How the CLI finds the helper, and why it refuses one of another release, is in [0013](/decisions/0013-release-train-packaging-and-distribution/#one-version).
- **The typed store clients are part of both kits.** The Go client `store.Client[T Kind]` and the Rust `Store<K: Kind>` are generated from the kind's message and pass the same conformance suite.
- **Attestation verification is Go,** in the CLI at node initialisation and in the `yubikey-attestation` verifier of `exchange` ([0022](/decisions/0022-workload-identity-and-host-enrolment/)), anchored on Yubico Attestation Root 1 and its intermediates, supplied by sluis.
- **OpenTelemetry from Rust** uses a Connect instrumentation crate of sluis's own, offered upstream, and OTLP over HTTP/protobuf; about 3 to 4 MiB more per Rust actor is accepted (measured 2026-11-02: 3.4 MiB at idle and 4.0 MiB after 22,000 calls). The measured CPU and size of the crate are in [0016](/decisions/0016-telemetry/).

### The spikes are gates

A conditional verdict holds once its spike passes. The gated components are built meanwhile against their contracts, and a spike that fails moves its components to a fallback candidate before their first release.

| Spike | What it shows | Gates | Result |
|---|---|---|---|
| S1 ([#939](https://github.com/truvity/sluis/issues/939)) | Connect for Rust over Unix sockets: `connectrpc` server and client with a peer-credential extension and the default-deny interceptor, against connect-go both ways; the conformance runner; streaming | The edge, the trusted base and the smallest-board rows | Passed (2026-11-01) |
| S3 ([#941](https://github.com/truvity/sluis/issues/941)) | A slim `entry` prototype in both languages, behind the tunnel client and the Lambda Web Adapter, with limits, timeouts and 24 hours of fuzzing; p99 latency, memory, cold start, binary size | `entry` | Holds the Rust row on memory, size and threads (latency is parity). Two checks remain: the real tunnel client (the edge relay tests) and 24-hour fuzz campaigns (CI, [#796](https://github.com/truvity/sluis/issues/796)) |
| S10 ([#947](https://github.com/truvity/sluis/issues/947)) | GoReleaser with a Cargo workspace: every Rust binary for two architectures, with an SBOM like the Go artefacts | Every Rust component | Passed (2026-11-01) |

The other spikes are done: S2 on the cryptography of the Rust crates ([#940](https://github.com/truvity/sluis/issues/940)), S4 on the YubiKey path ([#942](https://github.com/truvity/sluis/issues/942)), S5 on the board memory ([#943](https://github.com/truvity/sluis/issues/943)), S6 on OpenTelemetry ([#944](https://github.com/truvity/sluis/issues/944)) and S7 ([#945](https://github.com/truvity/sluis/issues/945)). Their results are in Context. The configuration fidelity spike ([#1177](https://github.com/truvity/sluis/issues/1177)) is done too ([0004](/decisions/0004-the-data-model/)).

### Fallbacks are candidates

A fallback is not chosen in advance. When a spike fails, the maintainer picks the fallback for that component from its candidates, with the results in hand.

| Component | Candidate fallbacks |
|---|---|
| `entry` | Go first, Rust after the other installations are proved |
| `sluis-node`, `sluis-host` | Go |
| Connect for Rust | The gRPC protocol through tonic, or a hand-written unary codec |

## Consequences

- More of sluis is Rust on the boards. Reviewers read more Rust, and the Rust gates cover more components; each move waits for the Rust kit to pass the suite.
- The public edge and the trusted base, apart from key@, are memory-safe without a garbage collector and can zeroise what they hold. store@ and exporter@ are among them. The cost is two languages on the request path and at the trust boundaries.
- Most of sluis stays Go where its deciding libraries are: the OpenID Connect provider, the CA, the vendor SDKs and Kubernetes.
- The AWS drivers share one SDK language and one toolchain in atoom. A Rust function that needs one carries it as a Lambda extension in a drivers layer ([0011](/decisions/0011-runtimes-and-installations/#the-lambda-shape)).
- `piv-go` reaches pcsc-lite through cgo on Linux, so key@ keeps a per-architecture native build ([0013](/decisions/0013-release-train-packaging-and-distribution/#one-version)).
- key@ is the one trusted-base process in Go: its PIN sits in a garbage-collected process and it uses a few megabytes more than a Rust holder, but the card has exactly one owner, enforced by the library.
- The Rust components build like any other Rust code, with cargo-zigbuild and no CMake, Perl or pinned clang.
- S3 and S10 are on the critical path of the Rust rows, and the Rust checks are hand-written until a validation bridge exists, with the conformance vectors as their only guard. If Connect for Rust stalls, a fallback candidate carries the Rust components; tonic is in maintenance only, so the hand-written codec is the longer-lived one.
- Two OpenTelemetry stacks, with Rust traces in Beta.
- The `sluis` CLI stays the only executable people run, and it is Go. `sluis config check` needs the store executable of the same release beside the CLI and runs on Linux only: a pipeline that checks config before it publishes runs on Linux, and a `config check` on another system says so and stops.

## Roads not taken

- **Rust validates with `prost-protovalidate`.** It cannot validate the `buffa` messages that `connectrpc` generates, and a second Protobuf runtime in the tree would carry `prost` beside `buffa` for one crate.
- **The rules of a config set ported to Go for the CLI.** Two implementations of one set of rules drift apart, and only the vectors would catch it, after the fact.
- **protovalidate in the CLI for `config check`.** The Rust checks are hand-written, so protovalidate-go would be a second rule set beside store@'s, able to pass a document that store@ refuses.
- **store@'s validation compiled to WebAssembly and embedded in the CLI.** It keeps one source and runs on every system, but it adds a WebAssembly runtime to the CLI and a second build target of store@ for one command.
- **Go for everything except `policy`.** It leaves the public edge with net/http's advisory stream and Go's data-race class, and keeps root-run secrets in a garbage-collected heap.
- **Rust for everything, or for every actor on a small board.** Rust has no embeddable OpenID Connect provider, no maintained static-ct-api log or C2SP witness, no PIV-attestation library and no maintained vendor clients; each would be written in-house.
- **Group by piece (core, audit, platform in one language each).** It overrides the library-driven choices, while the contracts already make each executable independent.
- **`exchange` in Rust, or `signin` and `exchange` together.** Three of the six proof verifiers and RFC 8693 would be written in-house, and the HPKE crate is unaudited; the secrets leave the process through the keys port instead.
- **key@ on the `yubikey` crate.** Its own notice says it is unaudited and experimental.
- **key@ in Rust through PKCS#11 (`cryptoki` with `ykcs11`).** The spike found that `ykcs11` resets the card for every client when a second one appears and keeps a PIN copy Rust cannot wipe; `piv-go`'s exclusive connection is what one holder per node needs.
- **store@ in Go for the vendor SDKs.** The store speaks to its engines through drivers, so the SDKs are in the drivers and not in store@; a Go store@ would cost about 8 MiB of private memory on a 2 GB board for no library it needs.
- **Rust AWS drivers.** A second implementation on the younger SDK, for no caller that needs it, and a Rust library cannot be linked into the Go Lambda function that needs them in process.
- **The AWS drivers in Rust "because one AWS SDK language in sluis's build".** The drivers are atoom's and built there, so the reason no longer holds.
- **Go by default on a small board.** About 250 MiB more private memory at 30 actors on a 2 GB board (235 to 255 MiB, measured 2026-11-02), where Rust costs no serious trade-off.

## Follow-ups

- Spike S3's two remaining checks: the real tunnel client and 24-hour fuzz campaigns ([#941](https://github.com/truvity/sluis/issues/941), [#796](https://github.com/truvity/sluis/issues/796)).
- A validation bridge from protovalidate to `buffa` messages, to replace the hand-written checks of the Rust kit. Open: the issue is not yet filed.
- Open: whether `signer` stays Rust and calls atoom's Go `token` block as a process, or follows the block's language, when the block is built.
