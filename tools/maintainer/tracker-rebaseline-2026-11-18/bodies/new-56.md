## Scope

The `quota` actor answers a limit check next to the policy decision (ADR 0046).

- The check compares the counter of usage for a customer, organisation and product in the current period with a limit, and answers allow or refuse.
- The product writes the limit; sluis holds the counter. sluis holds no plan, price, money or balance: the allowance engine of ADR 0039 is not built.
- The counter is a hot item. The starting point is the measurement of ADR 0039 of how one conditional update on one item behaves under load.
- Done when a request over its limit is refused and a request under it is allowed, in a test on the local installation.

Decision: ADR 0046. Requirement: REQ-QUOTA-097. The earlier Quota issues (#915, #1026, #1144, #1145) stay closed as not planned: they built the allowance and balance engine that ADR 0046 supersedes.

## Decision codes

D4 A, D7 A (ADR 0046); maintainer decision of 2026-11-18: Quota comes right after Core.

## Milestone

Quota

Linear: mirrored.
