# 0024 — sluis holds no root: it asks root custody to certify its intermediates, and signs artifacts through a signing API

**Status:** Accepted
**Layer:** add-on
**Date:** 2026-11-21

Proposal: [issue #1001](https://github.com/truvity/sluis/issues/1001).

## Context

A chain of trust has a few keys that almost never change, the roots, and many that work every day. A root is used rarely, by a person who is present, and a mistake with it is slow and expensive to repair. The intermediates sign certificates, tokens and artifacts all the time and must be online and automated. Putting both in one product puts the rarest and most valuable keys beside the busiest code.

A separate product, [ritueel](https://ritueel.id), owns the roots, the ceremonies that certify an intermediate, and every transition of a root between key stores. sluis owns the intermediates and everything that signs with them. The two share no code and have no code or runtime dependency on each other; they exchange standard formats through a written contract. This record is sluis's side of that contract: what it hands over, what it installs, how it warns, what it verifies and what it signs. How its keys are born and used is [0006](/decisions/0006-keys-and-sealing/).

Signing artifacts for a caller on the internet adds three facts about the formats:

- **An OpenPGP signature cannot be made from a digest alone.** The signature hashes the data, then its own version, type, algorithm and hashed subpackets, then a trailer (RFC 9580, section 5.2.4). The signer has to see the data. A release signs the checksum file, which is small; the large files are covered by the checksums in it.
- **A key signs only what it is for.** A second signature format under the same key would let a chosen input for one format pass as a signature in the other, which is why signing keys are never shared across purposes ([principle 3](/principles/#3-keys-are-shared-rotated-and-given-to-tenants-by-rule)).
- **`signer` must not be a signing oracle for whoever reaches it** ([0002](/decisions/0002-families-of-actors/)). The caller is a CI job on the internet's side of `entry`, so the decision to sign has to come from `policy` and reach `signer` in a form that only `policy` can make.

## Decision

The core runs none of this: it holds no intermediate and offers no signing API. This add-on starts when it is built. Built today: the key commands `sluis key request`, `sluis key install` and `sluis key status`, for the `x509-intermediate` request type, with the floor, the root list check and the expiry warning described below. Everything else in this record is planned ([roadmap](/roadmap/)).

### sluis holds no root

- **sluis holds intermediates and the keys that sign with them, never a root.** It does not create, keep, rotate or revoke a root.
- **ritueel owns the roots, the root list, the CA bundle, the revocation lists, its keyring file, its cards and the SSH CA. sluis owns the intermediates and the request and install side.** They meet only through the hand-off contract ([The hand-off](/decisions/0024-root-custody-and-the-signing-api/#the-hand-off)).
- **ritueel builds the trust files and does not publish them for sluis.** The trust files are the root list, the CA bundle, the revocation lists, the SSH CA key list and the keyring. A pipeline outside ritueel publishes them.
- **ritueel issues no leaf certificate.** `ca` issues every leaf from the boundary's intermediate ([0023](/decisions/0023-certificate-authorities/)).
- **A root is used only in a ceremony.** It is not "offline": a root in a cloud key service is not offline, and the rule that matters is that a person is present when it is used.
- **sluis never generates a root.** Its intermediates are certified by any certificate authority that signs a PKCS#10 request and issues the profile of ritueel's hand-off contract ([The floor](/decisions/0024-root-custody-and-the-signing-api/#the-floor)). The test harness uses a throwaway root from a plain CA tool, and ritueel's software-root mode when it exists.
- **The key commands are `sluis key request`, `sluis key install` and `sluis key status`.** There is no ceremony command in sluis.
- **The profile of an intermediate** (path length, name and URI constraints, extended key usage) and its test vectors are written once, in ritueel's [hand-off contract](https://ritueel.id/reference/hand-off-contract/). Each side tests against them; no code is shared.

#### The words

sluis follows ritueel's names:

- **A root key is one key; a root is what is built from keys,** of one kind: an X.509 trust anchor, an OpenPGP identity or an SSH CA.
- **The CA bundle is the one PEM file with the certificates of the active X.509 root keys.** The trust bundle is the SPIFFE document that `ca` renders ([0022](/decisions/0022-workload-identity-and-host-enrolment/)) and nothing else.
- **The trust files** are the root list, the CA bundle, the revocation lists, the SSH CA key list and the keyring. The root list pins each root key by a digest of its public key and carries the trust sequence; an X.509 root key signs it.
- **The hand-off contract has three request types:** `x509-intermediate`, `openpgp-subkey` and `ssh-ca-key`. A request is answered by an answer, which always holds a result file.
- **A ceremony is one act that uses a root key.** A step of sluis, such as writing a request or installing an answer, is not a ceremony.
- **`TrustAnchors` is sluis's own config kind.** It holds copies of the root list with its signature, the CA bundle and the SSH CA key list that ritueel builds. It is not a ritueel file.

### The hand-off

sluis hands ritueel a request and installs what comes back. The specification is ritueel's [hand-off contract, version 1](https://ritueel.id/reference/hand-off-contract/); it owns the files, the members, the profiles, the reason codes and the test vectors, and this record does not copy them. sluis writes exactly what the contract defines, refuses a version it does not know and a member it does not define, and passes the contract's vectors, the floor vectors included ([#1070](https://github.com/truvity/sluis/issues/1070)).

- **There are three request types:** `x509-intermediate`, `openpgp-subkey` and `ssh-ca-key`. `sluis key request` writes the first today. The second comes with the signing API, and the third with `ssh-ca` ([#1072](https://github.com/truvity/sluis/issues/1072)).
- **An X.509 answer is one certificate and, with the full contract, a result file.** The root is never in the answer. `sluis key install` takes its anchors from the CA bundle of `TrustAnchors`; when sluis also holds a root list, a certificate of the CA bundle is an anchor only if its key is on that list. The result file only names which anchor issued the certificate.
- **An OpenPGP request holds the subkey and one back-signature for every active primary** on the signed root list that sluis holds, and its proof names those primaries. Either root card can then make the binding, with no second request. A root list that is not current gives the contract's `unknown-primary`, and the fix is a newer list, not a new key.
- **key@ makes the back-signatures on the board.** The subkey stays in a PIV slot and the card signs the digest of each primary-key binding. The keys are version 4, ECDSA on P-384, and every signature uses SHA-384. Each back-signature carries the Issuer Fingerprint in its hashed area and the Issuer Key ID in its unhashed area; `gpg` 2.2 refuses a certificate whose signatures lack the key ID. The OpenPGP packets are written by an approved library ([the list](/reference/approved-libraries/)), never by the `gpg` tool, and no `gpg` runs on a board. A throwaway primary tested on 2026-10-31 showed that `gpg`, `gpgv` and `sqv` accept the subkey with the back-signature and refuse it without.
- **The proof names the store by the contract's words:** `piv` for a key in a PIV slot, which is the `yubikey-piv` driver of key@; `aws-kms`; and `software`. A node never uses the `openpgp-card` store, because the OpenPGP applet is not used on a node ([0006](/decisions/0006-keys-and-sealing/)).
- **A KMS proof names the key by its full ARN.** The certifier reads the key itself and trusts no copy. ritueel's roots live in an account of their own, so a sluis key is in another account: sluis creates a read-only role in the key's account that allows `DescribeKey`, `GetPublicKey` and `GetKeyPolicy` on that key and nothing else, and trusts the certifier's principal to assume it. The certifier refuses a key whose policy lets more than one principal `kms:Sign`, which is the rule [0006](/decisions/0006-keys-and-sealing/) already sets for every signing key. An OpenPGP subkey in KMS needs its creation time kept, for example as a tag, because the fingerprint depends on it and KMS's creation date is not it.
- **`sluis key request`** creates the key if it does not exist, reads the active primaries from the root list when the request needs them, writes the request and the proof, and prints the key's fingerprint. **`sluis key install`** runs the requester's checks of the contract and only then starts using the answer: the certificate verifies to an anchor; when it holds a root list, that list is at least as recent as the answer's `trust_sequence`; the key is its own; the subject, constraints and key usage are exactly what it asked; the validity is the length it expects and has not ended. It hands the verified chain to key@, which stores it as the kind `KeyCertificate`. **`sluis key status`** lists each key, its store, its expiry and its warning level.
- **Attestation is checked on both sides.** sluis checks the attestation of its own keys when it requests, and ritueel checks it again before it certifies. Both use the maker's current attestation root with the full set of its intermediate certificates, because the list that `piv-go` v2.6.0 embeds lacks the intermediate that firmware 5.8 uses (measured 2026-11-01).
- **The token certificate is not part of the hand-off.** The node's intermediate issues it on the node ([0006](/decisions/0006-keys-and-sealing/)), under the profile of [0023](/decisions/0023-certificate-authorities/#leaf-profiles), and only where an installation turns it on.
- **The request carries the intermediate's name constraints,** built from the boundary's declared names ([0023](/decisions/0023-certificate-authorities/#the-intermediates-name-constraints)). The contract requires at least one permitted subtree, and ritueel's certifier checks each against the entries its policy lists for the requester: its URI hosts and its DNS names, a permitted name being inside only when it equals a listed entry. Each intermediate of an installation's nodes carries one permitted DNS subtree, the same internal zone for every node. The harness's test certifier stands in until a ritueel certifier is built ([#1071](https://github.com/truvity/sluis/issues/1071)). For IP addresses, version 1 accepts the request's default form (every address excluded, as `0.0.0.0/0` and `::/0`) and refuses a permitted IP range, and so does the test certifier.
- **The request states its extended key usage and a subject the contract accepts.** Every intermediate request carries extended key usage `serverAuth` and `clientAuth`, in that order, not critical, and the certificate carries it as asked ([0023](/decisions/0023-certificate-authorities/#the-intermediates-name-constraints)). The subject is one common name of printable ASCII, 1 to 64 characters; `sluis key request` and key@ refuse another name and, when the host holds a CA bundle and names it, a name or key that is a root's. The test certifier reads a request as the contract's certifier does: a strict DER reader, the reason codes in the contract's order (`malformed`, `algorithm-unsupported`, `possession-invalid`, `policy`), and a policy step that checks the subject, the extended key usage and the names.
- **An SSH CA key is not certified, it is listed.** With `ssh-ca`, `sluis key request` writes the public key as an OpenSSH line, an SSH signature by that key in the contract's namespace, and a proof whose `use` is `user` or `host`. ritueel checks where the key lives, as for every other type, and lists it on its signed SSH CA key list; a key that replaces the old one replaces its entry. How the hosts of an installation learn the CA keys is under [SSH](/decisions/0023-certificate-authorities/#ssh) in 0023. Planned.

### The floor

The least that sluis needs from a root is a certificate for its request. That is the floor of ritueel's contract (PKCS#10 in, X.509 out), and sluis accepts it as its own.

- **Any certificate authority that signs a PKCS#10 request in the profile can certify sluis.** ritueel is the custodian the project builds and tests against; nothing in sluis needs it. The floor is X.509 only: an OpenPGP subkey and an SSH CA key need the full contract.
- **Anchors come from the CA bundle of `TrustAnchors` alone, and the root list is optional.** The CA bundle is a document committed with the installation's configuration, and sluis trusts it as a TLS client trusts the file it is given.
- **There is no rollback protection and no trust sequence on the floor.** An older CA bundle put back in place is accepted. A host that wants both holds a signed root list, and then the rules of [Root pins and the root list](/decisions/0024-root-custody-and-the-signing-api/#root-pins-and-the-root-list) apply.
- **`sluis key install` says what it did not run.** On the floor it checks the issuer, its own key, the profile and the validity, and reports as not run the contract version, `request_sha256`, the digests of the result file, the `trust_sequence`, and the certifier's checks of the store, the evidence and the key match. Nobody but sluis has then checked where its key lives. A check that cannot run is never reported as passed ([principle 4](/principles/#4-verify-never-trust-a-claim)).
- **The test harness may use the floor.** The contract's test vectors have an `openssl` recipe for a throwaway root, and the floor vectors fix what a requester must accept and refuse.

### Lifetimes and the warning

- **An intermediate certificate and an OpenPGP subkey last 13 calendar months,** the length the certifier sets. In month 12 the root re-signs the same key; the new certificate overlaps the old one. When key material must change, the new key is requested like the first ([0006](/decisions/0006-keys-and-sealing/)).
- **A renewal re-signs the same key, and every renewal is a ceremony with a person present.** Nothing renews unattended, in either product. With a card root the person touches the card once a year. With a KMS root the operations of the cloud key need a second factor as the proof that a person is there, and ritueel chooses the mechanism; sluis relies on none of it. The hand-off contract agrees: the person at the ceremony confirms every request. The long warning is the protection against a renewal that nobody runs.
- **sluis warns before expiry:** a notice 60 days before, an alert 30 days before and a critical alert 14 days before. The lead time is long because every renewal needs a person. The warning is a line in `sluis key status` and `sluis doctor`, with its own exit code for each level (built); a metric and an alert rule are planned.
- **A stolen or lost board is a small event.** ritueel revokes the board's certificate in the root CRLs and drops its subkey from the next keyring package, and sluis stops using the key and its token certificate ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)).

### Root pins and the root list

- **sluis verifies a signed root list and never makes one, and the list is optional** ([The floor](/decisions/0024-root-custody-and-the-signing-api/#the-floor)). ritueel signs the root list as a detached JSON Web Signature in the general JSON serialisation (RFC 7515, section 7.2.1), with one signature for every signer, and the list may carry `removals`, the stored removal authorisations of root keys that leave. The reader's format and its 11 checks, in order and with their codes, are part of the contract's reference pages, reached from the hand-off contract; sluis implements them and passes the vectors. `expires_at` is exactly 13 calendar months after `signed_at`, and `signed_at` is not later than the host's clock, or the list is `malformed`. The lowest sequence given with the initial pins (`--min-sequence`) is a floor for a host that holds none. The list is a part of `TrustAnchors`, so store@ loads it with the rest of the config. `sluis key install` checks a list it is given (built); `sluis trust sync` fetches it (planned, [#968](https://github.com/truvity/sluis/issues/968)).
- **The signing rule is ritueel's, and sluis checks exactly this:** a list with the same root keys carries the signature of one active root key; a list that changes the set of root keys carries, for every root key of the list it replaces, its signature or its stored removal authorisation, and every root key it adds signs it.
- **The list carries the trust sequence,** one integer that grows by one whenever any trust product changes. sluis takes a list only if a root key it already trusts has signed it (or one of its initial pins, before it holds a list), the rule above holds, and its sequence is not lower than the one it holds. A list at the same sequence must be byte for byte the one held, or it is refused. The same sequence is what an answer's `trust_sequence` is compared with.
- **sluis pins the public keys of root keys (SPKI hashes), never certificate fingerprints.** The list pins keys, so a root key whose certificate is extended gets a new certificate over the same key and no pin breaks.
- **The roots' certificates come from ritueel's CA bundle,** which carries the list's sequence in its first line and is checked against the list: one certificate for each root of the list, in its order, each for the pinned key, naming itself as its issuer, signed by its own key and a CA certificate. A bundle that fails is refused with `ca-bundle-mismatch`, and `sluis key install` runs the check whenever it reads a root list. `signer` takes the root certificate for the `x5c` chain of the key set from it, where the installation turns the token certificate on.
- **The trust bundle needs no signature of its own.** It is a set of certificates, and each one chains to a root key in the root list; the root list is what a root key signs ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **A host keeps its last good list.** The list is re-signed at least once a year and lasts 13 calendar months. When it expires with no newer one, the host keeps it, goes on trusting its root keys and warns (`held-expired`, the reader's code for a list a host holds; the refusal `expired` stands beside it when that same list comes again after its expiry); it does not drop them. `sluis key install` holds no list between runs: it reads the list it is given against the pins it is given, as a first list, and refuses an expired one. A list close to expiry is a warning on sluis's side and a renewal ceremony on ritueel's.
- **An intermediate is revoked by a CRL that each issuing root key signs,** valid for 25 months, so a root key that signs every second year always has a current one. No certificate carries a distribution point, and ritueel builds the files; a pipeline outside ritueel publishes them. sluis does not sign or replace them. A relying party that wants to check an intermediate against a root key's CRL configures the file, as with a leaf CRL ([0023](/decisions/0023-certificate-authorities/#issuance-log-and-revocation)). Serving the roots' CRLs with the trust bundle comes with `sluis trust sync` ([#968](https://github.com/truvity/sluis/issues/968)).
- **The limits of root security are ritueel's to state:** changing the set of root keys requires every current root key, or a root key's stored removal authorisation; any one root key signs the rest. sluis's threat model points to ritueel's documents. How hosts receive the trust bundle is in [0022](/decisions/0022-workload-identity-and-host-enrolment/).

### The signing API

sluis signs four kinds of thing, each with a key bound to its purpose: artifacts through the signing API; tokens and the key set that verifies them; X.509 leaf and SSH certificates, by `ca` and `ssh-ca` ([0023](/decisions/0023-certificate-authorities/)); and OpenPGP certifications of people's keys, which are not scheduled ([Company OpenPGP certification](/decisions/0024-root-custody-and-the-signing-api/#company-openpgp-certification)). The signing API is planned.

- **A caller, such as a CI job, proves its identity with an OIDC token.** Policy decides which repository, branch or workflow may use which key. sluis signs in place, records the signature in the audit log and returns it. The key is never shown.
- **The first method returns an OpenPGP detached signature;** a raw signature and a Sigstore bundle come later, each with a key of its own. The action names and audit events live in code, like every other action.
- **OpenPGP artifact signing arrives in stages:**

| Stage | What sluis signs |
|---|---|
| First | A detached, ASCII-armoured `.asc` signature over the content it is sent |
| Next | The `Release.gpg` and `InRelease` files of an apt repository, and the `repomd.xml.asc` of an rpm repository |
| Later | The header signature of each rpm package, and a stand-in for `gpg` that git can call (`gpg.program`), for CI |

#### Who does what

- **The signing API is a public Connect service of `exchange`,** reached through `entry`. `exchange` already verifies proofs (the `github-oidc` verifier driver, [0022](/decisions/0022-workload-identity-and-host-enrolment/)), asks `policy`, and calls `signer`; no new edge is needed.
- **The release-signing key belongs to `signer`,** the actor that signs, as a purpose of its own beside the token-signing key ([0006](/decisions/0006-keys-and-sealing/)). The two keys are never shared and have one user actor each. key@ holds the key and signs in place.
- **The flow is four steps:**
  1. The caller sends the content with its proof, a GitHub Actions identity token, in the `Authorization` header.
  2. `exchange` verifies the proof and asks `policy` to decide the action `artifact.sign` on the resource `SigningKey` `release-signing`, with the verified attributes of the job (repository, ref, workflow, environment) and the digest and length of the content as context.
  3. `policy` has key@ seal a **signing grant**: the decision, the policy digest, the key purpose, the SHA-384 of the content, the caller, and an expiry 60 seconds ahead ([0007](/decisions/0007-policy-and-sealed-grants/)).
  4. `exchange` passes the grant and the content to `signer`. `signer` opens the grant at key@, refuses unless the key purpose, the content hash and the expiry match, writes the hash of the grant as a `SpentGrant`, builds the signature and has key@ sign it in place.
- **One grant signs once.** The `SpentGrant` is the marker of [0007](/decisions/0007-policy-and-sealed-grants/) for a one-shot action: a second use of the same grant finds it and is refused. A repeat of the same request id within 24 hours returns the first outcome and needs no new grant.

#### The request and the response

```proto
service SigningService {
  rpc SignDetached(SignDetachedRequest) returns (SignDetachedResponse);
}

message SignDetachedRequest {
  string request_id = 1; // the caller's idempotency key
  bytes content = 2;     // 1 byte to 4 MiB
}

message SignDetachedResponse {
  string armored_signature = 1; // the .asc text
  string key_fingerprint = 2;   // the signing subkey, 40 upper-case hex characters
  string audit_id = 3;          // the id of the audit record of this signature
}
```

- **The caller names no key.** An installation has one release-signing key, in its release boundary only ([0006](/decisions/0006-keys-and-sealing/)), and the request is for that key. A second signing key adds a field, which is a compatible change ([Contracts and errors](/reference/contracts/)).
- **The caller sends the content, not a digest,** up to 4 MiB, the limit of a Connect message. The signed files of apt and rpm repositories and a release's checksum file are far below it. An empty or larger body is `invalid_argument`.
- **The request id follows the rule for public methods:** a repeat within 24 hours returns the first outcome, signature included, and a reused id with other content is `failed_precondition`.

#### The signature

- **It is an OpenPGP version 4 signature of type `0x00` (binary document),** with the public-key algorithm ECDSA (19) on P-384, the hash SHA-384 (10), made by the release subkey.
- **The hashed subpackets are the signature creation time and the issuer fingerprint (subpacket 33);** the one unhashed subpacket is the issuer key id (16). No other subpacket is written, and the signature does not expire. This is ritueel's packet rule, which its tools check.
- **The output is armoured** as RFC 9580, section 6 says, with the checksum line so that older readers accept it, and with no `Version` or `Comment` header. The creation time is `signer`'s clock, in UTC seconds.
- **The subkey signs; the primary does not.** The signature verifies against the release keyring that ritueel's tools build, which carries the primaries and every current subkey binding ([Release signing](/distribution/release-signing/)).
- **`signer` writes the packets with an approved library,** never with the `gpg` tool, and key@ signs the signature input (the content, then the hashed fields and the trailer). key@ parses that structure before it signs, as it does for a certificate body ([0006](/decisions/0006-keys-and-sealing/)).

#### When the API refuses to sign

- **The key's binding must be installed and unexpired.** `signer` signs only while the kind `OpenPgpBinding` holds the primary's certification of the subkey and the binding has not expired. Otherwise the answer is `failed_precondition`. In a development installation the binding is made by ritueel's software-root mode, so its signatures are for tests only.
- **A caller without a valid proof is `unauthenticated`;** one that `policy` does not permit is `permission_denied`, with the uniform refusal of the edge ([Contracts and errors](/reference/contracts/)). A repository, ref or workflow outside the policy looks the same as one that does not exist.
- **Every signature is audited,** as a key use, through [audit's emitter](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md): the key as the resource, the caller as the acting party and the policy decision. The record carries the SHA-384 and the length of the content, never the content. It is written before the response, and a signature whose record could not be written is not returned.

#### The first method and the next

- **The first method is `SignDetached`,** for a GitHub Actions proof. The next stage adds the signing of `Release.gpg`, `InRelease` and `repomd.xml.asc` as uses of the same method ([#1074](https://github.com/truvity/sluis/issues/1074)); later stages are the rpm package header and a stand-in for `gpg` that git can call ([#1075](https://github.com/truvity/sluis/issues/1075)).
- **A raw ECDSA signature is not in this stage.** It would be made by a key of its own, because the same key under two formats is the cross-use that principle 3 rules out, and nothing yet consumes one ([#1117](https://github.com/truvity/sluis/issues/1117)). A Sigstore bundle follows the same rule.

### Signed releases

- **Nothing is signed with OpenPGP before the roots exist.** Until ritueel has held its first ceremony and its drill, sluis's own releases carry checksums and SBOMs, and the maintainer signs each tag by hand ([0013](/decisions/0013-release-train-packaging-and-distribution/)).
- **From then on, releases are signed through the signing API with a release subkey held in a cloud key service (KMS), an `ECC_NIST_P384` key of its own with `SIGN_VERIFY` usage,** bound by a root primary in the release keyring. CI reaches the signing API with its OIDC token, so a release needs no card work. The project's releases are the API's first user. Channels and packaging are in [0013](/decisions/0013-release-train-packaging-and-distribution/).

### Company OpenPGP certification

Company OpenPGP certification is not built and not scheduled. ritueel keeps no staff keys, so there is no ritueel card helper, no shared bundle format and no trust-signature ceremony over a sluis certification key; sluis holds no root that could make one. What waits in the backlog is the sluis half: a person binds their own key to their identity and sluis checks the proof on the server ([0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/)), and sluis syncs company-issued keys to staff GitHub accounts ([0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/)).

## Consequences

- sluis gets smaller and easier to test. Its tests need a card or a key service and a root, which the test harness makes with a plain CA tool or, when it exists, with ritueel's software-root mode; none needs a ceremony.
- The contract is an interface between two products: a change needs a version and a compatibility rule on both sides. The floor is the part that needs no ritueel: a CA tool and a PKCS#10 request.
- Every renewal is a ceremony with a person at ritueel's side, once a year, and the long warning is sluis's only protection: if no one renews by month 13, certificates expire and `ca` stops issuing. A KMS root does not change this, because its operations need a second factor as the proof that a person is present.
- The revocation of a stolen board crosses the line between the products, and the time from theft to verifiers refusing it belongs to neither alone; the [incident page](/security/incident-response/) names both parts. sluis acts first: `ca` stops issuing under the intermediate and drops its certificate from the trust bundle, and the root's CRL from ritueel follows.
- The attestation check and the OpenPGP packet code exist on both sides, so the two share a test matrix: `gpg`, `gpgv`, `sqv`, apt and rpm.
- Releases are verifiable by checksum only, not by signature, until the signing API is built.
- A flaw in the parser of `entry` or in `exchange` can ask `signer` only for a signature that `policy` sealed for exactly that content, within a minute, and only once. To sign something else an attacker needs `policy` to agree, as for a credential.
- The policy sees the job's repository, ref and workflow, which is what a release key needs: "this workflow, on this ref, may sign" is a Cedar permit, not code.
- The cost of the signing API is a second key purpose behind `signer`, a slot on a node card ([0006](/decisions/0006-keys-and-sealing/)) and a key rule that lets `signer` open grants. A caller that must sign a large file signs its checksum file, which is how the packaging tools work already.
- The root-custody requirements live with ritueel; sluis keeps requirements for the hand-off, the back-signature, expiry warnings, the root list, exclusive card access, keys born in place and the signing API: REQ-SUPPLY-016 for content, and [REQ-SUPPLY-019](/requirements/supply/#REQ-SUPPLY-019) to [REQ-SUPPLY-024](/requirements/supply/#REQ-SUPPLY-024).

## Roads not taken

- **Keep ceremonies in sluis.** Root custody would sit next to the busiest code, and a user who only needs roots would have to adopt a signing system; the product boundary is the cleanest boundary in the model.
- **A development root inside sluis.** A plain CA tool already makes a throwaway root for a test, and ritueel's software-root mode is the other source; a root that exists in sluis can ship by mistake.
- **Always require a signed root list.** One mode, and rollback protection everywhere, but ritueel's format becomes a requirement on every CA, which is what the floor set out to remove. The floor path states what it lacks instead.
- **A shared library with ritueel.** It would tie two release trains together and make neither replaceable; a written profile with test vectors costs only a specification.
- **Pin the certificate of a root.** Every extension of a root's validity would break pinned hosts, and a pin alone gives no rollback protection.
- **Use `gpg` on the board for the back-signature.** `gpg` cannot bind an existing P-384 key as a signing subkey (tested 2026-10-31 with 2.2.27 and 2.4.7), and it sent wrong PINs to a test card.
- **A Sigstore bundle in the first method.** More to build before the first user, and apt and rpm do not read it.
- **Sign releases before the roots exist, with a provisional key.** A signature that a later root does not vouch for teaches users to trust a key that is then thrown away.
- **A trust log of sluis's own, appended by `sluis key install`.** The root list and the ceremony records that vouch for an intermediate are ritueel's, an installed certificate is a `KeyCertificate` and an audit event, and the certificates `ca` issues are in its issuance log ([0023](/decisions/0023-certificate-authorities/#issuance-log-and-revocation)); a fourth log would hold nothing the others do not.
- **Online OpenPGP certification under a trust signature from a company root.** ritueel keeps no staff keys, and sluis holds no root to make the signature.
- **A signing request that carries a digest.** It cannot produce an OpenPGP signature, because the signature hashes its own trailer after the data.
- **A `key` field naming the purpose.** With one signing key it carries nothing, and a field a caller can set invites a request for a key it should not name.
- **The signing API on `signer`.** `signer` would parse a caller's request and need an edge to `policy`; serving it from `exchange` reuses the edges that exist and keeps `signer` free of internet parsing.
- **A decision that `signer` takes itself.** It would make the actor that asks for the signature also the one that decides.
- **A signature without a grant.** It would rest on the word of `exchange` alone: a flaw in `exchange` could sign anything with the release key.
- **A raw signature from the OpenPGP subkey.** Two formats under one key allow one to be passed off as the other.
- **A token exchanged first, then a second call.** It adds a step and drops the job's attributes from the second decision; the first decision is the one with the facts.
- **A signature that does not wait for its audit record.** A signing service is found out by its records, so a signature without one is the failure to avoid.

## Follow-ups

- A ritueel certifier to replace the test certifier: [#1071](https://github.com/truvity/sluis/issues/1071).
- The signing API's later stages: [#1074](https://github.com/truvity/sluis/issues/1074), [#1075](https://github.com/truvity/sluis/issues/1075); release use and CI credentials: [#874](https://github.com/truvity/sluis/issues/874).
- A raw signature and a Sigstore bundle, each with a key of its own: [#1117](https://github.com/truvity/sluis/issues/1117).
- SSH CA key requests and the signed list of SSH CA keys on hosts: [#1072](https://github.com/truvity/sluis/issues/1072).
- The roots' CRLs served with the trust bundle, and the root list fetched by `sluis trust sync`: [#968](https://github.com/truvity/sluis/issues/968).
- Open: how a sluis certification key for people's OpenPGP keys would be vouched for, now that no ritueel ceremony does it; the backlog proposals [0042](/decisions/0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts/) and [0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/) carry the question.
