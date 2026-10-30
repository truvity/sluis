# 0010 — Cryptography uses EC P-384 by default, in FIPS mode, with named exceptions

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).

## Context

Every token, certificate and signed log head that sluis makes is signed, and every secret it keeps is sealed. The choice of algorithm touches three groups of readers:

- **Verifiers** that do not belong to sluis. Many OIDC libraries and several platforms were written for RS256 and accept little else.
- **Standards** that fix a set of algorithms. The FAPI 2.0 Security Profile (Final), section 5.4.1, item 1.2, names PS256, ES256 and EdDSA. Checked on 2026-10-29.
- **Drivers.** A cloud key service, a hardware key and a software key support different algorithms and sign at very different speeds.

v1 settled this in two steps. v1 ADR 0005 made ES384 the default signing algorithm. v1 ADR 0009 added exceptions per audience, with a separate key, for relying parties that accept RS256 only. v2 keeps both decisions as the starting point. It adds the FAPI 2.0 algorithm set, the cloud-federation exceptions, FIPS mode and a rule for randomness, and it says where the exceptions stop.

Which key signs, and where that key lives, is a separate decision ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).

## Decision

- **EC P-384 with ES384 is the default and sluis's internal recommendation.** One curve means one key type in the published key set, one verifier path and one sealing scheme. Anything sluis chooses for itself uses P-384, including the checkpoints of the audit log and the issuance log (a C2SP signed note with a P-384 key).
- **sluis supports the FAPI 2.0 algorithm set: PS256, ES256 and EdDSA (Ed25519).** A client or a resource server that follows that profile can be served. ES384 stays the default. PS256 is RSA, so this set is a named exception to "no RSA".
- **"No RSA" has exactly these exceptions, and each one has its own key.** A key that serves an exception is never shared with another purpose.
  1. **FAPI 2.0.** PS256, as above.
  2. **Cloud federation.** A key that signs a workload token for a cloud may use the algorithm that cloud requires: P-384 (ES384) for AWS, P-256 (ES256) for GCP and RSA-2048 (RS256) for Azure Entra.
  3. **A per-audience RS256 exception.** Some verifiers cannot take an elliptic-curve signature. For an audience that is configured to need RS256, sluis signs that audience's tokens with a separate RSA key. Every other audience keeps getting EC tokens. The RSA public key is published beside the EC keys. The audience, not the caller, decides the algorithm.
- **A format that fixes its own algorithm is followed, not changed.** The witness cosignatures of the C2SP tlog-cosignature format use Ed25519 or ML-DSA-44, as that specification requires. A witness key is not an authority key of sluis and serves no other purpose. (inferred)
- **This record governs what sluis signs and seals.** Which algorithms sluis accepts when it verifies a signature made by others, such as a passkey assertion or an upstream provider's token, is decided with those features (passkeys: ADR 0048). (inferred)
- **How a client or an audience selects a non-default algorithm is client-profile configuration.** It is not part of this record.
- **Driver support is checked per algorithm, not assumed.** Two facts are known now. PS256 on a YubiKey takes about 2.3 seconds per signature, so it suits a low-volume audience only. Ed25519 in the PIV application needs YubiKey firmware 5.7 or newer. A driver that cannot offer an algorithm refuses it when the installation is checked, not at the first token.
- **FIPS 140-3 mode is on by default.** The default build runs Go's FIPS 140-3 module with `fips140=on`, using the latest module. That refuses X25519 and ChaCha20 in TLS and runs a pairwise consistency test when a key is generated. Both are acceptable. A separately pinned build uses the CMVP-certified module (certificate #5247, module v1.0.0) for installations that need the certificate itself. BoringCrypto is not used.
- **The only source of randomness is the operating system's CSPRNG**, through Go's `crypto/rand`. Since Go 1.26 the standard library's crypto ignores a reader passed in and always uses it. Tests replace it with `testing/cryptotest.SetGlobalRandom`. sluis does not mix in randomness from a key service or a hardware key, and NIST SP 800-90C does not require it.
- **`sluis doctor` checks the platform's randomness**, because that is what can still go wrong:
  - the kernel is 5.18 or newer, so a cloned virtual machine reseeds its generator;
  - a hardware generator is present, or deliberately absent, and the check knows the difference;
  - the entropy pool is initialised before the first key is generated.
- **Tokens are standard JWS and JWT.** Any language verifies them with its standard libraries against the published key set, so sluis needs no client library.

## Consequences

- A client that needs RS256 or PS256 can be served without moving the installation off ES384. The cost is an RSA key to hold, rotate and publish for each such exception, and a larger token for that audience.
- Verifiers that cannot take ES384 are an integration problem with a documented fix, not a reason to change the default. The per-audience exception is the fallback when the fix is not available.
- A hardware key is slow for RSA. An audience that needs PS256 on a YubiKey installation either accepts a few seconds per signature or is served by a different key mode.
- FIPS mode changes what TLS can negotiate. Documentation and `sluis doctor` have to say so. Conformance tests run with `fips140=on` (a test task, not a decision).
- The certified build is a separate artefact with its own pin and its own release note. It is chosen on purpose and never by accident.
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

### Mix randomness from a key service or a hardware key into the system's

- Pros: protection against a weak operating system generator on one platform.
- Cons: more code in the path that makes every key. A second source that has to be available and trusted. NIST SP 800-90C does not require it.
- Why not: the failure mode is a platform that is not ready, and that is better caught by a check at startup than hidden by a mix.

### Build on BoringCrypto

- Pros: a long-used FIPS-oriented module.
- Cons: it needs cgo, a separate toolchain and a separate build. The standard library has its own FIPS 140-3 module. (inferred)
- Why not: the standard library module covers the need with no extra toolchain. (inferred)
