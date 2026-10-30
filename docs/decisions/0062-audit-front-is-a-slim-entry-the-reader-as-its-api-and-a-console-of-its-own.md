# 0062 — An audit installation has its own front: a slim entry, the reader as its API, and a console of its own

**Status:** Accepted
**Date:** 2026-10-31

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build: [issue #985](https://github.com/truvity/sluis/issues/985). The question was [OD-26](/open-decisions/#od-26).

## Context

Audit is its own installation, and sluis emits to it through the transport port ([OD-31](/open-decisions/#od-31)). A small installation may combine the audit pieces with sluis, and then they share driver instances ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). Either way, people have to read the log: auditors, operators, and each company for its own records. Something has to face them.

The open decision OD-26 asked how a console reaches the reader in a separate audit installation. The options were an `entry` in the audit installation in front of the reader, or a remote plugin call with a token. The maintainer chose the first on 2026-10-30, and added that audit gets the same split as sluis: a slim router `entry`, the API, which is the `reader`, and a console of its own.

The split it copies is [0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/): `entry` parses, validates against closed lists generated from the contracts and routes, and holds no key, no secret, no state and no outbound network; `session` holds the browser session; `console` is the browser's backend and routes each operator write to the actor that owns the record kind; the single-page application is a separate static artifact served on the same origin. Boundary installations already follow it: each runs its own slim `entry` with no sign-in of its own, takes the front's issuer as its upstream and checks passkeys itself where its policy asks for fresh MFA ([0059](/decisions/0059-each-boundary-has-its-own-issuer-and-tokens-carry-one-audience/)).

The public form of the hosts is the front at `sluis.example.com` and each environment boundary at `sluis.<env>.example.com`, one origin per host. The passkey relying-party ID is the registrable parent, so every verifier keeps a strict allow-list of origins.

v1 had a standalone audit console that was a native OpenID Connect relying party with a cookie session (v1 ADR 0049, on `master`). An earlier v2 proposal, withdrawn on 2026-10-29, had the audit installation sign users in through sluis and offered a break-glass read from the bucket on the CLI.

The threats this record answers are T-1 to T-7 and T-21 of the [threat model](/security/threat-model/).

## Decision

### The front

- **Every audit installation runs its own `entry`, also when the audit pieces are combined with sluis in one installation.** The audit front is the same shape everywhere: a combined installation runs two fronts, sluis's and audit's, each with its own `entry` and its own host.
- **It is the slim router of [0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/).** It parses, validates against closed parameter lists generated from the audit contracts, and routes. It holds no key, no secret and no state, and it has no outbound network. It reaches only the sockets of the actors it routes to; on serverless it invokes them (T-4).
- **It is the same `entry` executable as sluis's,** with the route tables of the audit contracts. (inferred: one executable per process kind, and the closed lists are generated per contract set) Its instance name tells it apart from sluis's `entry` in the same boundary; the name is fixed with the build.
- **It is its front's only edge listener** ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). The edge connector is the runtime's: a tunnel to a Unix-socket origin on bare-metal, the API gateway on serverless, a key-less relay container or `cloudflared` on Kubernetes ([OD-24](/open-decisions/#od-24)).
- **The `reader` is the API.** It authenticates every call itself, and serves queries from the search index or by a scan of the log ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)).
- **The audit front has a host of its own, a sibling of its boundary's host:** `sluis-audit.<env>.example.com` beside `sluis.<env>.example.com`, and `sluis-audit.example.com` beside `sluis.example.com` (the second form is inferred). A sibling inherits no cookie scope from the boundary host (T-6, T-7). Every passkey verifier's origin allow-list names it.

### Sign-in and the session

- **The audit installation runs its own `session` actor,** the same executable as sluis's. It is an OpenID Connect client of its upstream issuer, seals an HttpOnly cookie with its own key, and checks passkeys itself for step-up ([0048](/decisions/0048-passkeys-step-up-and-device-login/), T-5). The browser holds no token ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). The audit console therefore works with no sluis installation beside it.
- **The audit front accepts the issuer of the environment boundary whose log it holds,** with the group issuer as that issuer's upstream, as for any boundary installation ([0059](/decisions/0059-each-boundary-has-its-own-issuer-and-tokens-carry-one-audience/)). A token for one environment's log cannot be used for another's (T-3).
- **Tokens carry one audience.** A token that reaches the audit `entry` has as its `aud` exactly the audit installation's registered client id, and a token for another audience is refused (T-2).
- **A console of another installation reaches the audit front with an exchanged token.** It exchanges the person's session for a token (RFC 8693) at the issuer the audit front accepts, with `aud` the audit client id and a lifetime of at most 5 minutes, bound to the console's X.509-SVID by mutual TLS (RFC 8705). The person's identity reaches the reader, which decides with its own policy, and a copied token is useless (T-1, T-21).

### The console

- **The audit console is a static bundle of its own,** built from the same source tree on the same release train as the sluis console ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)). It is served on the same origin as the audit `entry`, by the runtime's static host. It carries only audit pages, so its content security policy is tighter.
- **Reads go straight to the `reader`:** search with facets, a record with its inclusion proof and its verification status, and exports, which are reader jobs ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)).
- **Writes go through the audit installation's own `console` actor,** the same executable as sluis's, which checks the operator's delegation and routes each write to the actor that owns its record kind: a rebuild of the index to the indexer. The reader stays read-only. Legal holds are not console writes, because they stay outside sluis, and a company's shred follows the promotion runbook ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/), [0009](/decisions/0009-one-installation-per-boundary/)).
- **The console shows the log's health:** the age of the latest checkpoint, the witness cosignatures, the index lag and the lag of the off-site copy.
- **Who sees what follows the reader's grants:** a company sees its own records in full, an operator sees the envelope (the class, the outcome and rotating pseudonyms, REQ-AUDIT-038), and a security reviewer sees what a declared grant names.

