## Scope

One audit fact for every KV request, with a blocking emit for a success (ADR 0047, "Audit").

- The actions are `kv.login`, `kv.read`, `kv.list`, `kv.metadata.read`, `kv.write`, `kv.metadata.write`, `kv.delete`, `kv.undelete`, `kv.destroy`, `kv.rewrap`, `kv.sweep` and `kv.deny`, each with a row in the audit action table.
- A success fails with 503 when its record is not durable. For a destroy the emit comes first. When the spool of one board is full or absent, that board's `kv` answers 503 and the other boards still serve.
- A denial is best effort and counted, and discloses nothing.
- A record never holds a value, a key name inside a value, a hash or the length of a value, a ciphertext or a token. A test greps every record of a full run for the values it wrote.
- The record's tenant is the caller's tenant, sealed under that tenant's evidence key. The operator's audit grant names every tenant.
- Done when a read, a write and a destroy each leave one record, a failed emit fails the call, and the grep test passes.

Decision: ADR 0047, ADR 0029 (the successful KV actions join the facts that wait). Requirement: REQ-KV-015. Related: {{ref:60}}, {{ref:63}}.

## Decision codes

K2 A (ADR 0047); decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
