# Architecture decisions

One record per decision that shapes sluis from the outside: what a user must do, what an installation must accept, what a release removes.
A record says why the thing is shaped the way it is, what else was weighed, and what follows.
Explanation pages describe the target state and carry a status badge; a page marked `shipped` describes what shipped. When a page and a record disagree, the record is the reasoning and the page is fixed.

How a decision is made is in [GOVERNANCE.md](../../GOVERNANCE.md).

## Numbering

v2 numbering starts again at `0001`. `0000` is the [template](0000-template.md).
v1 had its own records on the `master` branch. Refer to them as "v1 ADR NNNN" and link to `master`, for example "v1 ADR 0007". Do not renumber them and do not copy them here.
A v2 record that replaces a v1 decision says so in its context.

## Files

A record is `NNNN-short-title-in-kebab-case.md`. Numbers are taken in the order pull requests open. A number is never reused, even for a rejected record.

## Format

Every record has these parts, in this order:

- **Status.** One of `Proposed`, `Accepted`, `Rejected`, `Superseded by NNNN`, or `Accepted; amended by NNNN`.
- **Date.** The day the status last changed.
- **Context.** The forces at play. What problem, what constraints, what the reader needs to know.
- **Decision.** What we do, in the active voice.
- **Consequences.** What gets easier, what gets harder, what must follow. Include costs.
- **Roads not taken.** Each other option that was weighed, with its pros and cons and the reason it lost. This part is required.

## Rules

- **Never edit history** (after the first v2 release; the design phase below waives it until then). A changed mind gets a new record that supersedes the old one. The old record's status line changes to point at the new one. Its body stays.
- **Typos and links** may be fixed in place.
- **One decision per record.** Keep it short. A page of reasoning is better than ten.
- **Public means generic.** A record names no private deployment, account, hostname, ticket or person other than the maintainer. Use neutral examples.
- **Draft ADRs are internal.** Reasoning starts in internal discussion. Only the final record is written here, once the maintainer has decided.
- **Standards.** A deliberate deviation from a standard in the register needs a record.
- **Scope.** New product scope, and changes to the design model, need a record.

## Design phase (2026-10-29)

v2 is a clean start. During the v2 design phase, before the first v2 release, the maintainer may rewrite accepted records in place. The rule "Never edit history" above is waived for this period only, and the earlier text stays in git history. A rewritten record keeps its number and its proposal issue link. It may change its slug and title when the decision changed, and the slug change is then listed in the pull request so the site can redirect it.
After the first v2 release, records are append-only again: a changed mind gets a new record, as the rules above say.
A rewritten record says so in one line under its proposal link, with the date. On 2026-10-30 the maintainer had every record realigned to one vocabulary and one set of decisions (the realignment pass): records were rewritten where they contradicted the decisions or each other. No merged record was merged into another or removed, and no slug changed; the planned records 0018 to 0020, never written, were folded into 0004 and their numbers are not used.

## Index

