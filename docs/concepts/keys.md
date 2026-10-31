---
title: Keys
description: "Which key backs which purpose: key modes, the two main topologies, the key rule, touch and PIN policy, algorithms, KMS and YubiKey layouts, the roots."
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
| **Two YubiKey roots, a YubiKey intermediate on each node** | Two roots, one on each of two offline YubiKeys (see [the roots](#the-roots-and-the-ceremony)) | One intermediate on each node's own YubiKey, **certified by both roots** and attested | [boards](/runtimes/installations/#boards), and other installations on single-board computers |

YubiKey roots with KMS intermediates are allowed, but they are not a main pattern.

**An existing KMS root comes first.** An estate that already runs a KMS root keeps it, and its intermediates, keys and issuers are per environment. YubiKey roots join it later by **dual trust**: both roots are anchors in the trust bundles while the YubiKey roots take over, so no relying party has to switch on one day. Production's keys may start in the management account and move into the production account as a follow-up.

## The key rule

Which keys are certified, and which are only published, depends on how a verifier learns to trust them:

- **A key whose public half is trusted through the PKI chain is a hardware-held child key**, attested where possible, and certified by the roots. These are the CA intermediates, the SSH CA keys (`ssh-user` and `ssh-host`, held for the `ssh-ca` actor) and the OpenPGP release subkeys. Each is certified in its own format: an X.509 intermediate by a certificate from each root, an OpenPGP subkey by a binding under each primary (see [OpenPGP release signing](#openpgp-release-signing)). For each SSH CA public key the roots issue an X.509 certificate with a dedicated "SSH CA" usage, as provenance evidence; `sshd` never checks it.
- **Token-signing and workload-identity issuer keys are hardware-held and published through JWKS.** They are not certified, and a token carries **no `x5c`** header. A verifier gets the public key from the JWKS and from nothing else.
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

In the KMS topology, the X.509 CA row is a root and one intermediate for each environment. The SSH CA keys are separate keys of the `ssh-ca` actor, so one key never serves two purposes.

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
| `84` | Release-signing subkey S<sub>i</sub>, through `key-holder` (see [OpenPGP release signing](#openpgp-release-signing)) |
| `85` to `95` | Retired sealing keys |
| `9e` | Avoided |

:::note[Proposed]
The slots for the two SSH CA keys (`82` and `83`) and for the release subkey (`84`) are a proposal. They are the first three free retired slots, which moves the retired sealing keys to `85` to `95`. The release subkey is held in a PIV slot and reached through `key-holder`, like every other node key. The OpenPGP application is used only on the root cards, for the primaries.

The slots are a map with one reservation per purpose, and ranges kept for rotation and future use. Defaults ship in code and an installation may override them in configuration. `sluis config check`, `sluis ceremony` and node initialisation refuse a collision or a purpose with no slot. The exact numbers are settled in ADR 0032.
:::

There is one YubiKey per node. EUCLEAK, a side-channel flaw in the older firmware, is fixed from firmware 5.7. The cards of the root pair (below) are separate from the node cards, and never sit in a node.

Every actor reaches a node card only through **`key-holder`**, the one process that opens the card, exclusively, through `piv-go` ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)): one socket per caller, a peer-uid check on each call, and signing that understands the content of each purpose, so the `ca` purpose signs only a certificate within its constraints and the `ssh-ca` purposes only an OpenSSH certificate body. See [The mediating driver](/concepts/drivers/#the-mediating-driver).

### Touch and PIN policy

The policies are fixed when a key is generated, and they are visible in its attestation, so a verifier can check them. A PIV attestation chains through Yubico's published intermediates to **Yubico Attestation Root 1**, which sluis ships as the trust anchor of every attestation check (verified on firmware 5.8.0 on 2026-10-31).

| Card | Touch | PIN |
|---|---|---|
| Root cards | Always | Always |
| Node cards | Never | Once, at unlock (see below) |

A node card is touch never so that a node signs unattended. That includes the back-signatures that an OpenPGP subkey needs during a ceremony.

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

## The roots and the ceremony

The trust of an installation starts at two **roots**, held on ordinary YubiKeys in PIV mode (not an HSM).

- **Two independent roots.** Each card of a pair generates **its own root** on the card, in a PIV slot, with the PIN policy *always* and the touch policy *always*. Neither root is a copy or a spare of the other.
- **Both roots are authoritative, and both are in every trust bundle.** A verifier trusts either. Cross-signing between the roots is optional.
- **No key is ever copied between cards.** A key that was imported into two cards has existed in software once and has lost its attestation; a key generated on the card keeps its PIV attestation, which proves that it was made on a genuine device with those policies.
- **Two roots exist to survive hardware failure of a YubiKey.** Loss and theft are handled by the lost-card procedure below. Both cards may be held by one person and stored apart, or card B may be held by a second person, who also acts as the second person in a ceremony.
- **Each intermediate is certified by both roots.** There is one intermediate key and two certificates, one from each card's root. Losing either card breaks no chain.

### Why two roots

Having several roots, and several certificates for one intermediate, is ordinary practice. Checked on 2026-10-29:

- **Public CAs run several roots at once.** [Let's Encrypt](https://letsencrypt.org/certificates/) operates ISRG Root X1 (RSA 4096) and ISRG Root X2 (ECDSA P-384), and X2 also has a certificate cross-signed by X1.
- **One intermediate can have two certificates.** Several Let's Encrypt intermediates had one certificate from ISRG Root X1 and a second, cross-signed one from an older root, so clients that trusted either root could build a chain. sluis does the same: one intermediate key, two certificates.
- **Roots are replaced with old and new trusted together.** [RFC 4210, section 4.4](https://datatracker.ietf.org/doc/html/rfc4210) describes a root key update in which the old key stays in use until relying parties have the new one.
- **Purposes are kept apart.** The [Chrome Root Program policy](https://googlechrome.github.io/chromerootprogram/crp/policy), section 1.3.2, wants TLS server hierarchies dedicated to that one purpose. That rule is for the web PKI, but it is the reason the sluis roots certify only sluis intermediates and nothing else.

What is less common is two roots that are **both permanently active and back each other up**. The conventional design keeps one root key and copies it between hardware security modules, with the wrapping key split among several custodians (an M-of-N scheme). For example, the [YubiHSM 2 backup](https://docs.yubico.com/hardware/yubihsm-2/hsm-2-user-guide/hsm2-backup-restore.html) exports a key under a wrap key and imports it into a second device, and the wrap key can be split into shares. That design has one anchor to publish, but the copy is a key that has left the device it was born in, so the second device cannot attest where the key came from.

sluis takes the other branch for two reasons: no key is ever copied, so each card keeps its PIV attestation, and losing a card breaks no chain, because the other root already certifies every intermediate. The price is two anchors to publish, document and watch, and a few client effects:

- **Both roots publish a CRL (proposed; revocation state is open in ADR 0031).** A client may build the path through either root, so it may ask for either list. A root that has no revocations would still publish an empty, current CRL.
- **A server presents one chain.** The chain holds the leaf and one certificate of the intermediate, issued by one root. Do not put both certificates of the intermediate in the same chain.
- **Path building differs between libraries.** The OpenSSL command line takes the first intermediate whose name matches and does not try the other one if that path fails; Go backtracks and tries both. The same family of bug is why OpenSSL 1.0.x broke when [a cross-signed root expired in 2021](https://openssl-library.org/post/2021-09-13-letsencryptrootcertexpire/): it preferred the chain it was sent over a valid path through its own trust store. A verifier that uses the OpenSSL command line should pass the chain it wants explicitly, or the root that matches it.

### A lost root card

If a root card is lost, the procedure is:

1. **Remove that root from every trust bundle**, through the trust registry. Removing a root from the bundles is the only way to revoke it.
2. **Generate a new root** on a fresh card.
3. **Re-issue any intermediate that chained only to the lost root.** Because every intermediate is certified by both roots, none should exist.
4. Until the first step is done, the lost card's PIN and touch policy limit the exposure.

### OpenPGP release signing

The same rule holds for releases, with one difference: the two roots share one signature.

- **Two primaries, one on each root card.** Each primary is generated on its card. Neither is a copy of the other.
- **Each node holds one signing subkey S<sub>i</sub>, bound under both primaries, in a PIV slot (`84`, proposed) that `key-holder` reaches.** The OpenPGP application is used only on the root cards, for the primaries. The subkey carries a binding signature and a back-signature under each primary. Every release gets **one signature**, and it verifies with certificate A, certificate B or both.
- **Creating or rotating S<sub>i</sub> needs both root cards**, because each primary signs the binding and S<sub>i</sub> signs a back-signature to each.
- **A subkey is revoked under both primaries.** `gpg` and `gpgv` ignore a revocation made under only one.
- **Expiry.** The primaries expire in 2 years and are extended at each yearly drill. The subkeys last 2 years and are staggered, so they do not all expire together.
- **Target.** sluis signs as a service. CI proves its GitHub OIDC identity, and a node card signs. Sigstore provenance is added alongside.
- **Until a v2 signing installation exists**, releases are signed offline, directly with the root cards' primaries, using `sluis ceremony`, with a touch for each release. There is no dedicated release card.

Tested on 2026-10-29 with `gpg` 2.4.8, `gpgv`, `sqv` 1.5.0, apt 2.6 and 3.0, and rpm 6.0 (rpm-sequoia), using keyrings A, B and A+B. See [Release signing](/distribution/release-signing/#why-two-primaries-and-one-shared-subkey) for the results and the reasons.

Tested on two cards on 2026-10-31 (spike S8, [ADR 0060](/decisions/0060-the-language-follows-the-executable/)): an ECDSA P-384 primary generated on each of two YubiKey 5 test cards (firmware 5.8.0, OpenPGP applet 3.4), one P-384 signing subkey bound under both, and signatures by the subkey and by each primary. `gpg` and `gpgv` 2.2.27, 2.2.40 and 2.4.7, `sqv` 1.1.0 and 1.3.0, apt 2.4, 2.6 and 3.0, and rpm 6.0.2 with rpm-sequoia 1.10.2 accepted every signature with certificate A, B or both. The subkey in that run was a software stand-in for a node's PIV-held S<sub>i</sub>.

### The ceremony

Creating the roots, signing an intermediate and rotating a root are done in a **ceremony**: a scripted, reviewable procedure with a printed template and a hash that two people can compare, and a verification that is repeated on another machine. The `sluis pki` and `sluis ceremony` commands are a feature to plan. Their shape is not decided, apart from the card helper below. These are inputs to ADR 0032, which settles the ceremonies.

The OpenPGP part of `sluis ceremony` is decided ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)): a Rust helper on `openpgp-card` and rPGP, run by the CLI from `/usr/libexec/sluis/`. Spike S8 ran it end to end on two cards on 2026-10-31. The ceremony is not built on `gpg`: neither `gpg` 2.2.27 nor 2.4.7 can bind an existing P-384 key as a signing subkey, and `gpg`'s card key generation adds encryption and authentication subkeys. The release certificate holds the primary, its user ID and the signing subkeys, and no encryption subkey.

What S8 leaves open, for the maintainer to decide:

- **Where the back-signature comes from** when S<sub>i</sub> lives in a node's PIV slot. `key-holder` would have to sign a primary-key binding, and that purpose is not on its list.
- **How the binding splits when card B is held by a second person elsewhere.** One process cannot hold both cards, so the back-signatures, the binding by each card and the merge become separate steps.
- **The helper's contract with `sluis ceremony`:** arguments and files, or JSON on its standard streams, and how the PIN reaches the helper.
- **Attestation:** whether a ceremony requires a YubiKey OpenPGP attestation of each generated primary.
- **A missed touch** comes back from the card after about 15 s as a generic security status, not as a touch timeout; whether the helper reports it as one.

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
