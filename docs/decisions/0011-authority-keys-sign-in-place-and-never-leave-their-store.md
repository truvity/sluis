# 0011 — Authority keys sign in place and never leave their store

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).

## Context

sluis holds four authority keys: the token-signing key, the keys of the certificate authorities, the audit log key and the key that signs mint grants. It also holds the keys that seal secrets at rest, and it has to publish a key for each workload identity it issues. Every one of them is worth more than any single token it signs. Where each lives, who may use it and what happens when the hardware fails are one design, not several.

Three things shape it.

- **The volume is low.** A production installation of the previous line saw about 25,000 tokens in 7 days, a peak of 0.8 per second, and 95 % of them were token exchange. At that rate a key service or a hardware key can sign every token itself.
- **Hardware fails, is lost and is stolen.** A root that sits on one card is a single point of failure, and a root that was copied to a second card is a key that has existed outside its device.
- **Installations differ.** Some run on AWS with a key service. Some run on single-board computers with a hardware key in each node. A workstation runs a hardware key or, for development, a software key.

The algorithms are a separate decision ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)). The `keys` port and the rule that placement is derived are in [0002](/decisions/0002-actors-declare-ports-placement-is-derived/).

## Decision

### Signing and custody

- **An authority key signs in place.** The non-extractable key signs each digest itself, in the key service or on the card, and never leaves that store. There are no derived signing keys: a key is never used to certify a short-lived software key that signs in its place. There is no burst key.
- **There is one key for each purpose.** X.509 keys are separate for each trust domain (intermediates per trust domain: ADR 0031). The SSH user CA and the SSH host CA are separate keys. The token-signing key, the audit log key and the grant key are separate keys. An exception of [0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/) has its own key too. A key that is used for two purposes can be abused through the weaker of them.
- **On AWS, the layout follows how KMS works.** One symmetric key serves every sealing need inside the installation, separated by an encryption context. The key that wraps a backup archive is not that key: it may live in a separate account, and only the restore role may decrypt with it (ADR 0035). One P-384 signing key exists for each authority purpose, so each key policy names exactly one actor. KMS allows one usage for each key, and a signing call takes no encryption context, so this is the only way to say that only one actor may sign with one key.
- **On a YubiKey, there are no symmetric keys.** Sealing uses ECDH and HKDF in the style of HPKE, with a P-384 key held in a slot. Sealing needs no hardware. Opening does. Older sealing keys stay in retired slots after a rotation so that what they sealed can still be opened. The slot plan is on the keys page.
- **The `keys` port has `wrap` and `unwrap` beside `sign` and `public-key`** ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). A caller protects a data key with them without the key that protects it leaving the device. On AWS this is an encrypt call with a context that names the purpose. On a YubiKey it is ECDH and HKDF. A backup archive's data key is wrapped this way.
- **CA serial numbers are random and 128 bits long.** No serial ledger is needed.

### The key rule

- **Every key whose public half is trusted through the PKI chain is a hardware-held child key.** It is attested where the hardware can attest, and it is certified by the roots. These are the CA intermediates, the SSH user and host CA keys, and the OpenPGP release subkeys.
- **Token-signing and workload-issuer keys are hardware-held and published through a JWKS.** They are not certified. There is no `x5c`.
- **Ephemeral keys are outside the rule.** Session keys, ECDH keys, wrapped data keys and derived keys are not hardware-held, and are not certified.
- **SSH CA keys are certified by the roots.** The roots issue an X.509 certificate with a dedicated "SSH CA" usage for each SSH CA public key (`ssh-user` and `ssh-host`). The certificate is provenance evidence only: `sshd` never checks it. It keeps the rule above true for SSH. The SSH CA itself is decided in ADR 0013.

Hardware-held means a key service such as AWS KMS or a hardware key such as a YubiKey. KMS cannot attest where a key was born, so for it the checks are the key's description, its policy, the audit record of its creation and a match between the public key and the request.

### Two main topologies

1. **A KMS root and a KMS intermediate for each environment** (development, staging and production), on AWS.
2. **Two independent YubiKey roots and a YubiKey intermediate on each node**, on the boards. Each node's intermediate is certified by both roots and attested.

A YubiKey root with KMS intermediates is allowed but is not a main pattern.

### Roots on ordinary YubiKeys

- **Roots exist to survive hardware failure.** Loss and theft are handled by the runbook below. The two cards may be with one person, stored apart, or card B may be with a remote second person, who also acts as the second person of a ceremony.
- **Each card of a pair generates its own independent root, on the card.** The slot is PIV, the PIN policy is always and the touch policy is always. Both roots are authoritative, and both are in every trust bundle. Cross-signing is optional. No key is ever copied between cards, so each keeps its attestation. A second card is not a spare of the first.
- **Each intermediate is certified by both roots.** There is one intermediate key and two certificates, one from each root. Losing either card breaks no chain.
- **The runbook for a lost root card** is:
  1. Remove that root from every trust bundle through the trust registry. That is the only way to revoke a root.
  2. Generate a new root on a fresh card.
  3. Re-issue any intermediate that chained only to the lost root. Because every intermediate has two certificates, none should exist.
  4. Until the first step is done, the lost card's PIN and touch policy limit the exposure.
