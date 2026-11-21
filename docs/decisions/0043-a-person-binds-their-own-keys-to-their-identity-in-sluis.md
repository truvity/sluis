# 0043 — A person binds their own keys to their identity in sluis

**Status:** Proposed; partly superseded by 0052
**Layer:** add-on
**Date:** 2026-11-12

Proposal: [issue #1496](https://github.com/truvity/sluis/issues/1496).

## Context

Nothing in this record is built or scheduled: the work is in the backlog ([roadmap](/roadmap/)). The design is stated in full so that the build can start from it.

A company can issue each staff member an OpenPGP identity whose signing keys are born on the person's own hardware card. Someone has to bind those keys to the person. A ceremony per person, run by a separate tool, was reviewed and rejected: the card, the attestation and the "person present" check all run on the client of the ceremony, so anyone who can assume the ceremony role can bind a software subkey, and detection is the only real control. It also needs an operator at the ceremony for every joiner and every lost card.

sluis already knows who a person is, signs the person in with a hardware key, and can certify keys online. So the person binds their own keys in sluis: sluis checks the proof on the server, records a binding, and acts on it. The staff keys are card-born, and sluis never holds the private part of a card's key.

Two further needs shape the design.

- **One stable key per person.** A person keeps one OpenPGP primary and one fingerprint for the life of their employment. A lost or second card changes the subkeys under that primary. It never adds an identity, and nothing that trusts the key needs to learn a new one.
- **The loss from one compromise stays small.** sluis manages the identities of a company's employees, and the compromise of one company-wide key is much worse than the compromise of one person. Every online key is therefore per person or per company, never one for everyone ([The security of the design](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-security-of-the-design)).

The binding is the producer of the keys that the GitHub key sync consumes ([0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/)).

## Decision

### What a person has

- **One OpenPGP primary per person per company.** key@ generates it and keeps it only sealed, as the durable kind `PersonPrimary`, written by key@ and sealed under the `person-key` data key of the person's tenant, one per company. It is ECDSA on P-384 with SHA-384. It is opened only inside key@, and the private part is never returned to any caller.
- **The primary only certifies.** Its key flags are 0x01 (certify) and nothing else. It signs no data. Its only uses are the signatures of the person's own certificate: the user ID self-signature, the bindings and revocations of the person's subkeys, and the renewal of its own expiry, each for a one-shot grant that key@ checks ([The grant for the primary](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-grant-for-the-primary)). Every use is audited. Making a primary needs no approval, because nothing trusts a primary until a subkey is bound under it.
- **The sealed primary is a named exception to "keys sign in place"** ([0006](/decisions/0006-keys-and-sealing/)): it is made in key@ and sealed like a data key. The exception adds a sixth tenant data key purpose, `person-key`.
- **The subkeys are born on the person's card** and bound under that primary. A lost card means a new subkey under the same primary.
- **The primary has one user ID: the person's company address,** the verified address in the company's domain that the directory holds as primary ([0019](/decisions/0019-person-and-upstream-link/)). A change of address is a new user ID, certified by sluis from the directory, and the old one is revoked. The risk of that path is that whoever can change a person's primary address in the directory can make their key vouch for another address. So the new address must be one that the company's directory owns, in the company's domain, and verified; the change is audited, alerts the operators, and is notified to the person at the old and the new address.
- **The primary is certified by the company's certification key** ([OpenPGP certification](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#openpgp-certification)). No certification key serves two companies.

### What a binding is

- **A binding is a record that a person's card holds the subkeys it names.** One card is one binding. It holds the person, the key identity, the kind, the class, the bound set, the attestation as received, the time created, the expiry, and a state: `pending` (the bundle arrived and is not yet approved), `active`, `ended` or `revoked`. It is the durable kind `KeyBinding`, personal, sealed under the tenant's `data` key, written only by `exchange`, and erased with the person ([0004](/decisions/0004-the-data-model/)).
- **The key identity is the SHA-256 hash of the public key material of a subkey, over exact bytes.** For an OpenPGP ECDSA key on P-384 the bytes are the algorithm octet `0x13`, the curve OID length octet `0x05`, the OID `2b 81 04 00 22` (1.3.132.0.34), and the MPI of the public point exactly as the key packet carries it: a two-octet bit count (`03 03`, 771) and the 97 octets of the uncompressed point. The hash is 64 hex digits, lower case. The key identity of a binding is that of its signing subkey. It is not the version 4 fingerprint, because the fingerprint covers the creation time, which the holder chooses: the same card key could be given two creation times and bound twice.
- **A key identity is unique across the boundary, in every state.** A key identity that belongs to any binding (active, pending, ended or revoked) is refused, and a revoked key is never bindable again.
- **One card has one active binding in a boundary.** A bundle whose card serial belongs to an active or pending binding of any tenant is refused until that binding ends. A person at two companies uses two cards.
- **sluis never holds the private part of a card's subkey.** The request carries public keys and proofs, and the card keeps the rest.
- **The kinds are OpenPGP and `sk-ssh` for staff, and JWK for customers.** X.509 and PIV keys, and OpenPGP keys for customers, come later. Each kind has its own proof format and its own action; the binding, the classes and the gate are the same for all.

### Classes

The classes say what sluis verified. They are not the passkey classes of [0017](/decisions/0017-identity-and-sign-in/) and do not reuse their names: a passkey of class `hardware` is unattested, while a key whose attestation verified but whose card is not on the list would be the same word with the opposite meaning.

| Class | How it is derived |
|---|---|
| `hardware-attested` | The attestation of every subkey verified to the one pinned vendor root, and the card's firmware and form factor are on the installation's allowlist |
| `hardware-unlisted` | The attestation verified to the pinned root, but the firmware or the form factor is not on the allowlist |
| `unattested` | The key came with no attestation. This is also a card key whose person sent no statement |

An attestation that is present and does not verify is refused, not downgraded: a forged statement is an incident. Policy decides what each class may do.

**Staff company signing needs `hardware-attested`.** Binding a subkey under the person's primary, putting it into the staff keyring and handing it to the GitHub key sync are all company signing. A key of another class gets none of them. That is the rule that staff signing keys are card-born, with no SSH-signing fallback. An `unattested` or `hardware-unlisted` binding is still recorded and audited; what it may do is for policy, and by default it does nothing.

**The allowlist is defined on what the statement carries.** Yubico's OpenPGP attestation statement has no model name. It carries the firmware version, the form factor, the serial number, the FIPS and CSPN status, the user-interaction flag and the signature counter, beside a fingerprint, a date and a cardholder name that are not to be trusted ([Yubico's OpenPGP attestation page](https://developers.yubico.com/PGP/Attestation.html)). The allowlist therefore names a minimum firmware, a set of form factors and, optionally, a required FIPS flag. It is installation config that a tenant can narrow and not widen. The firmware floor is 5.7.4, the first firmware signed under Yubico's new root. sluis pins that one root and no other. Older cards are out: their statements chain to the old root and are refused, and a person with an older card needs a newer one.

### The flow

1. **The person has a primary,** made when the person joins. `sluis key bind --primary` reads its public key, so the card can sign the back-signature over it.
2. **The person generates the subkeys on the card.** `sluis key bind` makes the signing subkey and, if wanted, an authentication subkey on the card, sets the PINs, and makes the card sign the **back-signature** (type 0x19) over the primary and the signing subkey, as OpenPGP requires of a signing subkey. A bundle with only a signing subkey is bound; one with an authentication subkey and no signing subkey is refused. Staff keys have no encryption subkey, and a bound set refuses one: a lost card would lose the mail it decrypted, and an encryption subkey under the primary would put a key that the company cannot replace into the company's keyring. The command writes the **bundle** in standard formats: `<name>.subkey.pgp` (the public subkeys and the back-signature), `<name>.proof.json` and, in step 4, `<name>.possession.sig`.

   `proof.json` is a JSON object with the members `bundle` (the number 1); `subkey_sha256` (the SHA-256 of the bytes of `<name>.subkey.pgp`, so the proof cannot be moved to another file); `primary` (the version 4 fingerprint of the person's primary); `key` (the key identity of the signing subkey); and `subkeys`, one object per subkey with `slot` (`sig` or `aut`), `key` (that subkey's key identity), `statement` (the attestation certificate, PEM) and `chain` (the intermediate certificates, PEM).
3. **The person signs in with a fresh hardware step-up that sluis verified itself,** within the age that policy sets. The boundary that holds the binding checks the assertion; an upstream claim never counts ([0017](/decisions/0017-identity-and-sign-in/)). `sluis key bind` then asks sluis for a challenge that names the tenant, the person, the boundary, the signing subkey, the bound set and the expiry asked for. Each card gets its own challenge ([The challenge](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-challenge)).
4. **The command signs the challenge with every subkey on the card** and writes one signature per subkey into `<name>.possession.sig`. The card needs its PIN and, by the touch policy set per key, a touch.
5. **`sluis key bind` sends the bundle, and `exchange` verifies it.** `exchange` checks every possession signature; the back-signature against the person's primary; the attestation chain of every subkey against the pinned root; that each attested key, each presented subkey and each key that made a possession signature is the same key; that all subkeys come from one card (equal serial); and the firmware and form factor against the allowlist. The class follows from the result. The binding is recorded as `pending`.
6. **The binding is approved after the bundle has arrived** ([The gate](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-gate)): by an operator, or by a vouching signature of the person's existing card. Nothing is bound before that.
7. **`exchange` asks `policy` for a grant, and key@ binds** ([The grant for the primary](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-grant-for-the-primary)). The binding becomes `active`, and sluis acts on it, per kind:

| Kind | What sluis does, for a `hardware-attested` binding |
|---|---|
| OpenPGP | key@ signs the binding of each subkey with the person's primary, with the back-signature embedded, the key flags set by key@ and an expiry of 1095 days at most ([OpenPGP details](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#openpgp-details)) |
| OpenPGP | The person's certificate (the primary with its user ID, certification and bound subkeys) goes into the staff keyring ([The staff keyring](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-staff-keyring)) |
| OpenPGP | The GitHub key sync uploads the certificate to the person's account ([0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/)) |
| `sk-ssh` | Later: issues SSH certificates for the key, by the rules of [0023](/decisions/0023-certificate-authorities/) |

**How the proofs fit.** Both the back-signature and the challenge signature are made by the card's signing subkey, and they prove different things. The back-signature says that this subkey agrees to be a subkey of this primary. It carries no server nonce, so it could be replayed from an old bundle. The challenge signature has the server's nonce, the person, the boundary and the time, so it proves that the card is present now, for this request. sluis requires both.

**Every subkey proves possession, not only the signing subkey.** OpenPGP requires a back-signature of a signing subkey only. An attestation certificate is public, so for an authentication subkey with no proof of its own someone could present another person's certificate and have a key of theirs bound. sluis therefore requires a challenge signature (type 0x02) from every subkey, and asks no back-signature of an authentication subkey.

### The challenge

The challenge is sluis's choice, so it is built to be signed safely, and it commits to what the person approves.

- **The bytes are eleven lines of UTF-8 text, each ending in a line feed, in this order, with nothing before, between or after:**

```text
sluis-key-bind-v1
action: <bind, vouch, end, extend or change>
tenant: <tenant id>
person: <subject>
boundary: <boundary id>
key: <key identity of the signing key, 64 hex digits>
target: <key identity the action is about, 64 hex digits>
set: <SHA-256 of the bound set, 64 hex digits>
expiry: <RFC 3339 UTC time, to the second, ending in Z, or none>
nonce: <256 random bits, 64 hex digits>
not-after: <RFC 3339 UTC time, to the second, ending in Z>
```

- **`set` commits to the subkeys.** It is the SHA-256 of the concatenation of the 32-octet key identities of all subkeys of the card, sorted ascending as octet strings. `expiry` is the expiry the person approves, or `none` for `end`. `not-after` is the end of the challenge's own life. A tap for one set and expiry cannot authorise another.
- **The actions.**

| Action | Signed by | `key` | `target` | `set` | `expiry` |
|---|---|---|---|---|---|
| `bind` | Every subkey of the new card | The new card's signing subkey | The same | The new card's set | The expiry asked for |
| `vouch` | The signing subkey of an existing `hardware-attested` binding of the person | The existing card's signing subkey | The new card's signing subkey | The new card's set | The expiry asked for |
| `end` | The signing subkey of another active `hardware-attested` binding | That other card's signing subkey | The ending card's signing subkey | The ending card's set | `none` |
| `extend` | Every subkey of the card | The card's signing subkey | The same | The stored set, unchanged | The new expiry |
| `change` | Every subkey of the card | The card's signing subkey | The same | The new set | The binding's expiry |

- **Adding a card takes two challenges, each with its own nonce:** a `bind` challenge signed by the new card, and a `vouch` challenge signed by an existing card that names the new card's signing subkey and set.
- **An extension cannot widen the set.** sluis refuses an `extend` whose `set` is not the stored one. A wider or narrower set is a `change`.
- **It lives 600 seconds and is used once,** and only by the session of the fresh hardware sign-in that asked for it.
- **The signature is an OpenPGP version 4 standalone signature, type 0x02,** made by the subkey itself. The challenge is the value of a **critical** hashed Notation Data subpacket named `bind-challenge@sluis.id`, flagged human-readable. A type 0x02 signature is "calculated identically to a signature over a zero-length binary document" (RFC 9580 section 5.2.1.3), so it signs no document and cannot pass as a signature over a file, a commit or a checksum list. A type 0x00 signature over a text would be hashed with the data it follows, and formats such as a `sha256sum -c` list tolerate extra lines, so a label inside such a text separates nothing. A verifier that does not know a critical notation must reject the signature.
- **What the label does not protect.** The card signs the digest it is handed and does not see the text, so the protection is on the host: the command refuses a challenge that lacks the label, has other lines or has a time in the past, shows the challenge before it asks for the PIN, and builds the type 0x02 packet itself. A hostile host can ask the card to sign any digest; the PIN and the touch are what stand between a card and an unwanted signature.

### OpenPGP certification

- **The chain has two links.** The company's certification key certifies the person's primary. The primary signs its own user ID and binds the card's subkeys.
- **One certification key per company, behind key@.** It is a purpose enabled on use, for a tenant that issues staff keys ([0006](/decisions/0006-keys-and-sealing/)): a key of the cloud key service (AWS KMS) on a cloud installation and a YubiKey on a board. `signer` makes the certification, in place and for a grant that `policy` sealed, and the key is never shown. It is ECDSA on P-384 with SHA-384, like the primary.
- **It certifies only the one address of the person**.
- **Lifetimes, in days.** A certification lasts 395 days (13 months) and no longer than the primary's own expiry, which sluis renews with it. Renewal begins 60 days before it ends.
- **What a verifier can check.** A verifier with a web of trust (`gpg`, or `sq-wot`) that trusts the company's certification key validates a person's primary through the certification and rejects an address outside the domain. A verifier that adds a trust signature of its own over that key should limit it to the company's domain with an anchored expression, which for `example.com` is `<[^>@]+@example\.com>$`: the whole address, one `@`, the exact domain, no subdomain. `gpgv`, `sqv`, apt and rpm do no web of trust. They accept a signature if the signing subkey is in the keyring they are given, bound to a primary that has not expired or been revoked there; the certification and its 13 months play no part for them. What they rely on is the keyring ([The staff keyring](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-staff-keyring)) and the expiry of the primary and of the subkey.

### Test vectors

The key identity, the challenge and its signature are testable data. They are planned to be kept as vectors in `tests/testdata/keybinding/`, beside the other key test data, and the conformance test reads them. They cover:

- the key identity of a throwaway P-384 test key: the public point, the exact bytes that are hashed, and the SHA-256 of them;
- the version 4 fingerprint of that key for two creation times, which differ while the key identity stays the same;
- one `bind` challenge of eleven lines, and its type 0x02 signature with the critical notation, made by the test key, parsed by `gpg --list-packets` and checked with a P-384 ECDSA verifier;
- the padding rule: the OpenPGP MPI of `r` or `s` drops a leading zero octet, so a verifier pads both back to 48 octets before it checks them. The vectors keep at least one signature whose `r` or `s` has a leading zero octet and one whose neither has.

### The bound set

A person's certificate has exactly these packets, and sluis stores, lists in the keyring and syncs to GitHub nothing outside them:

- the primary key packet, with its self-signature and its expiry;
- the one user ID, with the primary's self-signature and the company key's certification;
- each subkey that was attested at bind time, with the primary's binding signature (with the card's back-signature embedded) and any revocation of it.

