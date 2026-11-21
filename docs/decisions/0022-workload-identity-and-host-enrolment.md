# 0022 — Workload identity follows SPIFFE, and hosts enrol the same way

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

sluis gives credentials to machines as well as to people: its own actors, hosts that people log in to or that need a TLS certificate and the internal trust anchors, CI jobs holding their platform's token, and pods holding a ServiceAccount token. A node may be replaced, a cloud instance may start with nothing but user data, and a lab host may have nothing but a person at its console.

SPIFFE is the published standard for workload identity: the ID format, the trust domain and its bundle, and the X.509-SVID and JWT-SVID documents. SPIRE is its reference implementation. sluis already holds what SPIRE would add: a CA under intermediates certified by a root that [ritueel](https://ritueel.id/) holds ([0024](/decisions/0024-root-custody-and-the-signing-api/)), a policy engine ([0007](/decisions/0007-policy-and-sealed-grants/)), a token issuer ([0017](/decisions/0017-identity-and-sign-in/)) and a node agent.

v1 gave workloads an identity and exchanged it for tokens (v1 ADR 0030, on `master`). It had no story for hosts.

## Decision

### Proofs and the exchange

- **Every machine credential is obtained the same way.** The machine presents a proof from something sluis trusts, `exchange@<boundary>` verifies it in an RFC 8693 token exchange, `policy` decides whether the facts of the proof allow the credential ([0007](/decisions/0007-policy-and-sealed-grants/)), and sluis returns a short-lived credential: a JWT-SVID, an X.509-SVID, an SSH certificate or a database client certificate ([0023](/decisions/0023-certificate-authorities/)), or cloud credentials through the adapter of that cloud ([0025](/decisions/0025-integrations/)).
- **Machines use no OAuth client credentials.** A machine proves what it is and holds no client secret.
- **A client that exchanges a person's token authenticates.** The request carries the client's authentication with the RFC 8693 parameters: the HTTP Basic header or `client_secret` today, and `private_key_jwt` when it is built ([0021](/decisions/0021-clients/), [0038](/decisions/0038-fapi-2-profile/); the fields are on [Contracts and errors](/reference/contracts/)). `exchange` checks it against the client's declaration and answers a failure with `invalid_client`, the same for a wrong secret and an unknown client (REQ-EXCH-018, REQ-EXCH-019). Built.
  - **One exception:** a public client of the `native` profile may exchange a token issued to itself without a secret, which is what `sluis token --audience` does. The client of the request must be the client the subject token was issued to (REQ-EXCH-007, REQ-EXCH-011). A public client that presents a secret, or that exchanges the token of another client, is refused with `invalid_client`.
  - **What an authenticated exchange issues.** The new token's `client_id` is the client that asked, as RFC 9068 section 2.2 says, and it carries no `act` claim: the core of a standard comes before its extensions. The audit record names both clients. `exchange` sends the requester with the person to `policy`, so policy on the person and on the requester limits what a confidential client may get, and a refusal is the one `invalid_target` (REQ-EXCH-003).
  - **Only a token issued to a public client of the `native` profile may be the subject of an exchange** until `private_key_jwt` is built, because `exchange` cannot yet check every way a client may authenticate (REQ-EXCH-020). The rule also keeps a confidential client's exchanged token from being exchanged again: its `aud` equals its `client_id`, like a sign-in token, with no marker. Lifting the rule needs a marker that only `exchange` sets, and a refusal on it, first.
- **Proof kinds are verifiers inside `exchange`,** holding public material only: roots, key sets, a registry of known devices. The config kind `ExchangeTrust` names the issuers, audiences and proof kinds an installation accepts, and the tenant of a workload: a workload's tenant is declared by the trust configuration for its issuer and is never reported by the workload. The published keys of a proof issuer (a cluster's ServiceAccount keys, a CI provider's OIDC keys) are cached in the temp kind `VerifierKeyCache`, for as long as HTTP caching allows and 24 hours at most. A proof that carries a `jti` is checked against the self-scoped temp kind `ReplayGuard` ([0004](/decisions/0004-the-data-model/)).

  | Verifier | Proof | State |
  |---|---|---|
  | own tokens | A token of the boundary's own issuer. | Built. |
  | `github-oidc` | A GitHub Actions OIDC token, checked against the provider's key set with `iss` and `aud` pinned. The built-in policy forbids signing artifacts for a workload that came from a pull request. | Built in part. |
  | `k8s-sa-token` | A projected Kubernetes ServiceAccount token, verified offline against the cluster's key set. KV reads with it ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)). | Planned. |
  | `aws-iid` | The EC2 instance identity document, accepted **once per instance**: `exchange` keeps the durable kind `SeenInstanceIdentity`. | Planned. |
  | `yubikey-attestation` | A YubiKey PIV attestation chained to **Yubico Attestation Root 1** through Yubico's published intermediates, checked against a registry of known cards. sluis ships that root, because `piv-go`'s built-in roots reject attestations from firmware 5.7 and newer (measured 2026-10-31). | Planned. |
  | `enrol-token` | A single-use or reusable enrolment token. | Planned. |
  | manual approval | A person approves the request in the console. | Planned. |
  | `tailscale-whois`, TPM | The Tailscale node identity of the caller's address; a TPM attestation. | Later. |

- **Renewal is by possession, with periodic re-attestation.** A machine renews with the credential it holds, over mutual TLS, and proves its original kind of proof again from time to time. Planned.

### SPIFFE identities

- **Followed:** the SPIFFE ID, the trust domain and its bundle, the X.509-SVID and the JWT-SVID. **Later:** SPIFFE Federation, the Workload Endpoint and the Workload API. **SPIRE is not deployed**; sluis plays its parts (the mapping is on [Machine identity](/concepts/identity/)).
- **ID paths.** The SPIFFE ID of a CI job and of a Kubernetes ServiceAccount is made by `kits/go/subject` from the verified claims of the proof, and is the `sub` of the token (built).

  | Subject | SPIFFE ID |
  |---|---|
  | A host | `spiffe://<trust-domain>/host/<name>` |
  | A Kubernetes ServiceAccount | `spiffe://<trust-domain>/k8s/<cluster>/ns/<namespace>/sa/<sa>` |
  | A sluis actor | `spiffe://<trust-domain>/sluis/<actor>`, for example `spiffe://prod.example.com/sluis/ca` |
  | A CI job | `spiffe://<trust-domain>/ci/github/<org>/<repo>` |
  | An AWS role | `spiffe://<trust-domain>/aws/<account>/role/<path>/<name>` |

- **An actor has one ID form.** The instance (a node or a unit) is a separate attribute: a DNS name in the X.509-SVID's subject alternative names, a claim in the JWT-SVID.
- **JWT-SVIDs come from the boundary's issuer:** `iss` is its URL, `sub` the SPIFFE ID ([0017](/decisions/0017-identity-and-sign-in/)).
- **Across boundaries, actors use mutual TLS with SPIFFE IDs.** A token for an actor in another boundary has that actor's SPIFFE ID as `aud`, and the target decides again with its own policy ([0008](/decisions/0008-boundaries-and-tenants/)).
- **Actors on a node get a token each.** `sluis-node` is a platform piece, not an actor ([0002](/decisions/0002-families-of-actors/)). It reads the caller's uid from the peer credentials of its Unix socket, maps it to an actor, and issues that actor a short-lived workload JWT. It signs through `key@` with the key purpose `workload-issuer`, a purpose of its own that `signer`'s token-signing key never shares ([0006](/decisions/0006-keys-and-sealing/)); `sluis-node` holds no key credential. The caller cannot name another actor. Planned.
- **One workload issuer per boundary.** Its key set holds one workload-issuer key per node, beside the token-signing keys. Per-node issuers are an option only for a fleet whose nodes run different sets of actors, where a cloud role can then trust only the nodes that run its actor.
- **An adapter exchanges the token for that cloud's credentials** (for example `AssumeRoleWithWebIdentity`), and the role's trust policy pins `sub` to the actor and its boundary.
- **Every actor uses its own credentials, and nothing mediates the data path.** sluis brokers credentials; it does not proxy data. KV is not a proxy to another system: it is a store with a protocol front ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)).
- **Where the platform gives an identity, sluis uses it:** the function's role on Lambda, and the service-account token on Kubernetes ([0003](/decisions/0003-units-transports-and-fusion/), [0011](/decisions/0011-runtimes-and-installations/)).