| ADR | Decision | Status |
|-----|----------|--------|
| [0001](0001-v1-and-v2-lines.md) | v1 and v2 are two lines that coexist until v2 replaces v1 | Accepted |
| [0002](0002-actors-declare-ports-placement-is-derived.md) | Actors declare ports, drivers declare requirements, and placement is derived | Accepted |
| [0003](0003-runtimes-are-runtime-kinds.md) | Runtimes are runtime kinds; installations add nodes and dependencies; four installations are tested | Accepted |
| [0004](0004-audit-is-four-pieces-joined-by-a-transport-port.md) | Audit is four pieces joined by a transport port; each log has exactly one writer | Accepted |
| [0005](0005-three-zoom-levels-contracts-and-a-permission-manifest.md) | The design has three zoom levels, every component boundary is a ConnectRPC contract, and a permission manifest per actor is its spine | Accepted |
| [0007](0007-core-is-a-few-actors-with-one-responsibility-each.md) | Core is a few actors with one responsibility each: a slim entry, session, console, exchange, signer, policy, ca and ssh-ca, with quota beside them | Accepted |
| [0008](0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp.md) | Drivers are platform-started services on Unix sockets, and only the public API listens on TCP | Accepted |
| [0009](0009-one-installation-per-boundary.md) | Boundaries are environments, companies and products on an isolation ladder, and a boundary on its own runtime is an installation federated by declared trust | Accepted |
| [0010](0010-cryptography-uses-p384-by-default-with-named-exceptions.md) | Cryptography uses EC P-384 by default, stays within the FIPS-approved algorithms, with named exceptions | Accepted |
| [0011](0011-authority-keys-sign-in-place-and-never-leave-their-store.md) | Authority keys sign in place and never leave their store | Accepted |
| [0012](0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials.md) | Machine identity follows SPIFFE: proofs are exchanged for short-lived credentials | Accepted |
| [0013](0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca.md) | The ca and ssh-ca actors are the X.509 and SSH certificate authorities, and SSH uses only sluis's own CA | Accepted |
| [0014](0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor.md) | Policy is written in Cedar and evaluated by a Rust policy actor | Accepted |
| [0015](0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema.md) | Configuration is defined once in Pkl and shipped as JSON Schema | Accepted |
| [0016](0016-data-has-an-origin-and-a-sensitivity.md) | Data has an origin and a sensitivity | Accepted |
| [0017](0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite.md) | State is one port with compare-and-set; one writer owns each record kind; clusters replicate with rqlite | Accepted |
| [0021](0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode.md) | The audit log is a C2SP tiled log in a blob, sealed in batches by its one writer, with a lock-mode rule, a declared retention and company encryption; it holds two log kinds, audit and usage | Accepted |
| [0022](0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes.md) | Search is an index fed from the log, and the index is one driver with provisioning modes | Accepted |
| [0023](0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model.md) | Quota's API is idempotent by a request id and `Idempotency-Key`, and its data follows the origin model | Accepted |
| [0029](0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime.md) | One repository, one version, one release train, and packaging follows the runtime | Accepted |
| [0031](0031-intermediates-and-the-hand-off-to-root-custody.md) | Intermediates and the hand-off to root custody: sluis holds no root, asks for its intermediates to be certified, and signs in place | Accepted |
| [0033](0033-database-access-uses-short-lived-credentials.md) | Database access uses short-lived credentials | Accepted |
| [0038](0038-the-local-installation-is-docker-compose-with-one-container-per-actor.md) | The local installation is Docker Compose with one container per actor, the published images and minimal drivers | Accepted |
| [0039](0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel.md) | Delivery goes local first, then boards, lambda and kubernetes in parallel | Accepted |
| [0048](0048-passkeys-step-up-and-device-login.md) | Passkeys, step-up and device login | Accepted |
| [0058](0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted.md) | Boundaries are enforced in three layers per port, by the driver, and every state value is encrypted | Accepted |
| [0059](0059-each-boundary-has-its-own-issuer-and-tokens-carry-one-audience.md) | Each boundary has its own issuer; tokens carry one audience that names a registered client | Accepted |
| [0060](0060-the-language-follows-the-executable.md) | The language follows the executable: Go by default, Rust at the edge, in the trusted base and in cloud drivers | Accepted |
| [0061](0061-optional-openid-plans-form-post-is-supported-ciba-comes-later.md) | Optional OpenID plans: form post is supported; front-channel logout and third-party-initiated login are not; CIBA comes later in poll mode | Accepted |
| [0062](0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own.md) | An audit installation has its own front: a slim entry, the reader as its API, and a console of its own | Accepted |
| [0063](0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api.md) | The site is static on Cloudflare Workers, with a live status API | Accepted |
| [0064](0064-audit-records-are-ocsf-events-with-a-sluis-extension-and-usage-is-a-separate-stream.md) | Audit records are OCSF events with a sluis extension, and usage is a separate stream | Accepted |
