# 0014 — Policy is written in Cedar and evaluated by a Rust policy actor

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).

## Context

Every credential sluis mints, and every sign-in it admits, rests on one question: may this subject do this? In v1 the answer comes from a policy document in YAML. Grants are names of the form `scope:thing:role`, a vocabulary fixes what the names may mean (v1 ADR 0010, on `master`), and the evaluation runs inside the issuer, next to the code that parses requests and holds the signing keys.

v2 separates the decision from everything else. `policy` is its own actor with no state and no network. It reads a declared, reviewed policy, it is given the facts of a request, and it returns a decision as a grant signed with its own key ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). Integrations return facts and never decide, and a minter acts only on a grant that `policy` signed.

That leaves two choices that belong together: the language the policy is written in, and the language and engine of the actor that evaluates it. They have to satisfy these forces:

- **The actor decides whether a mint is allowed.** It should be small, memory-safe and easy to reason about. A wrong decision is the worst thing it can do, so it has to fail closed.
- **The language needs a published grammar and a corpus of conformance cases.** A policy engine that cannot be tested against a specification is a hand-made one.
- **A policy that does not fit its schema must be refused before it takes effect.** A typo in a grant name should be a load error and not a grant that nobody holds.
- **The policy is data for reviewers.** It is read, changed and approved as a file, and weakening changes are risky.
- **Operators want some changes without a review.** Granting a person a standard set of rights for a project, or suspending someone at once, should not wait for a pull request, and it must not open a writable policy API either.

Rust is already the language planned for the cloud-port drivers, the second implementation language that [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) allows. A Rust toolchain, a shared Rust kit and Rust packaging for each runtime are therefore planned anyway. (inferred)

## Decision

- **Cedar is the policy language.** Policies are Cedar policies, checked against a Cedar schema.
- **The `policy` actor is written in Rust and uses the `cedar-policy` crate** to parse, validate and evaluate. The crate is the reference implementation of the language.
- **`policy` decides from declared policy and the request.** It has no state port and no network. Facts reach it only in the request. Policy is declared configuration ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)): it is never operator-set, with the one exception below.
- **Template-linked policies are the one exception.** Policy logic stays declared: Cedar templates, and a declared delegation policy that says who may link which template to which principals and resources. Within those bounds the console creates and removes template-linked policies. A link adds no logic that a reviewer has not seen in the template. Tighten-only actions, such as suspending a subject or revoking a grant, take effect at once. The record kind of a link, its owning actor and how links reach `policy` without giving it a state port are in ADR 0056.
- **The validator runs at load and at every reload.** The actor refuses a policy that does not fit the schema, with an error that names the offending path, and it does not start with it. Declared policy may live-reload as a whole versioned snapshot: it is validated, swapped atomically and audited with its digest, and a snapshot that fails validation is refused while the policy in force stays.
- **Evaluation fails closed.** A request is denied unless a policy explicitly permits it, an explicit `forbid` wins over a `permit`, and an evaluation that cannot be completed is a denial.
- **The output is a grant sealed under the sealing key.** Rewritten on 2026-11-01: a grant is read only by sluis itself, so it is not signed. `policy` asks the `keys` port to seal it with authenticated encryption, with `purpose=grant` in the encryption context, and does not hold the key ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/#grants-are-sealed-not-signed)). A minter opens the grant under the same context, and only the sealing key of its own environment opens it. There is no grant-signing key.
- **Callers do not link Cedar.** `entry` and the other actors reach `policy` through its ConnectRPC contract ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). The language never appears in another actor's code, whatever language that actor is written in.
- **Not decided here:**
  - the Cedar schema that models requests, entities and actions, and how a client or a resource is written in Cedar: ADR 0056;
  - the vocabulary of grant names and claims, which continues v1 ADR 0010 and is in ADR 0025;
  - how a v1 policy document is carried over, which is part of ADR 0044.

## Consequences