### Trust domains

- **The trust domain is the boundary.** Each environment has its own trust domain and its own CA intermediate, because the boundary owns the keys ([0008](/decisions/0008-boundaries-and-tenants/)). A host of one environment does not trust another environment's SVIDs unless a connection between the boundaries is declared. A host belongs to a boundary and to no tenant.

### Join cases

A host enrols with the same mechanism and receives a host X.509-SVID from `ca` under the host profile ([0023](/decisions/0023-certificate-authorities/)). A replacement sluis node enrols the same way and receives its host and workload identity. Host enrolment is planned, with the certificate authorities.

| Case | How | Notes |
|---|---|---|
| A. Manual approval | The host shows a short code; the approver sees the same code in the console. | Groups are assigned at approval. The request waits as the temp kind `PendingEnrolment`: 7 days by default, 30 at most. |
| B. Automation through user data | On EC2 the instance identity document, with no secret. Elsewhere a single-use token in the user data. | |
| C. Network | Tailscale WhoIs (later). | A plain IP address only for a low-privilege identity, or with approval. |
| D. Reusable token | A number of uses, an expiry, bound groups, a name pattern and a pre-approved flag. | Shown once. |
| E. Hardware attestation | A YubiKey; a TPM later. | |

- **Attributes keep their provenance.** What the verifier read from the proof is `attested`; what reviewed configuration says is `declared`; what an operator set in the console, audited, is `assigned`. What the host says about itself, and plain cloud tags, is `reported` and never grants anything ([0023](/decisions/0023-certificate-authorities/)).

