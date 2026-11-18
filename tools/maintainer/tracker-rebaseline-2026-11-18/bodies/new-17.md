## Scope

Decisions L1 B and K2 B: the index for the audit search on postgres, rebuildable from the log (REQ-AUDIT-016); one driver with two modes, `cnpg` here and `aurora` on Lambda (issue {{ref:42}}, ADR 0028); no DynamoDB index driver. Own database and role on the one CNPG cluster (issue {{ref:44}}).

## Decision codes

L1 B, K2 B (maintainer decisions of 2026-11-18).

## Milestone

Core: kubernetes

Linear: mirrored.
