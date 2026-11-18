## Scope

The Vault KV v2 subset that External Secrets (ESO) reads, at Level 1, and the command that staff use (ADR 0047, "External Secrets reads a Vault KV v2 subset").

- `kv` serves ten requests on a host of its own: token lookup-self and revoke-self, the JWT login, read of a version, read, list and write of metadata, write, soft delete of the latest version and destroy of a secret. About fourteen handlers. `entry` routes `/v1/` to `kv`.
- The Vault token is the sluis access token. The only login is the JWT one, with a ServiceAccount token ({{ref:64}}). AppRole and a static token are not offered.
- ESO's `path` is one segment, the store. A multi-segment path and a non-empty `X-Vault-Namespace` are refused. Errors are `{"errors": [...]}`.
- `sluis kv` reads and writes through the token of `sluis token --audience kv`. `sluis kv put` takes values from standard input or a file.
- Three layers of tests: unit tests in `kv`, a differential suite against a pinned OpenBao dev server that replays the 14 recordings ESO's OpenBao provider ships, and an ESO job in a kind cluster with a namespaced `SecretStore`, an `ExternalSecret` that extracts and finds, and a `PushSecret`.
- Level 2 of the facade is not here. It comes when a consumer needs it.
- Done when the ESO job passes in CI and asserts one `kv.read` record per read.

Decision: ADR 0047. Related: #798, {{ref:60}}, {{ref:61}}, {{ref:62}}, {{ref:64}}.

## Decision codes

K1 A (ADR 0047); decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
