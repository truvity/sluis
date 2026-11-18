## Scope

Retire the store-of-record export path and the sealed copy of generated client secrets, because KV replaces them (ADR 0047, "Secrets that sluis generates live in KV").

- The generated client secrets of clients that a consumer reads move to KV as `{client_id, client_secret}` under a managed prefix of a store, written by `exchange` alone. `ClientSecret`, the hash that `session` verifies, stays. A new version is a new secret; the old verifies during its overlap through the hash.
- A store's `managed` list names prefixes and their only writer. A rule at `kv`'s socket lets only that actor write there, and the Vault facade cannot write under a managed prefix.
- The sealed copy `ClientSecretExport` is retired and the kind registry loses it, in the change that makes `exchange` write the entry. Six exportable kinds remain, all public.
- The part of #1714 that builds the store-of-record destination (SSM, OpenBao) and the direct Kubernetes Secret, and exports `ClientSecretExport`, is not built.
- Done when a generated client secret is readable through KV by its consumer, the registry has no `ClientSecretExport`, and the exporter has no destination that carries a secret.

Decision: ADR 0047 (K4); ADR 0009 and ADR 0021 amended. Related: #798, #1714, {{ref:60}}, {{ref:63}}.

## Decision codes

K4 (ADR 0047); decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
