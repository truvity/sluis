## Scope

A smoke test for the Customer identity milestone on the local installation, with a Keycloak test realm as the upstream (two realms, to show the refusal). It follows {{ref:50}}, {{ref:51}} and {{ref:52}}.

- A token of the test realm is exchanged for a sluis token that carries `customer`, `org` and `product`.
- A token of a second realm is refused.
- The audit log holds the exchange and the refusal.
- The smoke runs in CI with the other smokes of the local installation.
- Done when the done-condition of phase P2 for the upstream is met: a token from the upstream test realm is exchanged for a sluis token with the three claims.

## Decision codes

Maintainer decision of 2026-11-18: Customer identity comes right after Core.

## Milestone

Customer identity

Linear: mirrored.
