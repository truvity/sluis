# 0032 — A customer-identity installation is a front and a few customer boundaries whose tenants each have their own data keys, a tenant's client IDs carry its name, and a move keeps every subject

**Status:** Superseded by 0032
**Layer:** customer identity
**Date:** 2026-11-12

Proposal: [issue #1023](https://github.com/truvity/sluis/issues/1023).

> Archived in the clean rewrite of 2026-11-21. It keeps the parts of the original record that the design of [0045](/decisions/0032-customer-identity-installations/) replaced (the broker's single sign-on, the pairwise subject, the issuer host per tenant and the client IDs of the form `<tenant>:<name>`), as they were decided. What stands is in [0032](/decisions/0032-customer-identity-installations/). Nothing in this text is built.

## Context

[0008](/decisions/0008-boundaries-and-tenants/) fixes the words and the scale: a boundary holds keys and policy, a tenant is a scope with data keys of its own, an installation is designed for about 5 to 10 customer boundaries and thousands of tenants, and growth past a cohort is another installation ([Scale](/decisions/0008-boundaries-and-tenants/#scale)). [0004](/decisions/0004-the-data-model/#profiles) fixes how a tenant's records are written. [0031](/decisions/archive/0031-ciam-is-a-broker/) fixes who owns what. What none of them says is what the customer-identity installation looks like when put together, how a tenant's client is named, what a tenant holds, and what moving a tenant or a cohort does to the people in it.

Three gaps remained.

- **Single sign-on across products needs a shape.** The products of a customer must not each ask the person again, yet each tenant has an issuer of its own ([0017](/decisions/0017-identity-and-sign-in/#issuers)).
- **Client IDs are readable and unique per issuer,** with names of 1 to 32 characters ([0017](/decisions/0017-identity-and-sign-in/#audiences)). Thousands of tenants that each have a `web` client need a rule.
- **The NATS mapping must not become an account per tenant.** [REQ-ISO-025](/requirements/iso/#REQ-ISO-025) says "in its boundary's account". With a tenant now meaning a customer organisation, an account per tenant would be thousands of accounts.

## Decision

### The shape

- **A person signs in once, at the upstream broker.** Single sign-on across tenants is the broker's session ([0033](/decisions/archive/0033-customer-connections/#which-upstreams-an-installation-has)). Each tenant's issuer has sluis sessions of its own and two tenants never share one ([0018](/decisions/0018-sign-in-sessions/#session-chains)); a second tenant's app asks the broker again and is answered silently while the broker's session lasts, and each tenant's policy may still demand a step-up or a fresh sign-in ([0017](/decisions/0017-identity-and-sign-in/#step-up)).
- **The tenant is the controller.** Each controller is a tenant with an issuer host of its own and its own data keys. Its apps see the person's own `sub`, which no tenant or client derives (the pairwise `sub` and its sectors were cancelled, [0045](/decisions/0032-customer-identity-installations/), and removed, [0020](/decisions/0020-grant-vocabulary-and-token-claims/#the-sub-of-a-token)). A regulated customer's tenant is declared regulated ([0008](/decisions/0008-boundaries-and-tenants/#tenants)).
- **A customer installation has one boundary per regulated customer plus one shared customer boundary** ([Example installations](/decisions/0008-boundaries-and-tenants/#example-installations)). The boundary is the unit of keys, engine and failure domain, and apps cannot see it. A regulated customer's boundary has an engine, a bucket and a queue of its own; the shared boundary serves the other customers as tenants. A tenant that outgrows the shared boundary is promoted to a boundary of its own ([Promotion](/decisions/0008-boundaries-and-tenants/#promotion)), and an app notices nothing.
- **A relying party pins its tenant's issuer.** Each tenant has an issuer host of its own, under the operator's domain or its own, with discovery and the key set served there from the boundary's static documents ([0017](/decisions/0017-identity-and-sign-in/#issuers)). A boundary serves one or more issuers. Which tenant a client belongs to is in its ID.

### Tenants (the bullets that no longer stand)

- **A tenant's records are clients and limits.** They are Tenant-adjustable durable kinds, written by their one owner: `TenantClient` and the tenant's settings by `exchange`. A tenant admin, or the customer-identity module for its tenant, writes them through the API inside the bounds of a client template in config ([0004](/decisions/0004-the-data-model/#profiles), [0021](/decisions/0021-clients/)). The template fixes the redirect rules of its clients and its limits ([0036](/decisions/0036-volumetric-abuse/#inside-sluis)). A tenant has no connection of its own in sluis: its identity provider is configured in the broker, which is the one upstream ([0033](/decisions/archive/0033-customer-connections/#which-upstreams-an-installation-has)). A tenant cannot widen a claim, a ceiling or a grant, because the store refuses a write outside the bounds ([0005](/decisions/0005-the-store/#rules)).
- **A person's tenant is the tenant of the client they sign in to.** The broker's organisation id binds only a tenant's own business staff ([0033](/decisions/archive/0033-customer-connections/#which-tenant-a-person-belongs-to)). A policy guard keeps a person of one tenant out of any other tenant's scope ([REQ-ISO-036](/requirements/iso/#REQ-ISO-036) is the model; [Tenants](/decisions/0008-boundaries-and-tenants/#tenants) names the guard).
- **sluis stores no organisation membership and no tenant claim.** Which users belong to an organisation is the customer-identity module's data ([0031](/decisions/archive/0031-ciam-is-a-broker/#who-owns-what)). The claim set of a token is fixed ([0020](/decisions/0020-grant-vocabulary-and-token-claims/#the-claims-of-a-token)); a token for a regulated audience names no organisation or tenant beyond what `iss` and `aud` already say, and `iss` is the tenant's own issuer host ([0020](/decisions/0020-grant-vocabulary-and-token-claims/#the-claims-of-a-token)).
- **A tenant leaves** by disconnecting its connections, making its persons `left` ([0034](/decisions/archive/0034-end-users-and-upstream-signals/#states)) and erasing the tenant, which deletes its `data`, `pseudonym`, `chain` and `credential` wraps at once ([Tenant lifecycle](/decisions/0008-boundaries-and-tenants/#tenant-lifecycle), [0006](/decisions/0006-keys-and-sealing/#erasing-a-tenant)).

### Client names

- **A client that belongs to a product is declared,** with a readable name that is the same in every environment (`web`, `wallet`), as in [0017](/decisions/0017-identity-and-sign-in/#audiences).
- **A client that belongs to a tenant is `<tenant>:<name>`,** where `<tenant>` is the tenant's name and `<name>` the client's, each 1 to 32 characters in lower-case kebab-case. The tenant's name is set when the tenant is created, is unique in its customer boundary and is never reused or changed. The colon is the separator that `aws:<account>:<role>` already uses, and client IDs stay unique per issuer.

### Moving (the rules that no longer stand)

[0008](/decisions/0008-boundaries-and-tenants/#scale) says a tenant does not move up alone: one that needs keys becomes a boundary by [promotion](/decisions/0008-boundaries-and-tenants/#promotion), and a boundary or a cohort moves to a boundary or an installation of its own. The rules below apply to a whole boundary and to one tenant that is promoted, and moving a tenant changes nothing an app sees.

1. **A moved tenant keeps its issuer host.** `iss` is unchanged: DNS is repointed to the new boundary's `entry`, and the apps pin nothing new. The new boundary signs with keys of its own, published in the key set of the same issuer.
7. **A promoted tenant keeps its `Person` ids and its client names.**

### Example

This example is not normative, and its names are generic. A wallet product (tenant `w`) and the portal of regulated customer A (tenant `a`) are tenants of the shared customer boundary. The portal of regulated customer B (tenant `b`) is a tenant of a boundary of its own.

- A single sign-in at the broker covers all three apps. The broker's session answers the second and the third silently, and regulated customers A and B may still demand a step-up or a fresh sign-in.
- When A is promoted to a boundary of its own, its issuer and its `sub` values stay the same. The wallet and B's portal see nothing.

### Amendment of 2026-11-12: one installation per boundary, and the token claims

Maintainer decisions D1 B, D3 and D7 A, 2026-11-12 ([0045](/decisions/0032-customer-identity-installations/)). What stands: a tenant's five data keys, one customer boundary per regulated customer, the rules of moving, and a tenant as the controller of its data. What does not stand, because customers sign in through the upstream identity provider and not through a broker in sluis:

- **Single sign-on at the broker as the shape of the installation,** the pairwise `sub` per tenant sector, the issuer host per tenant and the client IDs of the form `<tenant>:<name>` ([REQ-ISO-116](/requirements/iso/#REQ-ISO-116), [REQ-ISO-077](/requirements/iso/#REQ-ISO-077) and the sector rows are withdrawn).
- **"sluis stores no organisation membership and no tenant claim"** (the bullet under Tenants) is replaced. sluis still stores no membership list. The sluis token carries `customer`, `org` and `product`, which `exchange` sets from the upstream token ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)).
- **Realms follow boundaries.** One installation serves one boundary and trusts the one realm of that boundary.

### Amendment of 2026-11-18: the customer identity phase is 0045's scope

2026-11-18 (maintainer decisions Order, Placement and Customer identity; [0044](/decisions/0044-sluis-is-a-core-and-add-ons/#the-layers/)). The phase [Customer identity and quota](/roadmap/#customer-identity-and-quota) is the scope of [0045](/decisions/0032-customer-identity-installations/): the exchange of a token of the trusted realm for a sluis token with customer, org and product. It builds no customer front, no issuer host per tenant and no sector. Read this record with its amendment of 2026-11-12 and with 0045; what it says about the front, the tenants' client IDs and the pairwise subject stays superseded. The Kubernetes installation of the core is not "the customer-identity installation" ([0011](/decisions/0011-runtimes-and-installations/)).

## Consequences (the bullets that no longer stand)

- A tenant's identity provider is a path into a product, but through the broker and not through sluis. The tenant bound and the connection's trust keep a person of one tenant from another's data, and the broker's own isolation keeps one tenant's provider from vouching for another's people.
- A person of a moved tenant keeps their `sub` and the tenant keeps its `iss`, so a relying party that stored them keeps its users and its pin.
- Two controllers are two tenants, so a regulated customer's relying party and a product's never see the same `sub`, and an app cannot tell which boundary serves its tenant.
- Each tenant's issuer host is one more name to publish and to watch with the CAA and Certificate Transparency rules of [REQ-SEC-029](/requirements/sec/#REQ-SEC-029) to [REQ-SEC-031](/requirements/sec/#REQ-SEC-031).

## Roads not taken (the bullets that no longer stand)

- **An issuer per boundary, shared by its tenants.** Apps of different controllers would share an issuer and an audience space, and moving one tenant would change its `iss`.
- **Client IDs from a counter or a random string.** Nobody could read which tenant a client belongs to in a log or a relying party's configuration.
- **A new `iss` for a moved tenant.** Every relying party would re-pin and every person would sign in again, which fails the exit that [0008](/decisions/0008-boundaries-and-tenants/#example-installations) promises.
