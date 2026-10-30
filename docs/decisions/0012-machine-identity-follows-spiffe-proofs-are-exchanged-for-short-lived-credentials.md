# 0012 — Machine identity follows SPIFFE: proofs are exchanged for short-lived credentials

**Status:** Proposed
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
The open points at the end of the decision are tracked in [issue #910](https://github.com/truvity/sluis/issues/910).

## Context

sluis gives credentials to machines as well as to people. Four kinds of machine ask for them:

- **sluis's own actors.** An audit writer on a node writes to a bucket. An actor in one boundary calls an actor in another.
- **Hosts.** A virtual machine, a board or an appliance that people log in to, or that needs a TLS certificate and the internal trust anchors.
- **CI jobs**, which hold a token from their CI platform.
- **Pods**, which hold a ServiceAccount token from their cluster.

v1 gave workloads an identity on both of its platforms and exchanged it for tokens (v1 ADR 0030, on `master`). It had no story for hosts, and it never had to enrol a node of its own.

v2 does. A board cluster loses a node and gets a replacement. A cloud instance starts from an auto scaling group with nothing on it but user data. A host somewhere in a lab has nothing but a person who can see its console. ADR [0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/) says that a replacement node enrols its identity through `sluis-node` (the node agent). It does not say how.

SPIFFE is the published standard for workload identity: an ID format, the trust domain and its bundle, and two document formats for the identity itself (X.509-SVID and JWT-SVID). SPIRE is its reference implementation, with a server, an agent on every node, node and workload attestors, and registration entries. sluis already has most of what SPIRE brings: a certificate authority with two roots ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)), a policy engine ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)), a token issuer and a node agent. The question is which parts of SPIFFE to follow and whether to run SPIRE.

Other records constrain the answer:

- Authority keys sign in place. Workload-issuer keys are hardware-held and published through a JWKS with no `x5c` ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).
- P-384 is the default ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)).
- A boundary is the unit of keys and trust ([0009](/decisions/0009-one-installation-per-boundary/)). Actors are named `<actor>@<boundary>` in every layer, for example `ca@prod`.
- The maintainer wants sluis to follow specifications rather than invent formats.

## Decision

### One mechanism: a proof, a token exchange, a credential

- **Every machine credential is obtained the same way.** The machine presents a proof from something sluis trusts. `exchange@<boundary>` verifies it in an RFC 8693 token exchange. sluis returns a short-lived credential: a JWT-SVID, an X.509-SVID, an SSH certificate ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)), a database client certificate ([0033](/decisions/0033-database-access-uses-short-lived-credentials/)), or cloud credentials through a cloud driver.
- **Machines use no OAuth client credentials.** A machine proves what it is with a workload proof. It does not hold a client secret. RFC 7523 JWT client assertions are a possible middle ground later.
- **Proof kinds are verifier drivers of `exchange`.** A verifier driver holds public material only: roots, key sets, a registry of known devices. It holds no secret.

  | When | Verifier driver | Proof |
  |---|---|---|
  | First release | `aws-iid` | The EC2 instance identity document. It is accepted **once per instance**, and it is the primary path on EC2. |
  | First release | `k8s-sa-token` | A projected Kubernetes ServiceAccount token. |
  | First release | `github-oidc` | A GitHub Actions OIDC token. |
  | First release | `yubikey-attestation` | A YubiKey attestation, checked against the Yubico roots and a registry of known cards. |
  | First release | `enrol-token` | A single-use or reusable enrolment token (below). |
  | First release | manual approval | A person approves the request in the console. |
  | Next | `tailscale-whois` | The Tailscale node identity of the caller's address. |
  | Later | TPM | A TPM attestation. |

- **Renewal is by possession, with periodic re-attestation.** A machine renews with the credential it holds, over mutual TLS. It proves its original kind of proof again from time to time.

### Actors on a node get a token each