- **The roots are ordinary YubiKeys in PIV mode, not an HSM.**

### Touch and PIN

- **Root cards are touch always.** A person touches the card for every signature.
- **Node cards are touch never and PIN once.** A node signs unattended, including the back-signatures made during a ceremony. The policies are fixed when the key is generated and are visible in its attestation.
- **A node starts locked.** The `key-holder` driver on a node does not have the PIN when it starts:
  - `sluis node unlock <node>` delivers the PIN over an authenticated channel, either an SSH certificate from sluis's own CA or the admin API.
  - A peer that is already unlocked releases the node's encrypted PIN automatically. That covers a reboot of one node and a rolling update.
  - After a full power loss one manual unlock is needed. The first node then unlocks the others.
  - The PIN is never stored in plaintext on disk.
  - `key-holder` never retries a wrong PIN automatically, because the card blocks after three attempts and then needs the PUK.

### Floors in code

- **The software `keys` driver refuses to start when the environment is `production`.** It is allowed in development and staging, with a warning in staging. Configuration chooses only among the modes the floor allows. A development installation then cannot be mistaken for a production one by loading its configuration on the wrong node.

### Other key hardware

- **YubiHSM 2 and Nitrokey HSM 2 are planned PKCS#11 drivers of the `keys` port, not built now.** They are drivers, not root hardware: the roots stay on ordinary YubiKeys. `key-holder` is built on PKCS#11, so adding such a device later is cheap. Their detail is in ADR 0052, which also covers more `keys` drivers and a post-quantum root.

### The release subkey

- **The node's OpenPGP release subkey lives in a PIV slot, reached through `key-holder` over PKCS#11.** The OpenPGP applet is used only on the root cards, for the primaries. How a release is signed is in ADR 0030.

### The card slot map

- **A documented, versioned map says which card slot serves which purpose.** It has a reservation per purpose, and ranges kept for rotation and future use. It covers PIV slots `9a`, `9c`, `9d` and `9e`, the retired slots `82` to `95`, and the OpenPGP applet keys on the root cards.
- **Defaults ship in code, and an installation may override them in declared configuration.** There is a default map for each card role: root card and node card.
- **`sluis config check`, `sluis ceremony` and node initialisation refuse a collision and a purpose with no slot.** The map records each slot's attestation in the registry.
- **The exact slot numbers are decided in ADR 0032**, with the ceremonies. The keys page shows the proposed numbers.

### Related records

- **The detail of the ceremonies**: the steps, the commands, the attestation checks, the machine, the transcripts and the yearly drill, and the `sluis ceremony` command, are in ADR 0032. This record fixes the topology and the rules they follow.
- **Release signing**, including the OpenPGP primaries and subkeys: ADR 0030.
- **SSH**: the SSH CA, certificates for people and machines and host certificates: ADR 0013.
- **Workload identity**: the issuer, its URL and its key per node: ADR 0012.
- **TPM 2.0** is outside this record. It is a backlog feature, in addition to the YubiKey and not instead of it.

## Consequences

- No key can be exported, so a backup never holds one. A restore on new hardware means new keys and a new certificate for each public key. The roots and the runbook are what make that safe. Secrets that are not keys, such as a vendor-issued App key, are archived sealed, and the escrow of the sealing key never copies a key out of its device ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
- Signing speed is set by the store, not by software. A YubiKey signs P-384 at about 120 ms, or about 8 signatures a second, and the measured peak is a tenth of that. KMS costs about $2.62 a month at the measured volume. A software key is only for development and for CI.
- Every purpose adds a key, a policy and a rotation schedule. In return a key policy names one actor and one use.
- Two roots mean two trust anchors to publish, document and watch. Clients see two certificates for one intermediate, and some path builders do not try the second. A server presents one chain. These effects are documented on the keys page. Revocation state is open and belongs to ADR 0031.
- A lost card is not a revocation list entry. The trust registry is the mechanism, and its bundles have to reach every verifier.
- Nodes that sign unattended depend on the PIN handling above. A full power loss needs a person. A wrong PIN is never retried, so a bad unlock cannot lock the card.
- A root card is touched for every signature, so a root signs only in a ceremony, and a ceremony needs a card holder.
- `sluis config check` has to know the key modes, the floor and the slot map. The tests need a virtual card or a software stand-in for the signer, and rehearsals on a real card.
- A key born in a slot of the map is attested there, so a reviewer can compare the registry with the map.
- The keys page, the hardware page and the release-signing page say the same as this record.

## Roads not taken

### Derived signing keys and a burst key

- Pros: a short-lived software key could sign many tokens without a call to the store for each.
- Cons: a second key type to issue, certify, rotate and revoke. A key that exists in memory and can be copied.
- Why not: the volume is low, and the store can sign every token. A burst key solves a problem that is not there.

### One root on one card, with a spare

- Pros: one anchor to publish. The conventional design.
- Cons: the spare is a second card that holds either a copy of the key, which is the imported key below, or nothing a verifier trusts. Losing the one root card stops all signing. (inferred)
- Why not: it does not survive hardware failure, and the cheap way to make it survive is to copy the key. (inferred)

