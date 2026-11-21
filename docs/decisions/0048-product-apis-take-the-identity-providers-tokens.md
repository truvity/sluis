# 0048 — Customers sign in at the group's identity provider, product APIs take its tokens, and sluis exchanges one only for calls down to infrastructure

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #1023](https://github.com/truvity/sluis/issues/1023).

## Context

[0032](/decisions/0032-customer-identity-installations/) and [0020](/decisions/0020-grant-vocabulary-and-token-claims/) designed a customer layer in sluis: a customer signs in at an upstream identity provider, `exchange` takes that token as the subject token and issues a sluis token that carries `customer`, `org` and `product`, and "services call each other with it". The record [0044](/decisions/0044-sluis-is-a-core-and-add-ons/) named the layer `customer identity`. None of this is built.

The group that sluis serves has its own single sign-on rules, and they stand. The group's identity provider is a Keycloak installation with a realm per context, and a realm above the context realms holds the accounts. The rules are these.

- A product service uses the tokens of the group's identity provider. A product API never accepts a sluis token.
- The identifier of a person is pairwise per boundary: the same person has a different identifier in each context.

The earlier design gave product services a second kind of token, issued by sluis, for the same customers. This record removes it. sluis stays an identity product for staff, operators and workloads, and it is the layer under the products, not a second sign-in for them.

## Decision

### What the products use