- **`sluis-node` issues a short-lived workload JWT to each actor on its node.** It reads the caller's uid from the peer credentials of its Unix socket and maps the uid to an actor. The caller cannot name another actor. The token is signed with the node's non-extractable key.
- **Each node is its own issuer of these tokens.** A cloud role trusts only the issuers of the nodes that run its actor, so a compromised node can reach only the roles of its own actors.
- **A cloud driver exchanges the token for that cloud's credentials**, for example with `AssumeRoleWithWebIdentity` on AWS. The role's trust policy pins `sub` to the actor and its boundary.
- **Every actor uses its own credentials, and nothing mediates the data path.** Where a backend can tell callers apart, the actor reaches it with credentials of its own. sluis brokers credentials. It does not proxy data. The node's per-actor tokens, cloud token exchange, database client certificates from `ca` and NATS credentials from sluis are all forms of this.
- **Where the platform gives an identity, sluis uses it.** On serverless it is the function's role. On Kubernetes it is a projected ServiceAccount token per container by default, EKS Pod Identity for a pod with one actor, and `sluis-node` only as a fallback. The Kubernetes detail is ADR 0046.
- **The issuer keys follow the key rule.** They are hardware-held and published through a JWKS, with no `x5c` ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).

### Hosts, and replacement sluis nodes, enrol the same way

- **A host enrols with the same mechanism** and receives a host X.509-SVID from `ca`, under a host profile.
- **A replacement sluis node enrols the same way.** This is how the node agent of [0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/) gets its identity. The ceremony that certifies a node's intermediate with the root cards stays a separate step (ADR 0032).
- **There are five ways to join:**

  | Case | How | Notes |
  |---|---|---|
  | A. Manual approval | The host shows a short code. The person approving sees the same code in the console. | The groups are assigned at approval. |
  | B. Automation through user data | On EC2 the instance identity document, with no secret. Elsewhere a single-use token put into the user data. | |
  | C. Network | Tailscale WhoIs. | A plain IP address only for a low-privilege identity, or with approval. |
  | D. Reusable token | Like a Tailscale auth key: a number of uses, an expiry, bound groups, a name pattern and a pre-approved flag. | Stored as a hash and shown once. |
  | E. Hardware attestation | A YubiKey now, a TPM later. | |

- **Attributes keep their provenance.** What the verifier read from the proof is `attested`. What reviewed configuration says is `declared`. What an operator set in the console, audited, is `assigned`. What the host says about itself, and plain cloud tags, is `reported` and never grants anything. SSH uses these attributes ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)).

### The enrolment protocol

- **The enrolment token never travels.** The client sends the token's ID and an HMAC over its CSR and a nonce from the server, keyed with the token's secret.
- **Enrolment is encrypted end to end through the edge.** The signed trust bundle carries an enrolment public key. The host encrypts its proof and its CSR to that key with HPKE (RFC 9180). The private key is held by `key-holder` or KMS, and `exchange` opens the request through the keys port. The edge sees only ciphertext. (inferred)
- **The host pins the roots.** The fingerprints of both roots are mandatory at enrolment, except in manual approval, where the person compares the fingerprint the host shows. The pin updates itself through bundles signed by the current roots. Web PKI is kept so that the host can reach sluis, and as an extra layer. It is not the trust.
- **The enrolment API** has start, poll, renew over mutual TLS, leave, and bundle. `entry` routes it and `exchange` carries it out.
- **Policy has four actions:** `host.enrol`, `host.renew`, `enrol-token.create` and `enrolment.approve`.
- **Every enrolment, renewal, approval and refusal is audited**, and a pending request notifies the people who may approve it.

### SPIFFE: what is followed now and later

- **Implemented now:** the SPIFFE ID, the trust domain and its bundle, the X.509-SVID and the JWT-SVID.
- **Later:** SPIFFE Federation, the Workload Endpoint and the Workload API.
- **SPIRE is not deployed.** sluis follows the formats and plays SPIRE's parts itself.
- **The trust domain is the boundary.** Each environment has its own trust domain, its own bundle and its own CA. An earlier decision made the trust domain the installation. The boundary model replaces it, because the boundary is what owns the keys.
- **ID paths:**

  | Subject | SPIFFE ID |
  |---|---|
  | A host | `spiffe://<trust-domain>/host/<name>` |
  | A Kubernetes ServiceAccount | `spiffe://<trust-domain>/k8s/<cluster>/ns/<namespace>/sa/<sa>` |
  | An actor on a node | `spiffe://<trust-domain>/node/<node>/actor/<actor>` |
  | A CI job | `spiffe://<trust-domain>/ci/github/<org>/<repo>` |

  The actor naming of the boundaries record names an actor `spiffe://<trust-domain>/sluis/<actor>`, for example `spiffe://prod.example.com/sluis/ca`.
