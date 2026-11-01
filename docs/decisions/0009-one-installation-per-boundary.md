# 0009 — Boundaries are environments, companies and products on an isolation ladder, and a boundary on its own runtime is an installation federated by declared trust

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (the federation model: boundary kinds, the isolation ladder, a front installation with boundary installations, and the rules across boundaries). The slug is unchanged.
Rewritten in place on 2026-10-30 (threat IDs from the threat model added to the consequences).
Rewritten in place on 2026-11-01 (the roots are ritueel's; an installation gets its intermediates through the hand-off of [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)).

## Context

sluis holds the keys, the policy, the records and the audit trail of whatever it signs people and machines into. The first question about running it is how much of that may be shared. If development, staging and production, or the people of two companies, sit together, then a mistake or a compromise in one reaches the keys and state of the others.

v1 answered differently in different places. The sign-in service was deployed once for each party that ran it. The audit service was installed once per application, in that application's namespace (v1 ADR 0053, on `master`). The word "installation" therefore meant a deployment in one place and a deployment per product in another, and v1 ADR 0038 (on `master`) had the party that deploys render the documents of one instance without saying what an instance is for.

v2 has already fixed the words around this question. An **installation** is one concrete deployment: a runtime, its nodes, its dependencies and its drivers ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). The install file is the installation, and "installation" is not a configuration key.

The first v2 version of this record said "one installation per boundary", with a boundary being an environment or a regulated entity. That answer is too coarse for a group of companies that share people, clusters and an operator. Such a group runs on two cuts at once:

- **The machine plane** (clusters, hosts, workloads, certificates, SSH, workload identity, and the tokens that clusters and cloud accounts accept) is cut by **environment** and shared by the companies, because the clusters are shared.
- **The identity plane** (directories, sign-in upstreams, vendor integrations, people records, who may operate what) is cut by **company** and has no environment.

One installation per environment gives people one sign-in URL, one console and one identity plane per environment, and it still puts every company's vendor credentials in one place. One installation per company and environment multiplies keys and processes, and the group-level applications that serve every environment still need one issuer. A boundary has to be able to be less than an installation, and an installation has to be able to trust another.

Some deployments also serve a regulated entity: a legal entity whose customers must not be joined with those of another entity. The maintainer has decided the direction of the CIAM question: sluis stays a broker, and a CIAM product plugs in as an upstream identity provider. The record for the rest of it is ADR 0049.

## Decision

### Kinds of boundary

A **boundary** is a unit of isolation. There are two kinds, and a third on demand.

- **An environment boundary** owns the machine plane of one environment, such as management, development, staging or production. It owns its keys, its intermediate certificates ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)), its SSH CAs, its token issuer, its grant key, its audit log, its SPIFFE trust domain and its state. Production never shares a key, a state store or a policy grant key with development.
- **A company boundary** owns a company's identity plane: its directory and sign-in upstream, its integrations, its people records, its operator delegation and its audit tenant key. It holds **no authority key**.
- **A product boundary** is a company and an environment together. It is created only when a company's customers sign in through sluis, with its own keys and issuer. There is none now. A product boundary is the first candidate for promotion to an installation of its own.

### The isolation ladder

Each boundary declares a level. A higher level adds to the ones below it.

| Level | Adds |
|---|---|
| L0 tenant | Separation by sluis's code only |
| L1 keys | Its own key-encryption key, CA, signing key and grant key |
| L2 process | Its own actor instances, with their own uid or role, and its own state prefix |
| L3 store | Its own state store (an rqlite cluster or a DynamoDB table), bucket and queue |
| L4 runtime | Its own Kubernetes cluster, cloud account or hosts, and a separate install file joined by declared trust |

- **The ladder replaces the earlier scale of tenant, keys and process.** A tenant is L0: a division such as an audit tenant, a quota tenant or a directory tenant, kept apart by code and by the conformance suite of the port that holds it. A tenant is not a substitute for a boundary.
- **Every port has three layers of defence,** generated from one manifest: the port library in the actor scopes every call to the actor's own boundary and kind, the backend checks the caller's platform identity, and L3 and L4 add physical separation. A driver whose backend cannot check its callers is allowed at L3 and above only together with its physical separation, and `sluis config check` enforces it. (The layers themselves are the subject of the state and driver records.)

### A federation of installations

- **One front installation, plus one boundary installation for each L4 boundary,** run by one operator and joined by declared trust. People get one sign-in URL, one console and one identity plane.
- **Boundaries below L4 live in the front installation** and are declared in its install file. An L4 boundary has an install file of its own.
- **Companies are L2 boundaries with sealed data, in the front installation.**
- **An example layout:** the front and the management boundary share the management account; development, staging and production are each L4, with their own account or cluster, their own state and their own keys. The keys and state table of a boundary may start in the management account, with per-actor roles and per-boundary keys; moving production's into the production account is a follow-up.
- **Each boundary installation runs its own slim `entry`** ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). How issuers, sign-in and tokens work across the federation, including the host names `sluis.example.com` and `sluis.<env>.example.com`, is ADR 0059.
- **The SPIFFE trust domain is the boundary.** Each environment boundary has its own trust domain. This replaces the earlier rule "trust domain = installation" ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)).

