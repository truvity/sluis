# 0010 — Cryptography uses EC P-384 by default, stays within the FIPS-approved algorithms, with named exceptions

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass; then a named exception for NATS decentralized authentication, and FIPS parity for Rust components).
Rewritten in place on 2026-10-30 (alignment with 0007, 0058).
Rewritten in place on 2026-10-31 (decision FIPS1: no FIPS mode and no certified build in v2.0; every algorithm stays within the FIPS-approved set, so that a FIPS build variant can be added later. The result of spike S2, [issue #940](https://github.com/truvity/sluis/issues/940), is kept as context).

## Context

Every token, certificate and signed log head that sluis makes is signed, and every secret it keeps is sealed. The choice of algorithm touches three groups of readers:

- **Verifiers** that do not belong to sluis. Many OIDC libraries and several platforms were written for RS256 and accept little else.
- **Standards** that fix a set of algorithms. The FAPI 2.0 Security Profile (Final), section 5.4.1, item 1.2, names PS256, ES256 and EdDSA. Checked on 2026-10-29.
- **Drivers.** A cloud key service, a hardware key and a software key support different algorithms and sign at very different speeds.

v2 is written in two languages, Go and Rust; which actor uses which is ADR 0060. A FIPS rule written only for Go's standard library would leave the Rust components outside it. And sluis will issue credentials for NATS, whose decentralized authentication fixes its own key type.

FIPS 140-3 was first planned as a mode that is on by default, with a separately pinned, certified build for both languages. Spike S2 ([issue #940](https://github.com/truvity/sluis/issues/940), 2026-10-30) measured what that costs on the Rust side:

- **The only certified Rust pin is old.** `aws-lc-fips-sys` 0.13.11 bundles AWS-LC-FIPS 3.1.0, the module on CMVP certificate #5314. It needs `aws-lc-rs` 1.17.1 and, with it, `rustls` 0.23.43, which freezes rustls and every crate that depends on it. The current `aws-lc-rs` bundles AWS-LC-FIPS 4.2.0, which was still *in process* at the CMVP on 2026-10-22.
- **The FIPS build is heavy.** It needs CMake, Go, Perl and a pinned clang. It cannot be built with cargo-zigbuild, and the module's Security Policy asks for a separate compile on each processor and operating system.
- **The AWS SDK for Rust signs outside the module.** SigV4 request signing and S3 checksums use RustCrypto, and no feature changes that. TLS runs in the module, request signing does not. The Go SDK has no such gap.
- **The algorithms themselves passed.** With FIPS mode on, the module's service indicator reported P-384 key generation and ECDSA P-384 with SHA-384, AES-256-GCM with a nonce the module generates, HKDF-SHA-384, SHA-384 and HPKE with DHKEM(P-384) as approved. It reported AES-GCM with a caller-supplied nonce, and ChaCha20-Poly1305, as not approved.

No installation that v2.0 targets needs a FIPS certificate today. The cost of a certified build would be paid in every build, for every component, before any customer asks for it.

v1 settled this in two steps. v1 ADR 0005 made ES384 the default signing algorithm. v1 ADR 0009 added exceptions per audience, with a separate key, for relying parties that accept RS256 only. v2 keeps both decisions as the starting point. It adds the FAPI 2.0 algorithm set, the cloud-federation exceptions, the FIPS-approved set as a rule and a rule for randomness, and it says where the exceptions stop.

Which key signs, and where that key lives, is a separate decision ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).

## Decision

- **EC P-384 with ES384 is the default and sluis's internal recommendation.** One curve means one key type in the published key set, one verifier path and one sealing scheme. Anything sluis chooses for itself uses P-384, including the checkpoints of the audit log and the issuance log (a C2SP signed note with a P-384 key).
- **sluis supports the FAPI 2.0 algorithm set: PS256, ES256 and EdDSA (Ed25519).** A client or a resource server that follows that profile can be served. ES384 stays the default. PS256 is RSA, so this set is a named exception to "no RSA".
- **"No RSA" has exactly these exceptions, and each one has its own key.** A key that serves an exception is never shared with another purpose.
  1. **FAPI 2.0.** PS256, as above.
  2. **Cloud federation.** A key that signs a workload token for a cloud may use the algorithm that cloud requires: P-384 (ES384) for AWS, P-256 (ES256) for GCP and RSA-2048 (RS256) for Azure Entra.
  3. **A per-audience RS256 exception.** Some verifiers cannot take an elliptic-curve signature. For an audience that is configured to need RS256, sluis signs that audience's tokens with a separate RSA key. Every other audience keeps getting EC tokens. The RSA public key is published beside the EC keys. The audience, not the caller, decides the algorithm.
- **NATS decentralized authentication is a named exception to P-384.** NATS fixes Ed25519 for its operator, account and user keys (nkeys), and its JWTs are signed with them. sluis follows that:
  - **one Ed25519 key per purpose:** the operator identity, an operator signing key per environment, a scoped signing key per role and account, and the callout account's key. None of them serves another purpose;
  - **the keys live in the cloud key service, or behind `key-holder`** with an access list that names only the NATS credential minter. A user's own nkey is generated by the workload and never leaves it (inferred);
  - **no xkey.** The X25519 key that NATS can use to seal callout requests is not used, because X25519 alone is not an approved algorithm under FIPS 140-3. TLS on the client port protects the request instead;
  - Ed25519 is approved (FIPS 186-5) and is in Go's FIPS 140-3 module, so this exception stays within the approved set and would run in a FIPS build variant.
- **A format that fixes its own algorithm is followed, not changed.** The witness cosignatures of the C2SP tlog-cosignature format use Ed25519 or ML-DSA-44, as that specification requires. A witness key is not an authority key of sluis and serves no other purpose. (inferred) The same holds for age, which the backup archives and the export drops use because the recipient's tooling reads it: its X25519 and ChaCha20-Poly1305 are outside the approved set, so a FIPS build variant has to exclude those drops or replace the format. (inferred)
- **This record governs what sluis signs and seals.** Which algorithms sluis accepts when it verifies a signature made by others, such as a passkey assertion or an upstream provider's token, is decided with those features (passkeys: ADR 0048). (inferred)
- **How a client or an audience selects a non-default algorithm is client-profile configuration.** It is not part of this record.
- **Driver support is checked per algorithm, not assumed.** Two facts are known now. PS256 on a YubiKey takes about 2.3 seconds per signature, so it suits a low-volume audience only. Ed25519 in the PIV application needs YubiKey firmware 5.7 or newer. A driver that cannot offer an algorithm refuses it when the installation is checked, not at the first token.
- **No FIPS mode and no certified build in v2.0.** Go builds with the standard library as it is, without forcing FIPS mode (`GOFIPS140` and the `fips140` setting keep their defaults). Rust components use current crates and rustls, with no FIPS pin, and may be built with cargo-zigbuild.
- **Every algorithm stays within the FIPS-approved set.** What sluis signs, seals, hashes, derives or encrypts uses P-384 ECDSA, AES-256-GCM, HKDF-SHA384 and SHA-384, plus the named exceptions above (PS256, ES256, RSA for an audience or a cloud that requires it, Ed25519 where a protocol requires it). All of them are approved algorithms. A format that fixes its own algorithm, below, is the only exception to the set in what sluis signs or seals. The point of the rule is that a FIPS build variant can be added later as a build option, without a change to a format or a stored value.
- **AES-256-GCM is used with a nonce that the cryptographic library generates**, never one the caller supplies. In Rust that is `RandomizedNonceKey` of `aws-lc-rs`. A caller-supplied nonce is not approved in FIPS mode, so this keeps the same code valid in the variant.
- **TLS follows the libraries' defaults in v2.0.** Go and rustls may negotiate X25519 or ChaCha20-Poly1305 with a peer that prefers them. The variant's FIPS mode refuses both, so nothing sluis stores or publishes depends on them. (inferred)
- **A FIPS build variant is added when a customer needs it**, most likely after AWS-LC 4 is certified, because that lifts the Rust pin. It is a build option, not a second product. When it is added:
  - Go runs its FIPS 140-3 module with `fips140=on`, or a pinned build selects a CMVP-certified module version. BoringCrypto is not used.
  - Rust components that perform an approved operation use `aws-lc-rs` in FIPS mode, and the certified form pins a certified AWS-LC module.
  - The AWS SDK for Rust gap (SigV4 outside the module) is solved, or the AWS drivers are built in Go for that variant (ADR 0060, R8).
  - It needs its own build design, with its name and delivery, decided when it is added.
- **The only source of randomness is the operating system's CSPRNG**, through Go's `crypto/rand`. Since Go 1.26 the standard library's crypto ignores a reader passed in and always uses it. Tests replace it with `testing/cryptotest.SetGlobalRandom`. A Rust component uses the generator of its cryptographic library, which is seeded by the operating system, and no second library (inferred). sluis does not mix in randomness from a key service or a hardware key, and NIST SP 800-90C does not require it.
- **`sluis doctor` checks the platform's randomness**, because that is what can still go wrong:
  - the kernel is 5.18 or newer, so a cloned virtual machine reseeds its generator;
  - a hardware generator is present, or deliberately absent, and the check knows the difference;
  - the entropy pool is initialised before the first key is generated.
- **Tokens are standard JWS and JWT.** Any language verifies them with its standard libraries against the published key set, so sluis needs no client library.

## Consequences

- A client that needs RS256 or PS256 can be served without moving the installation off ES384. The cost is an RSA key to hold, rotate and publish for each such exception, and a larger token for that audience.
- Verifiers that cannot take ES384 are an integration problem with a documented fix, not a reason to change the default. The per-audience exception is the fallback when the fix is not available.
- A hardware key is slow for RSA. An audience that needs PS256 on a YubiKey installation either accepts a few seconds per signature or is served by a different key mode.
- No FIPS mode means no FIPS claim. sluis v2.0 does not say that it uses a validated module, and the documentation and the standards register say so. An installation that needs a certificate waits for the variant.
- The build stays light: one GoReleaser release with cargo-zigbuild for Rust, current rustls, and no CMake, Perl or pinned clang in the build.
- The approved-set rule has to be kept by review. A new algorithm, a crate that brings its own primitive, or a caller-supplied GCM nonce is a regression for the variant even when every test passes. (inferred)
- When the variant is added it costs its own build design, a second set of release artefacts and conformance runs with FIPS mode on.
- NATS keys add Ed25519 to the key types that the cloud key service and `key-holder` must offer. Each connection costs a few signatures, far below the key service's quotas at the volumes measured. Without the xkey, a callout request is protected by TLS only, so TLS on every client connection is a requirement, not an option.
- No randomness is mixed in. Platforms with a weak generator are caught by `sluis doctor`, which warns where a vendor kernel is older than 5.18. It does not compensate for them.
- Post-quantum signatures are not part of this record. A post-quantum root is parked with a dated trigger in ADR 0052, and ML-DSA-44 appears here only as a witness key type that a followed format allows.
- The site's keys page and the standards register say the same as this record.

## Roads not taken

### RSA as the default

- Pros: every OIDC verifier accepts RS256. No exception is ever needed.
- Cons: larger keys and signatures than ECDSA at a comparable security level, and slower signing. A smaller security margin for every audience, including those that never asked for RSA. (the cons are inferred)
- Why not: it taxes every relying party for the least capable one. The exception per audience gives that verifier what it needs and costs the others nothing (v1 ADR 0009). (inferred)

### P-256 (ES256) as the default

- Pros: the most widely supported elliptic curve. Faster than P-384. Part of the FAPI 2.0 set. (inferred)
- Cons: a 128-bit security level against 192 bits for P-384. P-384 signs fast enough on every key mode in use, at about 120 ms on a YubiKey. (the first part is inferred)
- Why not: the volume is low, so the extra speed buys nothing, and the larger margin is free. ES256 stays supported for the audiences and clouds that need it.

### Only the FAPI 2.0 set, with no ES384

- Pros: one list, taken from a standard. No algorithm that the profile does not name.
- Cons: AWS federation uses P-384. The internal default would be an algorithm chosen only by FAPI, not by what suits sluis.
- Why not: the standard says what a client may expect to be offered. It does not say what sluis should prefer.

### Ed25519 as the default

- Pros: a compact, fast, modern signature. Part of the FAPI 2.0 set.
- Cons: not every key mode offers it. The PIV application needs firmware 5.7 or newer. Fewer verifiers accept it than accept ES384.
- Why not: the default has to work on every key mode and every installation. Ed25519 is supported where the driver offers it.

### No RSA at all, with no exceptions

- Pros: one key type, no RSA key to protect.
- Cons: Azure Entra needs RS256, and some relying parties are fixed to it. The FAPI 2.0 set contains PS256.
- Why not: it would cut off clouds and clients that sluis has to serve. The exceptions are named, each has its own key, and none is the default.

### FIPS everywhere now: FIPS mode on by default, and a certified build for both languages

- Pros: one FIPS story from the first release. An installation that needs a certificate is served from v2.0.
- Cons: the only certified Rust pin freezes rustls at 0.23.43 and lags the current `aws-lc-rs`. Every Rust build needs CMake, Go, Perl and a pinned clang, cannot use cargo-zigbuild, and has to be compiled separately for each architecture. The AWS SDK for Rust still signs SigV4 outside the module, so the AWS drivers would not be fully inside it anyway. No installation that v2.0 targets needs the certificate.
- Why not: the cost lands on every build before anyone needs the result. Keeping to the approved algorithms keeps the variant possible at a fraction of the cost.

### FIPS mode in Go only

- Pros: Go's FIPS 140-3 module is in the standard library, so `fips140=on` costs Go almost nothing.
- Cons: the Rust components (the edge, the trusted base, the cloud drivers) would be outside it, so the installation as a whole could not claim FIPS mode. It would be a half claim that is easy to misread.
- Why not: a FIPS claim has to cover the whole installation, which is what the later variant does in both languages.

### NATS keys under the format rule, without a named exception

- Pros: no new exception. The rule "a format that fixes its own algorithm is followed" already covers the case in spirit.
- Cons: that rule is written for witness keys, which authorise nothing. NATS keys authorise access to accounts and subjects, so they deserve the explicit scrutiny of a named exception.
- Why not: the exception is named, with one key per purpose, as every other exception has.

### Keep the NATS xkey

- Pros: the callout request would be sealed end to end, not only protected by TLS.
- Cons: X25519 alone is not approved under FIPS 140-3, and NATS creates no xkey in its own FIPS mode.
- Why not: TLS on the client port carries the request, and the xkey is dropped.

### Mix randomness from a key service or a hardware key into the system's

- Pros: protection against a weak operating system generator on one platform.
- Cons: more code in the path that makes every key. A second source that has to be available and trusted. NIST SP 800-90C does not require it.
- Why not: the failure mode is a platform that is not ready, and that is better caught by a check at startup than hidden by a mix.

### Build on BoringCrypto

- Pros: a long-used FIPS-oriented module.
- Cons: it needs cgo, a separate toolchain and a separate build. The standard library has its own FIPS 140-3 module. (inferred)
- Why not: when the variant is added, the standard library module covers the Go side with no extra toolchain. (inferred)
