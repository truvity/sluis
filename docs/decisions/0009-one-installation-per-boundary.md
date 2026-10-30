# 0009 — There is one installation per boundary, and a boundary is an environment or a regulated entity

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

sluis holds the keys, the policy, the records and the audit trail of whatever it signs people and machines into. The first question about running it is how much of that may be shared. If development, staging and production, or the customers of two legal entities, sit in one installation, then a mistake or a compromise in one reaches the keys and state of the others.

v1 answered differently in different places. The sign-in service was deployed once for each party that ran it. The audit service was installed once per application, in that application's namespace (v1 ADR 0053, on `master`). The word "installation" therefore meant a deployment in one place and a deployment per product in another, and v1 ADR 0038 (on `master`) had the party that deploys render the documents of one instance without saying what an instance is for.

v2 has already fixed the words around this question. An **installation** is one concrete deployment: a runtime, its nodes, its dependencies and one driver per port ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). An **environment** is development, staging or production. What is missing is the rule that ties them together: what an installation is the unit of.

Two more terms are needed. A **boundary** is the unit of isolation of an installation. A **tenant** is a division inside one installation, such as an audit tenant, a quota tenant or a directory tenant. Isolation between tenants is done by sluis's own code, and the conformance suite of each port proves it. That is weaker than isolation by separate processes and keys, and it is meant for things that already trust one operator.

Some deployments also serve a regulated entity: a legal entity whose customers must not be joined with those of another entity. For them, separate installations are the natural answer. Whether v2 creates one installation per entity from the first release depends on how customer accounts are held, which is the CIAM question. The maintainer has decided its direction: sluis stays a broker, and a CIAM product plugs in as an upstream identity provider. The record for the rest of it is ADR 0049.

## Decision

- **There is one installation per boundary.** A boundary is an environment or a regulated entity.
  - **An environment boundary** is development, staging or production. It owns its account or host, its keys, its intermediate certificates ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)), its state and its own configuration overrides. Production never shares a key, a state store or a policy with development.
  - **A regulated-entity boundary** is a legal entity whose customers must not be joined with another entity's. An installation per entity is allowed.
- **A boundary is always a different process, at least.** On a self-hosted runtime it may be a different machine or a different Linux user. A boundary does not have to repeat every part of sluis, but at least the integrations that belong to it are its own.
- **This record defines the unit of isolation. It does not say that a boundary is exactly one deployment.** An installation never spans two boundaries. The pieces of a boundary may be separate installations, for example the audit log, Search and Quota ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)), each inside the boundary it belongs to, or they may be combined in one installation and share driver instances.
- **Tenants live inside an installation.** Several tenants of one kind share the installation's keys, state and policy. They are kept apart by sluis's code and by the conformance suite of the port that holds them. They are not a substitute for a boundary.
- **The install file is the installation.** "Installation" is not a configuration key. An installation has one install file, and the tested installations ship as example install files ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). A second boundary is a second install file, not a second block in the first.
- **Installations can trust each other, and the trust is explicit.** Nothing is shared by default. A person or a machine of one installation is accepted by another only through a configured connection, such as the generic OpenID Connect upstream and token exchange. (inferred)
- **An installation per regulated entity from the first release is not decided here.** What follows from the CIAM decision (sluis stays a broker and a CIAM product plugs in as an upstream identity provider) is this:
  - an installation for a regulated entity holds no store of that entity's end-user accounts, so it needs no account-hosting feature to exist;
  - it connects to that entity's CIAM product as an upstream, with the claim mapping, `acr` and `amr` passthrough, step-up, end-user subjects, back-channel logout and rate limits that the upstream integration provides;
  - two entities are kept apart first by being two installations, each with its own keys, state and subject space, and second by each having its own upstream;
  - sluis does not join accounts across installations.

  Whether each entity gets its own installation from day one, and the tooling to run many installations, will be decided in ADR 0049.

## Consequences

- A fault, a leaked key or a bad policy change in development cannot reach production, because they share no key, state or policy. The cost is that each boundary pays for its own installation: its keys, its state, its install file and its upgrades.
- Production and development are two installations that must be kept alike. Changes reach them through the same release and the same example install files, and the differences are the install file and the environment overrides. A test that the two differ only there is worth having. (inferred)
- Trust between boundaries becomes configuration that can be read and audited, because nothing is shared implicitly.
- A tenant is a cheap division, and it is also a weaker one. A requirement that two parties must not share keys or an operator cannot be met by tenants and needs a boundary.
- A regulated entity can be given an installation without waiting for an account-hosting feature. What is deferred is the tooling around many installations, and the customer-facing parts that a CIAM product provides: ADR 0049.
- The audit pieces (log, Search and Quota) can each be their own installation, or be combined and share driver instances ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). That is a different split from this one. This record says what the unit of isolation is. It does not require every piece of a boundary to live in one installation, and so "one installation per boundary" reads as "never more than one boundary per installation".
- The docs and the glossary use "installation" for the deployment and "boundary" as the parent term. v1's "installation" (one per application) is written "v1 installation" where it appears.
- A reader of the install file sees one boundary. Anything that needs to know about the others, such as a peer's issuer, is a connection in that file.

## Roads not taken

### One installation for every environment, with the environment as a tenant

- Pros: one thing to run, upgrade and sign in to. One set of keys to look after.
- Cons: development shares keys, state and policy with production, so a mistake there is a production incident. A test of a policy change touches the live policy. (inferred)
- Why not: an environment is a boundary. It owns its keys, its intermediate, its state and its overrides.

### An environment key inside one install file

- Pros: one file describes everything, and a diff shows how environments differ.
- Cons: the file holds the settings of the boundary it does not run, and a wrong value can reach the wrong boundary. "Installation" would become a key beside the file that is the installation. (inferred)
- Why not: the install file is the installation. Differences between environments are overrides in the last configuration layer, not extra installations inside one file.

### One installation per application, as in v1 audit

- Pros: the smallest blast radius. The application's team owns it (v1 ADR 0053, on `master`).
- Cons: as many installations as applications, each needing keys, state and an upgrade, and trust between them to configure. For the sign-in service, an application is a client of the service and not a reason to run another copy. (inferred)
- Why not: the application is a tenant or a client. The unit of isolation is the boundary.

### One installation for several regulated entities, with the entity as a tenant

- Pros: one installation to run, and one place for staff to sign in.
- Cons: the customers of two legal entities would sit in one store under one set of keys, and a mistake in a query layer would join them. Tenant isolation by code is not what a regulated entity needs.
- Why not: customers of different entities must not be joined. A regulated entity is a boundary.

### Decide per-entity installations from the first release in this record

- Pros: the rule would be complete in one place.
- Cons: whether and when each entity gets an installation depends on the CIAM product that provides its customer accounts, and on the tooling to run many installations. Neither is decided.
- Why not: the rule here is general. The entity-specific part will be decided in ADR 0049.

### Let the platform's own account or namespace define the boundary

- Pros: the cloud account or namespace is already a boundary the provider enforces.
- Cons: it works only where such a unit exists. On a self-hosted node there is no account, and a Kubernetes namespace is a weak unit for keys. (inferred)
- Why not: the boundary is defined by what is separate (process, keys, state), whatever the platform calls it.