### What each boundary owns

- **Keys per environment.** An installation that already has a cloud-key root may certify the environment intermediates with it first, and add hardware roots kept off the network later through a dual-trust window. The roots are held by [ritueel](https://ritueel.id), and each installation gets its intermediates through the hand-off of [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/).
- **An issuer per environment** (ADR 0059).
- **Process level, at least, for every environment on a cloud runtime.**
- **State.** Below L3 one state engine holds a prefix per boundary. From L3 a boundary has its own store.
- **Policy from one source,** compiled into a bundle per environment, with a grant key per environment. A minter accepts only its own environment's grant key. **Company-ceiling guards** are Cedar `forbid` rules: a subject from one company's directory gets no grant on a scope another company owns, unless the grant is declared as a cross-company grant ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)). A bundle for one environment that contains another environment's policy fails the build.
- **An audit log per environment,** with each company as a reader tenant that has its own key.
- **Operator delegation per company,** plus an environment scope for machine-plane records, such as enrolling a production host.
- **One integration process per vendor tenant,** owned by its company ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). Each directory tenant has its own upstream.
- **People.** A subject per directory account, plus a declared link between the subjects of one person. Grants are never inherited across the link. An SSH certificate carries the person key.
- **Trust bundles per environment, and issuance bound to the requester.** `ca` signs only the names and SPIFFE IDs that the requester's proven identity owns.
- **SSH CAs per environment.** Each company's principals are in that environment's principals bundle.
- **The management boundary hosts the group-level applications** that serve every environment.

### Across boundaries

- **Calls between boundaries use mutual TLS with SPIFFE IDs.**
- **The target re-decides.** A boundary that receives a request decides it again with its own policy. It never takes another boundary's decision as its own.
- **A sensitive boundary verifies the person's passkey assertion itself,** against its own challenge, so a compromised front cannot act there (passkeys: ADR 0048).

### Shared parts and accepted risks

- **Shared by every boundary:** the front with its `entry`, the console, the roots (held by ritueel) and the operator.
- **Accepted risks,** which no boundary level removes:
  - a compromise of the roots;
  - the operator who deploys sluis, and cluster administrators;
  - a compromise of an environment's `ca`, which reaches every company's workloads in that environment, because companies are not cryptographically separate inside an environment;
  - a bug in a shared policy definition, or in a ceiling guard;
  - data that lives outside sluis.

### The test installation

- **The test installation is separate.** It serves no real user, trusts nothing of the federation and lives outside the sluis host names.

### Promotion: when a company leaves

There is a runbook from day one. A company leaves, or moves to an installation of its own, in four steps:

1. Disconnect its company boundary: its upstream, directory, integrations and minters are its own processes and records.
2. Export its slice of the audit trail, then crypto-shred its audit tenant key.
3. Re-issue its workloads under the new installation's CA.
4. Remove the person links of its subjects.

### Regulated entities

- **A regulated entity is at least a product boundary, and may be an installation of its own.** A tenant is never enough for it.
- **An installation per regulated entity from the first release is not decided here.** What follows from the CIAM decision (sluis stays a broker and a CIAM product plugs in as an upstream identity provider) is this:
  - a boundary for a regulated entity holds no store of that entity's end-user accounts, so it needs no account-hosting feature to exist;
  - it connects to that entity's CIAM product as an upstream, with the claim mapping, `acr` and `amr` passthrough, step-up, end-user subjects, back-channel logout and rate limits that the upstream integration provides;
  - two entities are kept apart first by their own keys, state and subject space, and second by each having its own upstream;
  - sluis does not join accounts across entities.

  Whether each entity gets its own installation from day one, and the tooling to run many installations, will be decided in ADR 0049.

## Consequences

