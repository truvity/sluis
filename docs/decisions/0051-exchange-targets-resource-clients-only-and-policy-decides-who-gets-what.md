# 0051 — Exchange targets resource clients only, for every subject alike, and policy decides who gets what

**Status:** Accepted; partly superseded by 0053
**Date:** 2026-11-21

Proposal: [issue #1023](https://github.com/truvity/sluis/issues/1023).

## Context

[0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/) let `exchange` take a token of the group's identity provider only for "a call down to infrastructure", and it left open how an audience is marked as an infrastructure audience. It left four more points open: what the limit check and policy read as the customer, the organisation and the product; whether a customer still signs in through `signin`; what tenant clients and customer boundaries are for; and, with [0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/), whether KV is built before parity.

Marking audiences one by one would give each subject its own rules: one for staff, one for a customer's staff, one for an end user, one for a robot. The maintainer decided on one rule instead. The difference that matters is a property of the client that a token is for, and not of the person or machine that asks.

## Decision

### Two kinds of client

sluis has two kinds of client.

- **Resource clients:** an AWS role, Kubernetes, a database (including the roles used for row-level security), SSH, the vendor minters and KV. A token for a resource client is obtained by exchange.
- **Sign-in clients (applications):** staff web tools and the console. A token for a sign-in client is obtained by sign-in only, never by exchange.

The kind is a property of the client and not of the person. There is no "infrastructure" flag on an audience.

### Exchange targets resource clients only

- **`exchange` issues tokens only for resource clients, whatever the subject token is.** The subject token may be sluis's own sign-in (vendor staff), a token of the group's identity provider from a customer realm (customer staff or end users), or a workload's token (robots). No subject gets a sign-in client's token by exchange.
- **That keeps the rule of [0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/) structurally:** a product API or an application never receives a sluis token, because it is a sign-in client or a client of the group's identity provider, and neither is a target of an exchange.
- **sluis makes no formal distinction between a vendor's employee, a customer's employee, an end user and a robot.** Between machine-to-machine and employee credentials there is no difference: an engineer can assume a robot's role for testing, and a customer's token is exchanged to give fine-grained access inside a system, such as row-level security in a database.
- **Who gets which resource, and with what scope, is a Cedar policy decision** ([0007](/decisions/0007-policy-and-sealed-grants/)) on facts of the subject token: its issuer and realm, its subject, and the customer and the product (below).
- **[REQ-EXCH-030](/requirements/exch/#REQ-EXCH-030) keeps its id and is reworded** to "resource clients only".

### The facts of the limit check and of policy

- **The limit check and policy read the customer and the product as facts of the subject token:** its issuer and context realm, and its audience or claims.
- **The organisation is not taken from the token's claims.** A customer's token from the group's identity provider carries no organisation claim.
- **This closes the open point of 0048 on the limit key.** The counter of the limit check ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)) is keyed by what this section says, and no longer by the three claims of a sluis token.

### Only staff and operators sign in to sluis

- **Staff and operators are the only people who sign in to sluis.** A customer's token reaches sluis only as the subject of an exchange. Customers get no sluis sign-in session.
- **The "Customers" part of [0018](/decisions/0018-sign-in-sessions/#customers) goes.** The classes of sign-in sessions stay, for staff and operators.

### Boundaries and tenant clients

- **Boundaries stay the unit of keys, policy and audit for a customer's infrastructure** ([0008](/decisions/0008-boundaries-and-tenants/), [0032](/decisions/0032-customer-identity-installations/)).
- **Tenant clients are resource clients only.** A tenant admin, or a product for its tenant, registers a client for a resource of the tenant. Such a client signs nobody in, so it has no redirect hosts, no sign-in profile and no login domain.

### KV is built now, without External Secrets

- **The KV core is built now, before parity, in its place in the core of the [roadmap](/roadmap/):** stores, policy by path, a data key per store and a blocking audit fact ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)). It is built without the External Secrets facade.
- **KV is a resource client.** Staff and workloads get a token for it by exchange.
- **External Secrets reading KV stays a later option,** as [0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/) says; that record is unchanged on it. The requirement rows that 0049 moved to the backlog milestone are the facade, its `k8s-sa-token` login slice and the moving of generated secrets, and they stay there.

### What this supersedes