### One key generated off the card and imported into both cards

- Pros: one root and one anchor, and two cards that sign. (inferred)
- Cons: the key existed in software before it reached the cards, and no card can attest where it was born. No ceremony can prove otherwise, because a ceremony shows what people did and not where a key was made.
- Why not: it gives up attestation, which is the property that makes a root on a card worth having. (inferred)

### A software key wrapped by both cards

- Pros: any algorithm, including ones the cards lack. Software speed. (inferred)
- Cons: the key is in memory at every signing and has no attestation. (inferred)
- Why not (inferred): the key is no longer held by hardware. It stays a candidate for a future root with a post-quantum algorithm on an air-gapped machine, because no hardware listed offers one.

### A software key split 2-of-2 between the cards

- Pros: both cards are needed to sign. (inferred)
- Cons: the key is reassembled in memory at every signing and has no attestation. Both cards must be present, so losing either one stops all signing. That is the opposite of surviving a failure. (inferred)
- Why not: it fails the purpose of the pair. (inferred)

### An HSM pair with a wrapped export between the devices

Two YubiHSM 2 devices. The pros and cons are from the root-hardware research.

- Pros: a hash-chained audit log on the device, much more capacity, a device that can stay attached to a server, and attestation of keys made on the device.
- Cons: about EUR 786 for each device against an ordinary YubiKey (Yubico product page, checked 2026-10-29), a second device family to operate, and a key that is copied between devices as a wrapped export. A copy has no proof of where it was born. GnuPG cannot use it as an OpenPGP card.
- Why not: it reopens the decision to use ordinary YubiKeys and gives up the property that nothing is copied. (inferred)

### YubiHSM 2 and Nitrokey HSM 2 as root hardware, or as drivers built now

- Pros: an installation that wants a server-attached device or an on-device audit log could have one, through a PKCS#11 driver, without any change to the roots.
- Cons: more drivers to write and keep conformant, before any installation asks for one. The Nitrokey HSM 2 has no Ed25519 and no audit log, conflicting figures for its capacity and unclear attestation of single keys.
- Why not: they are not root hardware, because the roots stay on ordinary YubiKeys. They are planned as PKCS#11 drivers and not built now. `key-holder` is built on PKCS#11, so adding them later is cheap (ADR 0052).

### The software key allowed in production, chosen by configuration only

- Pros: one switch in configuration, and no refusal in code.
- Cons: a development installation can be mistaken for a production one by loading its configuration on the wrong node, and a software key is then a production authority key.
- Why not: the floor is in code. The software driver refuses to start in production.

### The release subkey in the card's OpenPGP applet

- Pros: GnuPG can use the card directly.
- Cons: `key-holder` would need a second path to the card, besides PKCS#11, and the applet has its own PIN and slot layout to manage on every node.
- Why not: a PIV slot reached through `key-holder` keeps one path to the card and one audit point. The applet stays on the root cards, for the primaries. (the cons are inferred)

### SSH CA keys outside the PKI chain

- Pros: `sshd` does not check a certificate on a CA key, so nothing would break.
- Cons: the SSH CA keys would be the only authority keys with no provenance record. The key rule above would have an exception.
- Why not: the roots certify them with an X.509 certificate as provenance, and `sshd` ignores it.

### Fixed slot numbers with no map

- Pros: nothing to configure.
- Cons: two purposes can quietly take one slot, and no range is kept for rotation or for a purpose that comes later.
- Why not: a versioned map with a reservation per purpose, checked by the tools, catches a collision before a card is written. (the cons are inferred)

### AWS KMS as the only root

- Pros: one anchor. The key is never in plaintext outside KMS, and every use is recorded.
- Cons: a dependency on one provider. No hardware attestation of its own. Not available to an installation on the boards.
- Why not: it is kept as the first of the two main topologies, for installations on AWS.

### A key for several purposes

- Pros: fewer keys to hold and rotate.
- Cons: a key policy cannot say that only one actor may use one purpose. A weakness in one use becomes a weakness in all.
- Why not: the key policy is the control, and it needs one purpose for each key.

### Certify the token-signing keys and publish them with `x5c`

- Pros: a verifier could validate them through the PKI chain.
- Cons: token verifiers already fetch a published key set. A chain in every token adds size and a second trust path.
- Why not: the key set is the trust path for tokens. The PKI chain is the trust path for certificates.

### Store the node PIN on disk, or retry a wrong PIN automatically

- Pros: a node restarts with no person, and recovers from a bad unlock by itself.
- Cons: a PIN on disk is available to anyone who reads the disk. Three wrong attempts block the card and need the PUK.
- Why not: the PIN is delivered by a peer or by a person, never stored in plaintext, and never retried.

### Touch always on the node cards

- Pros: a person confirms every signature.
- Cons: a node cannot sign unattended. Token issuance and the back-signatures of a ceremony would wait for a person.
- Why not: the root cards carry the touch. Nodes need to sign on their own, and their policy is visible in the attestation.
