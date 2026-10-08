# The adapter block: reference

## `keys`

The JSON Schema is `storage/schemas/keys.schema.json`; both products `$ref` it.

| Key | Meaning |
|---|---|
| `adapter` | required: `kms`, `transit` or `local` |
| `sign` | sluis: the key that wraps the signing key ring (a symmetric key; the ring's ES384 and RS256 pairs are generated locally). Direct signing with an asymmetric key is deprecated |
| `seal` | sealing the audit trail; audit |
| `pseudonym` | per-tenant pseudonyms (a MAC); audit |
| `conceal` | values that must be recoverable; audit |
| `archive` | long-term storage; audit |

A purpose is a key name (alias for `kms`, transit key name for `transit`) or `{key, context}`. `context` is `default`
(the instance and the purpose), `off`, or a map sent as is. ARNs and key ids are refused.

| Backend | Package | Notes |
|---|---|---|
| `kms` | `storage/keys/kms` | AWS KMS; aliases only; Encrypt takes 4096 bytes, use data keys beyond that |
| `transit` | `storage/keys/transit` | OpenBao or Vault transit; key names; the context travels as `associated_data`, or as the derivation context of a derived key |
| `local` | `storage/keys/local` | tests; refuses weak roots |

Operations: Encrypt, Decrypt, GenerateDataKey and UnwrapDataKey (envelope encryption), Sign over a digest (SHA-384 for
ES384, SHA-256 for RS256), PublicKey, and MAC (HMAC-SHA-256 under a secret unique to purpose and tenant). Asymmetric
Sign and PublicKey take no context, so an asymmetric key is separated from the others only by being a different key.
The default context is `{instance, purpose}`.

On `transit` the context rides in `associated_data` (AEAD keys) or in `context` (derived keys). Transit silently ignores
`context` on a key that is not derived and `hmac` ignores it altogether, so the backend refuses a key that cannot carry
the binding rather than send one that binds nothing. Data keys come from `datakey/plaintext`. In a policy, name
`required_parameters` as well as `allowed_parameters`, which pins a value but does not require the parameter.

## `state`

| Backend | Package | Notes |
|---|---|---|
| SSM | `storage/state/ssm` | parameters, encrypted under the KMS key the SSM parameter store uses |
| S3 | `storage/state/s3` | objects; any S3-compatible service, such as R2 |
| OpenBao | `storage/state/openbao` | KV version 2, through the shared client; the mount's `max_versions` must be 2 or more (the store refuses to open otherwise), and a key's versions start again at 1 after `Delete`, so do not hold a revision across a delete |
| memory | `storage/state/memory` | tests and single-process use |

A key is a relative path such as `signing/current`. Every value is a JSON object (anything else is `ErrNotObject`);
`Put` returns a revision and takes the revision last seen (empty means the key must not exist); a lost race is
`ErrConflict`, a missing key or version is `ErrNotFound`; `List` names the keys one level down; `Delete` removes a key;
`Child(prefix)` is a view of a sub-prefix; `GetRev` reads an older version.

`Value[T]` is a typed handle on one key (`JSON` encodes a struct or map, `Raw` keeps opaque bytes). `Rotating(grace)`
returns the current value and, for a grace period after a rotation, the one it replaced: sluis uses it for a client
secret (`secrets.grace`, default 24h), so the relying party can switch over while the old secret still works.

## The OpenBao client

| Field | Meaning |
|---|---|
| address | https only; plain HTTP is for a development server |
| namespace | empty is the root namespace |
| CA file | added to the system roots |
| login | JWT auth: mount, role, token file (a projected ServiceAccount token, re-read at every login) |

The role is what an estate controls: it binds the token's subject and audience and names the policies. The policies
for KV and transit, and how to pin the encryption context in them, are in
[use the OpenBao backends](../how-to/use-the-openbao-backends.md).
