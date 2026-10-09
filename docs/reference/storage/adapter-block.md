# The adapter block: reference

The JSON Schema is `storage/schemas/keys.schema.json`; both products `$ref` it. Concepts are in [the adapter block](../../concepts/storage/adapter-block.md).

## `keys`

| Key | Meaning |
|---|---|
| `adapter` | Required: `kms`, `transit` or `local` |
| `sign` | sluis: symmetric key that wraps the signing key ring. The ES384 and RS256 pairs are generated locally. Direct asymmetric signing is deprecated |
| `seal` | audit: seals the audit trail |
| `pseudonym` | audit: per-tenant pseudonyms (a MAC) |
| `conceal` | audit: values that must be recoverable |
| `archive` | audit: long-term storage |

A purpose is a key name or `{key, context}`. The name is an alias for `kms` and a transit key name for `transit`. ARNs and key ids are refused.

| `context` | Meaning |
|---|---|
| `default` | The instance and the purpose |
| `off` | No context |
| a map | Sent as is |

| Backend | Package | Notes |
|---|---|---|
| `kms` | `storage/keys/kms` | AWS KMS, aliases only. Encrypt takes 4096 bytes; use data keys beyond that |
| `transit` | `storage/keys/transit` | OpenBao or Vault. The context is `associated_data` (AEAD keys) or `context` (derived keys). Data keys come from `datakey/plaintext` |
| `local` | `storage/keys/local` | Tests. Refuses weak roots |

| Operation | Detail |
|---|---|
| Encrypt, Decrypt | Take the context |
| GenerateDataKey, UnwrapDataKey | Envelope encryption |
| Sign | Over a digest: SHA-384 for ES384, SHA-256 for RS256. Takes no context |
| PublicKey | Takes no context |
| MAC | HMAC-SHA-256 under a secret unique to purpose and tenant |

On `transit`, a key that cannot carry the binding is refused, because transit ignores `context` on a non-derived key and `hmac` ignores it always. In a policy, name `required_parameters` as well as `allowed_parameters`. [Use the OpenBao backends](../../guides/storage/use-the-openbao-backends.md) has the policies.

## `state`

| Backend | Package | Notes |
|---|---|---|
| SSM | `storage/state/ssm` | Parameters, encrypted under the SSM parameter store's KMS key |
| S3 | `storage/state/s3` | Objects; any S3-compatible service, such as R2 |
| OpenBao | `storage/state/openbao` | KV version 2. Opening reads `<mount>/config` and refuses `max_versions: 1`. Versions restart at 1 after `Delete`, so a revision is not held across one. A key soft-deleted outside (`kv delete`) reads as not found; `Delete` it before creating it again |
| memory | `storage/state/memory` | Tests and single-process use |

| Call | Behaviour |
|---|---|
| key | A relative path such as `signing/current`; the value is a JSON object, else `ErrNotObject` |
| `Put` | Returns a revision; takes the last seen one, empty meaning the key must not exist. A lost race is `ErrConflict` |
| `GetRev` | Reads an older version; a missing key or version is `ErrNotFound` |
| `List` | Names the keys one level down |
| `Delete` | Removes a key |
| `Child(prefix)` | A view of a sub-prefix |
| `Value[T]` | Typed handle on one key: `JSON` for a struct or map, `Raw` for opaque bytes |
| `Rotating(grace)` | The current value, and for the grace period the one it replaced. sluis uses it for `secrets.grace`, default 24h |

## The OpenBao client

| Field | Meaning |
|---|---|
| address | https only; plain HTTP is for a development server |
| namespace | Empty is the root namespace |
| CA file | Added to the system roots |
| login | JWT auth: mount, role, token file (a projected ServiceAccount token, re-read at every login) |
