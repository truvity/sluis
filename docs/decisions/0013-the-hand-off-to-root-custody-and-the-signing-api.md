# 0013 — sluis holds no root: it asks root custody to certify its intermediates, and signs artifacts through a signing API

**Status:** Accepted
**Date:** 2026-11-01

Proposal: [issue #1001](https://github.com/truvity/sluis/issues/1001).

## Context

A chain of trust has a few keys that almost never change, the roots, and many that work every day. A root is used rarely, by a person who is present, and a mistake with it is slow and expensive to repair. The intermediates sign certificates, tokens and artifacts all the time and must be online and automated. Putting both in one product puts the rarest and most valuable keys beside the busiest code.

On 2026-11-01 the maintainer split them. A separate product, [ritueel](https://ritueel.id), owns the roots, the ceremonies that certify an intermediate, and every transition of a root between key stores. sluis owns the intermediates and everything that signs with them. The two share no code and have no code or runtime dependency on each other; they exchange standard formats through a written contract. This record is sluis's side of that contract: what it hands over, what it installs, how it warns, what it verifies and what it signs. How its keys are born and used is [0012](/decisions/0012-cryptography-and-authority-keys/).

## Decision

### sluis holds no root

- **sluis holds intermediates and the keys that sign with them, never a root.** It does not create, keep, rotate or revoke a root.
- **sluis never generates a root, not even for development.** The first version and the test harness use ritueel's software-root mode, run by the harness or a setup script.
- **The key commands are `sluis key request`, `sluis key install` and `sluis key status`.** There is no ceremony command in sluis.
- **The profile of an intermediate** (path length, name and URI constraints, extended key usage) and its test vectors are written once, in ritueel's hand-off specification. Each side tests against them; no code is shared.

### The hand-off

sluis hands ritueel a request and installs what comes back. The specification lives in ritueel and is versioned; sluis refuses a version it does not know.

| Kind | sluis sends | sluis gets back |
|---|---|---|
| X.509 intermediate | A PKCS#10 certificate request, and a proof file: the YubiKey attestation chain (the slot's certificate and the card's own), or the cloud key's identifier and public key | A certificate chain |
| OpenPGP signing subkey | The public subkey packet and its back-signature (type 0x19) | The subkey bound to the primary |

- **`key-holder` makes the back-signature on the board.** The subkey stays in a PIV slot and the card signs the digest of the primary-key binding. sluis builds the OpenPGP packets itself and does not use `gpg` on a board. Tested 2026-10-31 with a throwaway primary: `gpg`, `gpgv` and `sqv` accept the subkey with the back-signature and refuse it without.
- **`sluis key request`** creates the key if it does not exist, writes the request and the proof, and prints the key's fingerprint. **`sluis key install`** checks that the chain leads to a root in the root list, that the certificate or binding matches the key, and that its validity and constraints are as expected, and only then starts using it. **`sluis key status`** lists each key, its store, its expiry and its warning level.
- **Attestation is checked on both sides.** sluis checks the attestation of its own keys when it requests, against Yubico Attestation Root 1 and its intermediates; ritueel checks it again before it certifies.
- **The token certificate is not part of the hand-off.** The node's intermediate issues it on the node ([0012](/decisions/0012-cryptography-and-authority-keys/#the-token-signing-key)).

### Lifetimes and the warning

- **An intermediate certificate and an OpenPGP subkey last about 13 months.** At the yearly ceremony, in month 12, the root re-signs the same key; the new certificate overlaps the old one. When key material must change, the new key is requested like the first ([0012](/decisions/0012-cryptography-and-authority-keys/#rotation)).
- **sluis warns before expiry:** a notice 60 days before, an alert 30 days before and a critical alert 14 days before. The warning is a metric, an alert rule and a line in `sluis key status` and `sluis doctor`. The lead time is long because the renewal needs a person at ritueel's side.
- **A stolen or lost board is a small event.** ritueel revokes the board's certificate and subkey, and sluis publishes the revocation.

### Root pins and the root list

- **sluis verifies a signed root list and never makes one.** `sluis trust sync` takes the list ritueel produces. It accepts a list signed by a root it already trusts and refuses an older version: the list carries a version number that only increases.
- **sluis pins the public keys of roots (SPKI hashes), never certificate fingerprints.** A root whose validity is extended gets a new certificate over the same key, and a pin on the certificate would break every host.
- **A host keeps its last good list.** A list close to expiry is a warning on sluis's side and a renewal on ritueel's.
- **The limits of root security are ritueel's to state:** any single root may change the root set, and whoever holds one root card and its PIN can sign until that root is revoked. sluis's threat model points to ritueel's documents. How hosts receive the bundle is [0007](/decisions/0007-workload-identity-and-host-enrolment/#the-trust-bundle).

### The signing API

sluis signs four kinds of thing, each with a key bound to its purpose: artifacts through the signing API; tokens and the key set that verifies them; X.509 leaf and SSH certificates, by `ca` and `ssh-ca` ([0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/)); and, later, OpenPGP certifications of people's keys.

- **A caller, such as a CI job, proves its identity with an OIDC token.** Policy decides which repository, branch or workflow may use which key. sluis signs in place, records the signature in the audit log and returns it. The key is never shown.
- **The outputs are an OpenPGP detached signature and a raw signature;** a Sigstore bundle comes later. The policy action names and audit events live in code, like every other action ([0017](/decisions/0017-audit-records-the-usage-stream-and-quota/#the-action-table-is-code-and-the-release-identifies-it)).
- **OpenPGP artifact signing arrives in stages:**

| Stage | What sluis signs |
|---|---|
| First working version | A detached, ASCII-armoured `.asc` signature over a file or a digest |
| Parity milestone | The `Release.gpg` and `InRelease` files of an apt repository, and the `repomd.xml.asc` of an rpm repository |
| Later | The header signature of each rpm package, and a stand-in for `gpg` that git can call (`gpg.program`), for CI |

### Signed releases

- **Nothing is signed before the roots exist.** sluis's own releases carry checksums only until ritueel has held the root ceremony and its drill.
- **From then on, releases are signed through the signing API with a release subkey held in a cloud key service (KMS),** bound by a root primary in the release keyring. CI reaches the signing API with its OIDC token, so a release needs no card work. The project's releases are the API's first user. Channels and packaging are [0019](/decisions/0019-release-train-packaging-and-distribution/#signing-and-checksums).

### Company OpenPGP certification

Later, not in the first working version. OpenPGP has no intermediates as X.509 does, but it can delegate: at a ritueel ceremony the company root makes a trust signature, limited to a domain, over a sluis certification key. That key then certifies online the OpenPGP keys of people whom sluis has authenticated against the directory, for their verified addresses in that domain, each with an expiry, and stops when the person leaves. The root stays offline.

## Consequences

- sluis gets smaller and easier to test. Its tests need a card or a key service and a software root from ritueel's software-root mode, run by the test harness; none needs a root ceremony. Until that mode exists, a developer has no certified intermediate.
- The contract is an interface between two products: a change needs a version and a compatibility rule on both sides.
- Re-signing needs a person on ritueel's side once a year. The long warning is sluis's only protection; if no one renews by month 13, certificates expire and `ca` stops issuing.
- The revocation of a stolen board crosses the line between the products, and the time from theft to verifiers refusing it belongs to neither alone; the runbook names both parts.
- The attestation check and the OpenPGP packet code exist on both sides, so the two share a test matrix: `gpg`, `gpgv`, `sqv`, apt and rpm.
- Early releases are verifiable by checksum only, not by signature.
- The root-custody requirements moved to ritueel; sluis keeps requirements for the hand-off, the back-signature, expiry warnings, the root list, exclusive card access, keys born in place and the signing API.

## Roads not taken

- **Keep root ceremonies in sluis.** Root custody would sit next to the busiest code, and a user who only needs roots would have to adopt a signing system; the product boundary enforces the cleanest boundary in the model.
- **A development root inside sluis.** It duplicates ritueel's software-root mode, and a root that exists in sluis can ship by mistake.
- **A shared library with ritueel.** It would tie two release trains together and make neither replaceable; a written profile with test vectors costs only a specification.
- **Pin the certificate of a root.** Every extension of a root's validity would break pinned hosts, and a pin alone gives no rollback protection.
- **Use `gpg` on the board for the back-signature.** `gpg` cannot bind an existing P-384 key as a signing subkey (tested 2026-10-31 with 2.2.27 and 2.4.7), and it sent wrong PINs to a test card.
- **A Sigstore bundle in the first version.** More to build before the first user, and apt and rpm do not read it.
- **Sign releases before the roots exist, with a provisional key.** A signature that a later root does not vouch for teaches users to trust a key that is then thrown away.
- **Online OpenPGP certification in the first version, or never.** It needs strict policy and expiry rules before it exists, and never would leave company-wide keys manual with no follow of joiners and leavers.

## Follow-ups

- The request and response contract of the signing API: [#890](https://github.com/truvity/sluis/issues/890).
- Leaf certificate profiles, leaf lifetimes and the revocation cadence, including the token certificate's profile: [#1073](https://github.com/truvity/sluis/issues/1073).