### Host enrolment

All of it is planned.

- **The enrolment token never travels.** The client sends the token's ID and an HMAC, keyed with the token's secret, over its CSR and a nonce from the server. The nonce is the temp kind `EnrolmentNonce`, hashed, valid for 5 minutes.
- **The server keeps the HKDF-derived HMAC key, not the secret,** for single-use and reusable tokens alike, as the durable kind `EnrolmentToken`. It is sealed at `key@` under the boundary's `data` key, because an enrolment token has no tenant in its address ([0006](/decisions/0006-keys-and-sealing/)), so a copy of the store alone cannot enrol a host.
- **Enrolment is encrypted end to end through the edge.** The trust bundle carries the boundary's enrolment public key in a certificate that the boundary's intermediate issues. The host encrypts its proof and CSR to it with HPKE (RFC 9180). The private key is the key purpose `enrolment` of `key@` (P-384, key agreement; enabled on use), and `exchange` has `key@` open the request. The edge sees only ciphertext.
- **The host pins the roots' public keys.** An SPKI-hash pin is mandatory at enrolment, except in manual approval, where the person compares the pin the host shows. Web PKI is kept for reachability and as an extra layer; it is not the trust.
- **The enrolment API** has start, poll, renew over mutual TLS, leave, and bundle. `entry` routes it and `exchange` carries it out.
- **Policy has four actions:** `host.enrol`, `host.renew`, `enrol-token.create` and `enrolment.approve`. Every enrolment, renewal, approval and refusal is audited, and a pending request notifies the people who may approve it.

### The trust bundle

Planned.