- **JWT-SVIDs come from the boundary's issuer.** `iss` is the boundary's issuer URL, such as `https://sluis.prod.example.com`, and `sub` is the SPIFFE ID.
- **Across boundaries, actors use mutual TLS with SPIFFE IDs.** A token for an actor in another boundary has that actor's SPIFFE ID as `aud`. The target decides again with its own policy.
- **How SPIRE's parts map to sluis** (prior art, not a dependency):

  | SPIRE | sluis |
  |---|---|
  | Node attestors | Verifier drivers of `exchange` |
  | Workload attestors | The peer uid on the node agent's socket; the ServiceAccount token on Kubernetes |
  | Registration entries | Declared rules plus Cedar policy |
  | The SPIRE server's CA | `ca`, under two roots |
  | The SPIRE agent | `sluis-node` on a node; nothing on serverless (inferred) |
  | Trust bundle endpoint | The signed bundle, published statically (inferred) |

### The trust bundle reaches hosts through one generic package

- **The `sluis` package ships `sluis trust sync`,** a `sluis-trust-sync.timer` and a oneshot service. They do nothing until `sluis trust enroll --url … --fingerprint …` configures them.
- **The sync fetches the bundle in SPIFFE format and checks it.** The signature must be by a key certified by both roots. A bundle with a lower `spiffe_sequence` than the last one is refused, so an old bundle cannot be replayed.
- **It writes the anchors and runs the system's tool**, `update-ca-trust` or `update-ca-certificates`.
- **The unit is hardened.** It may write only the anchors directory and the extracted store.
- **sluis generates no packages.** The same package serves every installation, and its configuration says what to pull.
- **On Kubernetes** the bundle is a trust-manager Bundle source or a ClusterTrustBundle.

### Open points

