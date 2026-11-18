## Scope

KV on the store engine of each shape, and a measurement before the add-on ships (ADR 0047, "Storage and encryption" and "Follow-ups").

- KV stores its three kinds through the store service, so it runs on rqlite (boards and local), DynamoDB (Lambda) and PostgreSQL on CloudNativePG (Kubernetes). The Lambda and Kubernetes engines are built later in their own milestones; this issue adds the KV cases to each engine's conformance tests as the engine arrives.
- The first limits are measured on three boards, with dates: latency of a read and a write, a list of 10,000 secrets, a value of 256 KiB, a full spool. Values pass through Raft, which is why the cap on a value is small.
- The limits are 256 KiB for a value, 512 bytes and 16 segments for a path, 64 pairs of custom metadata, 10,000 secrets for a store and 100 versions for a secret, until the measurement changes them.
- Done when the measurement is recorded with its date and the KV cases run on the rqlite engine in CI.

Decision: ADR 0047, principle 9 (measure before deciding). Related: {{ref:60}}, #952 (rqlite engine).

## Decision codes

Decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