### Without the front

- **`sluis audit verify` and `sluis audit read` work from the bucket alone,** with read-only cloud credentials and no `entry`, `reader` or sign-in. A payload decrypts only where the caller may unwrap the company's key ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). Auditors and incident responders are never locked out by the front or the issuer.

## Consequences

- The audit installation has the same edge as sluis: one listener per front that holds nothing, so a parsing flaw in the front reaches no record, no key and no session.
- A combined installation runs two edge listeners and needs two hosts. On a board that is a second tunnel route, a second certificate name and a second `entry` instance. In return the audit front is the same shape on every installation, and splitting audit off later changes no host.
- Each environment gains a host name in DNS, certificates, the edge and every passkey verifier's allow-list.
- An audit installation runs a `session` and a `console` of its own, so it has one more key (the cookie-sealing key) and two more actors than a bare log and search would.
- The reader is the only actor that turns a caller into records, so its authentication and its grant filter carry the confidentiality of the log. They need tests with the other layers switched off ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- A console in another installation and the audit console reach the same API, so there is one authorisation path to test, not two. A token exchange runs per console session and audience.
- Reading several environments' logs needs a token per environment.
- A second console bundle is built, signed and tested per release.
- The console shows the most of the options weighed: search, proofs, exports and health.
- An audit installation still needs an issuer to sign people in, and its availability becomes part of reading the log through the front; a read from the bucket is the way around it. Bucket read access becomes something to grant carefully.

## Roads not taken

### One `entry` for both contract sets in a combined installation

- Pros: one edge listener and one host; nothing extra to run on small machines.
- Cons: the combined `entry` parses both contract sets, and the audit front looks different when combined than when separate.
- Why not: the maintainer chose a second `entry` for audit, on its own host, in every installation.

### No audit front in a combined installation

- Pros: least to build.
- Cons: the audit console would exist only for separate installations.
- Why not: the audit front is the same everywhere.

### A remote plugin call from the sluis console, with a token

- Pros: no front to run in the audit installation; the sluis console calls the reader directly.
- Cons: the reader would listen without an `entry` in front of it, so the actor that holds the index credential would parse internet input. An audit installation would be unreadable without a sluis console.
- Why not: the maintainer chose an `entry` in the audit installation (OD-26 (a)).

### The reader faces the edge itself

- Pros: one actor fewer per request.
- Cons: parsing and the index credential in one process, against the rule of 0007.
- Why not: `entry` parses and routes; the reader serves.

### No browser session in audit, or a public client in the browser

- Pros: no session actor in audit.
- Cons: without a session, the audit pages would live in the sluis console, and the audit console could not work on its own. A public client with PKCE and DPoP puts tokens in the browser, which 0007 rules out.
- Why not: the audit installation runs its own `session`.

### No writes from the audit console, or writes through the reader

- Pros: a read-only front; no extra actor.
- Cons: rebuilds would need shell access and a deploy. The actor that parses queries would gain write rights over the index.
- Why not: a `console` actor routes writes to their owners, as in sluis.

### The sluis console bundle with only its audit routes

- Pros: one artifact to build, sign and test.
- Cons: the audit host would serve code for sluis pages it never uses, under a wider content security policy.
- Why not: a separate bundle on the same release train.

### Actor-to-actor mutual TLS with the person as an assertion, or only a link

- Pros: no token exchange; nothing to build between the consoles.
- Cons: the reader would have to trust the console's word for who the person is. With only a link there is no audit view inside the sluis console.
- Why not: an exchanged, sender-constrained token with one audience.

### The group issuer directly, or an issuer of its own

- Pros: one token for every audit installation; audit independent of sluis's issuers.
- Cons: a group-issued token would reach every environment's log. An issuer per audit installation means more keys and issuers to run, and a second identity plane.
- Why not: the environment's issuer, as for any boundary installation.

### Operators only, or search and record view only

- Pros: a smaller first console.
- Cons: companies could not see their own trail, a regression from v1; without health and exports, integrity problems would be visible only in metrics.
- Why not: search, proofs, exports and health, by grant.

### Verify only from the bucket, or nothing without the front

- Pros: every payload read would be authorised and audited by the reader; one path.
- Cons: an outage of the front or the issuer would block reading, and with nothing from the bucket verification would need a running installation, which defeats a static log.
- Why not: verify and read from the bucket, with payloads decrypting only for those who may unwrap the key.

### A host under the boundary host, or a path on it

- Pros: `audit.sluis.<env>.example.com` groups with its boundary and a wildcard certificate covers it; a path needs no new host.
- Cons: cookies set for the boundary host's domain reach a child host. A path puts two installations on one origin, against one origin per host.
- Why not: a sibling host.

### A bundled authentication proxy in front of the audit installation

- Pros: the front would not need to know about tokens.
- Cons: one more component to run and secure, and the reader still has to check every call.
- Why not: v1 already chose no bundled proxy (v1 ADR 0049), and v2's `entry` is the front.