Every other packet is refused or dropped. There is no encryption subkey in a staff certificate, and a bundle that carries one is refused. A card whose bundle carries a subkey with no verified attestation of its own is refused: sluis binds no software subkey.

### The staff keyring

The check of commit signers and the `gpgv` verification of releases both need a file, so this record owns it.

- **Form.** For each company sluis publishes three files: `staff.gpg`, a binary OpenPGP keyring of the certificates of the people who have an active `hardware-attested` binding; `staff.withdrawn`, a list of key identities and fingerprints that left the keyring, each with the reason (`ended`, `lapsed` or `revoked`) and the date; and `staff.manifest`, which names the digest of each, a sequence number that rises with every build, the time built and the time after which a verifier must not use it. A detached OpenPGP signature `staff.manifest.sig` covers the manifest.
- **One certificate per person.** A second card adds a subkey to the same certificate. The sync of [0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/) replaces the person's certificate on GitHub when a subkey, the user ID or an expiry changes.
- **Produced from the bindings only,** from the people with an active `hardware-attested` binding, with only their bound sets.
- **Signed by a key of its own.** The manifest is signed by the company's **keyring-signing key**, a separate purpose behind key@ with a separate key. It is not the certification key, so one compromise gives an attacker either the power to certify or the power to publish, not both.
- **A verifier checks the manifest signature,** the digests, the sequence number against the last it saw, and the expiry time. A keyring older than that time is unreadable, not empty (the same rule as for the sync's input in 0042).

### The grant for the primary

The gate has to hold where the primary signs, not only where the request arrives. A check that only the asking actor makes would let a buggy or compromised caller bind any subkey for anyone in the tenant while a reusable grant lives.

- **A primary-use grant is sealed by `policy` and names exactly one use:** the tenant, the person, the action (`bind`, `revoke`, `user-id` or `renew`), the key identity of the subkey it is about, the expiry that is approved, the digests of the evidence, and its own expiry, 60 seconds.
- **It is one-shot.** key@ writes a `SpentGrant` for it before it signs and refuses a second use ([0007](/decisions/0007-policy-and-sealed-grants/), [REQ-KEYS-225](/requirements/keys/#REQ-KEYS-225)). A grant for one subkey cannot bind another, and a grant for one person cannot serve another.
- **key@ builds the packets itself from the grant's values.** The caller sends no packet to be signed: key@ makes the binding signature, the revocation signature and the user ID signature, with the key flags, the creation time and the expiry that the grant allows, as the signing API of [0024](/decisions/0024-root-custody-and-the-signing-api/) signs only the content a grant names.
- **key@ re-verifies the evidence it can.** It checks the back-signature against the primary it holds and the possession signatures of every subkey against the challenge the grant names, and compares the challenge's fields with the grant. It does not hold the vendor root or the approvals, so `exchange` checks the attestation and the approval before it asks `policy` for the grant, and the grant carries their digests.
- **What this does not close.** A compromised `exchange` can ask `policy` for a grant with attestation or approval evidence that is false, and key@ cannot tell ([The security of the design](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-security-of-the-design)).

### OpenPGP details

- **Key flags are set by key@, never by the caller or the card.** The primary has 0x01 (certify) only. A signing subkey has 0x02 and must carry the embedded back-signature. An authentication subkey has 0x20. A subkey never has 0x01, 0x04 or 0x08. The `slot` of a subkey in `proof.json` must be the slot of its attestation: `sig` gives 0x02 and `aut` gives 0x20, and a mismatch is refused.
- **Key Expiration Time is an offset from a creation time the holder chooses,** in seconds after the subkey's creation time. key@ therefore sets it as the approved `expiry` minus the subkey's creation time, and bounds the creation time: not more than 5 minutes in the future, and not more than 30 days before the binding. A subkey made long ago, or dated in the future, is refused.
- **The primary's expiry is at most 395 days from its last renewal**, so a verifier that cannot read certifications still stops trusting a leaver's primary within that time. The conformance test checks that `gpgv`, `sqv` and apt with `signed-by` reject a signature by a valid subkey of an expired primary, and a signature by a revoked subkey.

### The gate

Attestation proves that a genuine card made a key. It does not prove whose card it is: anyone can buy one and attest a key of their own. Whose it is comes from three things: the fresh hardware sign-in of the person, approval of the bundle after it has arrived, and a proof from an existing card for every other change. This applies the enrolment rule of [0017](/decisions/0017-identity-and-sign-in/) to bindings. It holds for every class: a binding of class `unattested` or `hardware-unlisted` is gated like a `hardware-attested` one, because the gate is about who may add a key, not what the key proves.

- **A person who holds an active `hardware-attested` binding adds a card with a vouching signature:** a `vouch` challenge signed by that binding's signing subkey. A stolen session cannot add a key of its own.
- **A person with no active `hardware-attested` binding needs an operator's approval:** the first binding, and the re-binding of a person whose cards are all lost or ended, of any class. The approver holds the `identity-administration` grant, is a different person from the enrollee, and approves with a fresh `hardware-attested` assertion of their own. A directory fact is not an approval. The config kind `FirstAdmin` names the first operator of an installation, who has no approver.
- **The approval binds to what it approves.** It comes after the bundle has arrived, names the key identities of all subkeys and the card serial, expires within one hour and is used once. The operator compares the serial with the company's purchase register before approving, so a key reaches the keyring and the GitHub sync only after that comparison has been made. A binding that is `pending` is in neither.
- **Ending a binding is gated the same way.** It needs an `end` challenge signed by another active `hardware-attested` binding of the person, or an operator's approval as above. Without this rule a stolen session could end the person's good card and then ask for a re-binding. A declaration of compromise is the one exception, and it still needs a fresh hardware step-up: the person's own, or an operator's when the person has no usable card. A stolen session alone cannot revoke the good card for good.
- **Extending and changing need the card itself:** an `extend` or a `change` challenge signed by every subkey, with a fresh hardware step-up ([The subkey's own expiry](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-subkeys-own-expiry)).
- **Every bind, every refusal and every end is audited**. A new `hardware-attested` binding alerts the operators, with the fingerprint, the class and the card's serial, and notifies the person at their verified address, with the fingerprint.
- **The card's serial is kept for staff cards, and operators see it only.** The binding keeps the statement as received, because it must be re-verified when the allowlist or the pinned root changes, and the Yubico statement carries the card's serial. A passkey record keeps no serial, because a passkey is registered at many sites and a serial would let them follow a person. This statement goes only to the company's own sluis, for a card the company issued, and the serial lets an operator compare a new binding with the company's purchase register. It is never written into an audit record, a token, a log line or the keyring.

### The subkey's own expiry

Two clocks run, and they are not the same. The certification and the primary's expiry are sluis's, last 395 days and are renewed by sluis. The card's subkeys are the person's: the binding of each expires after 1095 days, and only the card can prove an extension.

- **The binding has an expiry** at most 1095 days after it is made, or the bound that policy sets. The binding signature that key@ makes carries it as the subkey's key expiration time ([OpenPGP details](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#openpgp-details)).
- **Two presentations are the same bound set** when the signing subkey and the other subkeys, by key material, are the same. Only the expiry may differ.
- **An extension is the same bound set with a later expiry, proved by the card.** The person signs in with a fresh hardware step-up and the command signs an `extend` challenge with every subkey; the challenge carries the stored set and the new expiry. sluis re-verifies the attestation of every subkey, checks that the set is the stored one, and key@ makes the new binding signature with the primary. The card does not make the signature; the primary does, and the card's proof is what permits it.
- **Anything else is a change.** A subkey added to or removed from the card's bound set needs a fresh hardware step-up, a `change` challenge signed by every subkey of the new set, the back-signature and the attestation of each new subkey. A change keeps the binding and its key identity.
- **A subkey that is not extended lapses with its binding.** sluis warns the person and the operators at 60, 30 and 14 days before the expiry. When it expires, the binding lapses, the subkey leaves the keyring and the GitHub sync replaces the person's certificate.
- **The GitHub sync re-uploads the extended certificate.** GitHub's copy changes only when the key is uploaded again, so the sync adds the new copy and removes the old one, by the update rule of 0042.

### Life of a binding

- **Renewal is automatic.** From 60 days before the certification ends, sluis re-certifies the person's primary for another 395 days and renews its expiry, with no ceremony and no person present, while the person has an active binding, is live in the directory and the primary is not revoked. If renewal has not succeeded, sluis alerts at 30 and 14 days before the end. Renewal re-checks the directory and the state, not the card: a card that was lost without anyone saying so stays bound until its binding ends.
- **A second card has its own binding,** and its own subkeys under the same primary. Ending one never ends the other.
- **A lost card:** the person (with the second card) or an operator ends that binding ([The gate](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-gate)), then the person binds a new card. sluis revokes the lost card's subkeys under the same primary; the fingerprint of the person does not change.
- **Compromise:** a person declares it, with a fresh hardware step-up ([The gate](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-gate)), as [0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/) describes. The binding becomes `revoked` at once, sluis revokes the subkeys with a subkey revocation signature of the primary, and the keys go on the withdrawn list of the next keyring. A compromised primary is a compromise of sluis's key@ and follows [The security of the design](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-security-of-the-design).
- **A leaver:** when the directory marks the person `left`, every binding ends. Renewal stops, so the certification and the primary's expiry lapse within 395 days; the certificate leaves the keyring; the GitHub sync removes it from the account. Nothing is revoked, and old commits stay Verified, by design.

### The security of the design

- **The blast radius is cut to the company.** The sealed primaries of a company are sealed under that company's own `person-key` data key, so a ciphertext of one company opens with no other company's data key. That is a logical separation: on a shared cloud key service all tenants' data keys are wrapped by the same boundary key. The certification key and the keyring-signing key are per company, and no online signing key serves two companies. A compromised card gives a few subkeys bound for one person, and a compromised company costs that company only.
- **What remains.** Anyone who controls key@ for a company can make a subkey of their own and bind it under any of that company's people, because key@ opens the sealed primaries. A compromised `exchange` is the other way in: `policy` seals a grant on its word, so it can obtain grants for false evidence. key@'s own checks (the grant is one-shot and names the subkey, key@ builds the packets, key@ re-verifies the signatures) stop it from binding any other subkey, but not from presenting a forged attestation and approval for a key that someone really holds. An attacker who takes the wrapping key and the ciphertext can do it quietly and offline, with no call to sluis and no audit record. The per-company seal protects one company from another's compromise; it does not protect a company from the compromise of its own boundary's wrapping key. A company that wants a harder wall puts itself on a boundary of its own, or keeps its wrapping keys in its own account ([0006](/decisions/0006-keys-and-sealing/)). On boards each company needs two PIV slots of its own, one for the certification key and one for the keyring-signing key.
- **The control against that is detection.** A binding made through sluis is audited, alerts the operators, notifies the person and shows up in the published keyring as a new subkey that anyone can diff. A subkey bound offline shows up in none of those, but a commit signed with it fails the later CI check of commit signers ([0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/)). The wrapping key's use shows in the cloud key service's own logs. Until that check exists, "cards only" is written policy.
- **sluis could still bind a rogue subkey through its own path.** Four things stand in the way: policy decides who may bind; a bind needs a fresh hardware sign-in that the boundary checked; a person with no hardware-attested binding needs an operator's approval; and every later change needs a proof from the person's existing card. None of these stops an insider who holds both an operator's hardware key and the policy to change them.
- **A compromised certification key stops certifying at once.** `signer` refuses to certify once the key is on the `KeyRevocation` of [0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/). The operator makes a new certification key, and sluis re-certifies the primary of every person of that company under it, from the records, with no card and no person. The keyring-signing key is separate, so this compromise does not let an attacker publish a keyring.
- **How verifiers learn.** `gpgv`, `sqv`, apt and rpm never look at the certification, so for them a compromised certification key changes nothing: the keyring is what they trust, and a bad key leaves it at the next build. A verifier on an old copy of the keyring trusts every key that was in it until the expiry of the subkey or of the primary (at most 395 days for a leaver's primary) or until it fetches the new one, which is why the manifest has a time after which it must not be used. GitHub does not read certifications at all: keys already uploaded stay, and the sync replaces or removes them.
- **Signature times are the signer's.** An OpenPGP signature carries a creation time that the signer chooses, so a key that has ended, lapsed or expired can still make a signature that claims an earlier time. Revocation and lapse stop new trust in a key; they do not make an old or backdated signature false.
- **The card's keys stay on the card.** A compromise of sluis lets an attacker bind a key of their own; it does not let them make a signature with a card's key.
- **A lapse is not a revocation.** A leaver's keys are accepted by a verifier that holds an old copy of the keyring until the expiry. A leaver who is a risk is a compromise, and is revoked.

### Customers

A customer may bind a key inside the company's tenant, for that company only, to sign or authorise data for the company's service. The customer signs in at the customer upstream ([0032](/decisions/0032-customer-identity-installations/)).

- **A customer's key is a JWK, and the customer signs a JWS.** The binding holds the key as a JSON Web Key (RFC 7517), and its key identity is the RFC 7638 thumbprint. The possession proof is a JWS (RFC 7515) over the same eleven challenge lines as the payload, signed by the key being bound. OpenPGP keys for customers come later.
- **What a customer signs is a JWS with a fixed shape.** The header has `typ` `sluis-customer-signature`, `kid` the key identity, and an `alg` from the allow-list; a `crit` header is refused unless it names an extension that sluis implements, and sluis implements none. The payload is a JSON object with `aud` (the service), `tenant` (so the tenant is in the signed bytes), `iat`, `exp` (at most 5 minutes after `iat`, or the shorter limit policy sets), a `jti` and the data the service defines. sluis keeps each `jti` until its `exp` and refuses a second use.
- **The algorithms are ES384 and ES256.** `none`, every `HS*` and every `oct` key are refused. This list differs from the P-384 default for staff keys on purpose: customers' keys come from browsers, phones and tokens, where P-256 is the common curve, and a customer key signs data for a service and certifies nothing.
- **sluis verifies, and writes the audit record.** The service sends the JWS; sluis verifies it against the customer's active binding in that tenant, and the audit record names the tenant, the key identity and the outcome. A binding that is ended, revoked or expired verifies nothing. sluis does not decide what the data means: the service defines the document.
- **A customer's key is a binding like any other, in the customer's tenant,** with a key identity, a class, an expiry and a state. A signature by it is accepted only for that tenant. It never certifies anything and never signs for the company: it is never certified by the company's key, never placed in the staff keyring and never synced to GitHub. A customer has no primary in sluis.
- **It is optional.** A customer holds a key only if the tenant's policy demands one for an action. A bound key is a signing key and never a sign-in factor, which agrees with [REQ-SESS-037](/requirements/sess/#REQ-SESS-037): no factor is enrolled for a person who signs in through a customer upstream.
- **Binding needs a fresh sign-in at the customer's upstream,** with the `acr` and `max_age` that policy asks for ([REQ-SESS-036](/requirements/sess/#REQ-SESS-036)). A customer has no hardware step-up to give.
- **Every binding of a customer is gated, and the gate is the customer's own.** The first binding is approved by the tenant's admin, and no tenant can waive that approval. The approver holds the `identity-administration` grant for the tenant, is never the customer, and approves with a fresh `hardware-attested` assertion of their own, after the bundle has arrived and naming the key. A tenant admin sees only their own customers. Every further add and every change is approved the same way, or vouched by a fresh signature of the customer's own active bound key, so a stolen customer session cannot add a key. A customer ends a lost key with a fresh re-authentication at their upstream; ending is the safe direction, because binding again needs the tenant admin.
- **The class rules are the same.** Policy decides what each class of customer key may do; most customer keys will be `unattested`.

### What ritueel owns

[ritueel](https://ritueel.id) owns the root of trust for roots only. It holds no staff keys, and it has no part in binding a person's key: there is no ritueel card helper, no bundle format shared with it and no ceremony over a company's certification key. The binding is entirely sluis's design.

### The attestation

Under this design the card holds no primary, so the attestation must cover the card's signing and authentication subkeys, and the card must be able to make the back-signature. Yubico's documentation says ([OpenPGP attestation](https://developers.yubico.com/PGP/Attestation.html), [`ykman openpgp keys attest`](https://docs.yubico.com/software/yubikey/tools/ykman/OpenPGP_Commands.html)) that the signature, decryption and authentication keys can be attested on firmware 5.2 and later, and only a key generated on the device can be ("imported" is not permitted). Firmware 5.7.4 and later is signed under Yubico's new root. The statement carries the key's fingerprint and generation date, and Yubico says not to trust them, because the Admin PIN can overwrite them. So sluis checks the public key of each statement against the public key of the subkey it attests, and ignores the fingerprint and date.

The conformance test runs on a YubiKey 5 with firmware 5.7.4 or later, and on an older one to see it refused. It covers:

- keys generated on the card are attested and verify to the pinned root; an imported key has no attestation; a statement whose public key differs from the subkey presented is refused; overwriting the fingerprint and date in the statement changes nothing; a real statement carries the fields the allowlist uses and no model name;
- the card makes the type 0x19 back-signature and the type 0x02 challenge signature from the signing slot, and a type 0x02 signature from the authentication slot, with the slot's PIN and touch policy; `gpg` and `sq` parse and accept the bound certificate; `gpg --verify` and `sqv` reject the challenge signature as a signature over any file;
- a bundle with a statement copied from another card, a bundle whose subkeys have different serials, and a bundle whose possession signature is by another key are each refused;
- the card signs a commit and a checksum file, and `gpgv`, `sqv` and `git verify-commit` accept them; a web-of-trust verifier accepts an address in the domain and rejects one outside it and one at a subdomain; `gpgv`, `sqv` and apt with `signed-by` reject a signature by a valid subkey of an expired primary and by a revoked subkey;
- the certificate is uploaded to a GitHub account (`POST /user/gpg_keys`), and a commit signed on the card shows Verified.

## Consequences

- A joiner binds a card in minutes and a lost card is replaced the same way, with no ceremony and no operator at a ceremony. A person keeps one fingerprint for as long as they work for the company. The yearly renewal of the certification stops being work, and the card's keys are extended by the person once in three years.
- sluis holds a sealed private key per employee, and key@ can act as that employee's primary. That is the price of one stable key, and [The security of the design](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/#the-security-of-the-design) states what it exposes and what stands against it. Every key that signs data stays on a card.
- sluis certifies on its own authority, for the company's domain. The weight moves to policy, the hardware sign-in and detection.
- A card that sluis cannot attest, or that is not on the allowlist, cannot sign for the company. An installation that wants to accept another form factor extends the allowlist; cards older than firmware 5.7.4 are not accepted.
- The staff keyring is a file that sluis produces, signs with a key of its own and publishes. The sync of 0042 reads the bindings, not the file; the CI check of commit signers and the `gpgv` check of releases read the file.
- The tenant gains a sixth data key purpose, `person-key`, and a company that issues staff keys gains a certification key and a keyring-signing key. [0006](/decisions/0006-keys-and-sealing/) says that a tenant has no authority key of its own by default; these two are the authority keys that a tenant has when it issues staff keys.
- The sign-in tiers of the hardware-attested passkey ([REQ-AUTHN-091](/requirements/authn/#REQ-AUTHN-091) to [REQ-AUTHN-100](/requirements/authn/#REQ-AUTHN-100)) are separate work and are not part of this record.
- The requirements are [REQ-KEYS-122](/requirements/keys/#REQ-KEYS-122) to [REQ-KEYS-143](/requirements/keys/#REQ-KEYS-143), [REQ-KEYS-146](/requirements/keys/#REQ-KEYS-146) to [REQ-KEYS-168](/requirements/keys/#REQ-KEYS-168) and [REQ-KEYS-171](/requirements/keys/#REQ-KEYS-171) to [REQ-KEYS-227](/requirements/keys/#REQ-KEYS-227).

## Roads not taken

- **Card-born primaries.** Each card would have its own primary and so its own fingerprint: a lost card is a new identity, and every upload and trust decision is redone.
- **One KMS key per person.** It works on AWS only, the company would hold everyone's signing key in a service that signs on request, and each key needs a yearly ceremony to certify; a sealed primary in key@ is cheaper and the same on a board.
- **One company-wide online key that certifies or binds for everyone.** Its compromise is every employee at once, which is the loss the per-person design exists to avoid.
- **One company primary with everyone's subkeys.** It brings GitHub attribution problems and churn in user IDs and revocations: one revocation touches every person.
- **A ceremony per person that binds each subkey.** The card and the "person present" check run on the client, so a ceremony role can bind a software subkey, and it needs an operator for every joiner and lost card.
- **Trust an attestation alone.** Anyone can attest a key on a card they bought, so the attestation says what a key is and not whose it is.
- **Directory approval of a binding.** The directory cannot say whose device a key is, and a directory change needs no hardware proof, so a changed entry would add a key to a person; operator approval alone is the rule.
- **An SSH-signing fallback for staff.** It cannot be tied to a card the same way, and GitHub treats it as a different key.
- **A new attestation at every renewal.** It brings a person back to the card once a year for a check that the lost-card rule already covers.
- **A subkey binding that never expires.** A lost card would leave a key valid for ever; an expiry that the card's proof extends keeps the binding in step with the card.
- **A label inside a type 0x00 signature over text.** The text is hashed with its data and lenient formats tolerate extra lines, so a label separates nothing.
- **The version 4 fingerprint as the key identity.** It covers a creation time the holder chooses, so one card key could be bound twice.
- **The certification key also signs the keyring.** One compromise would give an attacker both the power to certify and the power to publish.
- **A model-name allowlist.** The Yubico statement has no model name, so such a list would name nothing that sluis can check.
- **Software subkeys bound under the primary.** The private part should never reach a host; a bound software subkey would put a copy of the person's signing power in the company's keyring.

## Follow-ups

- Build `sluis key bind`, the kinds `PersonPrimary` and `KeyBinding`, the `person-key` data key, the certification and keyring-signing purposes, and the keyring producer: [REQ-KEYS-122](/requirements/keys/#REQ-KEYS-122) to [REQ-KEYS-143](/requirements/keys/#REQ-KEYS-143), [REQ-KEYS-146](/requirements/keys/#REQ-KEYS-146) to [REQ-KEYS-168](/requirements/keys/#REQ-KEYS-168) and [REQ-KEYS-171](/requirements/keys/#REQ-KEYS-171) to [REQ-KEYS-227](/requirements/keys/#REQ-KEYS-227) ([#1719](https://github.com/truvity/sluis/issues/1719)), after the conformance test passes.
- Run the conformance test and date the result ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Write the test vectors under `tests/testdata/keybinding/` ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Name the sealed primary, the `person-key` purpose and the two per-company authority keys in the key plan of [0006](/decisions/0006-keys-and-sealing/), with their two PIV slots per company on a board, by a new record when this is built ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: how a verifier learns the company's certification key and keyring-signing key, which no root vouches for in this design. The requirements [REQ-KEYS-130](/requirements/keys/#REQ-KEYS-130), [REQ-KEYS-153](/requirements/keys/#REQ-KEYS-153) and [REQ-KEYS-167](/requirements/keys/#REQ-KEYS-167) still assume a trust signature from a root ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: whether the card's authentication slot can make a type 0x02 challenge signature through the card tooling and with the slot's touch and PIN policy. If it cannot, an authentication subkey is not bound and the bound set is the signing subkey alone ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: whether `gpg`, `sqv` and GitHub accept a certificate whose signing subkey was bound this way, and whether GitHub accepts a P-384 certificate with third-party certifications ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: sealed software keys for non-human identities such as bots and agents, a later feature with its own record when it is designed ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: enforcement of commit signing on GitHub, which is a later CI check against the staff keyring ([#1719](https://github.com/truvity/sluis/issues/1719)).
