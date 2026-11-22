# 0053 — The limit counter is keyed by the customer and the product, or by the tenant and the client, and sluis never uses the organisation

**Status:** Accepted
**Date:** 2026-11-22

Proposal: [issue #841](https://github.com/truvity/sluis/issues/841).

## Context

[0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/) decided that the limit check and policy read the customer and the product from the subject token: its issuer and context realm, and its audience or claims. It said that the organisation is not taken from the token's claims, because a customer's token from the group's identity provider carries none. It left two points open in its Follow-ups:

- whether the organisation is looked up from the group's organisation service, or left out of the limit key and the policy facts;
- what the counter uses for a subject token that has no customer or product, such as vendor staff or a robot.

The counter of the limit check ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)) is a hot item that every limited request changes, so its key has to be known before the check is built. This record closes both points.

## Decision

### The organisation is left out

- **The limit check and policy use the customer and the product, and never the organisation.** Both are read from the subject token's context realm and its audience, as 0051 says.
- **sluis does not look an organisation up anywhere.** It calls no organisation service, keeps no organisation record and puts no organisation in a counter or a policy fact.
- **Organisation-level control stays with the products,** which own that relationship. A product that limits or decides per organisation does it in its own code and with its own data.
- **The `org` attribute of `Person` and `Account` in the policy schema stays.** It is the tenant that the person belongs to ([0019](/decisions/0019-person-and-upstream-link/#the-records)), not an organisation of a customer.

### The key when the subject token has no customer or product

- **The counter is keyed by the tenant and the client** of the request, for a subject token that names no customer and no product. Vendor staff and robots are such subjects.
- **The key of every counter is therefore one of two pairs:** the customer and the product, or the tenant and the client. A counter holds identifiers and no personal data, as before.

### What this supersedes

- **[0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/), in part.** The `Open:` line in its Follow-ups, on the organisation and on the key for a subject token with no customer or product. Both are closed here. Everything else in 0051 stands, including the rule that the organisation is not taken from a claim.
- **[0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/), in part.** The words that a limit check compares a counter "for a customer, an organisation and a product", and the bullet that says use is "counted per customer, organisation and product: the three claims of the sluis token". 0051 had already replaced the three claims with the customer and the product of the subject token; this record gives the whole key. The check, the counter, the measurement, the usage records and the rest of 0046 stand. 0046's status line holds one record number, and it now names this one.

## Consequences

- The limit check has one rule for every subject. The key is a pair, and which pair depends only on whether the subject token names a customer and a product.
- sluis has no dependency on a service of the group in the request path of a limit, so a limit check never waits for an organisation lookup and never fails because one is down.
- A limit for one organisation of a customer is not something sluis offers. A product that sells by organisation keeps and checks that limit itself, beside the limit that sluis checks for the customer and the product.
- Staff and robots are counted too, per tenant and client, so a loop in one client of a tenant cannot use the limit of another.
- The requirement rows of the quota area follow: REQ-QUOTA-097 and REQ-QUOTA-099 keep their ids and cite this record, and REQ-QUOTA-100 and REQ-QUOTA-101 are added.

## Roads not taken

- **Look the organisation up from the group's organisation service.** It would put a call to another service on the request path of every limited request, and the relationship between a customer and its organisations is the products', not sluis's.
- **Take the organisation from a claim of the subject token.** A customer's token from the group's identity provider carries none ([0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/)).
- **No counter for a subject token with no customer or product.** Vendor staff and robots would pass any limit, and a runaway robot would be the one caller that is never counted.

## Follow-ups

- The key of the counter and the facts of policy, built with the first caller of the check: [#841](https://github.com/truvity/sluis/issues/841), [#1843](https://github.com/truvity/sluis/issues/1843).
- Open: sluis's usage records. The usage class of audit names a customer, an organisation and a product ([audit 0006](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md)), and sluis has no organisation to put in it. Whether sluis leaves that part empty is decided with the usage part of the emitter ([#841](https://github.com/truvity/sluis/issues/841)).