- **A customer signs in at the group's identity provider.** A product API checks the token of that provider and never a sluis token. sluis hosts no customer account, page or registration ([REQ-CIAM-017](/requirements/ciam/#REQ-CIAM-017)).
- **The identifier of a customer is pairwise per boundary.** A context realm gives the same person a different identifier from another context realm's. sluis never joins them.
- **sluis issues no token with `customer`, `org` or `product` to a product service,** and services do not call each other with sluis tokens. The three claims leave the claim set of [0020](/decisions/0020-grant-vocabulary-and-token-claims/); a sluis token never carries them ([REQ-TOKEN-058](/requirements/token/#REQ-TOKEN-058)).

### What sluis accepts

- **sluis accepts a token of the group's identity provider only as the subject token of an exchange** (RFC 8693), and only for a call down to infrastructure ([REQ-EXCH-030](/requirements/exch/#REQ-EXCH-030)). An infrastructure call is a call to a sluis service, or to something sluis mints a credential for. A call to a product API is never one.
- **sluis trusts an upstream context realm, never the central realm.** The realm is the one that the `ExchangeTrust` of the installation names, and one installation trusts one realm, as [0032](/decisions/0032-customer-identity-installations/#realms-follow-boundaries) says. A token of any other realm, the central one included, is refused ([REQ-EXCH-027](/requirements/exch/#REQ-EXCH-027)).
- **The token that comes out of the exchange is for an infrastructure audience.** It has the claims of [0020](/decisions/0020-grant-vocabulary-and-token-claims/) and no `customer`, `org` or `product`. Its lifetime ends no later than the subject token's ([REQ-EXCH-004](/requirements/exch/#REQ-EXCH-004)).

### What this supersedes

- **[0020](/decisions/0020-grant-vocabulary-and-token-claims/), in part.** The row `customer`, `org`, `product` of the claim table; the bullet that says the token a customer receives after the exchange carries one customer, one organisation and one product; the sentence in the Consequences about a regulated boundary's relying party reading the three claims; and "Planned: `customer`, `org` and `product`" in the built-and-planned note and the Follow-ups. Everything else in 0020 stands, including the exchange as the way a token is obtained for another audience.
- **[0032](/decisions/0032-customer-identity-installations/), in part.** In "Who signs in where", the last bullet (a customer holds a sluis token and services call each other with it). In "The exchange", the second bullet (the sluis token carries `customer`, `org` and `product`, set through a mapping of realm claims), and in the planned part, the mapping and the three claims. The first bullet of "The exchange" stands, with the limit to infrastructure calls above, and its third bullet is open (see the Follow-ups). The Consequences and Follow-ups that rest on the three claims. The `customer identity` layer, and the name "customer-identity installation" for a part of the product, go too. The sections on realms, tenants and moving stand.
- **[0044](/decisions/0044-sluis-is-a-core-and-add-ons/), in part.** The layer `customer identity`: sluis is a core and add-ons, and the Customer identity row of the core table is not a layer. What remains of that row is the exchange of a context realm's token for an infrastructure call, which is core work of the `exchange` actor. The `Layer` value `customer identity` stays valid for the records that carry it as decided, and no new record takes it.
- **[0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/), in part.** The sentence that says what is limited is counted "per customer, organisation and product: the three claims of the sluis token". The limit check, the counter, the usage records and the rest of 0046 stand. The key of a counter is open (see the Follow-ups).

### The requirements

The rows that require a sluis token for a product service are withdrawn or reworded, and keep their ids ([REQ-TOKEN-045](/requirements/token/#REQ-TOKEN-045), [REQ-TOKEN-058](/requirements/token/#REQ-TOKEN-058), [REQ-EXCH-029](/requirements/exch/#REQ-EXCH-029), [REQ-POLICY-024](/requirements/policy/#REQ-POLICY-024), [REQ-TEST-020](/requirements/test/#REQ-TEST-020), [REQ-QUOTA-097](/requirements/quota/#REQ-QUOTA-097)).

## Consequences

- A product keeps one kind of token to verify, the group's, and a product's sign-in does not depend on sluis. A sluis outage stops infrastructure calls and staff sign-in, and not a customer's session in a product.
- sluis answers "who is this customer" with a token only for the infrastructure call that asked, and holds no organisation or product data of a customer's session. It still cannot answer "who is in this organisation" ([0032](/decisions/0032-customer-identity-installations/)).
- The roadmap milestone `customer-identity` shrinks to the exchange of a context realm's token for infrastructure calls, and the page of the product says "a core and add-ons".
- A policy rule cannot read `customer`, `org` or `product` from a sluis token, because none carries them.
- The tenants, clients and keys of a customer boundary are not withdrawn here, and their purpose needs a second look now that no product relies on a sluis token for its customers (see the Follow-ups).

## Roads not taken

- **Keep the exchange as designed.** Product services would hold two kinds of token for the same customer, and sluis would become a second issuer in the sign-in path of every product, against the group's single sign-on rules.
- **A sluis token for products that carries only a pairwise identifier.** It is still a sluis token that a product API would accept, which the rules forbid.
- **Trust the central realm as well.** One token would then reach the relying parties of every boundary, which the realm per boundary is there to prevent.
- **Withdraw the exchange altogether.** An infrastructure call still needs a short-lived sluis credential for a caller whom the group's identity provider vouches for, and the exchange is the one place where a proof is verified ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **Move 0032 and 0020 to the archive.** Most of both stands: tenants, moving, grant names, the other claims, the lifetimes. A record that is superseded in part keeps its place and says so in its status.

## Follow-ups

- The exchange of a context realm's token for an infrastructure audience: [#782](https://github.com/truvity/sluis/issues/782), [#785](https://github.com/truvity/sluis/issues/785), [#961](https://github.com/truvity/sluis/issues/961).
- Open: how an audience is declared an infrastructure audience, so that `exchange` refuses a subject token of the group's identity provider for any other ([#961](https://github.com/truvity/sluis/issues/961)).
- Open: which `sub` the token of such an exchange carries: the person's sluis subject through the link of [0019](/decisions/0019-person-and-upstream-link/), or the pairwise identifier of the context realm ([#961](https://github.com/truvity/sluis/issues/961)).
- Open: whether the limit check of [0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/) and a policy decision may read the customer, the organisation and the product from the claims of the subject token, as facts, now that no sluis token carries them ([#841](https://github.com/truvity/sluis/issues/841), [#1843](https://github.com/truvity/sluis/issues/1843)).
- Open: whether a person of a customer upstream still signs in through `signin` with a sluis session ([0018](/decisions/0018-sign-in-sessions/#customers)), now that a token of the group's identity provider enters sluis only as a subject token ([#793](https://github.com/truvity/sluis/issues/793)).
- Open: what the tenant clients and the customer boundaries of [0032](/decisions/0032-customer-identity-installations/) are for when a product API accepts no sluis token ([#1716](https://github.com/truvity/sluis/issues/1716)).