- **[0018](/decisions/0018-sign-in-sessions/), in part.** The section "Customers" (the renewal at the upstream and a customer's step-up) and the sentence in the Consequences about a customer who is signed out of sluis once a day. The requirement rows REQ-SESS-035, REQ-SESS-036 and REQ-SESS-037 are withdrawn. Everything else in 0018 stands, including the human and agent classes and the staff sign-in.
- **[0021](/decisions/0021-clients/), in part.** In "The client declaration", the slots of a tenant client that only a sign-in client uses (redirect hosts, sign-in profiles) and the tenant's login domain. A tenant client is a resource client. Everything else stands, including the client `kind` (`public`, `confidential`, `exchange`), which says how a client authenticates and is separate from the two kinds above.
- **[0032](/decisions/0032-customer-identity-installations/), in part.** In "Tenants", the redirect rules of a tenant's clients: a tenant's clients are resource clients. The sections on realms, boundaries, the rest of tenants and moving stand.
- **[0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/), in part.** What a counter is counted by: the customer and the product of the subject token, as the section above says. The check, the counter, the usage records and the rest of 0046 stand.
- **[0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/), in part.** "An infrastructure audience" and "a call down to infrastructure" are read as "a resource client", and the token that comes out of an exchange is a token for a resource client. The open points on how an audience is declared, on the limit key and the facts of policy, on a customer's sign-in session and on tenant clients and customer boundaries are closed here. [0050](/decisions/0050-the-exchanged-token-carries-the-context-realms-pairwise-subject/) uses the same words and is read the same way.
- **[0049](/decisions/0049-ssm-stays-the-only-secret-store-for-now/), in part.** The sentence in the Consequences and the open follow-up on whether the `kv` actor and `sluis kv` are built before parity.

A record that pointed at 0048 or 0050 in its status line (0020, 0032, 0046 and 0048 itself) is still superseded in the parts that record names. The status line holds one record number, and it now names this one.

## Consequences

- One rule covers every subject. A new kind of caller needs a policy rule and no new exchange path.
- A client's kind says what it is for, so an application cannot be given a sluis token by exchange, whoever asks and whatever policy says.
- A policy rule can read the issuer, the realm, the subject, the customer and the product of the subject token. Rules for a customer's person and for a robot are written in the same language as rules for staff.
- sluis no longer plans a session for a customer, so there is no silent renewal at the upstream and no customer step-up in sluis. The group's identity provider keeps the customer's session.
- The exchange has to know the kind of every client it is asked to issue a token for, and refuses a sign-in client. The tenant client and its template lose the slots that only a sign-in client needs.
- The KV core arrives before parity, so staff and workloads can use it without waiting, and the roadmap has no open question about it.

## Roads not taken

- **An "infrastructure" flag on an audience.** It names the purpose of an audience instead of what the client is, and each audience would need its own decision.
- **A different rule for each kind of subject.** Staff, a customer's staff, an end user and a robot would each have an exchange path to build, test and keep apart, and a robot role could not be tried by an engineer.
- **Exchange for sign-in clients too.** A product API or an application would then receive a sluis token, against 0048.
- **A sluis sign-in session for customers.** It would be a second sign-in for the customers of a product, beside the group's identity provider that already holds it.
- **Take the organisation from a claim of the subject token.** The group's identity provider's token carries none.
- **Wait for parity before building the KV core.** Nothing in the core of KV waits for External Secrets, and a consumer needs it sooner.
- **Withdraw customer boundaries and tenant clients.** The boundary is still the unit of keys, policy and audit for a customer's infrastructure, and a tenant's resource client is how a product gets a client for its own resource.

## Follow-ups

- The exchange actor, the client kinds and the refusal of a sign-in client as a target: [#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961).
- The facts of policy and of the limit check: [#1843](https://github.com/truvity/sluis/issues/1843), [#841](https://github.com/truvity/sluis/issues/841).
- Tenant clients and customer boundaries as resource clients: [#1716](https://github.com/truvity/sluis/issues/1716).
- Open: whether the organisation is looked up from the group's organisation service or left out of the limit key and the policy facts. A subject token with no customer or product, such as vendor staff or a robot, gives none of these facts, and what the counter uses for it is open ([#841](https://github.com/truvity/sluis/issues/841), [#1843](https://github.com/truvity/sluis/issues/1843)).
- The KV core, before parity: [#798](https://github.com/truvity/sluis/issues/798).