These are tracked in [issue #910](https://github.com/truvity/sluis/issues/910) and keep this record Proposed:

1. **The per-node issuer: its URL and how it is published.** Each node is its own issuer of per-actor tokens, so that a compromised node reaches only its own actors' roles. JWT-SVIDs come from the boundary's issuer. Whether the node's tokens are JWT-SVIDs, and under which URL and bucket layout the per-node discovery documents and key sets are published, is not decided.
2. **The two forms of an actor's ID.** The naming gives `spiffe://<trust-domain>/sluis/<actor>` for an actor and the ID paths give `/node/<node>/actor/<actor>`. Reading the first as the actor in any instance and the second as one instance on one node is an inference to be confirmed.
3. **How a reusable token is kept.** Case D stores the token as a hash. The HMAC check needs a key derived from the token's secret, and the record kind that holds it must say what the server keeps.

## Consequences

- One flow covers sluis's own nodes, hosts, CI and pods. Each new kind of proof is a verifier driver with its own conformance tests, not a new flow.
- A node that is replaced needs no secret copied to it. On EC2 it needs nothing at all, because the instance identity document is its proof. The document is accepted once per instance, so a copied one cannot enrol a second host.
- sluis runs no SPIRE server, no SPIRE agent and no second CA. The cost is that sluis implements the SPIFFE formats itself. Workloads that want the Workload API wait for it.
- Moving the trust domain to the boundary means a host of one environment does not trust another environment's SVIDs by default. A connection between boundaries is declared.
- The trust bundle is a signed static document. A host keeps working with its last good bundle when sluis is down, and an attacker who controls the network cannot roll it back.
- The enrolment key sits in the bundle, so rotating it is a bundle change.
- The work that follows:
  - **server:** the enrolment API; the verifier drivers; state for enrolments, token hashes and the host registry with attribute provenance; the four policy actions; the host profile in `ca`; audit and notifications;
  - **client:** `sluis-host enroll` detects the platform; the key is held in a card if there is one, else a TPM, else a file; the matching code; status, renew, leave and re-enrol; `sluis host user-data --group <group>` prints a cloud-init snippet, as text and not as an artifact;
  - **console:** a list of hosts, a queue of pending requests, token management with each token shown once, an "Add host" page, and the rules shown read-only.
- The standards register lists the SPIFFE ID, the trust domain and bundle, the X.509-SVID and the JWT-SVID as Full, and SPIFFE Federation, the Workload Endpoint and the Workload API as Planned. RFC 9180 is added for enrolment (inferred). A "Machine identity" page carries the mapping table above.

## Roads not taken

### Deploy SPIRE

- Pros: a mature implementation, with many attestors, the Workload API and federation today.
- Cons: a second CA and a second root of trust beside sluis's two roots, a server and an agent on every node, its own datastore, and registration entries that repeat what declared rules and Cedar already say. (inferred)
- Why not: sluis already holds the CA, the verifiers and the policy. It follows the SPIFFE formats and does not run a second system for them.

### Own formats instead of SPIFFE

- Pros: no constraint from a specification. (inferred)
- Cons: every verifier and every mesh would need sluis-specific code. (inferred)
- Why not: the maintainer wants sluis to follow specifications rather than invent formats.

### All of SPIFFE now, including Federation and the Workload API

- Pros: workloads could fetch SVIDs from a local endpoint, and two trust domains could federate in the standard way.
- Cons: a local endpoint for every workload, and work before anyone needs it. (inferred)
- Why not: the four parts that sluis needs now come first. The other three are planned.

### The trust domain is the installation

- Pros: one trust domain, one bundle and one CA per installation.
- Cons: one installation can hold several boundaries, and a boundary owns its keys, so environments would share a trust domain.
- Why not: the boundary model replaced it. The trust domain follows the keys.

### OAuth client credentials for machines

- Pros: every OAuth library supports them.
- Cons: a shared secret for each machine, to store, deliver and rotate. A leaked secret works from anywhere. (inferred)
- Why not: a machine proves what it is with a workload proof. RFC 7523 assertions remain a later middle ground.

### A local credential helper that serves cloud credentials on a port

- Pros: the cloud SDKs support it out of the box.
- Cons: every local process can read the credentials, which removes the separation between actors.
- Why not: the node agent checks the caller's uid on each request.

### The enrolment token sent in the request

- Pros: simple, and what most tools do.
- Cons: anything on the path that sees the request can replay the token. (inferred)
- Why not: the token never travels. The HMAC binds the request to the CSR and to the server's nonce.

### TLS to the edge as the only protection of enrolment

- Pros: nothing extra to build.
- Cons: the edge connector terminates TLS and would see every proof and CSR. (inferred)
- Why not: HPKE to a key in the signed bundle keeps the request opaque to the edge.

### Web PKI as the trust at enrolment

- Pros: no fingerprint to type.
- Cons: any public CA that issues a certificate for the sluis host name could stand in for sluis. (inferred)
- Why not: the root fingerprint is mandatory, except in manual approval where a person compares it. Web PKI stays for reachability.

### The instance identity document accepted on every request

- Pros: no state about which instances have enrolled. (inferred)
- Cons: the document does not change for the life of an instance, so a copy of it would work again. (inferred)
- Why not: it is accepted once per instance, and renewal is by possession.

### sluis generates a trust package for each installation

- Pros: the package would carry the anchors and the URL, with nothing to configure.
- Cons: the sluis runtime would have to build deployment artifacts on the fly.
- Why not: one generic package pulls what its configuration says, and sluis generates no packages.

### Trust sync inside the SSH host service

- Pros: one service on the host instead of two. (inferred)
- Cons: hosts that need the anchors but not SSH would need the SSH service, and the root service would grow. (inferred)
- Why not: the trust sync is in the `sluis` package and `sluis-host` stays the SSH host service.
