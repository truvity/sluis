## Scope

A smoke test for the Quota milestone on the local installation, run in CI with the other smokes. It follows {{ref:55}} and {{ref:56}}.

- A usage record whose store fails fails its operation.
- A repeated usage record is stored once.
- A security fact is lost and counted while a usage record is refused.
- A request over its limit is refused and a request under it is allowed.
- Done when the done-condition of phase P2 for usage is met: a usage record whose store fails fails its operation, and a repeated record is stored once.

## Decision codes

Maintainer decision of 2026-11-18: Quota comes right after Core.

## Milestone

Quota

Linear: mirrored.
