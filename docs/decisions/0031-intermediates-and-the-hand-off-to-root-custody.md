# 0031 — Intermediates and the hand-off to root custody: sluis holds no root, asks for its intermediates to be certified, and signs in place

**Status:** Accepted
**Date:** 2026-11-01

Decided by the maintainer. Proposal: [issue #1001](https://github.com/truvity/sluis/issues/1001). The planned record 0032, "ceremonies", was never written here: root ceremonies belong to [ritueel](https://ritueel.id), and this record is the contract between the two products.

## Context

A chain of trust has a few keys that almost never change, the roots, and many that work every day. A root is used rarely, by a person who is present, and a mistake with it is slow and expensive to repair. The keys that work every day, the intermediates, sign certificates, tokens and artifacts all the time and must be online and automated.

Putting both in one product puts the rarest and most valuable keys beside the busiest code. The maintainer decided on 2026-11-01 to split them. A separate product, [ritueel](https://ritueel.id) (ADR 0001 in its repository), owns the roots, the ceremonies that certify an intermediate, and every transition of a root between key stores. sluis owns the intermediates and everything that signs with them. The two share no code and no hard dependency; they exchange standard formats.

This record fixes sluis's side: which keys it holds, what it hands over, what it installs, how it warns, what it verifies, and what it signs. It rests on [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/) (keys sign in place) and replaces the plan for a "two-root PKI hierarchy" record. Certificate profiles, leaf lifetimes and the revocation cadence for leaf certificates are not decided here; they are tracked in their own planned record, ADR 0067 ([issue #1073](https://github.com/truvity/sluis/issues/1073)), which also takes the profile of the token-signing key's certificate.

Measurements used below were taken on 2026-10-31 on a YubiKey 5 Nano with firmware 5.8.0, on one of the operator's boards.

## Decision

### sluis holds no root

- **sluis holds intermediates and the keys that sign with them, and never a root.** It does not create, keep, rotate or revoke a root. A root is made, held and replaced by ritueel.
- **sluis never generates a root, not even for development.** The first version, and the test harness, use ritueel's software-root mode, run by the harness or a setup script. A development root inside sluis would duplicate it, and could ship by mistake.
- **The `sluis ceremony` command does not exist.** Its place is taken by `sluis key request`, `sluis key install` and `sluis key status` (below).

### How an intermediate lives

- **Every intermediate is born where it lives and never leaves.** On a board it is generated on the node's YubiKey in a PIV slot. In a cloud it is a key in the key service. It is non-extractable and attested where the hardware can attest.
- **sluis signs in place, and the token-signing key is its own key.** There is no short-lived online key certified by the intermediate to sign in its place. The intermediate signs certificates and nothing else; tokens are signed by a separate hardware key (below). A YubiKey 5 Nano signs about 7 P-384 signatures a second (147 ms each, measured 2026-10-31); three boards give about 21 a second against a measured peak of 0.8 tokens a second in the previous line. Where a cloud key service holds the keys, its rate is far higher. The keys in sluis's published key set are the token-signing keys, each certified by the node's own intermediate.
- **`key-holder` has exclusive access to its card.** It runs as a dedicated user, the card service is limited to that user, and no other software that talks to cards runs on the board. Measured 2026-10-31: the card's PIN state belongs to the powered card and not to the connection, so while it is unlocked any local process can sign with it, any client's default disconnect resets it, and three wrong PINs from any local user lock it. Exclusive access is a requirement, not a hope.
- **The first version supports three key stores.** A YubiKey in PIV mode, a cloud key service (AWS KMS), and software keys for development only, which the production floor of [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/) refuses. PKCS#11 and HSM drivers come later (ADR 0052).
- **The token-signing key is separate from the intermediate and carries `x5c`.** The node's own intermediate issues it a non-CA certificate that allows signing only, so a key that signs tokens cannot issue a certificate. An entry of the published key set includes that token certificate, the intermediate's certificate and the chain up to the root. The token certificate is issued on the node by the intermediate, so it is not part of the hand-off table below. Standard clients ignore the member and fetch the key over TLS as before; a verifier that wants to check custody can follow the chain to a root it trusts.
- **SSH CA keys are anchors, not a chain.** They live in `key-holder`. Hosts trust a list of SSH CA keys that ritueel publishes, signed by a root. The break-glass SSH CA is made and held by ritueel; sluis only distributes its public key in that list.
- **No code is shared with ritueel.** The profile of an intermediate (path length, name and URI constraints, extended key usage) and its test vectors are written once, in ritueel's hand-off specification. Each side tests against them.

### The hand-off

sluis hands ritueel a request and installs what comes back. The specification lives in ritueel and is versioned; sluis refuses a version it does not know.

| Kind | sluis sends | sluis gets back |
|---|---|---|
| X.509 intermediate | A PKCS#10 certificate request, and a proof file: the YubiKey attestation chain (the slot's certificate and the card's own), or the cloud key's identifier and public key | A certificate chain |
| OpenPGP signing subkey | The public subkey packet and its back-signature (type 0x19) | The subkey bound to the primary |

- **`key-holder` makes the back-signature on the board.** The subkey stays in a PIV slot, so the card signs the digest of the primary-key binding. sluis builds the OpenPGP packets itself and does not use `gpg` on a board. Tested 2026-10-31 with a throwaway primary: `gpg`, `gpgv` and `sqv` accept the subkey with the back-signature and refuse it without.
- **`sluis key request`** creates the key if it does not exist, writes the request and the proof, and prints the key's fingerprint. **`sluis key install`** takes what ritueel returned, checks that the chain leads to a root in the root list, that the certificate or binding matches the key, and that its validity and constraints are as expected, and only then starts using it. **`sluis key status`** lists each key, its store, its expiry and its warning level.
- **Attestation is checked on both sides.** sluis checks the attestation of its own keys when it requests, against Yubico Attestation Root 1 and its intermediates ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). ritueel checks it again before it certifies.

### Lifetimes and the warning

- **An intermediate certificate and an OpenPGP subkey last about 13 months.** Each year at the ceremony in month 12 the root re-signs the same key: a new certificate for the intermediate, a new binding that extends the subkey's expiry. The new certificate overlaps the old one. A renewal makes no new key.
- **Key material rotates only on an event, or at most every three years.** The events are a suspected compromise, a change of algorithm or store, and a change of custody. The limit follows NIST SP 800-57's one to three years for a signing key, and the forced rotation keeps the path exercised. A new key is requested like the first one, with `sluis key request`, and goes into a free spare slot on a card while the old one finishes ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/#rotation)).
- **sluis warns before expiry.** A notice 60 days before, an alert 30 days before, and a critical alert 14 days before. The warning is a metric, an alert rule and a line in `sluis key status` and `sluis doctor`. The lead time is long because the renewal needs a person at ritueel's side.
- **A stolen or lost board is a small event.** ritueel revokes the board's certificate and subkey, and sluis publishes the revocation. The window runs from the theft to that publication.

### The root list

- **sluis verifies a signed root list and never makes one.** `sluis trust sync` takes the list ritueel produces. It accepts a list signed by a root it already trusts, and refuses an older version: the list carries a version number that only increases.
- **sluis pins the public keys of roots, not certificate fingerprints.** A root whose validity is extended gets a new certificate with the same key, and a pin on the certificate would break every host.
- **The accepted limit is ritueel's to state.** Any single root may change the root set, and whoever holds one root card and its PIN can sign until that root is revoked. The security of the roots is in ritueel's documents; the threat model of sluis points there.

### What sluis signs

sluis signs four kinds of thing, each with a key bound to its purpose ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).

