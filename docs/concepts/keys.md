---
title: Keys
description: "Which key backs which purpose: key modes, topologies, the key rule, touch and PIN policy, algorithms, KMS and YubiKey layouts, and how an intermediate is certified."
status: target
sidebar:
  order: 4
---

sluis holds five kinds of authority key (token signing, the X.509 CA, the SSH CA, the log keys of the audit and usage logs, and the mint grant), plus the keys that seal data at rest. Each [environment](/runtimes/boundaries/) has its own set: no key is shared between development, staging and production. This page says what backs each key in each installation, how it signs, and which algorithms it uses. The access rules, which actor may use which key, are enforced by `key-holder` or by the cloud's key policy, not by this page: see [Ports and drivers](/concepts/drivers/#defence-in-depth). The threats to the keys (another boundary's or purpose's key, a key made to sign the wrong content, a compromised `key-holder`) are T-8 to T-10 on the [threat model](/security/threat-model/#keys-and-signing).

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

## Topologies

Two topologies are the main patterns. The local installation is a third case: it uses a software key in CI, or the host's YubiKey on a workstation (see the key modes above). Every installation uses one of them, and the key mode above follows from it.

| Topology | Root | Intermediates | Where it applies |
|---|---|---|---|
| **KMS root, KMS intermediate per environment** | One KMS root | One KMS intermediate for each environment (development, staging, production) | AWS installations: [lambda](/runtimes/installations/#lambda) and [kubernetes](/runtimes/installations/#kubernetes) on EKS |
| **A YubiKey intermediate on each node** | Held by [ritueel](https://ritueel.id), not by sluis (see [Roots and certification](#roots-and-certification)) | One intermediate on each node's own YubiKey, generated on it, attested, and **certified by a root card for about 13 months**, then renewed every year | [boards](/runtimes/installations/#boards), and other installations on single-board computers |

Roots on YubiKeys with KMS intermediates are allowed, but they are not a main pattern.

**An existing KMS root comes first.** An estate that already runs a KMS root keeps it, and its intermediates, keys and issuers are per environment. Hardware roots, held by ritueel, join it later by **dual trust**: both are anchors in the trust products while the hardware roots take over, so no relying party has to switch on one day. Production's keys may start in the management account and move into the production account as a follow-up.

## The key rule

Which keys are certified, and which are only published, depends on how a verifier learns to trust them:

- **A key whose public half is trusted through the PKI chain is a hardware-held child key**, attested where possible, and certified through a root that ritueel holds. These are the CA intermediates and the OpenPGP signing subkeys. Each is certified in its own format: an X.509 intermediate by a certificate, an OpenPGP subkey by a binding signature from a primary (see [Roots and certification](#roots-and-certification)).
- **The SSH CA keys are anchors, not a chain.** The `ssh-user` and `ssh-host` keys are held for the `ssh-ca` actor. Hosts trust a list of SSH CA keys that ritueel publishes, signed by a root. `sshd` never checks a certificate on a CA key.
- **Token-signing and workload-identity issuer keys are hardware-held and published through JWKS.** They are the intermediates themselves, and each JWKS entry carries an **`x5c`** chain up to a root. A token carries no `x5c` header. A standard verifier ignores the member and gets the public key from the JWKS; a verifier that wants to can check the chain to a root it trusts.
- **Ephemeral keys are outside the rule.** Session keys, ECDH keys, wrapped data keys and derived keys are made for one use and are not certified or published as authority keys.

## Direct signing

Tokens are signed directly with the non-extractable key in every installation. A key is never used to certify a short-lived software key that signs in its place, so there are no derived signing keys and no burst key: a hardware key or KMS signs each token itself, and the signing key never leaves the device or the service.

The volume makes this affordable. On a production install, a measurement showed about 25,000 tokens in 7 days, with a peak of 0.8 per second. 95 % of them were token exchange. At that volume:

- **KMS** costs about $2.62 a month for signing every token.
- **A YubiKey** signs P-384 in about 120 ms on firmware 5.4.3, which is about 8 signatures per second, and in about 146 ms (6.8 per second) on firmware 5.8.0 (measured 2026-10-31). The measured peak is a tenth of that. A software P-384 key would sign about 200 per second per core, which is why the local installation can use one.

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

On the AWS installations there are six key purposes, in each environment. Aliases follow the naming of the actor that uses the key, `alias/sluis/<boundary>/<actor>/<purpose>` ([Actors](/concepts/actors/#naming)):

| Key | Spec | Purpose |
|---|---|---|
| Sealing and wrapping | One symmetric key, `ENCRYPT_DECRYPT` | Every sealing and wrapping need, separated by **encryption context** |
| Token signing | `ECC_NIST_P384`, `SIGN_VERIFY` | The signing ring |
| X.509 CA | `ECC_NIST_P384`, `SIGN_VERIFY` | The X.509 CA key of each trust domain, used by `ca` |
| SSH CA | `ECC_NIST_P384`, `SIGN_VERIFY` | `ssh-user` and `ssh-host`, one key each, used by `ssh-ca` |
| Audit log | `ECC_NIST_P384`, `SIGN_VERIFY` | Audit batch heads, one key per log, used only by that log's writer |
| Grant | `ECC_NIST_P384`, `SIGN_VERIFY` | Mint grants, held by `policy` |

In the KMS topology, the X.509 CA row is the intermediate of each environment. Its root is also a KMS key, held by ritueel. The SSH CA keys are separate keys of the `ssh-ca` actor, so one key never serves two purposes.

The symmetric key wraps the data key of each (boundary, record kind) for the state encryption ([Data](/concepts/data/#every-value-is-encrypted)), the cookie-sealing key of `session` and the sealed secrets, each with its own encryption context; the key policy lets each actor decrypt only under the contexts its manifest names. The enrolment key, which opens the HPKE-sealed enrolment requests of [machine identity](/concepts/machine-identity/#enrolment-is-encrypted-end-to-end), is held by KMS or `key-holder`, and only `exchange` may use it.

The reason for one key per authority purpose is how KMS works. A KMS key has one usage, and `Sign` takes no encryption context, so the only way to say "only this actor may sign with this key" is a key policy on a key that exists for that one purpose. With a key per purpose, each key policy names exactly one actor. Sealing is the exception, because `Encrypt` and `Decrypt` do take an encryption context, so one key serves every sealing need with a context per use.

## YubiKey layout

A YubiKey in PIV mode has 24 slots and **no symmetric keys**. The slot plan for a node card:

| Slot | Use |
|---|---|
| `9c` | Node intermediate and signing |
| `9d` | ECDH sealing and ring wrap |
| `82` | `ssh-user` CA, for `ssh-ca` |
| `83` | `ssh-host` CA, for `ssh-ca` |
| `84` | OpenPGP signing subkey, through `key-holder` (see [Roots and certification](#roots-and-certification)) |
| `85` to `95` | Retired sealing keys |
| `9e` | Avoided |

:::note[Proposed]
The slots for the two SSH CA keys (`82` and `83`) and for the release subkey (`84`) are a proposal. They are the first three free retired slots, which moves the retired sealing keys to `85` to `95`. The signing subkey is held in a PIV slot and reached through `key-holder`, like every other node key. The OpenPGP application is not used on a node.

The slots are a map with one reservation per purpose, and ranges kept for rotation and future use. Defaults ship in code and an installation may override them in configuration. `sluis config check`, `sluis key request` and node initialisation refuse a collision or a purpose with no slot. The exact numbers are settled when `key-holder` is built.
:::

There is one YubiKey per node. EUCLEAK, a side-channel flaw in the older firmware, is fixed from firmware 5.7. The root cards belong to ritueel, are kept apart, and never sit in a node.

Every actor reaches a node card only through **`key-holder`**, the one process that opens the card, exclusively, through `piv-go` ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)): one socket per caller, a peer-uid check on each call, and signing that understands the content of each purpose, so the `ca` purpose signs only a certificate within its constraints and the `ssh-ca` purposes only an OpenSSH certificate body. See [The mediating driver](/concepts/drivers/#the-mediating-driver).

### Touch and PIN policy

The policies are fixed when a key is generated, and they are visible in its attestation, so a verifier can check them. A PIV attestation chains through Yubico's published intermediates to **Yubico Attestation Root 1**, which sluis ships as the trust anchor of every attestation check (verified on firmware 5.8.0 on 2026-10-31).

| Card | Touch | PIN |
|---|---|---|
| Node cards | Never | Once, at unlock (see below) |

A node card is touch never so that a node signs unattended. That includes the back-signature of the node's OpenPGP subkey, which `key-holder` makes on the card.

**`key-holder` has exclusive access to its card.** It runs as a dedicated user, the card service accepts only that user, and no other software that talks to cards runs on the node. Measured on 2026-10-31 on a YubiKey 5 Nano (firmware 5.8.0): the PIN state belongs to the powered card, not to a connection, so while it is verified any local process can sign; a default disconnect by any client resets the card and wipes the PIN state; and three wrong PINs from any local user lock it. A Nano signs about 7 P-384 signatures a second, whatever the data size.

### Node PIN unlock

The `key-holder` on a node starts **locked**. A node card needs its PIN once, and the PIN is never stored in plaintext on disk.

- **A person or a tool unlocks it.** `sluis node unlock <node>` delivers the PIN over an authenticated channel: an SSH certificate from sluis's own CA, or the admin API.
- **A peer unlocks it.** An already-unlocked peer releases the node's encrypted PIN automatically. This covers a single-node reboot and a rolling update.
- **After a full power loss**, one manual unlock is needed. The first node then unlocks the others.
- **A wrong PIN is never retried automatically.** The card blocks after three wrong attempts and then needs the PUK, so `key-holder` stops at the first failure and reports it.

### Sealing without a symmetric key

Because the device has no symmetric key, sealing uses ECDH and HKDF in the style of HPKE with a P-384 key in slot `9d`:

1. To seal, the sealer generates an ephemeral P-384 key and performs ECDH against the slot's **public** key. It runs HKDF over the shared secret to derive an AEAD key, encrypts, and stores the ephemeral public key beside the ciphertext. No hardware is needed to seal.
2. To open, the device performs ECDH between slot `9d`'s private key and the stored ephemeral public key, and the same HKDF yields the same AEAD key.

The consequence is that sealing needs the YubiKey only when opening. Retired slots `85` to `95` keep older sealing keys alive after rotation, so what was sealed under them can still be opened.

## Roots and certification

sluis holds **no root**. The roots, the ceremonies that use them and every transition from one root to another belong to the separate product [ritueel](https://ritueel.id). sluis holds intermediates and signs with them every day. The two meet at standard formats ([ADR 0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)):

1. **Request.** `sluis key request` creates the key if it does not exist, on the node's YubiKey or in the key service, and writes a PKCS#10 request with a **proof of where the key lives**: the YubiKey attestation chain, or the KMS key's identifier and public key. For an OpenPGP signing subkey it writes the public subkey and its back-signature, which `key-holder` makes on the card.
2. **Certify.** At ritueel's ceremony the card of the year checks the proof, shows the fingerprint, and signs. This is the only step that touches a root.
3. **Install.** `sluis key install` checks that the chain leads to a root in the root list and that the certificate matches the key, then starts using it.
4. **Watch.** `sluis key status` shows each key and its expiry. An intermediate lasts about 13 months; sluis warns 60, 30 and 14 days before it ends, because the yearly renewal needs a person at ritueel's side.

Hosts hold a signed **root list** that ritueel produces, pin the public keys of the roots, and refuse an older list. `sluis trust sync` verifies it.

What sluis does not describe any more: how a root is made, why there are two roots, how the cards alternate, a lost root card, OpenPGP release keys and their keyring, and the ceremony. These are taught at [ritueel.id](https://ritueel.id). The accepted limit of the model is stated there too: whoever holds one root card and its PIN can sign until that root is revoked.

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

sluis v2.0 has **no FIPS mode and no certified build** (decided 2026-10-31, [ADR 0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)). Go builds without forcing FIPS mode, and the Rust components use current crates and rustls with no FIPS pin. sluis v2.0 makes no FIPS 140-3 claim.

What is kept is a rule: **every algorithm stays within the FIPS-approved set.** sluis signs with P-384 ECDSA, seals with AES-256-GCM using a nonce that the library generates, derives with HKDF-SHA384 and hashes with SHA-384. The exceptions are the ones named on this page: PS256, ES256 and RSA where an audience or a cloud requires them, and Ed25519 where a protocol requires it, such as NATS. A format that fixes its own algorithm, such as age for backups and export drops, is followed as it is.

The rule is there so that a **FIPS build variant** can be added later as a build option, when a customer needs it. That is likely after AWS-LC 4 is certified, which lifts the old pin that a certified Rust build would need today. In that variant, Go runs its FIPS 140-3 module and the Rust components use `aws-lc-rs` in FIPS mode ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)). The variant gets its own build design when it is added. BoringCrypto is not used.

## JOSE

:::note[Proposed]
JOSE is implemented with `github.com/lestrrat-go/jwx/v4` behind sluis's own `jose` package, so the library can be replaced without touching the actors. There is no Rust JOSE driver.

Tokens are standard JWS and JWT. Any language verifies them with its standard libraries against the published JWKS, so sluis needs no client library. Small verification demos in a few languages are on the backlog.
:::

## Post-quantum readiness

:::note[Proposed]
This section is a placeholder. A design note on post-quantum readiness is in preparation. It will cover which keys and exchanges would change first, and which of the choices above (P-384 everywhere, the hardware keys, the cloud-federation exception) it affects. Nothing on this page depends on it yet.
:::
