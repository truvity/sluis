---
title: Components
description: "L2, inside the box: components with one responsibility each, the integration contracts, ports, the call graph, and one writer per record kind."
status: target
sidebar:
  order: 2
---

At the second zoom level the [black box](/architecture/context/) is split into components. Each component has one responsibility, a ConnectRPC contract, and a short list of other components it may call. A component is not a process: [L3](/architecture/processes/) decides which components run together.

Every major component is a ConnectRPC contract, even when caller and callee share a process. In-process calls use an in-memory transport, so moving a component into its own process changes configuration, not code.

## Component groups

| Group | Components | Responsibility |
| ----- | ---------- | -------------- |
| **Front** | web surface, OIDC endpoints, CLI and CI API, webhook ingress, PKI endpoint, exports API, console HTTP, audit query HTTP | Parse the wire protocol, apply uniform hardening (headers, cookies, throttling, uniform refusals), and delegate every decision to an owner. A front component decides nothing |
| **Identity path** | authn, authorization flows, sessions, token exchange, token issuer, client registry, key ring, proof verifier, facts broker, policy, mint broker, tick scheduler, grants export, console | Own sessions, codes, refresh tokens and clients; verify proofs; turn facts into grants; ask for credentials to be minted |
| **Certificates and quota** | PKI, SSH CA, host enrolment, quota, backup, restore | Issue X.509 and SSH certificates, admit metered operations, and protect and recover the install (see [Backup and restore](/concepts/backup/)). Quota (the quota engine and its quota state) is a separate piece, combinable with sluis, and its placement is derived from the port and driver rules, see [Ports and drivers](/concepts/drivers/#where-an-actor-runs-is-derived) ([OD-38](/open-decisions/#od-38), [OD-33](/open-decisions/#od-33)) |
| **Audit and trust** | audit writer, witness coordinator, audit indexer, audit query, trust log, static publisher | Take records from every other component, seal them in batches, sign the batch heads through the keys port, and serve them back ([Audit](/concepts/audit/)). The indexer, the search index and the reader are the separate Search piece; its placement is derived from the port and driver rules, see [Ports and drivers](/concepts/drivers/#where-an-actor-runs-is-derived) ([OD-32](/open-decisions/#od-32)) |
| **Integrations** | upstream, workload verifier, facts source, credential minter, provisioner, notifier | Talk to one external system each ([Integrations](/concepts/integrations/)). Return facts, or carry out effects core has authorized |
| **Ports** | keys, state, lease, blob, transport, index, cloud, secrets and export, and for audit also witness | Libraries linked into the component that needs them. Never a hop |
| **Drivers** | the technology behind one port | See [Drivers](/concepts/drivers/) and the table below |

The groups map onto [actors](/architecture/processes/) (see also [Actors](/concepts/actors/)): `entry` hosts the front only, parsing, validating against closed parameter lists and routing; `session` hosts authn, the authorization flows and sessions; `exchange` hosts token exchange, the proof verifier, the client registry and host enrolment; `console` hosts the console; `signer` hosts the key ring; `policy` hosts the decision point; `ca` hosts PKI; `ssh-ca` hosts the SSH CA; the audit components split across the `writer`, the `witness`, the `indexer` and the `reader`; `quota` is its own actor; and each integration runs as its own process, one per vendor tenant and credential. There is no notary actor (agreed, [OD-39](/open-decisions/#od-39)): the writer asks `key-holder` to sign, and its access list lets only the writer use the log key.

The console component, in the `console` actor, is the operator API and the browser's backend, reached through `entry`: the session is an HttpOnly cookie that `session` checks, and the browser holds no token. It reads through the components it calls and routes each operator-set write to the component that owns the record kind, which validates it ([ADR 0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). The single-page application is a separate artifact, served on the same origin by each runtime's static host.

## Integration contracts

All integrations share one base contract, `Integration`, and implement one or more of six contracts.

| Contract | What it does | Facts only? | Implementations |
| -------- | ------------ | ----------- | --------------- |
| `Integration` (base) | Describe itself, probe health, connect and disconnect, link a person's account, handle a webhook | n/a | Every integration |
| `Upstream` | Authenticate a person elsewhere: begin and complete a sign-in, verify an upstream access token | Yes | Workspace directory sign-in (one per tenant, such as `google-signin@<company>`), generic OIDC (`oidc@<name>`), peer sluis |
| `WorkloadVerifier` | Verify a machine proof. Needs no secret | Yes | GitHub Actions, Kubernetes, AWS web identity, peer sluis |
| `FactsSource` | Answer what an external system knows about a subject or group, with freshness; refresh a snapshot | Yes | Workspace directory (`google-directory@<company>`, a credential of its own), GitHub teams, chat workspace users, generic OIDC claims |
| `CredentialMinter` | Produce a short-lived downstream credential for a scope core has authorized | Effector | Cloud API tokens, GitHub installation tokens, peer sluis |
| `Provisioner` | Make an external system equal to a desired state core supplies | Effector | GitHub team membership, chat channels, wiki users and groups, on-call rotations |
| `Notifier` | Deliver a message to a person or place; receive interactions through the webhook | Yes | Chat workspace, Telegram |

Rules shared by the contracts:

- **Opaque references.** Core never parses an identifier an integration returns. Subjects and groups are `{namespace, id}` pairs; policy can match only on the namespace and on named attributes.
- **Attributes, not vendor structs.** Everything vendor-shaped travels as namespaced attributes (`github.repository`, `k8s.namespace`). Each integration declares its attribute names and types when it describes itself, so a policy file is validated at load without core knowing the vendor.
- **A closed error model.** `UNAVAILABLE`, `UPSTREAM_REJECTED`, `INVALID`, `NOT_FOUND`, `RATE_LIMITED`, `UNSUPPORTED`, `STALE`. No vendor error text crosses the boundary.
- **Freshness is part of every fact.** A fact carries `as_of`, `max_age` and whether it is authoritative. "I could not ask" is not "gone".
- **The same protobuf on every transport.** A Unix socket with peer credentials locally, a Lambda invoke on AWS, HTTPS with mTLS remotely.
- **One process per credential.** An integration is named `<vendor>[-<capability>]@<tenant>`, and a vendor whose capabilities use different credentials is split ([Naming](/concepts/actors/#naming)).

Six of the sixteen integrations that v1 lists call no external system: databases, hosts, cert-manager, the GitOps controllers, MCP, and SSH certificate login. They become **client profiles** (an OIDC client entry plus a resource profile), not integrations, which leaves eleven real ones.

### Integrations return facts, or carry out authorized effects

Four of the six contracts only return facts. A credential minter and a provisioner are effectors by nature, so two conditions keep them from deciding anything:

1. **Core decides, the integration executes.** A minter receives a **mint grant sealed by `policy` under the sealing key** (authenticated encryption, with `purpose=grant` in the encryption context, opened only by sluis itself): the decision, the policy digest and the hash of the scope. It refuses a scope the grant does not cover and cannot lengthen the lifetime. A provisioner pulls a desired state **addressed by its digest** from a read-only prefix that core writes. It never asks core a question.
2. **Business limits live in core.** Caller caps, mint rates and which group may hold which role are policy or quota, checked before the minter is called. An integration enforces only technical limits: the vendor's API rate and maximum lifetime.

The mint flow is drawn in two steps.

Step 1: the mint broker verifies the proof and asks policy.

```mermaid
sequenceDiagram
  participant C as Caller
  participant F as CLI and CI API
  participant M as mint broker
  participant P as proof verifier
  participant D as policy
  C->>F: request credential
  F->>M: request
  M->>P: verify the proof
  P-->>M: verified subject
  M->>D: decide mint
  D-->>M: decision, sealed grant
```

The caller asks for a credential of a kind and a scope, with a proof. The mint broker asks policy to decide with the subject, the facts, the kind and the scope. The decision carries the scope bound, the ttl cap and the sealed mint grant.

Step 2: the mint broker admits the operation and the minter mints.

```mermaid
sequenceDiagram
  participant F as CLI and CI API
  participant M as mint broker
  participant Q as quota
  participant X as credential minter
  participant A as audit
  M->>Q: admit
  Q-->>M: admitted
  M->>X: mint
  X-->>M: credential, handle
  M-)A: emit the mint record
  M-->>F: credential
```

Quota admits by operation id. The minter receives the kind, the scope, the ttl, the operation id and the mint grant, and returns the credential, its expiry and a handle. The emitted mint record never holds the secret. The CLI and CI API hands the credential to the caller.

## Ports

A port is the narrowest interface that every driver can honour and every user needs. Ports are libraries linked into the component that uses them; a port call is never a network hop. A driver is the technology behind a port (see [Concepts](/concepts/)).

| Port | Operations | Notes |
| ---- | ---------- | ----- |
| **state** | `Get`, `Create`, `CompareAndSwap`, `DeleteIf`, `List`, `WriteBatch`, `Adjust` (proposed) | Two consistency levels, strong and local. An atomic write batch keeps the audit batch row, its dedupe record and the index cursor together. Quota state is the state port with compare-and-set; a conditional adjust (`Adjust`), like `DeleteIf` and `List`, is a proposed further operation (ADR 0057; the decided operations are in [ADR 0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)). The port library scopes every call to `<boundary>/<kind>/…` from the actor's own identity, so no one builds key strings, and it encrypts every value ([Data](/concepts/data/#every-value-is-encrypted)) |
| **index** | `Apply`, `Cursor`, `Query`, `Rebuild` | The search index of the audit trail. Query syntax never leaks past the driver. Drivers: sqlite, rqlite, postgres, or none (a scan) ([ADR 0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)) |
| **keys** | `Describe`, `Sign`, `Wrap`, `Unwrap`, `Derive`, `Agree`, `PublicKey` | See below. Callers name a key purpose, never an ARN or a slot |
| **blob** | `PutIfAbsent`, `Put`, `Get`, `List`, `Delete`, `Capabilities` | Immutability is the common case. `PutIfAbsent` is an atomic create-if-absent that every blob driver must give (`If-None-Match: *` on S3 and R2, an exclusive link or rename on `fs`), which is what fences the audit writer; a backend without it, such as Garage today, is not supported ([Drivers](/concepts/drivers/#create-if-absent)). `Capabilities` reports the lock mode (none, removable or compliance), versioning and maximum object size. The audit log is a blob with a lock mode. One `s3-compatible` driver serves S3 and R2, with credentials from the cloud port |
| **transport** | `put`, `receive`, `ack`, or `stream` | How a record gets from an emitter (a library in every actor) to the writer: **direct** (a stream to the writer) or **queue** (spool, SQS, NATS JetStream). Carries a durability level on put. Visibility timeouts and dead-letter queues stay in the driver. It replaces the older queue port and "outbox" |
| **lease** | `Acquire(name, ttl)`, `Renew`, `Release` | Named leases with fencing tokens, one per target, not "one leader", built on the state store and independent of the Raft leader. Needed by an actor that declares instances: exactly 1 when there is more than one node. Where the target store can fence, the fence is in that store ([Data](/concepts/data/#leases-and-fencing-tokens)) |
| **cloud** | `Invoke`, `InvokeAsync`, `CallerIdentity` | How a process calls another and learns who called it. `CallerIdentity` returns a normalized `{class, id}` whatever proves it: a peer credential, a platform token, a certificate. Its drivers also hand out cloud credentials: AWS STS, GCP, Azure and Cloudflare R2 temporary credentials |
| **secrets** (proposed) | `Get`, `Put` against a revision, `Delete`, `List` | Whole values under addresses: sluis's own secrets and its integrations'. A listing returns names, never values. Not a general secrets engine: see [Secrets and export](/concepts/secrets/) |
| **export** (proposed) | `Put`, `Delete`, `Status` | A small, typed set of documents that something outside sluis may read. File and bucket drops are age-encrypted to the consumer's own recipient |
| **witness** | `cosign` | The one port audit adds to the shared ones. Drivers: none, peer (two or more nodes) or external. A witness is optional everywhere, and `sluis config check` warns when there is none ([OD-36](/open-decisions/#od-36)) |

Each driver is a service started by the platform that speaks ConnectRPC over a Unix socket; nobody execs a driver. A driver declares `instance: per-actor | shared`, and every port is protected in three layers: the port library, the backend's own check of the caller, and physical separation at L3 and above ([Defence in depth](/concepts/drivers/#defence-in-depth)). See [Processes](/architecture/processes/). The drivers behind each port, what each requires, and which fit which runtime are on [Ports and drivers](/concepts/drivers/).

### Actors and the ports they require

Every actor declares which ports it requires (R), may use (O), or does not use (–). The actors are described on [Actors](/concepts/actors/), and the drivers on [Ports and drivers](/concepts/drivers/). Every actor is a producer on the transport port, because every success is audited.

| Actor | keys | state | lease | blob | transport | index | witness | cloud |
| ----- | ---- | ----- | ----- | ---- | --------- | ----- | ------- | ----- |
| `entry` | – | – | – | – | R | – | – | – |
| `session` | R | R | – | – | R | – | – | – |
| `console` | – | – | – | – | R | – | – | – |
| `exchange` | R | R | – | – | R | – | – | – |
| `signer` | R | R | O | O | R | – | – | – |
| `policy` | R | – | – | – | R | – | – | – |
| `ca` | R | R | – | O | R | – | – | – |
| `ssh-ca` | R | R | – | O | R | – | – | – |
| `quota` | – | R | – | – | R | – | – | – |
| Upstream | O | O | – | – | R | – | – | – |
| WorkloadVerifier | – | O | – | – | R | – | – | – |
| FactsSource | – | R | O | – | R | – | – | O |
| CredentialMinter | O | O | – | – | R | – | – | O |
| Provisioner | – | O | O | R | R | – | – | O |
| Notifier | – | O | – | – | R | – | – | – |
| `sluis-node` | R | – | – | R | R | – | – | – |
| `key-holder` | – | – | – | – | R | – | – | – |
| third-party app | O | – | – | – | R | – | – | – |

The audit actors, with the same legend (agreed, [OD-39](/open-decisions/#od-39)). The cell marked * is the one place an actor receives from the transport instead of putting into it.

| Actor | keys | state | lease | blob | transport | index | witness | cloud |
| ----- | ---- | ----- | ----- | ---- | --------- | ----- | ------- | ----- |
| `writer` | R | – | O | R | R* | – | O | O |
| `witness` | R | R | – | R | – | – | – | – |
| `indexer` | – | – | O | R | – | R | – | – |
| `reader` | – | – | – | R | – | R | – | – |

Three actors declare **instances: exactly 1**: the audit writer (one per log), the FactsSource reconciler and the indexer. When an installation has more than one node, or its nodes are elastic, such an actor needs the lease port; on serverless the platform's concurrency limit of 1 may stand in for it.

### The keys port

The keys port binds a **key purpose** to a backend and refuses a backend that is too weak for the purpose when the configuration loads. The code refuses a software key in production; configuration picks among the modes the installation's runtime allows. The key purposes are the unit callers name: `token-signing`, `ca`, `ssh-user`, `ssh-host`, `log`, `sealing`, `enrolment`, and one per cloud federation. There is no `grant` purpose: a mint grant is sealed under the `sealing` key with `purpose=grant` in the encryption context. Callers never see an ARN, a slot or a path. Each boundary has its own keys, and a purpose that an installation does not enable has none. On a YubiKey the slot of each purpose comes from the card slot map, whose version 1 defaults are on [Keys](/concepts/keys/#yubikey-layout); the sharing and rotation rules are on the same page.

| Key purpose | Requires | On AWS KMS | On a YubiKey (PIV) |
| -------- | -------- | ---------- | ------------------ |
| `token-signing` | Never exportable; used by the `signer` actor only; a key of its own, never the CA key, with a non-CA certificate for signing only that the intermediate issues | One `ECC_NIST_P384` key; tokens are signed directly | Slot 9a; about 8 signatures per second |
| `ca` (X.509) | Never exportable; used by the `ca` actor only; one key per trust domain; present only if the installation issues certificates | One `ECC_NIST_P384` key per trust domain | Slot 9c |
| `ssh-user`, `ssh-host` | Never exportable; used by the `ssh-ca` actor only; present only if enabled | One `ECC_NIST_P384` key each | Slots 82 and 83 |
| `log` | Never exportable; used by the audit `writer` only, through `key-holder` or a key policy; one key signs both the audit log and the usage log, and each signed batch head names its log | One `ECC_NIST_P384` key for the installation | The node's own key in slot 84; a cluster keeps one log per node and peers witness it |
| `sealing` | Wraps and unwraps; takes an encryption context. Wraps the data key of each (boundary, record kind), the data keys of tenants and the cookie-sealing key of `session`, and seals the mint grants (`purpose=grant`, opened only by sluis itself) | One symmetric key per account or environment with one alias for each use and its encryption contexts enforced by key-policy conditions; in a production-grade environment a multi-Region key with a replica | ECDH plus HKDF (HPKE-style) with a P-384 key in slot 9d; older sealing keys stay in the spare slots 86 to 95 |
| `enrolment` | Opens HPKE-sealed enrolment requests; used by `exchange` only | Held by KMS | Held by `key-holder` |

Each backend describes itself with traits: exportability, attestation, whether a human must touch it, latency class, throughput, and who enforces which caller may use it (platform IAM, a shared driver, or nothing). The port signs bytes or digests; JWT shaping (`exp`, `iat`, lifetime limits) lives in a `jose` library in the token issuer, not in the port. `Derive` (a keyed, deterministic function) and `Agree` (ECDH) are separate operations because a PIV token has no HMAC. ES384 (P-384) is the default; the cloud-federation keys and the FAPI 2.0 algorithms (PS256, ES256, EdDSA) are the exceptions, each in its own slot or key. See [Keys](/concepts/keys/#algorithms).

Tokens are signed directly with the non-extractable key in every installation; there are no derived signing keys. A P-384 signature on a YubiKey takes about 120 ms, which is about 8 tokens per second, far above the measured load.

## The call graph

The graph is at actor granularity. A solid arrow is a synchronous ConnectRPC call; a dashed arrow is asynchronous (the transport, a tick, or a read of a store). The graph is a reviewed file, enforced by one interceptor in every process, **default deny**: a call that is not an edge is refused. It is drawn in five views: sign-in and exchange, minting and certificates, the integrations, the audit, and the ports.

The callers are browsers, CLI, CI jobs, workloads, cert-manager and auditors. Each reaches the front box that serves it: the OIDC endpoints, the CLI and CI API, the console, the webhook ingress or the PKI endpoint.

### View 1: sign-in and exchange

```mermaid
flowchart LR
  subgraph FRONT["Front, in entry"]
    oidc["OIDC endpoints"]
    cli["CLI and CI API"]
  end

  subgraph CORE["Identity path: session, exchange, signer, policy"]
    sess["sessions and codes"]
    xchg["token exchange"]
    issuer["token issuer"]
    ring["key ring"]
    proof["proof verifier"]
    facts["facts broker"]
    pol["policy"]
  end

  oidc --> sess
  oidc --> xchg
  oidc --> issuer
  cli --> xchg
  xchg --> proof
  xchg --> facts
  xchg --> pol
  xchg --> issuer
  issuer --> ring
  sess --> ring
```

### View 2: minting and certificates

```mermaid
flowchart LR
  clients(["Callers"])
  cli["CLI and CI API"]
  pkiep["PKI endpoint"]
  mint["mint broker"]
  proof["proof verifier"]
  facts["facts broker"]
  pol["policy"]
  quota["quota"]
  pki["PKI and SSH CA"]

  clients --> cli
  clients --> pkiep
  cli --> mint
  mint --> proof
  mint --> facts
  mint --> pol
  mint --> quota
  pkiep --> pki
```

### View 3: the integrations

```mermaid
flowchart LR
  oidc["OIDC endpoints"]
  hook["webhook ingress"]
  con["console"]
  proof["proof verifier"]
  facts["facts broker"]
  mint["mint broker"]
  tick["tick scheduler"]

  subgraph INT["Integrations"]
    ups["Upstream"]
    ver["WorkloadVerifier"]
    fsrc["FactsSource"]
    cm["CredentialMinter"]
    prov["Provisioner"]
    notif["Notifier"]
  end

  ext(["external systems"])

  oidc --> ups
  proof --> ver
  facts --> fsrc
  mint --> cm
  hook --> ups
  hook --> prov
  hook --> notif
  con --> prov
  tick -.-> prov
  tick -.-> fsrc
  prov --> ext
  fsrc --> ext
  cm --> ext
  ups --> ext
  notif --> ext
```

### View 4: the audit

```mermaid
flowchart LR
  core["Identity path"]
  cert["PKI and SSH CA"]
  integ["Integrations"]
  con["console"]

  subgraph AUDIT["Audit"]
    aw["audit writer"]
    kh["key-holder<br/>(log key)"]
    wit["witness"]
    idx["audit indexer"]
    aq["audit query"]
  end

  core -.-> aw
  cert -.-> aw
  integ -.-> aw
  aw --> kh
  aw -.-> wit
  aw -.-> idx
  idx -.-> aq
  con --> aq
```

The dashed arrows into the audit writer are emits. The writer hands the indexer the `audit_batches` records, and the indexer hands audit query the `index`. The console reads the audit through audit query.

### View 5: the ports

```mermaid
flowchart LR
  core["Identity path actors"]
  cert["PKI and SSH CA"]
  integ["Integration actors"]
  audit["Audit actors"]
  ports["ports"]
  drivers["drivers, one per<br/>port per install"]

  core --> ports
  cert --> ports
  integ --> ports
  audit --> ports
  ports --> drivers
```

The ports are keys, state, lease, blob, transport, index, cloud, secrets and export, plus witness for audit. The identity path actors include the key ring, which reaches the ports through its drivers like every other actor, and the `tick` scheduler, whose edges are in view 3.

Rules the graph encodes:

- **Core calls integrations, never the reverse.** An integration calls no core component over Connect. Its results travel through state, the audit transport, and the reply to its caller. A flow that looks circular, such as console to integration to provider to webhook to integration, is a flow over time with the pending state kept as a record.
- **Integrations never call each other.** No integration imports another.
- **Only the audit sink is called by everyone**, and asynchronously. The one synchronous audit call is the blocking sink used by maintenance, restore and migration.
- **The decision point has no outgoing edge.** The `policy` actor maps facts to grants and seals a mint grant. It reads a reviewed file and nothing else.

## One writer per record kind

Every record kind has exactly one component that writes it. Others read through the owner's contract, or read a prefix the owner publishes. The manifest generator refuses two actors that both declare `writer: true` for one kind, and the store enforces the same rule at run time with the owner's own credentials: the only rqlite user with `execute` on the owner's cluster, an IAM leading-key condition on DynamoDB, grants and row-level security on Postgres.

| Record kind | Writer |
| ----------- | ------ |
| Sessions (single sign-on, per client, agent class), refresh tokens, the revocation list | sessions, in `session` |
| Authorization codes, consent, device authorization records | authorization flows, in `session` |
| Pending sign-in state, the recovery verifier and switch (passkeys: a generic WebAuthn relying party, planned) | authn, in `session` |
| Client secret versions and the client metadata cache | client registry, in `exchange` |
| Enrolments, enrolment-token hashes and the host registry with attribute provenance | host enrolment, in `exchange` ([Machine identity](/concepts/machine-identity/)) |
| The signing ring (published keys, key references) and its rotation schedule | key ring |
| Last-known (held) fact sets per subject | facts broker |
| Directory snapshots and tenant records | the directory integration |
| Organization records, App catalogue, account links, mint ring | the GitHub integration |
| Workspace records, Slack Connect pending shares | the chat integration |
| Minted-credential records (recent credentials, caller caps) | mint broker |
| Leases per target; the desired-state projections per connection | tick scheduler; grants export |
| Per-target tick reports | the ticked integration |
| Operator-set records of a kind (console channels, runtime switches, console-connected tenants) | the one component that owns the kind, on a request routed by the console. An integration may own an operator-set kind of its own (the chat integration's console channels) and is then its only writer; it never writes learned and operator-set data of the same object |
| Issued-certificate and SSH-certificate ledgers, CRL and KRL state | PKI, in `ca`; SSH CA, in `ssh-ca`. The ledger exists for revocation and roll-forward, not for serial uniqueness: serials are random 128-bit values |
| Trust log tiles | trust log, the only appender; `sluis key install` submits through it |
| Audit batch objects, the `audit_batches` sequence, dedupe records | audit writer. Erasure and purge commands are appended by the writer on request, never by their requesters |
| The last signed head | audit writer, through `key-holder`; the witness stores its co-signatures in the blob |
| The audit index and its cursor | audit indexer |
| Quota balances and ledger rows | quota |
| Backup archives and manifests | backup |
| Throttle counters | web surface |

Two cases need a rule rather than a name. A GitHub App's catalogue record is written by two flows, so it is split into kinds by origin ([ADR 0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)): the ids and installation the vendor returned are issued records (pins), written by the integration only; fields that carry authority are declared in configuration and written by no one at run time. And the held fact set is a copy with an expiry, not a source: the integration owns the snapshot, the facts broker owns the held record.

## Integration invariants

The structure above is kept by tests that run in CI. They are cheap to write because the graph, the manifests and the import tree are all data.

| Invariant | What fails |
| --------- | ---------- |
| Core packages import no vendor package | A core component that names GitHub, Slack or a cloud |
| No edge from an integration to an owner | An integration that calls back into core |
| No integration imports another integration | Two integrations sharing code by import |
| Policy matchers carry no vendor fields | A policy schema that knows a vendor. Matchers are `{type, namespace, attributes}` |
| Only the wiring package imports drivers | A component that links a driver directly instead of the port |
| Every record kind has exactly one writer across all manifests | Two actors that both write a kind |
| An authority key has exactly one user actor | A key policy that names two actors |
| No unit holds a key beside an unauthenticated listener | A fusion that breaks [the fusion rule](/architecture/processes/#the-fusion-rule) |
| Each driver passes the port's conformance suite | A driver that diverges from the port contract |
| The deny matrix: for every pair of actors, X is denied Y's records, secrets, keys and sockets | A generated policy that is too wide |
| Isolation per layer, with the other layers switched off | A layer that passes only because another layer caught the case ([Defence in depth](/concepts/drivers/#defence-in-depth)) |
| The drift test: generated scopes, access lists, policy documents, units, sockets and ServiceAccounts agree | A hand edit, or a generator change that missed one output |

:::note[Proposed]
Two further start-up checks are proposed but not yet agreed. First, every `policy` instance of a boundary reports its policy digest, and the install refuses to serve if they disagree for more than a few seconds. Second, the trust graph between installs, where one sluis trusts another as an upstream or a verifier, must be a directed acyclic graph, checked when the configuration loads.
:::

Next: [Processes](/architecture/processes/) says which of these components run together, and with which credentials.
