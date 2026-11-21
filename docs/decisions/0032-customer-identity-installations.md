# 0032 — Customers sign in through the upstream identity provider, realms follow boundaries, and a customer-identity installation serves one boundary whose tenants each have their own data keys

**Status:** Accepted
**Layer:** customer identity
**Date:** 2026-11-18

Proposal: [issue #1023](https://github.com/truvity/sluis/issues/1023).

## Context

sluis signs in staff and operators itself, with passkeys and the hardware tiers of [0017](/decisions/0017-identity-and-sign-in/). Customers are a different case. The customers of a product are many, they bring their own identity providers, and their accounts, organisations and sign-in screens are a product concern, not an identity-core concern. An upstream OpenID provider (Keycloak is one) holds the customer accounts and the organisations and federates the customers' own identity providers, as a set of realms. sluis does not build a second customer-facing identity layer beside it.

[0008](/decisions/0008-boundaries-and-tenants/) fixes the words and the scale: a boundary holds keys and policy, a tenant is a scope with data keys of its own, an installation is designed for about 5 to 10 customer boundaries and thousands of tenants, and growth past a cohort is another installation. [0004](/decisions/0004-the-data-model/) fixes how a tenant's records are written. What those records do not say is who signs a customer in, which realm an installation trusts, what a tenant holds, and what moving a tenant or a cohort does to the people in it.

An earlier design made sluis a broker: a customer-identity product owned the accounts, and sluis owned single sign-on across products, pairwise subjects per tenant, an issuer host per tenant, the federation of customers' providers, the end-user lifecycle and customer pages. That design is archived (records 0031, 0033, 0034, 0035 and the superseded part of 0032 under `docs/decisions/archive/`). Nothing in it is built, and it stays superseded.

## Decision

### Who signs in where

- **sluis signs in staff and operators itself,** and that is the whole of its own sign-in ([0017](/decisions/0017-identity-and-sign-in/)).
- **A customer signs in only at the upstream identity provider.** sluis hosts no customer account, page, registration, recovery or factor enrolment ([REQ-CIAM-017](/requirements/ciam/#REQ-CIAM-017)). The lifecycle of an end-user account, the federation of a customer's own provider and the pages of sign-in belong to the upstream. The FAPI 2.0 profile of [0038](/decisions/0038-fapi-2-profile/) is a profile of the core issuer, not a customer sign-in.
- **sluis keeps the sign-in session and the tokens.** After the exchange below, a customer holds a sluis token, and services call each other with it.

### Realms follow boundaries

- **A regulated boundary has a realm of its own,** in which only that boundary's people are known. A generic realm serves a product that is not a regulated boundary. A central realm holds the end-user accounts, and a boundary's realm brokers to it: end user, boundary realm, central realm.
- **One installation serves one boundary and trusts one realm.** The trusted issuer is declared in the config kind `ExchangeTrust` of that installation ([0022](/decisions/0022-workload-identity-and-host-enrolment/)), so a token of another realm is refused. A regulated customer's boundary therefore never sees a person or a claim of another boundary's realm.
- **A customer boundary is one installation.** A regulated customer's boundary has an engine, a bucket and a queue of its own. The other customers are tenants of a shared customer boundary. A tenant that outgrows the shared boundary is promoted to a boundary of its own ([0008](/decisions/0008-boundaries-and-tenants/)). The boundary is the unit of keys, engine and failure domain, and apps cannot see it.

### The exchange

- **`exchange` takes a token of the trusted realm as the subject token** (RFC 8693) and issues a sluis token for one audience.
- **The sluis token carries `customer`, `org` and `product`.** `exchange` sets them from the claims of the upstream token through a mapping that the installation declares for its realm, and never from the request. The claim set is in [0020](/decisions/0020-grant-vocabulary-and-token-claims/). A token that did not come from such an exchange, such as a staff sign-in, carries none of them.
- **The subject is the person's sluis subject** ([0019](/decisions/0019-person-and-upstream-link/)), one per tenant ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)).
- **What is built:** the `exchange` actor, which exchanges a sluis token for another audience (RFC 8693) and verifies a token of a trusted issuer against the key set that `ExchangeTrust` names. Sign-in of staff through a generic OpenID Connect upstream is built (`actors/adapter-upstream-oidc`).
- **What is planned:** accepting the person's token of the trusted realm as a subject token (today `exchange` verifies it and then refuses it), the mapping of realm claims to `customer`, `org` and `product`, and the three claims in the token ([#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961)).

### Tenants

- **A tenant has five data keys of its own and, by default, no authority key.** `key@` makes its `data`, `pseudonym`, `evidence`, `chain` and `credential` keys when `exchange` writes the `Tenant` ([0008](/decisions/0008-boundaries-and-tenants/)). No tenant shares a data key with another, the tenants of the shared boundary included. Where the keys live, and the issuer signing key of a tenant that keeps its keys in a customer account, is [0006](/decisions/0006-keys-and-sealing/)'s.
- **A tenant's records are clients and limits.** They are tenant-adjustable durable kinds, written by their one owner: `TenantClient` and the tenant's settings by `exchange`. A tenant admin, or the product for its tenant, writes them through the API inside the bounds of a client template in config ([0004](/decisions/0004-the-data-model/), [0021](/decisions/0021-clients/)). The template fixes the redirect rules of its clients and its limits ([0036](/decisions/0036-volumetric-abuse/)). A tenant cannot widen a claim, a ceiling or a grant, because the store refuses a write outside the bounds ([0005](/decisions/0005-the-store/)). A tenant has no identity-provider connection in sluis: its provider is the upstream's concern.
- **sluis stores no organisation membership list.** Which users belong to an organisation is the upstream's and the product's data. The token carries the one organisation of the exchange.
- **A tenant is the controller of its data.** Two controllers are two tenants, and a person known to both has a subject at each ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)).
- **A tenant is not a transport account.** The audit transport's account is per boundary ([REQ-ISO-025](/requirements/iso/#REQ-ISO-025)). Thousands of tenants never become thousands of accounts.
- **A tenant leaves by being erased,** which deletes its `data`, `pseudonym`, `chain` and `credential` wraps at once ([0008](/decisions/0008-boundaries-and-tenants/), [0006](/decisions/0006-keys-and-sealing/)).

### Moving

[0008](/decisions/0008-boundaries-and-tenants/) says a tenant does not move up alone: one that needs keys becomes a boundary by promotion, and a boundary or a cohort moves to a boundary or an installation of its own. The rules below apply to a whole boundary and to one tenant that is promoted. Moving a tenant changes no subject an app sees.

1. **A moved boundary keeps its issuer host.** `iss` is unchanged: DNS is repointed to the new boundary's `entry`, and the relying parties pin nothing new. The new boundary signs with keys of its own, published in the key set of the same issuer.
2. **A moved tenant's subjects survive.** A person's `sub` is the id of the `Person` item and is made of no tenant key, so a move changes none. A whole boundary that moves also keeps its `mac` key, so its link ids and upstream session ids survive; a promoted tenant's link ids are made again under the new boundary's `mac` key ([0008](/decisions/0008-boundaries-and-tenants/)).
3. **The persons keep their subject.** The move reads each durable kind through the old boundary's store and writes it at its new address. A cohort that moves to another installation is exported from the subject lifecycle and imported with the same subjects.
4. **Sessions and tokens do not move.** People sign in again, silently at the upstream where its session lasts. Temp kinds do not move.
5. **While the old and the new installation both run, they may exchange tokens** only under the declared trust of one operator ([0008](/decisions/0008-boundaries-and-tenants/)); every other exchange between them stays refused.
6. **A move into a customer account** re-wraps the tenant's keys under the customer's KMS key in the same step and changes nothing else ([0006](/decisions/0006-keys-and-sealing/)).
7. **A promoted tenant keeps its `Person` ids and its clients' names.**

## Consequences

- sluis has one sign-in surface to defend, for staff, and no second identity layer for customers.
- The three claims need the upstream's claims to be trustworthy. The installation trusts one realm, so a wrong mapping is a configuration error of that installation and shows in its audit log, not a cross-boundary leak.
- A regulated boundary's relying parties learn only that boundary's customer, organisation and product, because the installation issues nothing about another boundary.
- Thousands of customer organisations cost durable items and five data keys each, not authority keys, processes or ceremonies. The yearly certification count follows the customer boundaries, a handful.
- A regulated customer that needs keys of its own is one step: a tenant in a customer account ([0006](/decisions/0006-keys-and-sealing/)), or a customer boundary of its own. Whether the keys stay with the operator or sit in an account of the customer is a contract choice that the design does not wait on.
- sluis cannot answer "who is in this organisation": the upstream and the product can.
- A person of a moved tenant keeps their `sub`, so a relying party that stored it keeps its users.
- The Kubernetes installation of the core is not "the customer-identity installation" ([0011](/decisions/0011-runtimes-and-installations/)).
- Requirements: [REQ-AUTHN-112](/requirements/authn/#REQ-AUTHN-112), [REQ-TOKEN-045](/requirements/token/#REQ-TOKEN-045), [REQ-EXCH-027](/requirements/exch/#REQ-EXCH-027) and [REQ-DATA-025](/requirements/data/#REQ-DATA-025).

## Roads not taken

- **Keep sluis as a broker with its own customer-identity layer.** It builds a second identity layer beside the upstream that already owns the accounts.
- **A realm per customer.** Realms follow boundaries: a product that is not regulated shares the generic realm, and only a regulated boundary needs a segregation of its own.
- **One installation that trusts every realm.** A token of one boundary's realm could then reach another boundary's relying parties.
- **One customer boundary for the whole customer base.** All products would share an issuer and an audience space, so a token for one product would be a candidate for another's relying parties.
- **A boundary for every tenant.** Each would carry keys, an issuer host and a yearly certification ([0008](/decisions/0008-boundaries-and-tenants/)).
- **Pairwise subjects per sector.** They needed tenants with sectors and a lifecycle of subjects, and the upstream already separates the boundaries by realm.
- **Organisation membership stored in sluis.** It would make sluis the store of a product's user groups.
- **A `memberships` claim listing a person's organisations.** It would carry the product's membership data through sluis and name organisations to relying parties that must not learn them. The product holds the active organisation.
- **A new subject for a moved person.** Every relying party would lose its users and every link would be a new person.

## Follow-ups

- Customer boundaries, tenant templates and the product's contract: [#1716](https://github.com/truvity/sluis/issues/1716).
- Subject export and import for a move: [#1717](https://github.com/truvity/sluis/issues/1717).
- The exchange of the trusted realm's token and the three claims: [#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961).
- Open: how a tenant's client is named so that client ids stay unique in the boundary's issuer ([#1716](https://github.com/truvity/sluis/issues/1716)). The earlier rule `<tenant>:<name>` went with the issuer host per tenant.
- Open: which issuer a promoted tenant's relying parties pin after the promotion ([#973](https://github.com/truvity/sluis/issues/973)).
