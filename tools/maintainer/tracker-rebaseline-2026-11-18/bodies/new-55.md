## Scope

Usage is its own record class, apart from security audit (ADR 0046). The emitter side is #841 (moved to this milestone). This issue is the transport and the writer side.

- A usage record is never dropped, never buffered in memory and never counted as lost. The producing operation uses the blocking emit (`EmitRequired`) and fails when the transport does not accept the record durably.
- Every usage record carries the idempotency key of ADR 0030, `source` plus `id`. The writer stores a repeat once.
- A full spool, an absent transport or a refusing writer is an error of the operation.
- A usage record names the customer, organisation and product as identifiers and holds no personal data.
- Security audit keeps its counted loss (ADR 0029), unchanged; a test shows that a security fact is lost and counted while a usage record is refused.
- Done when a usage record whose store fails fails its operation, and a repeated record is stored once.

Decision: ADR 0046 (accepted 2026-11-12). Requirements: REQ-AUDIT-189, REQ-AUDIT-190, REQ-AUDIT-132.

## Decision codes

D4 A, D7 A (ADR 0046); maintainer decision of 2026-11-18: Quota comes right after Core.

## Milestone

Quota

Linear: mirrored.
