## Scope

The shape of the limit counters, their period and the limit's configuration, written with the first caller of the check ({{ref:56}}; ADR 0046, follow-ups).

- A configuration kind for limits, written by the product: the meter (name, unit and kind, ADR 0040), the period and the limit value, per customer, organisation and product.
- The counter item in the store, its period roll-over and its key.
- A dated load test of the hot counter on each state driver in production use, before enforcement is switched on in production.
- Done when the kind and the counter shape are written down in the quota requirement rows, the first caller uses them, and the load test result is recorded.

Decision: ADR 0046, with the meters of ADR 0040.

## Decision codes

Maintainer decision of 2026-11-18: Quota comes right after Core.

## Milestone

Quota

Linear: mirrored.
