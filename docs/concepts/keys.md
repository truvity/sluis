---
title: Keys
description: "Which key backs which purpose in each installation: key modes, direct signing, signing algorithms, KMS and YubiKey layouts, the two roots, randomness."
status: target
sidebar:
  order: 4
---

sluis holds four authority keys (token signing, the certificate authorities, the audit log key and the mint grant), plus the keys that seal secrets at rest. This page says what backs each one in each installation, how it signs, and which algorithms it uses. The access rules, which actor may use which key, are enforced by the driver or the cloud, not by this page: see [Drivers](/concepts/drivers/).

## Key modes

The key mode is the choice of the `keys` driver, and it follows the installation:

| Installation | Mode |
|---|---|
| [lambda](/runtimes/installations/#lambda), and other AWS installations | AWS KMS (`aws-kms`) |
| [boards](/runtimes/installations/#boards), and other installations on single-board computers | A hardware key, a YubiKey in PIV mode (`yubikey-piv`), one per node |
| [local](/runtimes/installations/#local) | The host's YubiKey through `key-holder`, or a software key (`software`) |
| [kubernetes](/runtimes/installations/#kubernetes) | AWS KMS on EKS; no mode yet for other clusters |

:::note[Proposed]
**Floors in code, choices above them in config.** The code refuses a software key in a production installation. Configuration chooses among the modes the floor allows. A development install can therefore never be mistaken for a production one by loading its configuration on the wrong node.
:::

## Direct signing

Tokens are signed directly with the non-extractable key in every installation. A key is never used to certify a short-lived software key that signs in its place, so there are no derived signing keys and no burst key: a hardware key or KMS signs each token itself, and the signing key never leaves the device or the service.

The volume makes this affordable. On a production install, a measurement showed about 25,000 tokens in 7 days, with a peak of 0.8 per second. 95 % of them were token exchange. At that volume:

- **KMS** costs about $2.62 a month for signing every token.
- **A YubiKey** signs P-384 in about 120 ms, which is about 8 signatures per second. The measured peak is a tenth of that. A software P-384 key would sign about 200 per second per core, which is why the local installation can use one.

## Algorithms

**ES384 (ECDSA on P-384) is sluis's internal recommendation and its default.** One curve means one set of verifiers, one JWKS key type and one sealing scheme.

sluis also supports **the FAPI 2.0 set of signing algorithms**, so that a client or a resource server that follows that profile can be served: **PS256**, **ES256** and **EdDSA (Ed25519)**. This follows the FAPI 2.0 Security Profile (Final), section 5.4.1, item 1.2, checked on 2026-10-29. ES384 stays the default. How a client or an audience selects another algorithm is proposed.

| Algorithm | Family | Where it is used |
|---|---|---|
| ES384 | ECDSA, P-384 | The default, and AWS federation |
| ES256 | ECDSA, P-256 | FAPI 2.0 audiences, and GCP federation |
| PS256 | RSA-PSS | FAPI 2.0 audiences. This is RSA, so it is another exception to "no RSA" |
| EdDSA | Ed25519 | FAPI 2.0 audiences |

Support depends on the driver, and has to be checked per driver rather than assumed:

- **PS256 on a YubiKey** takes about 2.3 seconds per signature, so it suits a low-volume audience only.
- **Ed25519 in PIV** needs YubiKey firmware 5.7 or newer.

**No RSA**, apart from three exceptions, each kept in its own slot or key and never shared with anything else:

1. **The cloud-federation exception.** A key used to sign a workload token for a cloud may use the algorithm that cloud requires.
2. **The per-audience RS256 exception.** Some verifiers cannot take an elliptic-curve signature. For an audience that is configured to need RS256, sluis signs that audience's tokens with a separate RSA key. Every other audience keeps getting EC tokens. The RSA public key is published in the JWKS beside the EC keys.
3. **PS256 for FAPI 2.0**, as above.

| Cloud | Algorithm |
|---|---|
| AWS | P-384 (ES384) |
| GCP | P-256 (ES256) |
| Azure Entra | RSA-2048 (RS256) |

See [Identity](/concepts/identity/) for how these keys are used.

**Kubernetes.** For now the kubernetes installation supports AWS KMS only (EKS), and the keys are the AWS KMS keys of the layout below ([OD-20](/open-decisions/#od-20)). A cluster outside AWS has no key mode that the floor allows, and refuses to start rather than fall back to a software key.

:::note[Proposed]
The workload tokens on Kubernetes are the cluster's own ServiceAccount tokens, signed by the cluster's key with the algorithm the cluster uses. sluis neither holds nor signs them, so the P-384 rule above does not cover them. How an actor gets its cloud identity from them is [OD-19](/open-decisions/#od-19). See [Kubernetes](/runtimes/kubernetes/).
:::

## Wrap and unwrap

The `keys` port has two more operations beside `sign` and `public-key`: **`wrap`** and **`unwrap`** (or an `ecdh` operation that gives the same result). A caller uses them to protect a data key without the key that protects it ever leaving the device or the service. A backup archive's data key is wrapped this way, and so are the sealed secrets at rest. See [Backup](/concepts/backup/#encryption) and [Secrets and export](/concepts/secrets/).

- On **AWS**, `wrap` is KMS `Encrypt` with an encryption context that names the purpose, so one key serves every wrapping need and only the role that the key policy names can `unwrap`.
- On a **YubiKey**, `wrap` is ECDH and HKDF against the public key of the sealing slot, and `unwrap` is the same exchange done on the card (see [Sealing without a symmetric key](#sealing-without-a-symmetric-key)).

## AWS KMS layout

On the AWS installations there are five keys:

| Key | Spec | Purpose |
|---|---|---|
| Sealing and wrapping | One symmetric key, `ENCRYPT_DECRYPT` | Every sealing and wrapping need, separated by **encryption context** |
| Token signing | `ECC_NIST_P384`, `SIGN_VERIFY` | The signing ring |
| CA | `ECC_NIST_P384`, `SIGN_VERIFY` | The certificate authorities |
| Audit log | `ECC_NIST_P384`, `SIGN_VERIFY` | Audit batch heads, one key per log, used only by that log's writer |
| Grant | `ECC_NIST_P384`, `SIGN_VERIFY` | Mint grants, held by `policy` |

The reason for one key per authority purpose is how KMS works. A KMS key has one usage, and `Sign` takes no encryption context, so the only way to say "only this actor may sign with this key" is a key policy on a key that exists for that one purpose. With a key per purpose, each key policy names exactly one actor. Sealing is the exception, because `Encrypt` and `Decrypt` do take an encryption context, so one key serves every sealing need with a context per use.

## YubiKey layout

A YubiKey in PIV mode has 24 slots and **no symmetric keys**. The slot plan:

| Slot | Use |
|---|---|
| `9c` | Node intermediate and signing |
| `9d` | ECDH sealing and ring wrap |
| `82` | SSH CA |
| `83` to `95` | Retired sealing keys |
| `9e` | Avoided |

There is one YubiKey per node. EUCLEAK, a side-channel flaw in the older firmware, is fixed from firmware 5.7. The cards of the root pair (below) are separate from the node cards, and never sit in a node.

### Sealing without a symmetric key

Because the device has no symmetric key, sealing uses ECDH and HKDF in the style of HPKE with a P-384 key in slot `9d`:

1. To seal, the sealer generates an ephemeral P-384 key and performs ECDH against the slot's **public** key. It runs HKDF over the shared secret to derive an AEAD key, encrypts, and stores the ephemeral public key beside the ciphertext. No hardware is needed to seal.
2. To open, the device performs ECDH between slot `9d`'s private key and the stored ephemeral public key, and the same HKDF yields the same AEAD key.

The consequence is that sealing needs the YubiKey only when opening. Retired slots `83` to `95` keep older sealing keys alive after rotation, so what was sealed under them can still be opened.

## The roots and the ceremony

The trust of an installation starts at two **roots**, held on ordinary YubiKeys in PIV mode (not an HSM).

- **Two independent roots.** Each card of a pair generates **its own root** on the card, in a PIV slot, with the PIN policy *always* and the touch policy *always*. Neither root is a copy or a spare of the other.
- **Both roots are authoritative, and both are in every trust bundle.** A verifier trusts either. Cross-signing between the roots is optional.
- **No key is ever copied between cards.** A key that was imported into two cards has existed in software once and has lost its attestation; a key generated on the card keeps its PIV attestation, which proves that it was made on a genuine device with those policies.
- **Each intermediate is certified by both roots.** There is one intermediate key and two certificates, one from each card's root. Losing either card breaks no chain.

### A lost root card

If a root card is lost, the procedure is:

1. **Remove that root from every trust bundle**, through the trust registry. Removing a root from the bundles is the only way to revoke it.
2. **Generate a new root** on a fresh card.
3. **Re-issue any intermediate that chained only to the lost root.** Because every intermediate is certified by both roots, none should exist.
4. Until the first step is done, the lost card's PIN and touch policy limit the exposure.

### OpenPGP release signing

The same rule holds for releases. Each card has **its own OpenPGP primary key**, generated on the card, and **each primary has its own signing subkey**. **Every release is signed twice**, once with each, so either fingerprint verifies every release. A verifier needs one of the two, and losing one card removes one of the two signatures and no release.

### The ceremony

Creating the roots, signing an intermediate and rotating a root are done in a **ceremony**: a scripted, reviewable procedure with a printed template and a hash that two people can compare, and a verification that is repeated on another machine. The `sluis pki` and `sluis ceremony` commands are a feature to plan. Their shape is not decided.

## TPM 2.0

TPM 2.0 is a backlog feature. When it ships, it is an addition to the YubiKey, not a replacement for it.

## Randomness

sluis uses Go's `crypto/rand`, which is the operating system's CSPRNG. Since Go 1.26 the standard library's crypto ignores an `io.Reader` that is passed in and always uses it; tests replace it with `testing/cryptotest.SetGlobalRandom`. sluis does not mix in KMS `GenerateRandom` or YubiKey randomness. NIST SP 800-90C does not require mixing.

What can still go wrong is the platform, so `sluis doctor` checks it:

- **Kernel 5.18 or newer**, so that a cloned virtual machine reseeds its generator.
- **A hardware generator that is present or deliberately absent.** The RK3566 hardware generator, for example, is disabled in mainline Linux on purpose, and the check knows the difference.
- **The entropy pool is initialised before the first key is generated.**

The vendor kernel 5.10 that the boards run for now makes `sluis doctor` warn on the first check.

## FIPS

The **default build** runs Go's FIPS 140-3 module in `fips140=on` mode, using the latest module. That refuses X25519 and ChaCha20 in TLS and runs pairwise consistency tests when keys are generated, and both are acceptable for sluis.

A **separate build** pins the CMVP-certified module (certificate #5247, module v1.0.0) for installs that need the certificate itself.

BoringCrypto is not used.

## JOSE

:::note[Proposed]
JOSE is implemented with `github.com/lestrrat-go/jwx/v4` behind sluis's own `jose` package, so the library can be replaced without touching the actors. There is no Rust JOSE driver.

Tokens are standard JWS and JWT. Any language verifies them with its standard libraries against the published JWKS, so sluis needs no client library. Small verification demos in a few languages are on the backlog.
:::

## Post-quantum readiness

:::note[Proposed]
This section is a placeholder. A design note on post-quantum readiness is in preparation. It will cover which keys and exchanges would change first, and which of the choices above (P-384 everywhere, the hardware keys, the cloud-federation exception) it affects. Nothing on this page depends on it yet.
:::