1. **Artifacts, through the signing API.** A caller, such as a CI job, proves its identity with an OIDC token. Policy decides which repository, branch or workflow may use which key. sluis signs in place, records the signature in the audit log, and returns it. The key is never shown. The first outputs are an OpenPGP detached signature and a raw signature; a Sigstore bundle comes later. The project's own releases are the first user, signed with a subkey held in a cloud key service.
2. **Tokens**, and the key set that verifies them.
3. **X.509 leaf certificates and SSH certificates**, by `ca` and `ssh-ca` ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)).
4. **OpenPGP certifications of people's keys.** Planned for later, not in the first version, and described below.

OpenPGP artifact signing arrives in stages:

| Stage | What sluis signs |
|---|---|
| First version | A detached, ASCII-armoured `.asc` signature over a file or a digest |
| v1 parity | The `Release.gpg` and `InRelease` files of an apt repository, and the `repomd.xml.asc` of an rpm repository |
| Later | The header signature of each rpm package, and a stand-in for `gpg` that git can call (`gpg.program`), for CI |

The shape of the signing API, its policy actions and its audit events are the subject of a later record (issue #890).

### Planned later: online OpenPGP certification of people

OpenPGP has no intermediates, but it can delegate. A company root, kept by ritueel, may sign a certification key with a trust signature limited to a domain. That key can then certify, online, the OpenPGP keys of people whom sluis has authenticated against the directory, for their verified addresses in that domain, each with an expiry, and stop when the person leaves. The delegation is made at a ritueel ceremony, so the online key never holds the root. This is not in the first version. It is a candidate for v1 parity or later, with its own record.

### Not decided here

- Leaf certificate profiles and lifetimes, and the revocation cadence for leaf certificates. The profile of the non-CA token certificate belongs to ADR 0067 and is planned there.
- Release signing and its channels (ADR 0030).
- The detailed signing API (issue #890).

## Consequences

- sluis gets smaller and easier to test. Its tests need a card or a key service and a software root from ritueel; none needs a root ceremony.
- The first version of sluis depends on ritueel's software-root mode for the roots its harness uses. Until ritueel delivers it, a developer has no certified intermediate.
- A contract is now an interface between two products. A change to it needs a version and a compatibility rule on both sides; sluis refuses a version it does not know.
- Re-signing needs a person on ritueel's side once a year. The long warning is sluis's only protection; if the person is away in month 13, certificates expire and `ca` stops issuing.
- The revocation of a stolen board crosses the line between the products, and the end-to-end time from theft to verifiers refusing it belongs to neither alone. The runbook has to name both parts.
- The root list can expire or be replaced. A host keeps its last good list, and a stale list is a warning on sluis's side and a renewal on ritueel's.
- `x5c` in the key set adds one certificate chain to the published keys, which must be refreshed at each yearly re-signing. The chain has the token certificate in front of the intermediate, and the intermediate issues a new token certificate with each new intermediate certificate.
- The attestation check and the OpenPGP packet code exist on both sides, because there is no shared library. The cost is a shared test matrix: `gpg`, `gpgv`, `sqv`, apt and rpm.
- The requirements for root custody move to ritueel. sluis gains requirements for the hand-off, the back-signature, expiry warnings, the root list, exclusive card access, keys born in place, and the signing API.

## Roads not taken

### Keep root ceremonies in sluis

- Pros: one product to install; shared code between the ceremony and the issuing side; no contract to maintain.
- Cons: root custody next to the busiest code; the boundary between rare and daily keys disappears; the root hardware, tools and teaching are a large subject of their own, and a user who only needs roots would have to adopt a signing system.
- Why not: the boundary between roots and intermediates is the cleanest in the model, and a product boundary enforces it.

### A short-lived online key certified by the intermediate

- Pros: many signatures a second without touching the card; the intermediate is used rarely.
- Cons: a second key type to issue, certify, rotate and revoke; a key in memory that can be copied.
- Why not: the measured rate of three boards is about 21 signatures a second and a cloud key service is faster, both far above the load. Delegation can come later, inside `key-holder`, if a load needs it.

### A development root inside sluis

- Pros: a developer needs no second product.
- Cons: it duplicates ritueel's software-root mode, and a root that exists in sluis can ship in a release by mistake.
- Why not: ritueel's software-root mode is run by the harness or a setup script, and sluis never generates a root.

### A shared library with ritueel

- Pros: one implementation of attestation checks, OpenPGP packets and the intermediate's profile.
- Cons: the products stop being replaceable; a change in one breaks the other; one release train for two.
- Why not: a written profile with test vectors costs a specification and keeps either side replaceable.

### The intermediate signs the tokens itself

- Pros: one key fewer on the card, and a chain in the key set that is one certificate shorter. This was the earlier design.
- Cons: one key both issues certificates and signs tokens, so it is used for every token and a flaw in token handling reaches the CA.
- Why not: the token-signing key is its own hardware key with a non-CA certificate for signing only. It still signs in place, so it is not the online key above. The reasoning and the slot map are in [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/).

### Plain key set with no `x5c`

- Pros: nothing extra to publish and refresh.
- Cons: custody is visible only in the audit log.
- Why not: a standard client ignores `x5c`, and a verifier that wants the chain can have it.

### Pin the certificate of a root

- Pros: the simplest pin; what [0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/) said before.
- Cons: every extension of a root's validity changes the certificate and breaks pinned hosts; a pin alone gives no rollback protection.
- Why not: pin the public key, and version the root list.

### Use `gpg` on the board for the back-signature

- Pros: no OpenPGP code in sluis.
- Cons: `gpg` cannot bind an existing P-384 key as a signing subkey (tested 2026-10-31 with `gpg` 2.2.27 and 2.4.7), and it asks for cards in ways that sent wrong PINs to a test card.
- Why not: `key-holder` signs the digest on the card, and sluis builds the packets.

### Certify the SSH CA keys with an X.509 certificate as provenance

- Pros: every authority key has a record in the chain; `sshd` ignores the certificate, so nothing breaks.
- Cons: a second kind of record for a key that has no chain; hosts need the anchor list anyway.
- Why not: an SSH CA key is an anchor, and a list signed by a root gives the same provenance.

### A Sigstore bundle from the signing API in the first version

- Pros: one output that verifies with the Sigstore tools.
- Cons: more to build before the first user; apt and rpm do not read it.
- Why not: the first outputs are the formats the package tools read. The bundle comes later.

### Online OpenPGP certification in the first version, or never

- Pros: company-wide GPG sooner (online); nothing online that vouches for identities (never).
- Cons: an online key that vouches for people needs strict policy and expiry rules before it exists (online); manual, and no follow of joiners and leavers (never).
- Why not: later, by delegation from the company root, once the directory and the signing API work.
