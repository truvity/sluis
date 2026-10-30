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

- **Never edit history.** A changed mind gets a new record that supersedes the old one. The old record's status line changes to point at the new one. Its body stays.
- **Typos and links** may be fixed in place.
- **One decision per record.** Keep it short. A page of reasoning is better than ten.
- **Public means generic.** A record names no private deployment, account, hostname, ticket or person other than the maintainer. Use neutral examples.
- **Draft ADRs are internal.** Reasoning starts in internal discussion. Only the final record is written here, once the maintainer has decided.
- **Standards.** A deliberate deviation from a standard in the register needs a record.
- **Scope.** New product scope, and changes to the design model, need a record.

## Design phase (2026-10-29)

v2 is a clean start. During the v2 design phase, before the first v2 release, the maintainer may rewrite accepted records in place. The rule "Never edit history" above is waived for this period only, and the earlier text stays in git history. A rewritten record keeps its number and its proposal issue link. It may change its slug and title when the decision changed, and the slug change is then listed in the pull request so the site can redirect it.
After the first v2 release, records are append-only again: a changed mind gets a new record, as the rules above say.

## Index

| ADR | Decision | Status |
|-----|----------|--------|
| [0001](0001-v1-and-v2-lines.md) | v1 and v2 are two lines that coexist until v2 replaces v1 | Accepted |
| [0002](0002-actors-declare-ports-placement-is-derived.md) | Actors declare ports, drivers declare requirements, and placement is derived | Accepted |
| [0003](0003-runtimes-are-runtime-kinds.md) | Runtimes are runtime kinds; installations add nodes and dependencies; four installations are tested | Accepted |
| [0004](0004-audit-is-four-pieces-joined-by-a-transport-port.md) | Audit is four pieces joined by a transport port; each log has exactly one writer | Accepted |
| [0005](0005-three-zoom-levels-contracts-and-a-permission-manifest.md) | The design has three zoom levels, every component boundary is a ConnectRPC contract, and a permission manifest per actor is its spine | Accepted |
| [0007](0007-core-is-a-few-actors-with-one-responsibility-each.md) | Core is a few actors with one responsibility each: entry, signer, policy, ca, quota | Accepted |
| [0008](0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp.md) | Drivers are platform-started services on Unix sockets, and only the public API listens on TCP | Accepted |
| [0009](0009-one-installation-per-boundary.md) | There is one installation per boundary, and a boundary is an environment or a regulated entity | Accepted |
| [0010](0010-cryptography-uses-p384-by-default-with-named-exceptions.md) | Cryptography uses EC P-384 by default, in FIPS mode, with named exceptions | Accepted |
| [0011](0011-authority-keys-sign-in-place-and-never-leave-their-store.md) | Authority keys sign in place and never leave their store | Accepted |
| [0014](0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor.md) | Policy is written in Cedar and evaluated by a Rust policy actor | Accepted |
| [0015](0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema.md) | Configuration is defined once in Pkl and shipped as JSON Schema | Accepted |
| [0017](0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite.md) | State is one port with compare-and-set; one writer owns each record kind; clusters replicate with rqlite | Accepted |
| [0029](0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime.md) | One repository, one version, one release train, and packaging follows the runtime | Accepted |
| [0038](0038-the-local-installation-is-docker-compose-with-one-container-per-actor.md) | The local installation is Docker Compose with one container per actor and minimal drivers | Accepted |
| [0039](0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel.md) | Delivery goes local first, then boards, lambda and kubernetes in parallel | Accepted |
