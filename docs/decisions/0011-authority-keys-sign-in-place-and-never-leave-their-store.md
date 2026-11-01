# 0011 — Authority keys sign in place and never leave their store

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).
Rewritten in place on 2026-10-31 (after spike S4, `key-holder` reaches a YubiKey through `piv-go` rather than PKCS#11 ([0060](/decisions/0060-the-language-follows-the-executable/)); PKCS#11 stays for the planned HSM drivers).
Rewritten in place on 2026-11-01 (the roots and their ceremonies moved to the separate product [ritueel](https://ritueel.id); this record keeps the keys sluis holds: intermediates, which sign directly and are certified through the hand-off of [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)).
Rewritten in place on 2026-11-01 (the maintainer approved a token-signing key that is separate from the CA key, and a static slot map for node cards).
Rewritten in place on 2026-11-01, later (decided by the maintainer on 2026-11-01: when a key is shared and when it is separate, a rotation rule, one log key for the audit log and the usage log, grants sealed instead of signed, and the compact slot map, which replaces the rotation pairs).

## Context

sluis holds three kinds of authority key: the token-signing key, the keys of the certificate authorities and the log key that signs the audit log and the usage log. A mint grant is not signed by a key of its own: it is sealed under the sealing key. sluis also holds the keys that seal secrets at rest, and it has to publish a key for each workload identity it issues. Every one of them is worth more than any single token it signs. Where each lives, who may use it and what happens when the hardware fails are one design, not several.

Three things shape it.

- **The volume is low.** A production installation of the previous line saw about 25,000 tokens in 7 days, a peak of 0.8 per second, and 95 % of them were token exchange. At that rate a key service or a hardware key can sign every token itself.
- **Hardware fails, is lost and is stolen.** A key that was copied to a spare is a key that has existed outside its device, so a lost board is replaced by a new key and a new certificate, never restored.
- **Installations differ.** Some run on AWS with a key service. Some run on single-board computers with a hardware key in each node. A workstation runs a hardware key or, for development, a software key.

sluis holds no root. The roots are made and kept by [ritueel](https://ritueel.id), which certifies the intermediates that this record describes; the hand-off is [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/). The algorithms are a separate decision ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)). The `keys` port and the rule that placement is derived are in [0002](/decisions/0002-actors-declare-ports-placement-is-derived/).

## Decision

### Signing and custody

- **An authority key signs in place.** The non-extractable key signs each digest itself, in the key service or on the card, and never leaves that store. There are no derived signing keys: a key is never used to certify a short-lived software key that signs in its place. There is no burst key.
- **There is one key for each signing purpose.** X.509 keys are separate for each trust domain (intermediates per trust domain: ADR 0031). The SSH user CA and the SSH host CA are separate keys. The token-signing key and the log key are separate keys, and the token-signing key is not the key of the intermediate that certifies it: a key that issues certificates never signs tokens. An exception of [0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/) has its own key too. A key that is used for two purposes can be abused through the weaker of them. The rule for sharing and separating keys is below.
- **On AWS, the layout follows how KMS works.** A symmetric key is shared per account or environment, with one alias for each use and its own encryption context, enforced by `kms:EncryptionContext:` conditions in the key policy. The key that wraps a backup archive is not the sealing key of the installation: it may live in a separate account, and only the restore role may decrypt with it (ADR 0035). One P-384 signing key exists for each enabled authority purpose, so each key policy names exactly one actor. KMS allows one usage for each key, and a signing call takes no encryption context, so this is the only way to say that only one actor may sign with one key. The layout is on the keys page.
- **On a YubiKey, there are no symmetric keys.** Sealing uses ECDH and HKDF in the style of HPKE, with a P-384 key held in a slot. Sealing needs no hardware. Opening does. Older sealing keys stay in spare slots after a rotation so that what they sealed can still be opened. The slot plan is on the keys page.
- **The `keys` port has `wrap` and `unwrap` beside `sign` and `public-key`** ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). A caller protects a data key with them without the key that protects it leaving the device. On AWS this is an encrypt call with a context that names the purpose. On a YubiKey it is ECDH and HKDF. A backup archive's data key is wrapped this way.
- **CA serial numbers are random and 128 bits long.** No serial ledger is needed.
- **sluis signs in place with the key of each purpose.** The key in the published key set is the token-signing key, a hardware key of its own that signs every token itself. It carries an `x5c` chain (below). A short-lived online key is a road not taken ([0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)).

### The key rule

- **Every key whose public half is trusted through the PKI chain is a hardware-held child key.** It is attested where the hardware can attest, and it is certified through a root that ritueel holds. These are the CA intermediates and the OpenPGP signing subkeys.
- **Token-signing and workload-issuer keys are hardware-held, separate from the CA key, and published through a JWKS.** The node's own intermediate certifies each of them with a **non-CA certificate that allows signing only**, so a key that signs tokens cannot issue a certificate. Each entry carries an `x5c` chain: the token certificate, the node intermediate, then up to the root. A standard client ignores it and fetches the key over TLS; a verifier that wants to can check the chain. The intermediate issues the token certificate on the node, without a ceremony. The profile of this certificate belongs with the leaf certificate profiles of the planned ADR 0067 ([issue #1073](https://github.com/truvity/sluis/issues/1073)); until that record is written, it is noted there as planned.
- **Ephemeral keys are outside the rule.** Session keys, ECDH keys, wrapped data keys and derived keys are not hardware-held, and are not certified.
- **SSH CA keys are anchors, not a chain.** The `ssh-user` and `ssh-host` keys are hardware-held in `key-holder`. Hosts trust a list of SSH CA keys that ritueel publishes, signed by a root; the list is the provenance. The SSH CA itself is decided in ADR 0013.

Hardware-held means a key service such as AWS KMS or a hardware key such as a YubiKey. KMS cannot attest where a key was born, so for it the checks are the key's description, its policy, the audit record of its creation and a match between the public key and the request.

### When keys are shared and when separate

Decided by the maintainer on 2026-11-01. A key is shared between two uses only when every one of these is equal:

- **the audience**: who verifies or decrypts;
- **the required lifetime**;
- **revocation coupling**: you would never need to disable one use without the other;
- **the callers**: the blast radius of a misuse;
- **the store itself separates the uses**: an encryption context together with key-policy conditions.

If any one differs, the uses get separate keys. This has these consequences:

- **Symmetric KMS keys are shared** per account or environment, with one alias for each use and its own encryption context, enforced by `kms:EncryptionContext:` conditions in the key policy. `Encrypt` and `Decrypt` take a context, so the store itself separates the uses.
- **Asymmetric signing keys are never shared** across purposes, environments or installations. `kms:Sign` takes no encryption context, and an alias only points to the same key, so the store cannot separate two uses of one signing key.
- **Tenants get no keys of their own.** Each tenant's data keys are wrapped under the sealing key with the tenant in the encryption context, and shredding a tenant deletes its data key. A per-tenant signing key is a possible later bring-your-own-key feature and is not part of 2.0.
- **Environments never share keys.** Development, staging and production each have their own.

### Rotation

Decided by the maintainer on 2026-11-01. The yearly cycle **re-signs**, and key material rotates only when it has to.

- **The yearly renewal makes a new certificate or binding over the same key.** An X.509 intermediate is re-signed at the yearly ceremony. An OpenPGP subkey's expiry is extended with a new binding over the same subkey. Neither renewal makes a new key.
- **Key material rotates only on an event, or at most every three years.** The events are a suspected compromise, a change of algorithm or store, and a change of custody. With no event, a signing key is replaced no later than three years after it was made. This follows the one to three years that NIST SP 800-57 gives as the cryptoperiod of a signing key. The forced rotation also keeps the rotation path exercised, so it is known to work before a compromise needs it.
- **Symmetric KMS keys use KMS automatic rotation.**
- **The log key rotates only on compromise.** A verifier has to keep the old public key to check old checkpoints, and a log is one continuous history.
- **A rotation makes the new key first and retires the old one after.** On a card the new key goes into a free spare slot while the old one finishes; in KMS it is a new key under the same alias.

### Grants are sealed, not signed

Decided by the maintainer on 2026-11-01. A mint grant is read only by sluis itself, so no outside verifier needs a signature it can check against a public key. `policy` seals the grant with authenticated encryption under the sealing key, with `purpose=grant` in the encryption context, and the actor that carries it out opens it under the same context. On AWS the key policy lets `policy` encrypt and the minters decrypt under that context only. On a YubiKey the sealing slot `9d` serves grants too. There is no grant-signing key. A grant sealed in one environment cannot be opened in another, because environments never share keys.

### Two main topologies

1. **A KMS root and a KMS intermediate for each environment** (development, staging and production), on AWS.
2. **A YubiKey intermediate on each node**, on the boards. Each node's intermediate is generated on its card, attested, and certified by a root card through ritueel. The certificate lasts about 13 months and is re-signed every year over the same key.

In both, the root is ritueel's: a KMS root, or root cards. A root on a YubiKey with KMS intermediates is allowed but is not a main pattern.

### Roots belong to ritueel

- **sluis holds no root.** It does not create, keep, rotate or revoke one, and it generates none, not even for development.
- **sluis asks for its intermediate to be certified, and installs what comes back.** The request carries proof of where the key lives. The formats and the commands (`sluis key request`, `install` and `status`) are in [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/).
- **A lost or stolen board is replaced, not restored.** A new key is born on a new card, a new certificate is requested, and ritueel revokes the old certificate. The card's PIN policy limits the exposure until then.
- **Root cards, their ceremonies, revocation readiness and root rotation are described by ritueel.**

### Touch and PIN

- **Node cards are touch never and PIN once.** A node signs unattended, including the back-signature of its OpenPGP subkey. The policies are fixed when the key is generated and are visible in its attestation.
- **A node starts locked.** The `key-holder` driver on a node does not have the PIN when it starts:
  - `sluis node unlock <node>` delivers the PIN over an authenticated channel, either an SSH certificate from sluis's own CA or the admin API.
  - A peer that is already unlocked releases the node's encrypted PIN automatically. That covers a reboot of one node and a rolling update.
  - After a full power loss one manual unlock is needed. The first node then unlocks the others.
  - The PIN is never stored in plaintext on disk.
  - `key-holder` never retries a wrong PIN automatically, because the card blocks after three attempts and then needs the PUK.
- **`key-holder` has exclusive access to its card.** It runs as a dedicated user, the card service accepts only that user, and no other software that talks to cards runs on the node. Measured 2026-10-31 on a YubiKey 5 Nano: while the PIN is verified any local process can sign, a default disconnect by any client resets the card, and wrong PINs from any local user block it. Exclusive access is a requirement of the node, not a convention.

### Floors in code

- **The software `keys` driver refuses to start when the environment is `production`.** It is allowed in development and staging, with a warning in staging. Configuration chooses only among the modes the floor allows. A development installation then cannot be mistaken for a production one by loading its configuration on the wrong node.

### Other key hardware

- **YubiHSM 2 and Nitrokey HSM 2 are planned PKCS#11 drivers of the `keys` port, not built now.** They are drivers for the keys sluis holds. `key-holder` reaches a YubiKey through `piv-go` ([0060](/decisions/0060-the-language-follows-the-executable/)), and such a device is added later as a PKCS#11 driver beside it. Their detail is in ADR 0052, which also covers more `keys` drivers and a post-quantum root.

### The release subkey

- **The node's OpenPGP signing subkey lives in a PIV slot, reached through `key-holder`'s PIV driver.** `key-holder` also makes the subkey's back-signature on the card, so binding the subkey to a primary needs nothing else from the board. The OpenPGP applet is not used on a node, and the primary is ritueel's. How a release is signed is in ADR 0030, and the signing API is in [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/).

### The card slot map

- **A documented, versioned map says which card slot serves which purpose.** It has one slot per purpose. A key made by a rotation that an event forces goes into a free spare slot while the old key finishes. The table is the default map, version 1, decided by the maintainer on 2026-11-01. It covers PIV slots `9a`, `9c`, `9d` and `9e` and the retired slots `82` to `95`, which in hex are 20 slots. With the four named slots that makes 24, and the map uses seven.

  | Slot | Purpose |
  |---|---|
  | `9c` | X.509 intermediate |
  | `9a` | Token signing |
  | `9d` | Sealing (ECDH, ring wrap), also used for grants |
  | `82` | `ssh-user` CA |
  | `83` | `ssh-host` CA |
  | `84` | Log key (audit and usage) |
  | `85` | OpenPGP release-signing subkey |
  | `86` to `95` | Spare: a key made by an event rotation goes in a free spare slot while the old one finishes. Older sealing keys stay here until their data keys are re-wrapped |
  | `9e` | Not used: the card-authentication slot signs without a PIN by default |

- **A purpose an installation does not enable gets no slot and no key.** An installation that issues no SSH certificates has no SSH CA key, and so on. `sluis config check` refuses only an enabled purpose that has no slot.
- **Keys are per environment boundary, not per tenant.** A card belongs to one boundary. The data keys of tenants and companies are wrapped by the sealing key, so they need no slots.
- **Defaults ship in code, and an installation may override them in declared configuration.** The map is for node cards. The slot map of root cards is ritueel's.
- **`sluis config check`, `sluis key request` and node initialisation refuse a collision and an enabled purpose with no slot.** The map records each slot's attestation in the registry.
- **The exact slot numbers are settled when `key-holder` is built** (issue #914). The table above is the default map and the keys page shows it.

### Related records

- **Roots and ceremonies** are held by [ritueel](https://ritueel.id). The hand-off, the commands and the expiry warning are in [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/).
- **Release signing**, including the OpenPGP subkeys and the keyring: ADR 0030.
- **SSH**: the SSH CA, certificates for people and machines and host certificates: ADR 0013.
- **Workload identity**: the issuer, its URL and its key per node: ADR 0012.
- **TPM 2.0** is outside this record. It is a backlog feature, in addition to the YubiKey and not instead of it.

## Consequences

- No key can be exported, so a backup never holds one. A restore on new hardware means new keys and a new certificate for each public key. The hand-off to ritueel and the yearly re-signing are what make that safe. Secrets that are not keys, such as a vendor-issued App key, are archived sealed, and the escrow of the sealing key never copies a key out of its device ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
- Signing speed is set by the store, not by software. A YubiKey signs P-384 at about 120 ms, or about 8 signatures a second, and the measured peak is a tenth of that. KMS costs about $2.62 a month at the measured volume. A software key is only for development and for CI.
- Every enabled signing purpose adds a key and a policy. In return a key policy names one actor and one use. Sharing symmetric keys per environment, and sealing grants and tenant data keys under the sealing key, keeps the number of keys small.
- The token-signing key adds a certificate to issue under each new intermediate, and a certificate to the chain in the key set. In return no key signs both certificates and tokens.
- A node card uses seven slots and keeps sixteen spare, for event rotations and for older sealing keys. `9e` stays empty.
- The roots, their trust anchors and their revocation are ritueel's. What reaches sluis is a signed root list, and a revoked intermediate has to reach every verifier ([0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)).
- Nodes that sign unattended depend on the PIN handling above. A full power loss needs a person. A wrong PIN is never retried, so a bad unlock cannot lock the card.
- Intermediates are certified for about 13 months, so each is re-signed once a year over the same key, and the re-signing needs a person at ritueel's side. Key material changes on an event or after three years at most.
- `sluis config check` has to know the key modes, the floor and the slot map. The tests need a virtual card or a software stand-in for the signer, and rehearsals on a real card.
- A key born in a slot of the map is attested there, so a reviewer can compare the registry with the map.
- The keys page, the hardware page and the release-signing page say the same as this record.

## Roads not taken

### Derived signing keys and a burst key

- Pros: a short-lived software key could sign many tokens without a call to the store for each.
- Cons: a second key type to issue, certify, rotate and revoke. A key that exists in memory and can be copied.
- Why not: the volume is low, and the store can sign every token. A burst key solves a problem that is not there.

### YubiHSM 2 and Nitrokey HSM 2 as drivers built now

- Pros: an installation that wants a server-attached device or an on-device audit log could have one, through a PKCS#11 driver.
- Cons: more drivers to write and keep conformant, before any installation asks for one, at about EUR 786 a device for the YubiHSM 2 (Yubico product page, checked 2026-10-29). The Nitrokey HSM 2 has no Ed25519 and no audit log, conflicting figures for its capacity and unclear attestation of single keys.
- Why not: they are planned as PKCS#11 drivers and not built now, added beside `key-holder`'s PIV driver when an installation asks for one (ADR 0052).

### The software key allowed in production, chosen by configuration only

- Pros: one switch in configuration, and no refusal in code.
- Cons: a development installation can be mistaken for a production one by loading its configuration on the wrong node, and a software key is then a production authority key.
- Why not: the floor is in code. The software driver refuses to start in production.

### The release subkey in the card's OpenPGP applet

- Pros: GnuPG can use the card directly.
- Cons: `key-holder` would need a second path to the card, besides PIV, and the applet has its own PIN and slot layout to manage on every node.
- Why not: a PIV slot reached through `key-holder` keeps one path to the card and one audit point. The applet is not used on a node. (the cons are inferred)

### An X.509 certificate on each SSH CA key, as provenance

- Pros: every authority key has a record in the chain, and `sshd` ignores the certificate, so nothing breaks.
- Cons: a second kind of record for a key that has no chain; hosts need the anchor list anyway.
- Why not: an SSH CA key is an anchor, and a list of CA keys signed by a root gives the same provenance.

### The intermediate signs the tokens itself

- Pros: one key fewer on the card, and the published key is the intermediate, so the chain in the key set is one certificate shorter. This was the earlier design.
- Cons: one key both issues certificates and signs tokens, so the key policy cannot say that the token signer may not mint a certificate, and a flaw in token handling reaches the CA. The CA key is used for every token, not only for certificates.
- Why not: the maintainer decided on 2026-11-01 that the token-signing key is its own hardware key, certified by the intermediate with a non-CA certificate for signing only. It still signs in place on the card, so this is not the short-lived key road below.

### A rotation slot for each signing purpose, used in turn

- Pros: every yearly renewal could make a new key while the old one still signs.
- Cons: the yearly renewal does not need a new key. Most purposes would hold a slot that is empty for years, and the pairs left no room for a purpose that comes later.
- Why not: the yearly cycle re-signs the same key, and key material rotates only on an event or after three years at most. A free spare slot serves the rare rotation, and the spare slots are shared by every purpose.

### Yearly rotation of key material

- Pros: a key is never older than a year.
- Cons: a hardware key is replaced every year with a ceremony for each node, which adds exposure and work, and the rotation path is exercised no better than by a rotation every three years.
- Why not: NIST SP 800-57 gives one to three years for a signing key, and re-signing the same key each year keeps the trust chain fresh. Key material rotates on an event, or at most every three years, which also keeps the rotation path exercised.

### A separate usage-log key

- Pros: the usage log could be given to a reader with a key of its own.
- Cons: a second key, a second policy and a second slot, for readers that verify the same writer's output.
- Why not: both logs have one audience, one lifetime and one writer. One log key per installation signs both, and each signed batch head names its log, which keeps the two apart.

### A grant-signing key

- Pros: a grant would carry a signature that a verifier checks against a public key.
- Cons: no outside party verifies a grant. A key for it adds a slot, a key policy and a rotation, and signing authenticates the grant but does not need to be readable by anyone else.
- Why not: grants are verified only by sluis itself, so they are sealed under the sealing key with `purpose=grant` in the encryption context.

### A signing or sealing key for each tenant

- Pros: a tenant's data and signatures could be cut off by deleting its key in the store.
- Cons: thousands of tenants would mean thousands of keys, policies and aliases, and KMS charges for each key.
- Why not: each tenant's data keys are wrapped under the sealing key with the tenant in the encryption context, and shredding a tenant deletes its data key. A per-tenant signing key is a possible later bring-your-own-key feature.

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
- Why not: the key policy is the control, and it needs one purpose for each signing key. Symmetric keys are the exception, because the store separates their uses by encryption context (see the sharing rule above).

### A key set with no `x5c`

- Pros: nothing extra to publish and refresh at the yearly re-signing.
- Cons: custody can be checked only in the audit log.
- Why not: a standard client ignores the member, and a verifier that wants the chain to the root can have it. The chain is in the key set and not in each token, which would add size and a second trust path.

### Store the node PIN on disk, or retry a wrong PIN automatically

- Pros: a node restarts with no person, and recovers from a bad unlock by itself.
- Cons: a PIN on disk is available to anyone who reads the disk. Three wrong attempts block the card and need the PUK.
- Why not: the PIN is delivered by a peer or by a person, never stored in plaintext, and never retried.

### Touch always on the node cards

- Pros: a person confirms every signature.
- Cons: a node cannot sign unattended. Token issuance and the back-signature of a subkey would wait for a person.
- Why not: nodes need to sign on their own, and their policy is visible in the attestation.

### A short-lived software key certified by the intermediate

- Pros: many signatures a second without a call to the store.
- Cons: a second key type to issue, certify, rotate and revoke; a key that exists in memory and can be copied.
- Why not: three boards sign about 21 P-384 signatures a second (measured 2026-10-31) and a key service more, far above the load. This is the derived-key road above, and [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/) records it again for the intermediates.

### Keep the roots and their ceremonies in this record

- Pros: one place for the whole chain.
- Cons: the rarest and most valuable keys sit beside the busiest code, and the teaching of root transitions is buried in a record about something else.
- Why not: roots are a separate product, [ritueel](https://ritueel.id), with its own decisions.