- The decision is made by a small process that cannot read or exfiltrate anything. Its failure modes are a wrong decision and a refusal to decide, and both are visible in the audit trail.
- An operator can grant a standard set of rights, or suspend someone, from the console without a pull request, and the delegation policy bounds what a stolen operator session can link. The cost is a template and delegation design for every right that is granted this way, and ADR 0056 has to say how links reach a stateless actor.
- The policy is a file with a grammar, a validator and a conformance corpus. Reviewers read Cedar, and a test can state the behaviour of a policy with a request and an expected decision.
- A second language is now part of the core, in the smallest and most sensitive actor. The costs are a Rust build and a supply-chain review of its dependencies for each runtime, and a reader who knows Rust for this one actor. The Rust kit, the packaging and the review are shared with the cloud drivers.
- Cedar is a dependency that moves. The language and the crate are pinned, and a change of the pinned version is a reviewed change that runs the conformance cases first, so sluis follows the language's releases deliberately and not by accident. (inferred)
- Cedar is added to the standards register as a candidate with the version that is pinned, and the register's own record is ADR 0050.
- The footprint of the actor has to be measured on the smallest installation (a single-board computer, arm64, 2 GB RAM) and in a serverless function. (inferred)
- The v1 YAML policy does not carry over unchanged. Operators of v1 who migrate rewrite their grants in Cedar, and the migration tooling is the part of ADR 0044 that deals with policy.
- Docs that said "a reviewed file-based policy" now say "declared policy", name the language and mention template links.

## Roads not taken

### Keep a YAML policy document of our own

- Pros: it exists in v1, and operators know it. No second language and no new engine.
- Cons: a grammar and a validator of our own to specify, test and keep compatible. Matching rules such as wildcards live in code that we have to prove correct. There is no published corpus to test against, and a decision engine without one is hard to trust. (inferred)
- Why not: the maintainer chose Cedar for the language. A specified language with a validator is the better base for a decision point.

### Cedar in Go, inside the core

- Pros: one language for all the core actors. No Rust toolchain in the core.
- Cons: the reference implementation and its validator are in Rust. A port in another language has to follow it, and a gap between them is a gap in a decision engine. (inferred)
- Why not: the decision point is in Rust with the reference crate. An earlier plan put the decision point in the core Go code, and this decision replaces it.

### Another policy engine, such as Open Policy Agent with Rego

- Pros: widely used. Written in Go. Partial evaluation and a large ecosystem of tooling.
- Cons: Rego is a general-purpose query language and a policy author can write queries that are hard to bound. A schema check at load is not part of the language the way it is in Cedar. (inferred)
- Why not: Cedar was designed for authorisation and its validator checks policies against a schema before they take effect. The maintainer chose Cedar.

### A general expression language, such as CEL, with rules in our own format

- Pros: small, embeddable, safe to evaluate.
- Cons: it evaluates one expression. It has no policy sets, no `permit` and `forbid` with a defined way to combine them, and no schema validator for the whole set. We would build those. (inferred)
- Why not: the combination of rules is what a policy language is for, and Cedar specifies it.

### Rules written in the actor's code

- Pros: no language to learn and no engine to depend on.
- Cons: a policy change is a code change and a release. Reviewers read code, not a policy. The policy cannot be tested apart from the actor. (inferred)
- Why not: the policy is data that operators own and reviewers approve as a file.

### Policies written from the console

- Pros: every change is instant, with no review and no template to design.
- Cons: it is a writable policy API. A stolen operator session could widen any grant, and `policy` would need a state port to read what the console wrote.
- Why not: policy logic stays declared. The console links declared templates within a declared delegation policy, and anything else goes through review.

### A decision point with state or network access

- Pros: it could read live facts itself and need no facts to be passed in.
- Cons: an actor that can be made to read or exfiltrate anything is a target, and its decisions can no longer be reviewed as a file.
- Why not: decided in [0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/). `policy` has no state and no network.
