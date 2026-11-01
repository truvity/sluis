---
title: Threat model
description: "What sluis v2 protects, who attacks it from where, the control that stops each threat at which layer, what is left, and the test that proves it."
status: target
sidebar:
  order: 0
---

This page lists the threats that sluis v2 is designed against. Each threat names the asset, the attacker's position, the control that stops it, what is left after the control, and the requirements whose test vectors prove the control. The requirements are in the [isolation area](/requirements/iso/) of the catalogue. Each test vector names the threat it proves (`T-n`).

The model follows the decisions of 2026-10-30: boundaries and their isolation levels ([ADR 0005](/decisions/0005-boundaries-and-the-isolation-ladder/)), defence in depth per port ([ADR 0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/)), issuers and tokens ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/)), workload identity ([ADR 0007](/decisions/0007-workload-identity-and-host-enrolment/)), SSH ([ADR 0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/)) and passkeys with step-up ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/)). Nothing here is built yet.

:::note[Proposed]
This page is the first version of the threat model required by [REQ-SEC-009](/requirements/sec/#REQ-SEC-009). A threat is added here before its control is designed, and a control is accepted only with a vector that fails at its own layer.
:::

## Scope and assets

The scope is one sluis operator's estate: a **front installation** (the shared sign-in, `entry`, `session`, `console` and `exchange`, and the group issuer at `sluis.example.com`) and one **boundary installation** per environment that runs on its own runtime (issuer at `sluis.<env>.example.com`), joined by declared trust.

A **boundary** is the unit of isolation:

- an **environment** boundary owns the machine plane: its `policy`, `ca`, `ssh-ca`, `signer` and `quota` actors, its keys, its issuer, its trust domain and its audit log;
- a **company** boundary (company A, B, C) owns the identity plane: its directory, its upstream identity providers, its integrations, its people records, its operator delegation and its audit tenant key. It holds **no authority key**;
- a **product** boundary (one company in one environment) is created only when that company's customers sign in through sluis.

How much a boundary has of its own is its **isolation level**:

| Level | What the boundary has of its own |
|---|---|
| L0 tenant | Nothing but code: rows kept apart by sluis's own logic. |
| L1 keys | Its own sealing key (which also seals grants), CA and signing keys. |
| L2 process | Its own actor instances, Unix user or cloud role, and state prefix. |
| L3 store | Its own state store (an rqlite cluster or a DynamoDB table), bucket and queue. |
| L4 runtime | Its own Kubernetes cluster, cloud account or hosts, and its own install file. |

Environments run at L2 at least, and an environment on its own runtime is L4. Companies are L2 with sealed data, in the front installation.

The assets, in the order an attacker wants them:

| Asset | Where it lives |
|---|---|
| Authority keys: each environment's CA, SSH CA and token-signing keys, and the sealing key that seals its grants | The keys port: a YubiKey behind the `key-holder`, or KMS. Never exported ([ADR 0012](/decisions/0012-cryptography-and-authority-keys/)). |
| Issued credentials: tokens, X.509 and SSH certificates, SVIDs | With the clients and workloads; short-lived and, where possible, sender-constrained. |
| Records: sessions, people, grants, hosts, enrolments, secrets of integrations | The state port, encrypted value by value. |
| Vendor credentials: OAuth client secrets, API tokens of each company's tenants | The secrets of one integration process each. |
| The audit trail | The blob port, written create-if-absent and witnessed. |
| Trust: the bundles hosts install and the policies each boundary compiles | Static, signed documents. |

## The three layers

Every port (keys, state, lease, blob, transport, index, witness, cloud, secrets, export) is protected by three independent layers, generated from one manifest:

1. **Layer 1, the port library in the actor.** The scope `<boundary>/<kind>/…` comes from the actor's own identity, never from the request. Every state value is encrypted with AES-256-GCM under a data key per boundary and record kind, wrapped by the boundary's key-encryption key, with the boundary, kind, key and version as additional authenticated data. Keys are chosen by purpose. Blobs are created only if absent.
2. **Layer 2, the driver's or backend's own check** of the caller's platform identity: the peer uid on a Unix socket, the IAM role, the Kubernetes service-account token or the client certificate.
3. **Layer 3, physical separation** at L3 and L4: another store, bucket, queue, account or host.

Encryption is part of layer 1 but stands on its own: with layers 2 and 3 off, a raw read returns ciphertext, and unwrapping the data key is a keys-port call that the key's own access list refuses.

A test vector runs one layer with the others switched off ([REQ-ISO-001](/requirements/iso/#REQ-ISO-001)), and a vector that is refused even with every layer off is void ([REQ-ISO-002](/requirements/iso/#REQ-ISO-002)). A driver whose backend cannot tell callers apart has no layer 2, so it is allowed only at L3 or above ([REQ-ISO-054](/requirements/iso/#REQ-ISO-054)).

What layer 2 is, per driver:

| Port | Driver | Layer 2 | Layer 3 |
|---|---|---|---|
| keys | `yubikey-piv` | `key-holder`: a socket per caller, a peer-uid check, content-aware signing | A card per node; more cards later |
| keys | `aws-kms` | Key policy with an encryption-context condition | Keys in the boundary's own account at L4 |
| state | `rqlite` | One cluster per boundary and owning actor; the owner may execute, declared readers may only query | Own cluster per boundary |
| state | `dynamodb` | IAM `dynamodb:LeadingKeys` per actor role | A table per boundary, in its own account at L4 |
| state | `postgres` | Forced row-level security by group-role membership; actors never own tables | A database per boundary |
| state | `embedded` | A file per Unix user | |
| blob | `s3-compatible` | IAM prefix conditions, or prefix-scoped temporary credentials | A bucket per boundary; Object Lock |
| blob | `fs` | A directory per Unix user, mode 0700 | |
| transport | `direct-unix`, `spool` | Peer uid at the receiver; a directory per producer | |
| transport | `sqs` | Queue policy per sender role | A queue per boundary |
| transport | `nats-jetstream` | NATS accounts and permission templates: account per tenant, user per actor instance | |
| cloud | web identity | Per-uid node tokens; the role's trust policy pins the actor at its boundary | |

## Attacker positions

| Position | Who | What they start with |
|---|---|---|
| **AP-1** Internet client | Anyone on the internet, including a holder of a copied token or cookie | The public endpoints |
| **AP-2** Compromised `entry` | Code execution in the front's parser and router | Every request in flight; no key, secret, state or outbound network |
| **AP-3** Compromised actor instance | Code execution in one actor, such as `ca@env-a`, `policy@env-b` or `session` | That actor's platform identity and its own keys, state and sockets |
| **AP-4** Compromised integration | Code execution in one integration, such as `google-signin@company-a` | That vendor tenant's credential |
| **AP-5** Compromised mediator | Code execution in the `key-holder` of one node | Every key on that node's card |
| **AP-6** Compromised store or its account | An admin of the state store, the bucket or the queue, or its cloud account | Every row, object and message in it |
| **AP-7** Operator mistake | A delegated operator on the console, or a wrong install file | That operator's delegation |
| **AP-8** Malicious neighbour | Another workload in a shared Kubernetes cluster, or another Unix user on a host | The same kernel, network and API server |
| **AP-9** Lying machine | A host or workload that enrols or renews with a false claim | An enrolment endpoint and whatever it can read on its machine |
| **AP-10** Company leaving | A company that leaves the operator's group, and the group it leaves | Its people, workloads and records at the time it leaves |

## Threats

Each threat says what is attacked, from where, what stops it and at which layer, what is left, and which requirements test it.

### Tokens and sign-in

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-1** | A copied token or code is replayed | Access and refresh tokens, codes | AP-1, AP-2 | Tokens are sender-constrained by DPoP or mutual TLS, so a copy without the client's key is refused; codes are bound by PKCE. | A thief who also has the client's private key; relying parties that take only bearer tokens, such as a cloud's web-identity endpoint, keep a short lifetime as their only bound. | [REQ-ISO-027](/requirements/iso/#REQ-ISO-027) |
| **T-2** | A token for one client is used at another | Access to relying parties | AP-1, a malicious relying party | `aud` is exactly one client ID registered at the issuer; a resource indicator is mapped to a client ID and never copied into `aud`. | A relying party that does not check `aud`. | [REQ-ISO-028](/requirements/iso/#REQ-ISO-028), [REQ-ISO-029](/requirements/iso/#REQ-ISO-029) |
| **T-3** | A token of one environment is accepted in another | Production relying parties | AP-1, AP-3 in a lower environment | An issuer, a key set and token-signing keys per environment; every printed relying-party snippet pins `iss` and `aud`. | A relying party configured by hand without the snippet. | [REQ-ISO-030](/requirements/iso/#REQ-ISO-030), [REQ-ISO-031](/requirements/iso/#REQ-ISO-031) |
| **T-4** | A compromised `entry` steals or alters what passes through it | Tokens, codes, enrolments in flight | AP-2 | `entry` holds no key, secret or state and has no outbound network (Unix sockets to its children only, read-only file system, closed parameter lists with fuzzing). Tokens are sender-constrained; enrolments are encrypted end to end with HPKE. | It can refuse service, and it can alter the pages it serves; the origin check of passkeys and PKCE limit what an altered page gains. | [REQ-ISO-042](/requirements/iso/#REQ-ISO-042), [REQ-ISO-043](/requirements/iso/#REQ-ISO-043), [REQ-ISO-047](/requirements/iso/#REQ-ISO-047), [REQ-ISO-027](/requirements/iso/#REQ-ISO-027) |
| **T-5** | A compromised front acts in a sensitive boundary on a person's behalf | Grants in production and management | AP-3 (`session`, `console`, `exchange` of the front) | The target boundary decides every request with its own policy, and a boundary that demands fresh MFA verifies the person's passkey assertion over a challenge it issued itself. | Boundaries whose policy asks for no fresh passkey are reachable within the person's grants for as long as the front is compromised. | [REQ-ISO-032](/requirements/iso/#REQ-ISO-032), [REQ-ISO-033](/requirements/iso/#REQ-ISO-033) |
| **T-6** | A passkey assertion is collected on another host | A person's passkey | AP-1 with a host under the same parent domain | One passkey serves every boundary, so the relying-party ID is the registrable parent; every verifier refuses an origin that is not one of the installation's own sluis hosts. | A compromised sluis host on the allow-list. | [REQ-ISO-034](/requirements/iso/#REQ-ISO-034), [REQ-AUTHN-050](/requirements/authn/#REQ-AUTHN-050) |
| **T-7** | A stolen browser session is used | That person's grants | AP-1 with a copied cookie | Each boundary sets its own freshness; a stale session is challenged with an RFC 9470 step-up. | The boundaries with a loose freshness rule, within that rule. | [REQ-ISO-035](/requirements/iso/#REQ-ISO-035) |

### Keys and signing

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-8** | An actor uses another boundary's key, or a key of another purpose | Authority keys | AP-3, such as `ca@env-a` | Layer 1 picks keys by purpose from the actor's identity; layer 2 is the `key-holder`'s access list or the KMS key policy; layer 3 holds the boundary's keys in its own account or hosts at L4. | Inside its own boundary and purpose, a compromised `ca` signs what its constraints allow until it is found in the issuance log. | [REQ-ISO-003](/requirements/iso/#REQ-ISO-003), [REQ-ISO-004](/requirements/iso/#REQ-ISO-004), [REQ-ISO-005](/requirements/iso/#REQ-ISO-005) |
| **T-9** | A key is made to sign another protocol's data or a wider certificate | Authority keys | AP-3 | Content-aware signing per purpose in the `key-holder`: the ca purpose signs only a parseable TBSCertificate within its constraints, ssh-ca only an OpenSSH certificate body, token purposes only a JWS signing input, and the OpenPGP binding purpose builds its own digest from the keys. | A wrong constraint set, which is configuration under review. KMS has no content check; there the key policy limits each key to one role. | [REQ-ISO-006](/requirements/iso/#REQ-ISO-006), [REQ-ISO-007](/requirements/iso/#REQ-ISO-007), [REQ-ISO-008](/requirements/iso/#REQ-ISO-008) |
| **T-10** | The mediator is compromised | Every key on one node's card | AP-5 | The `key-holder` is the only mediator: a memory-safe language, no network, seccomp, the PIN in locked memory, a rate limit and an audit record per signature. Backends that tell callers apart are used with per-actor credentials and no mediator. | It can sign with every key on that node's card, within the rate limit, until found in the audit log. Keeping environments on separate nodes and cards removes the cross-environment part. | [REQ-ISO-011](/requirements/iso/#REQ-ISO-011), [REQ-ISO-012](/requirements/iso/#REQ-ISO-012) |

### State, blobs and transport

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-11** | An actor reads or writes another actor's or boundary's records | Records | AP-3, AP-4 | Layer 1 scope from identity and encryption; layer 2 per driver (rqlite cluster per owning actor with query-only readers, DynamoDB `LeadingKeys`, PostgreSQL forced row-level security, file per user); layer 3 an own store at L3. Only the writer and declared readers may unwrap a kind's data key. | Its own records, and the records it is a declared reader of. | [REQ-ISO-003](/requirements/iso/#REQ-ISO-003), [REQ-ISO-013](/requirements/iso/#REQ-ISO-013) to [REQ-ISO-016](/requirements/iso/#REQ-ISO-016), [REQ-ISO-019](/requirements/iso/#REQ-ISO-019) |
| **T-12** | An actor writes into another's blob prefix or rewrites a written blob | The audit trail, exports, backups | AP-3 | Layer 1 scope and create-if-absent; layer 2 IAM prefix conditions or prefix-scoped temporary credentials; layer 3 a bucket per boundary, with Object Lock where the store has it. | Blobs not yet written can be raced for; the witnesses of the audit log detect a fork. | [REQ-ISO-021](/requirements/iso/#REQ-ISO-021) to [REQ-ISO-023](/requirements/iso/#REQ-ISO-023) |
| **T-13** | An actor sends messages as another actor | Audit events, work items | AP-3 | Layer 2 judges the sender at the receiver or the broker: peer uid, queue policy per sender role, or a NATS user per actor instance limited to its own subjects; layer 3 a queue per boundary. | A sender may flood its own queue; quotas and limits bound it. | [REQ-ISO-024](/requirements/iso/#REQ-ISO-024) to [REQ-ISO-026](/requirements/iso/#REQ-ISO-026) |
| **T-14** | The store or its account is compromised | Records | AP-6 | Every value is encrypted in the port library; the AAD binds a ciphertext to its boundary, kind, key and version, so a moved value does not decrypt; record keys hold no personal data; an own store per boundary at L3. | Metadata: key names, sizes, times and access patterns. Deletion and denial of service. A rollback of the whole store to an older snapshot is visible only against the audit log and its witnesses. | [REQ-ISO-017](/requirements/iso/#REQ-ISO-017), [REQ-ISO-018](/requirements/iso/#REQ-ISO-018), [REQ-ISO-020](/requirements/iso/#REQ-ISO-020) |

### Integrations, policy and companies

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-15** | A compromised integration reaches other tenants or signs for sluis | Vendor credentials, authority | AP-4 | One process per vendor tenant, with its own platform identity, secret and state prefix; integrations hold no authority key; egress only through the proxy. | That tenant's credential, and whatever it allows at the vendor. | [REQ-ISO-051](/requirements/iso/#REQ-ISO-051), [REQ-ISO-052](/requirements/iso/#REQ-ISO-052) |
| **T-16** | A subject of one company gets a grant in another company's scope | Company boundaries | AP-1 with a valid account, AP-7 | Company-ceiling guards compiled as Cedar `forbid`, which no `permit` overrides; a partner grant is the only exception. | A bug in a shared policy definition (accepted, below). | [REQ-ISO-036](/requirements/iso/#REQ-ISO-036) |
| **T-17** | One environment's policy leaks into another's bundle | Environment boundaries | AP-7 | Policy has one source and is compiled per environment; the build fails on a policy scoped to another environment. | As T-16. | [REQ-ISO-037](/requirements/iso/#REQ-ISO-037) |
| **T-18** | A grant decided in one environment is spent in another | Environment boundaries | AP-3 | A sealing key per environment; a signing actor refuses a grant that its own environment's sealing key does not open. | None beyond T-8. | [REQ-ISO-066](/requirements/iso/#REQ-ISO-066) |
| **T-19** | A person link is used to inherit grants across companies | Company boundaries | AP-1 with one linked account | A subject per directory account and a declared person link with no grant inheritance across it; SSH certificates carry the person key, not the grants. | None beyond T-16. | [REQ-ISO-058](/requirements/iso/#REQ-ISO-058) |

### Machine identity, enrolment and trust

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-20** | A workload or host gets a credential for another identity | Machine identities | AP-9 | Issuance bound to the requester: the subject of a certificate, SVID or workload token comes from the requester's authenticated identity, never from the request. | A wrong declared rule that gives one identity too wide a name. | [REQ-ISO-039](/requirements/iso/#REQ-ISO-039) |
| **T-21** | A caller from one boundary passes for an actor of another | Cross-boundary calls | AP-3 in another boundary | Mutual TLS with SPIFFE IDs, the trust domain being the boundary; a JWT-SVID across boundaries has the target's SPIFFE ID as its only `aud`; the target re-decides with its own policy. | What the target's own policy allows that trust domain. | [REQ-ISO-040](/requirements/iso/#REQ-ISO-040), [REQ-ISO-041](/requirements/iso/#REQ-ISO-041), [REQ-ISO-032](/requirements/iso/#REQ-ISO-032) |
| **T-22** | An enrolment proof is stolen, replayed or altered | Host and workload enrolment | AP-2, AP-9 | The enrolment token never travels: the client sends its ID and an HMAC over the CSR and a server nonce; tokens are single-use or counted, with an expiry; an instance identity document is accepted once per instance; the request is HPKE-encrypted end to end. | A token read on the target machine before its first use; bound groups and a short expiry limit it. | [REQ-ISO-044](/requirements/iso/#REQ-ISO-044) to [REQ-ISO-047](/requirements/iso/#REQ-ISO-047) |
| **T-23** | A host is made to trust the wrong roots | Trust bundles | AP-1 on the path, AP-6 for the bucket that serves bundles | The root list is signed by a root the host already holds, and an older version is refused; a lower `spiffe_sequence` is refused; the roots' public keys are pinned at enrolment except where a person compares the pin; web PKI is used for reachability only. | One root card with its PIN, until that root is revoked and the update reaches the hosts (accepted, below). | [REQ-ISO-048](/requirements/iso/#REQ-ISO-048) to [REQ-ISO-050](/requirements/iso/#REQ-ISO-050) |

### Operators, shared platforms and leaving

| ID | Threat | Asset | Position | Control | Residual | Test |
|---|---|---|---|---|---|---|
| **T-24** | A neighbour on a shared host or cluster uses an actor's socket or cloud role | Keys, records, cloud roles | AP-8 | Key-holder sockets in directories only their caller can open, plus a peer-uid check; a container mounts only its own actor's socket; per-uid node tokens and trust policies that pin the actor at its boundary. | Cluster admins and node root (accepted, below). | [REQ-ISO-009](/requirements/iso/#REQ-ISO-009), [REQ-ISO-010](/requirements/iso/#REQ-ISO-010), [REQ-ISO-056](/requirements/iso/#REQ-ISO-056), [REQ-ISO-057](/requirements/iso/#REQ-ISO-057) |
| **T-25** | An operator's mistake opens a boundary | Configuration, delegation | AP-7 | Console delegation per company and environment, every change audited under the operator's name; one generator for library scopes, ACLs, IAM and KMS policies, units, sockets and service accounts, with a drift test; `sluis config check` refuses an inconsistent installation. | A mistake inside one's own delegation; the deploying operator (accepted, below). | [REQ-ISO-053](/requirements/iso/#REQ-ISO-053) to [REQ-ISO-055](/requirements/iso/#REQ-ISO-055) |
| **T-26** | A company that leaves keeps access, or leaves its records readable | Company records, workloads | AP-10 | The promotion runbook: disconnect its company boundary, export and crypto-shred its audit tenant key, re-issue its workloads under the new CA, remove its person links. Certificates carry identity only, and hosts pin their own boundary's CA. | Copies taken before it left, and data outside sluis. | [REQ-ISO-059](/requirements/iso/#REQ-ISO-059), [REQ-ISO-060](/requirements/iso/#REQ-ISO-060) |

## Accepted risks

These are shared by design and are not mitigated by isolation. They are accepted by the maintainer and revisited when a boundary is promoted to its own operator.

- **The roots.** The roots are held by [ritueel](https://ritueel.id) and certify every boundary's intermediates. Any single root may change the root list, so whoever holds one root card and its PIN can sign things that hosts trust until that root is revoked and the update reaches them. Standard tools cannot require two roots to agree. A stolen card without its PIN locks after three wrong tries (T-23).
- **The operator who deploys sluis.** Whoever writes the install files and runs the deployment can change any boundary they deploy.
- **Cluster admins.** An admin of a shared Kubernetes cluster can mint service-account tokens and read mounted secrets of any pod on it.
- **Production `ca` reaching every company in production.** One environment's `ca` signs for every company in that environment; company boundaries hold no authority key.
- **Bugs in shared policy definitions.** Policy has one source, so a wrong definition is wrong in every environment it compiles into, until a test finds it.
- **Data outside sluis.** Secrets stores, observability and deployment tools of the operator's estate are outside this model.

## Related

- The [isolation requirements](/requirements/iso/) and their test vectors, and the [coverage](/requirements/coverage/) of the catalogue.
- The [standards register](/standards/), for SPIFFE, HPKE, RFC 9470 and WebAuthn.
- [Drivers](/concepts/drivers/) and [Keys](/concepts/keys/), for the ports and the key layouts.