- **The bundle is a static document of certificates in SPIFFE format, with no signature of its own.** Each certificate in it verifies itself: it chains through the boundary's intermediate to a root key in the root list. ritueel builds and signs the root list, and a pipeline outside ritueel publishes it ([0024](/decisions/0024-root-custody-and-the-signing-api/)), so an online signature over the bundle would add no trust.
- **`ca` renders the bundle as the exportable kind `TrustBundle`** from the config kind `TrustAnchors` and the `KeyCertificate` chains, and the exporter publishes it to a bucket as public material ([0009](/decisions/0009-export/)). Hosts fetch it from there, so enrolment and renewal do not depend on the compute of the issuer.
- **The `sluis` package ships `sluis trust sync`,** a timer and a oneshot service ([0013](/decisions/0013-release-train-packaging-and-distribution/)). They do nothing until `sluis trust enroll --url <url> --root-pin <spki-hash>` configures them, with `--root-pin` given once per root key. sluis generates no per-installation packages.
- **The sync checks every certificate and refuses rollback.** Each certificate in the bundle must chain through the intermediate to a root key in the pinned root list, and one that does not is dropped; a root list with a lower trust sequence, or a bundle with a lower `spiffe_sequence` than the last one, is refused.
- **It writes the anchors and runs the system's tool** (`update-ca-trust` or `update-ca-certificates`). The unit is hardened and may write only the anchors directory and the extracted store.
- **On Kubernetes, trust-manager reads the bundle as a source** from the bucket. sluis writes no Kubernetes object for it.

## Consequences

- One flow covers sluis's own nodes, hosts, CI and pods. A new kind of proof is a verifier with its own conformance tests, not a new flow.
- A replaced node needs no secret copied to it; on EC2 it needs nothing, and a copied identity document cannot enrol a second host.
- sluis runs no SPIRE server or agent and no second CA. The cost is implementing the SPIFFE formats itself, and workloads that want the Workload API wait for it.
- A host keeps working with its last good bundle when sluis is down, and a network attacker cannot roll it back. Rotating the enrolment key is a bundle change: a new certificate in the next bundle.
- One issuer per boundary keeps the cloud side small: one OIDC provider per boundary and one key per node, well inside AWS's limits of 100 providers per account and 100 keys per key set. The cost is that a compromised node can sign a token for any actor of its boundary until its key leaves the key set ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)).
- Every workload token on a board is signed at `key@` with a card key. The tokens are short-lived, and their holders cache them.
- The threats this addresses are in the [threat model](/security/threat-model/).

## Roads not taken

- **Deploy SPIRE.** Mature, with the Workload API today, but a second CA and root of trust beside ritueel's, an agent on every node and registration entries that repeat declared rules and Cedar.
- **Own formats instead of SPIFFE.** Every verifier and mesh would need sluis-specific code.
- **All of SPIFFE now.** A local endpoint for every workload before anyone needs it.
- **The installation as the trust domain.** Environments in one installation would share a trust domain; the trust domain follows the keys.
- **An issuer per node by default.** One provider and key set per node, and nothing gained where every node runs the same actors.
- **OAuth client credentials, or a local credential helper on a port.** A shared secret that works from anywhere, or credentials readable by every local process.
- **The enrolment token sent in the request, or TLS to the edge as the only protection.** Anything on the path, including the edge connector, could replay or read the proof.
- **Web PKI as the trust at enrolment.** Any public CA that issues for the sluis host name could stand in for sluis.
- **The trust bundle signed by a root.** A root would have to be online for every bundle change.
- **The trust bundle signed by an online bundle-signing key.** A bundle of certificates already verifies itself against the root-signed root list, so the signature adds no trust, and the key would be one more purpose, slot and policy.
- **A tenant binds its own CI workloads to grants through a delegated template.** The binding would have to prove that the repository belongs to the tenant, and a host belongs to a boundary and to no tenant. Workload bindings stay reviewed boundary config.
- **`sluis-node` signing with a card key of its own.** `key@` has exclusive access to the card, and a second holder of the card would undo that.
- **AppRole or a static token as a login for KV.** A secret ID is a long-lived client secret, which a machine proof replaces.

## Follow-ups

- Open: the proof kind for an AWS-role caller, and its checks. Nothing issues it yet, and v1's `aws:<account>:role/<path><name>` spelling is not reproduced ([0001](/decisions/0001-v1-and-v2-lines/)). The proof is designed when it is scheduled on the [roadmap](/roadmap/).
- Build: workload verifiers and the node agent ([#870](https://github.com/truvity/sluis/issues/870)); host enrolment and the trust bundle with the certificate authorities ([#909](https://github.com/truvity/sluis/issues/909)).
