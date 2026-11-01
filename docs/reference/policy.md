---
title: Policy
description: "The Cedar schema of sluis policy: entity types, actions and context, how template links are made and sent to the stateless policy actor, and a worked example."
status: target
sidebar:
  order: 7
---

`policy` decides every question of the form "may this person have a token for this client" ([ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/)). It has no state and no network, so everything it needs arrives in the request. This page fixes what the request holds, the Cedar schema the policy is written against, how an operator's template links reach `policy`, and one policy file with test vectors. Who calls `policy` is in the [actor registry](/reference/actors/#policy); the install-file key that names the files is [`policy`](/reference/install-file/#policy).

## The schema is sluis's, the policy is the operator's

- **One schema, version 1, fixed by sluis and embedded in `policy`.** An operator writes policy against it and cannot add a type, an action or an attribute. This keeps every request checkable: `policy` validates each request against the schema and refuses one that does not fit, and every policy file against it at load and at every reload ([ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/#decision)).
- **No namespace.** The names below are the whole vocabulary; nothing an operator writes can collide with them.
- **Adding is compatible.** A type, an action, an optional attribute or a new optional context name may be added in a release; removing or changing one is a new schema version beside this one, as for the install file ([Install file](/reference/install-file/)).
- **The file is** [`policy-schema.cedarschema`](https://github.com/truvity/sluis/blob/main/docs/reference/policy-schema.cedarschema), in the Cedar schema syntax of Cedar 4.14.0 (language version 4.6, checked on 2026-11-01). This copy is checked against that file (`just check-examples`).

<!-- example: docs/reference/policy-schema.cedarschema -->
```text
// The Cedar schema of sluis policy, schema version 1 (the first working version).
// It is fixed by sluis and embedded in the `policy` actor; an operator writes policy against it
// and cannot add types, actions or attributes. Every request is validated against it.

// A group of a directory. A grant, in this version, is membership of a group.
// `org` is the company the group belongs to (the claim `org`).
entity Group = {
  org: String,
};

// A person, known by the opaque `sub` of the issuer, and a member of the groups the sign-in
// brought. `org` is the company claim.
entity Person in [Group] = {
  org: String,
};

// An environment boundary, such as `dev`.
entity Boundary;

// A client of an issuer. The token's `aud` is exactly one client id, so a client is also the
// resource of a token request. `requires` is the client's declared list of groups, and `kind`
// is `public`, `confidential` or `exchange`.
entity Client in [Boundary] = {
  kind: String,
  requires: Set<Group>,
};

// What the asking actor knows about how the person signed in. A name that the upstream did not
// send is absent, which a policy reads as unknown.
type Authentication = {
  acr?: String,
  amr?: Set<String>,
  // Seconds since the person last authenticated for this boundary, counted by the asking actor.
  auth_age?: Long,
};

// A person asks for a token for a client, at a code exchange or a refresh.
action "token.issue" appliesTo {
  principal: Person,
  resource: Client,
  context: Authentication,
};

// A person's token is exchanged for a token for a client of a boundary (RFC 8693).
action "token.exchange" appliesTo {
  principal: Person,
  resource: Client,
  context: Authentication,
};

// An operator asks to link a template in a boundary. The context names the template and the
// values of its slots, for the delegation policy to judge.
action "policy.link" appliesTo {
  principal: Person,
  resource: Boundary,
  context: {
    template: String,
    principal_group?: Group,
    principal_person?: Person,
    resource_client?: Client,
  },
};
```

## Entity types

The types follow the [actor registry](/reference/actors/) and the [record kinds](/reference/storage/#record-kinds-of-the-first-working-version): a type exists when an actor of the first working version asks about it ([principle 8](/principles/#8-nothing-without-a-consumer)). The asking actor sends the facts as a slice of entities with the request; `policy` reads no store.

| Type | What it is | Declared by | Facts sent by | Id |
|---|---|---|---|---|
| `Person` | A person who signed in, a member of the `Group` entities that sign-in brought | The upstream, at sign-in | `session` or `exchange`, from the session | The opaque `sub` ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#subjects)), never an e-mail address |
| `Group` | A set of people. In the first working version a grant is membership of a group | The upstream's group names | The asker | The group's name, as the upstream sends it |
| `Client` | A client of an issuer, and the resource of a token request | The install file: [`clients`](/reference/install-file/#clients) and `boundaries.<name>.clients` | The asker, from its own configuration | `<boundary>/<client id>`, such as `dev/example-app`; the group issuer's clients are under `front` |
| `Boundary` | An environment boundary, and the place a link lives in | The install file: [`boundaries`](/reference/install-file/#boundaries) | The asker | The boundary's name; `front` for the group issuer |

- **A client's id carries its issuer.** Client ids are unique per issuer, not across issuers ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#audiences)), and a Cedar entity id must be unique, so `dev/example-app` and `prod/example-app` are two entities. A `Client` is `in` its `Boundary`, so a policy can say "any client of `dev`".
- **`Client.requires` is the install file's `requires`,** read as groups. The standard rule of the example below, `principal in resource.requires`, is what makes `requires` mean "at least one of these groups".
- **`kind` is the install file's** `public`, `confidential` or `exchange`.
- **`org` is the claim `org`** ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#issuers)): the company of a person or a group.

:::note[Proposed]
**Where a person's `org` and groups are recorded.** The asker takes them from the session of the first working version's sign-in. The kinds that hold a person, their subject and the facts of a directory wait on the people records of the next milestone ([#1081](https://github.com/truvity/sluis/issues/1081)).
:::

### What comes with parity

Each of these is added by the record or pull request that first asks about it, and is compatible with version 1:

| Added | By | Because |
|---|---|---|
| `Person.disabled` and a directory-driven forbid | Directory sync ([#805](https://github.com/truvity/sluis/issues/805)) | The first working version reads no directory; a person is suspended by a link until then |
| `Workload`, `Host` and the action `cert.issue` | Workload identity and host enrolment ([ADR 0007](/decisions/0007-workload-identity-and-host-enrolment/)) | Their first asker is `exchange` with a proof |
| `Resource` | Declared resources ([#979](https://github.com/truvity/sluis/issues/979)) | A resource is a declared URL with its own `requires`, which the install file does not have in schema v1 |
| `artifact.sign` | The signing API ([#890](https://github.com/truvity/sluis/issues/890)) | Release signing is not in the first working version |
| Attributes for token lifetimes and claims | The claims and grants record ([#999](https://github.com/truvity/sluis/issues/999)) | Lifetimes and the claim vocabulary per audience are decided there |

## Actions

| Action | Principal | Resource | Asked by | Meaning |
|---|---|---|---|---|
| `token.issue` | `Person` | `Client` | `session` | Whether this person may have a token for this client, at a code exchange or a refresh |
| `token.exchange` | `Person` | `Client` | `exchange` | Whether this person's token may become a token for this client, as when `sluis login` exchanges the group issuer's token at a boundary |
| `policy.link` | `Person` (the operator) | `Boundary` | `session`, for a write that `console` routed | Whether this operator may link this template, with these slot values, in this boundary |

There is no separate action for step-up. A request that needs a fresher or stronger authentication is denied by a policy that says so ([below](#step-up)), which keeps the one question "may this person have this token" in one place.

## Context

The context of `token.issue` and `token.exchange` is the same record, `Authentication`:

| Name | Type | Meaning |
|---|---|---|
| `acr` | `String` | The authentication class reference of the person's latest authentication, as the issuer states it |
| `amr` | `Set<String>` | The methods used, with the values of RFC 8176 (`pwd`, `mfa`, `hwk`) |
| `auth_age` | `Long` | Seconds since the person last authenticated for this boundary, counted by the asker from the session's `auth_time` |

- **An absent name is unknown.** An upstream such as Dex sends no `acr`, `amr` or `auth_time` ([ADR 0018](/decisions/0018-runtimes-installations-and-delivery-order/#the-local-installation)), and then the asker leaves the names out. A policy tests `context has amr` before it reads `context.amr`, and treats absence as "not shown": a rule that requires multi-factor authentication is then not met. The asker never invents a value.
- **`policy` has no clock.** The asker counts `auth_age`, so a request and its answer are the same on any day, which is what makes the vectors below repeatable ([principle 6](/principles/#6-generic-products-joined-by-a-contract)).
- **The values of `acr` are the installation's.** The example uses `urn:example:acr:mfa`; the values sluis itself issues are fixed with the claims and grants record ([#999](https://github.com/truvity/sluis/issues/999)).

The context of `policy.link` is `template` (the template's id), and the slot values that the template has: `principal_group` or `principal_person` for a `?principal` slot, and `resource_client` for a `?resource` slot. It carries what the delegation policy needs to judge the link, and nothing else.

## What a decision holds

`policy`'s contract is a ConnectRPC service like the others ([Contracts and errors](/reference/contracts/)). Its request and answer carry:

| In the request | |
|---|---|
| The question | The principal, action and resource as entity ids, and the context |
| The facts | The slice of entities the question needs: the person with the groups, the client with its `requires`, the boundary |
| The links | The template links that apply, as [below](#template-links) |

| In the answer | |
|---|---|
| The decision | `allow` or `deny`. A policy that errors counts as no permit; a request that does not fit the schema is refused, not decided |
| The reasons | The ids of the determining policies, each with its annotations |
| The snapshot | The digest of the policy set and the version of the schema, for the audit record |
| The grant | On `allow` only: the mint grant, sealed under the environment's sealing key ([ADR 0012](/decisions/0012-cryptography-and-authority-keys/#grants-are-sealed-not-signed)) |

The `proto/` file is written with the first code. Every decision is audited with its reasons and the snapshot digest.

### Policy ids and annotations

- **Every policy and template carries an `@id`.** The id is stable and appears in the reasons, the audit record and the tests. A policy with no `@id` is refused at load.
- **The only annotation sluis reads is `@step_up`,** on a `forbid`. Other annotations are carried in the reasons and ignored.

## Step-up

[ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#step-up) has `policy` decide which clients need multi-factor authentication and how fresh it must be, and the asker answer with RFC 9470. The two meet in an annotation:

- **A `forbid` that expresses a step-up rule carries `@step_up("acr_values=<values> max_age=<seconds>")`,** the values RFC 9470 puts in `insufficient_user_authentication`.
- **When the determining policies of a `deny` include one with `@step_up`,** `session` answers `insufficient_user_authentication` with those values and the person signs in again. When there is none, the answer is a plain refusal, and it looks the same for an unknown and a refused client ([Contracts and errors](/reference/contracts/#oauth-and-openid-connect-endpoints)).
- **A rule is written with `unless`:** the `forbid` applies unless the context shows what is asked for. Unknown context therefore forbids, and the person is asked to step up.

## Template links

A template is declared policy, in a policy file, with `?principal` and `?resource` slots. A **link** gives the slots values and so makes a policy. Links are the one operator-set part of policy ([ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/#template-links), [ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#origins)).

### Making a link

1. An operator asks the console for a link: a template id and the slot values.
2. `console` routes the write to the actor that owns the record kind, which is `session`.
3. `session` asks `policy` the question `policy.link` with the operator as principal, the boundary as resource and the template and slot values as context. A `deny` ends it.
4. On `allow`, `session` writes the link into the record `policy-link-set` of the boundary by compare-and-set, and the audit record names the operator, the template, the slot values and the delegation policy that permitted it.

- **The delegation policy is Cedar too.** It is a set of ordinary policies in the policy files, on the action `policy.link`. A template with no delegation policy cannot be linked by anyone: the link set stays as it is.
- **One record, one owner.** The kind is `policy-link-set`, owned by `session`, a reader being `exchange` ([Storage](/reference/storage/#record-kinds-of-the-first-working-version)). The first working version's state port has no delete and no listing by prefix ([Storage](/reference/storage/#state)), so the whole set of a boundary is one record under the fixed id `links`, changed by compare-and-set, and removing a link writes the set without it. Parity may split the set when the port can list.
- **A link has** an id, the template id, the slot values as entity ids, who made it and when. A link made twice with the same template and slot values is one link.
- **Tighten-only.** A template whose effect is `forbid` can only narrow what is permitted; the console offers those for "suspend a person" and "revoke a grant". The delegation policy still says who may link it, and a link takes effect on the next decision, with no cache between the record and the request.

### Sending links to `policy`

- **The asker reads the set before each decision** and sends, with the facts, the links whose slot values are the request's principal, a group the principal is in, the resource, or the resource's boundary. `policy` keeps nothing between requests.
- **`policy` links them in memory,** validates each against the loaded templates, and evaluates. The links are part of the snapshot digest of the audit record.
- **A link that cannot be applied denies the request.** An unknown template or a slot of the wrong type means the policy files and the link set have drifted, and ignoring the link could drop a `forbid`. The denial is audited with the link id. `sluis config check --live` reports such a link as a `fail`, so that an edit to the policy files that orphans a link is found before it is deployed ([Commands](/reference/cli/#sluis-config-check-and-explain)).

### The grants of one tenant

A company is a tenant: its groups carry its `org`, and its administrators may link groups of their own `org` to the clients the company is offered, with no review of the operator's and no change to the policy files. The delegation policy of the example below does this in one policy; the vectors show what it allows and what it refuses.

## A worked example

The local installation's [example install file](/reference/install-file/#the-example-file-for-local) names `policy/local.cedar`. The example declares one client, `example-app` in the boundary `dev`, which requires the group `example-users`. The policy has four parts: one standard rule, one step-up rule, two templates and two delegation policies.

<!-- example: tests/installations/local/policy/local.cedar -->
```text
// The policy of the local installation (tests/installations/local), written against the Cedar
// schema of sluis policy, version 1. The ids are stable: audit records and tests name them.

// ---- What a person may get a token for -------------------------------------------------

// A person who belongs to one of a client's required groups may have a token for it.
@id("token-requires")
permit (
  principal is Person,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource is Client
)
when { principal in resource.requires };

// ---- Step-up ---------------------------------------------------------------------------

// `example-app` of the `dev` boundary wants multi-factor authentication no older than 15
// minutes. When a request carries no `amr` or no `auth_age`, as with an upstream that sends
// none, the answer is "not shown", so the person is asked to step up. The annotation tells
// `session` what to ask for (RFC 9470): the values of `acr_values` and `max_age` that it puts
// in `insufficient_user_authentication`.
@id("example-app-fresh-mfa")
@step_up("acr_values=urn:example:acr:mfa max_age=900")
forbid (
  principal is Person,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource == Client::"dev/example-app"
)
unless {
  context has amr && context.amr.contains("mfa") &&
  context has auth_age && context.auth_age <= 900
};

// ---- Templates, for grants that operators link at run time -----------------------------

// Members of a group may have tokens for one client. Linked per tenant.
@id("grant-group-to-client")
permit (
  principal in ?principal,
  action in [Action::"token.issue", Action::"token.exchange"],
  resource == ?resource
);

// One person gets nothing. A forbid can only narrow, so it is what the console offers for
// "suspend".
@id("suspend-person")
forbid (principal == ?principal, action, resource);

// ---- Who may link which template (the delegation policy) -------------------------------

// A company's administrators may grant their own company's groups access to the clients that
// the company is offered.
@id("delegate-company-grants")
permit (
  principal in Group::"company-admins",
  action == Action::"policy.link",
  resource == Boundary::"dev"
)
when {
  context.template == "grant-group-to-client" &&
  context has principal_group && context.principal_group.org == principal.org &&
  context has resource_client && [Client::"dev/example-app"].contains(context.resource_client)
};

// The security group may suspend anyone.
@id("delegate-suspension")
permit (
  principal in Group::"security",
  action == Action::"policy.link",
  resource == Boundary::"dev"
)
when { context.template == "suspend-person" && context has principal_person };
```

Two links are in force in the vectors: the staff group of the company `acme` has access to `example-app`, and one person is suspended. This is the file the asker would send them from.

<!-- example: tests/installations/local/policy/links.json -->
```json
[
{"template_id": "grant-group-to-client", "link_id": "grant-acme-staff-example-app", "args": {"?principal": "Group::\"acme-staff\"", "?resource": "Client::\"dev/example-app\""}},
{"template_id": "suspend-person", "link_id": "suspend-p-suspended", "args": {"?principal": "Person::\"p-suspended\""}}
]
```

### Test vectors

The vectors are in [`tests/installations/local/policy/`](https://github.com/truvity/sluis/tree/main/tests/installations/local/policy): `vectors.json` runs with the two links above, and `vectors-unlinked.json` runs the same questions with no link. Each vector is a request, the entities it needs, the decision and the ids of the determining policies, in the format of `cedar run-tests`. The people are stand-ins for opaque subjects: `p-analyst` is in `example-users`, `p-acme-staff` is in `acme-staff` of the company `acme`, `p-acme-admin` is in `company-admins` of `acme`, `p-security` is in `security`, and `p-suspended` is in `example-users`. Run with Cedar 4.14.0 (language version 4.6) on 2026-11-01, all 19 pass:

```sh
cedar run-tests --policies local.cedar --schema policy-schema.cedarschema \
  --template-linked links.json --tests vectors.json
```

| Question | Context | Decision | Determined by |
|---|---|---|---|
| `p-analyst` asks for a token for `example-app` | `mfa`, 300 s old | allow | `token-requires` |
| `p-analyst` exchanges a token for it | the same | allow | `token-requires` |
| `p-analyst` asks for `batch-job`, which has no step-up rule | password only, 9000 s old | allow | `token-requires` |
| `p-analyst` asks for `example-app` | password only | deny, step up | `example-app-fresh-mfa` |
| the same | `mfa`, but 1200 s old | deny, step up | `example-app-fresh-mfa` |
| the same | nothing: an upstream that sends no `amr` and no `auth_age` | deny, step up | `example-app-fresh-mfa` |
| `p-contractor`, in no group | `mfa`, fresh | deny | no policy permits |
| `p-acme-staff` | `mfa`, fresh, with the grant link | allow | `grant-acme-staff-example-app` |
| the same | password only, with the grant link | deny, step up | `example-app-fresh-mfa` |
| the same, for `batch-job` | with the grant link | deny | no policy permits |
| the same person, with no link | `mfa`, fresh | deny | no policy permits |
| `p-suspended` | `mfa`, fresh, with the suspension link | deny | `suspend-p-suspended` |
| the same, with no link | `mfa`, fresh | allow | `token-requires` |
| `p-acme-admin` links `grant-group-to-client` for `acme-staff` and `example-app` | | allow | `delegate-company-grants` |
| the same, for the group `globex-staff` of another company | | deny | no policy permits |
| the same, for `batch-job`, which the company is not offered | | deny | no policy permits |
| the same, for `suspend-person` | | deny | no policy permits |
| `p-security` links `suspend-person` | | allow | `delegate-suspension` |
| `p-analyst`, in neither delegating group, links the grant | | deny | no policy permits |

A request that does not fit the schema, such as an `auth_age` that is a string, is refused and not decided. The `policy` actor's conformance test runs these vectors against the Rust crate, and the same vectors run against any other engine that is offered as a replacement ([principle 6](/principles/#6-generic-products-joined-by-a-contract)).

## What `sluis config check` checks

Offline, on the policy files and the install file ([Install file](/reference/install-file/#what-sluis-config-check-checks)):

- every policy file parses, and the set validates against the schema;
- every policy and template has a unique `@id`, and every `@step_up` is on a `forbid` and holds well-formed `acr_values` and `max_age`;
- every `forbid` or `permit` that names a `Client` names one the install file declares, and every `Boundary` a declared boundary;
- every client's `requires` is not empty.

With `--live`, the operator-set records are read as well, and a link whose template the files no longer have is a `fail`.

## Roads not taken

- **Entities loaded from the install file by `policy`.** It would give `policy` a view of the clients, but `policy` would then read configuration that only `session`, `exchange` and `signer` read ([Install file](/reference/install-file/#top-level-keys)), and a request could not be judged from itself. The asker sends the slice.
- **A schema the operator extends.** A type or an attribute of an operator's own would let a policy depend on facts no actor sends, and a reload could change what a request means. A new attribute is a release.
- **A Cedar namespace for sluis's names.** It makes every policy longer to write and protects against a collision that no operator can cause.
- **An action `session.step_up`.** It would split "may this person have this token" from "is the authentication enough for it" into two questions with two sets of policies that can disagree. One `forbid` with `unless` keeps them in one.
- **Dates and durations in Cedar's `datetime` extension for freshness.** It would put a clock into `policy`, which has none, and a request would no longer answer the same on another day. Seconds counted by the asker keep the vectors fixed.
- **A link kind per asking actor.** Two writers cannot share a kind, and a grant that covers both `token.issue` and `token.exchange` would have to be linked twice. One kind owned by `session`, read by `exchange`, keeps one link in one place.
- **`policy`, `console` or `exchange` as the owner of the links.** `policy` has no state port ([ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/)); `console` never writes a record ([ADR 0011](/decisions/0011-state-is-one-port-with-compare-and-set/#one-writer-per-kind)); `exchange` would own the grants of sign-in, which `session` decides.
- **Ignoring a link that does not apply.** The request would go through without a `forbid` that an operator meant to have in force.
- **Grant names beyond group names in the first working version.** Roles with an implied ladder, and `*` that never reaches sensitive scopes, are v1's grammar ([#787](https://github.com/truvity/sluis/issues/787)); how they are written in Cedar is part of the claims and grants record ([#999](https://github.com/truvity/sluis/issues/999)).
