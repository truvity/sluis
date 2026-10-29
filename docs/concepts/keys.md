---
title: Keys
description: "Which key backs which purpose on each shape: key modes and floors, direct signing, algorithms, KMS and YubiKey layouts, randomness, FIPS and JOSE."
status: target
sidebar:
  order: 4
---

sluis holds four authority keys (token signing, the certificate authorities, the audit notary and the mint grant), plus the keys that seal secrets at rest. This page says what backs each one on each shape, how it signs, and which algorithms it uses. The access rules, which role may use which key, are enforced by the driver or the cloud, not by this page: see [Drivers](/concepts/drivers/).

## Key modes

The key mode is a property of the shape:

| Shape | Mode |
|---|---|
| AWS shapes (EC2, Lambda) | AWS KMS |
| Board shapes (single node, cluster) | A hardware key, a YubiKey in PIV mode |
| Development (local) | A software key |
| Kubernetes (T6, proposed) | AWS KMS on EKS; no mode yet for other clusters |

:::note[Proposed]
**Floors in code, choices above them in config.** The code refuses a software key on a production shape. Configuration chooses among the modes the floor allows. A development install can therefore never be mistaken for a production one by loading its configuration on the wrong node.
:::

## Direct signing

Tokens are signed directly with the non-extractable key on every shape. A key is never used to certify a short-lived software key that signs in its place, so there are no derived signing keys and no burst key: a hardware key or KMS signs each token itself, and the signing key never leaves the device or the service.

The volume makes this affordable. On a production install, a measurement showed about 25,000 tokens in 7 days, with a peak of 0.8 per second. 95 % of them were token exchange. At that volume:

- **KMS shapes** pay about $2.62 a month for signing every token with KMS.
- **YubiKey shapes** sign P-384 in about 120 ms, which is about 8 signatures per second. The measured peak is a tenth of that. A software P-384 key would sign about 200 per second per core, which is why the development shape can use one.

## Algorithms

- **EC P-384 everywhere. No RSA**, apart from the two exceptions below. One curve means one set of verifiers, one JWKS key type and one sealing scheme.
- **The cloud-federation exception.** A key used to sign a workload token for a cloud may use the algorithm that cloud requires, each in its own slot or key and never shared with anything else:

| Cloud | Algorithm |
|---|---|
| AWS | P-384 (ES384) |
| GCP | P-256 (ES256) |
| Azure Entra | RSA-2048 (RS256) |

See [Identity](/concepts/identity/) for how these keys are used.

**Kubernetes (T6).** For now the Kubernetes shape supports AWS KMS only (EKS), and the keys are the AWS KMS keys of the layout below ([OD-20](/open-decisions/#od-20)). A cluster outside AWS has no key mode that the floor allows, and refuses to start rather than fall back to a software key.

:::note[Proposed]
The workload tokens on Kubernetes are the cluster's own ServiceAccount tokens, signed by the cluster's key with the algorithm the cluster uses. sluis neither holds nor signs them, so the P-384 rule above does not cover them. How a role gets its cloud identity from them is [OD-19](/open-decisions/#od-19). See [Kubernetes](/shapes/kubernetes/).
:::

- **The per-audience RS256 exception.** Some verifiers cannot take an elliptic-curve signature. For an audience that is configured to need RS256, sluis signs that audience's tokens with a separate RSA key, kept in its own slot or KMS key and used for nothing else. Every other audience keeps getting EC P-384 tokens. The RSA public key is published in the JWKS beside the EC keys.

## AWS KMS layout

On AWS shapes there are five keys:

| Key | Spec | Purpose |
|---|---|---|
| Sealing | One symmetric key, `ENCRYPT_DECRYPT` | Every sealing need, separated by **encryption context** |
| Token signing | `ECC_NIST_P384`, `SIGN_VERIFY` | The signing ring |
| CA | `ECC_NIST_P384`, `SIGN_VERIFY` | The certificate authorities |
| Notary | `ECC_NIST_P384`, `SIGN_VERIFY` | Audit batch heads |
| Grant | `ECC_NIST_P384`, `SIGN_VERIFY` | Mint grants, held by `policy` |

The reason for one key per authority purpose is how KMS works. A KMS key has one usage, and `Sign` takes no encryption context, so the only way to say "only this role may sign with this key" is a key policy on a key that exists for that one purpose. With a key per purpose, each key policy names exactly one role. Sealing is the exception, because `Encrypt` and `Decrypt` do take an encryption context, so one key serves every sealing need with a context per use.

## YubiKey layout

A YubiKey in PIV mode has 24 slots and **no symmetric keys**. The slot plan:

| Slot | Use |
|---|---|
| `9c` | Node intermediate and signing |
| `9d` | ECDH sealing and ring wrap |
| `82` | SSH CA |
| `83` to `95` | Retired sealing keys |
| `9e` | Avoided |

There is one YubiKey per node. EUCLEAK, a side-channel flaw in the older firmware, is fixed from firmware 5.7.

### Sealing without a symmetric key

Because the device has no symmetric key, sealing uses ECDH and HKDF in the style of HPKE with a P-384 key in slot `9d`:

1. To seal, the sealer generates an ephemeral P-384 key and performs ECDH against the slot's **public** key. It runs HKDF over the shared secret to derive an AEAD key, encrypts, and stores the ephemeral public key beside the ciphertext. No hardware is needed to seal.
2. To open, the device performs ECDH between slot `9d`'s private key and the stored ephemeral public key, and the same HKDF yields the same AEAD key.

The consequence is that sealing needs the YubiKey only when opening. Retired slots `83` to `95` keep older sealing keys alive after rotation, so what was sealed under them can still be opened.

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
JOSE is implemented with `github.com/lestrrat-go/jwx/v4` behind sluis's own `jose` package, so the library can be replaced without touching the roles. There is no Rust JOSE driver.

Tokens are standard JWS and JWT. Any language verifies them with its standard libraries against the published JWKS, so sluis needs no client library. Small verification demos in a few languages are on the backlog.
:::

## Post-quantum readiness

:::note[Proposed]
This section is a placeholder. A design note on post-quantum readiness is in preparation. It will cover which keys and exchanges would change first, and which of the choices above (P-384 everywhere, the hardware keys, the cloud-federation exception) it affects. Nothing on this page depends on it yet.
:::
