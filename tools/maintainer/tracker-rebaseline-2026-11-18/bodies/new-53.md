## Scope

The policy schema gains the three claims of the sluis token ({{ref:52}}) as facts that a decision may use (ADR 0007, ADR 0045). The schema stays fixed; the three attributes are added to it.

- The facts come from the verified token, never from the request.
- A decision that does not use them behaves as before; an absent claim is an absent fact.
- A negative test shows that a request cannot supply its own customer, organisation or product.
- Done when a policy document can allow or refuse on the three facts and the render and drift tests pass.

## Decision codes

Maintainer decision of 2026-11-18: Customer identity comes right after Core.

## Milestone

Customer identity

Linear: mirrored.
