## Scope

The store model of the KV add-on, and the data key of each store (ADR 0047, "Storage and encryption").

- A KV store is a named, boundary-wide store declared in signed config as the kind `KvStore`. It is not a tenant's. The name is 1 to 32 characters of `a-z`, `0-9`, `-` and `_`.
- Three kinds are added to the registry: `KvStore` (config), `KvSecret` (the metadata of one path) and `KvVersion` (the value of one version), sealed, durable and written by the work actor `kv` alone. The addresses are keyed hashes of the store and the path; an index of hashed ancestors serves a list.
- Each store has one random data key of its own: a new key purpose `secret` with the store as its scope, and the wrap id `secret/<store>/<generation>/<recipient>`. key@ refuses a grant for store A on store B's key. A store is erased by deleting its wraps.
- The sealed grant gains a `stores` field for purpose `secret`; policy seals it and key@ checks it.
- A rewrap job re-seals live versions under the newest generation and is idempotent and resumable.
- Versions, `cas`, `max_versions`, soft delete, undelete and destroy follow Vault KV v2 as ADR 0047 states.
- Done when a value is written, read, deleted and destroyed on the local installation, a grant for store A cannot open store B's key, and a deleted store is unreadable.

Decision: ADR 0047 (accepted 2026-11-12), with ADRs 0004, 0006 and 0007 amended. Requirements: REQ-KV rows, REQ-DATA-094. Related: #798 (the proposal), {{ref:61}}, {{ref:62}}, {{ref:63}}.

## Decision codes

K1 A, K3, K4 (ADR 0047); decision A4 A, 2026-11-18: KV is a building block built in the core phase, before the Lambda and Kubernetes shapes.

## Milestone

Core: kv

Linear: mirrored.