- A fault, a leaked key or a bad policy change in development cannot reach production, because they share no key, grant key or state. People still sign in once, in one place.
- Each boundary picks its level, so the cost follows the need. A company costs processes and prefixes, not an installation. An L4 boundary pays for its own installation: its keys, its state, its install file and its upgrades.
- The front is the one place everyone trusts for sign-in. A compromised front can ask any boundary for a token as a signed-in person, which is why each target re-decides and why sensitive boundaries check the passkey themselves.
- A call between boundaries costs a mutual-TLS hop and a second decision. The front never holds a boundary's keys.
- No code enforced company ceilings before. Now a ceiling guard is checked when the policy loads, and a cross-company grant has to be declared as one.
- A company can leave by a tested runbook, not by an extraction project. The runbook needs a test, and so does each isolation layer with the others switched off.
- The isolation requirements get test vectors per layer, for example: `ca` of one environment asks for another environment's key and is refused; an actor writes another prefix and is refused; a subject of one company asks for another company's scope without a declared grant and is refused. A threat-model page lists the threats this record answers. (Both now exist: the [isolation requirements](/requirements/iso/) and the [threat model](/security/threat-model/).)
- Threats addressed: T-3, T-5, T-8, T-11, T-16, T-17, T-18, T-19, T-21 and T-26, see the [threat model](/security/threat-model/).
- The SPIFFE trust domain changes from the installation to the boundary. A workload's ID says which environment it belongs to, and SPIFFE federation is the path for a boundary that moves out.
- The docs and the glossary use "installation" for the deployment, "boundary" for the unit of isolation, and the level names L0 to L4. v1's "installation" (one per application) is written "v1 installation" where it appears.

## Roads not taken

### One installation per boundary (the first version of this record)

- Pros: the strongest default separation. A reader of an install file sees exactly one boundary.
- Cons: a group gets one sign-in URL, one console and one identity plane per environment, while its people are the same. Companies would either be installations too, multiplying everything, or tenants, with every vendor credential in one process.
- Why not: the ladder lets each boundary take the level it needs, and an L4 boundary is still an installation of its own, joined by declared trust.

### One installation for every environment, with the environment as a tenant

- Pros: one thing to run, upgrade and sign in to. One set of keys to look after.
- Cons: development shares keys, state and policy with production, so a mistake there is a production incident. A test of a policy change touches the live policy. (inferred)
- Why not: an environment is a boundary. It owns its keys, its intermediate, its state and its grant key.

### Environment boundaries only, with companies as tenants

- Pros: one axis to explain, and it matches shared clusters.
- Cons: one integration process holds every company's credential for a vendor, every operator can change every company's records, and nothing in code stops a grant across companies.
- Why not: the identity plane is cut by company, so a company is a boundary of its own kind, without authority keys.

### Company and environment cells as the only boundary

- Pros: cryptographic separation between companies in every environment, and the cleanest exit.
- Cons: about three times the keys and issuing processes. Shared clusters and shared services must trust every company's CA anyway, and group-level applications still need one issuer. A directory has no environment to be filed under.
- Why not: the two planes are cut where they are already cut. A company that needs more becomes a product boundary or its own installation.

### One kind of boundary, with levels only

- Pros: one concept.
- Cons: a company owns no CA and an environment owns no directory, so every rule needs exceptions.
- Why not: the kinds say what a boundary owns, and the levels say how strongly it is separated.

### Three levels: tenant, keys and process

- Pros: fewer words. It was the earlier scale.
- Cons: it has no level for a boundary's own store, or for its own runtime, which is where an installation of its own begins.
- Why not: the ladder L0 to L4 names both.

### The front decides, and boundaries trust its assertion

- Pros: one decision per request, and boundaries stay simple.
- Cons: a compromised front could then act in every boundary.
- Why not: the target re-decides with its own policy, and a sensitive boundary verifies the passkey itself.

### One installation per application, as in v1 audit

- Pros: the smallest blast radius. The application's team owns it (v1 ADR 0053, on `master`).
- Cons: as many installations as applications, each needing keys, state and an upgrade, and trust between them to configure. For the sign-in service, an application is a client of the service and not a reason to run another copy. (inferred)
- Why not: the application is a tenant or a client. The unit of isolation is the boundary.

### One installation for several regulated entities, with the entity as a tenant

- Pros: one installation to run, and one place for staff to sign in.
- Cons: the customers of two legal entities would sit in one store under one set of keys, and a mistake in a query layer would join them. Tenant isolation by code is not what a regulated entity needs.
- Why not: customers of different entities must not be joined. A regulated entity is at least a product boundary.

### The test installation as a boundary of the federation

- Pros: one fewer installation to run.
- Cons: tests would run next to real trust, and a test identity could be accepted by a real boundary. (inferred)
- Why not: the test installation is separate, trusts nothing and lives outside the sluis host names.

### Decide per-entity installations from the first release in this record

- Pros: the rule would be complete in one place.
- Cons: whether and when each entity gets an installation depends on the CIAM product that provides its customer accounts, and on the tooling to run many installations. Neither is decided.
- Why not: the rule here is general. The entity-specific part will be decided in ADR 0049.

### Let the platform's own account or namespace define the boundary

- Pros: the cloud account or namespace is already a boundary the provider enforces.
- Cons: it works only where such a unit exists. On a self-hosted node there is no account, and a Kubernetes namespace is a weak unit for keys. (inferred)
- Why not: the boundary is defined by what is separate (keys, process, store, runtime), whatever the platform calls it. An account is one way to reach L4.
