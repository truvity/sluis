# Bucket contract

The v1 archive layout: prefixes, object formats, metadata and seals. The three parts share nothing else ([0058](../../decisions/0058-three-parts-installed-independently.md)). See [capabilities](capabilities.md) for what is built. Decided in [0060](../../decisions/0060-v1-bucket-layout.md), [0061](../../decisions/0061-seals.md) and [0062](../../decisions/0062-observe-follows-the-bucket.md).

## Layout

Paths are relative to the installation's prefix. Times are UTC. `<profile>` and `<tenant>` contain no `/`.

| Prefix | Holds | Written by | Locked |
|---|---|---|---|
| `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` | One object per ingest batch | ingest | Yes, the profile's retention |
| `catalogue/<app>/<version>` | The application's catalogue at that version | ingest | Yes |
| `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws` | One seal per profile, tenant and hour | notary | Yes, as the records it covers |
| `keys/roots.jwks` | The trust anchors, as a JWK Set | the operator | No, versioned |
| `keys/delegations/<thumbprint>/<ULID>.jws` | Delegations to a signing key | a root | No |
| `keys/revocations/<ULID>.jws` | Revocations of a signing key | a root | No |

## Records

| Part | Rule |
|---|---|
| `<hh>` | The hour of ingest time: when ingest took the batch, not any record's time |
| `<ULID>` | Generated when the put starts, from the same clock, so keys sort in arrival order within an hour. Never shared. A key whose ULID is from another hour than the key names is not a key of this contract |
| Body | One batch, newline-delimited JSON, zstd-compressed (`Content-Encoding: zstd`), one record per line |
| `record` | `audit.v1.Record` in canonical JSON (RFC 8785 over the protobuf JSON mapping) |
| `hash` | Lower-case hex SHA-256 of that canonical encoding |
| Lines | A writer embeds canonical bytes as they are. A reader canonicalises `record` before hashing, so other whitespace still checks. A line is never changed, merged or reordered. Line numbers, from one, address a record in an index |
| Tenant with `/` | Cannot be written; the writer keeps the record aside with the reason. `@platform` is valid |
| Retention | Set on the put from the profile ([0045](../../decisions/0045-s3-object-lock-as-the-record.md), [0056](../../decisions/0056-lock-modes-and-store-tiers.md)) |

Each line:

```json
{"hash": "<hex sha256>", "record": { ... }}
```

Every object carries these `x-amz-meta-` keys:

| Key | Value |
|---|---|
| `format` | `1` |
| `sha256` | Hex SHA-256 of the object's stored bytes |
| `count` | The number of records |

## Catalogue

`catalogue/<app>/<version>` is written once with `If-None-Match: *`. Identical bytes succeed. Different bytes stop the writer.

## Seals

A seal is a compact JWS.

| Header field | Value |
|---|---|
| `alg` | `ES384` |
| `typ` | `audit-seal+jws` |
| `kid` | The RFC 7638 thumbprint of the signing key |

Payload `audit.v1.Seal`, proto JSON, proto field names:

| Field | Meaning |
|---|---|
| `tenant`, `profile` | What the seal covers |
| `hour` | Start of the hour covered, RFC 3339, UTC |
| `count` | Records in scope; zero for an empty hour |
| `root` | Root of a binary Merkle tree (RFC 6962 §2.1), hex. Empty hour: SHA-256 of the empty string |
| `first`, `last` | First and last object key of the hour; empty for an empty hour |
| `prev` | Hex SHA-256 of the previous seal's bytes for the profile and tenant; empty for the first |
| `sealed_at` | When made, RFC 3339, whole seconds, never before the hour ends |
| `meters` | Optional counters for the hour (`objects`, `records`, `bytes`); absent for an empty hour |

| Rule | Detail |
|---|---|
| Encoding | A 64-bit integer (`count`, each meter) is a decimal string. Every field is written, an empty `prev` as `""`. Members are in canonical order (RFC 8785) |
| Verifier | Checks the signature over the payload bytes as they are; never re-encodes |
| Put | Conditional (`If-None-Match: *`). Locked as long as the records it covers: the latest of the hour's objects, or for an empty hour the seal before it |
| Notary | Reads the hour's objects before signing. Refuses an hour whose objects do not match their metadata (sha256, count, every record hash) and seals nothing past it. The chain stops at a fault and the newest seal's age shows it |
| Timing | Written once the hour has settled ([0062](../../decisions/0062-observe-follows-the-bucket.md)). An empty hour still gets a seal, so a missing seal is a fault |

### Merkle tree construction

Leaves are the hour's records in key order, then line order. A leaf input is the record's 32 raw `hash` bytes.

| Item | Definition |
|---|---|
| Leaf | SHA-256(0x00 \|\| leaf input) |
| Interior node | SHA-256(0x01 \|\| left \|\| right), over the children's 32-byte hashes |
| Shape | RFC 6962 §2.1: split at the largest power of two smaller than n. Unbalanced when n is not a power of two; no leaf is duplicated |
| Empty hour | The root is SHA-256("") |

Leaf inputs are `SHA-256("<i>")` for ASCII decimal `i` from 0:

| n | root |
|---|---|
| 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| 1 | `13a77175e35eb1d9da91ee14df0d7772cea71289800206e2b45c882ecb06efbf` |
| 2 | `bbb441530bdded54e6e2bfcdc829819ff39b30768eb9f023071dffc16b410f10` |
| 3 | `8be871f13785b4c81a1700459c76ac2b3ae2caebb7876c376e223c6adff98c47` |
| 5 | `4e23fb40d8876f1299cca9b3c28432a01b11d1a20126606914612892ff2e09a7` |

Inputs and audit paths: [`internal/merkle/testdata/vectors.json`](../../../audit/internal/merkle/testdata/vectors.json).

### Worked example

Synthetic seals in [`internal/seal/testdata`](../../../audit/internal/seal/testdata): hour `10` has three records (the n = 3 vector); hour `11` is quiet. `keys/roots.jwks`:

```json
{"keys": [{"kty": "EC", "crv": "P-384", "alg": "ES384", "use": "sig",
  "kid": "o2yB9qPIG1yB95LuYok20fSC80yOxCu7VF8rmi5LyGg",
  "x": "CGbXeVcyaJ3FX1fp6aFwTmOd2o4Az9onclXvRWez9zb1zHXkRIatexKJnvf6gVXn",
  "y": "25YgRZW8Wuk4VGnjnSBJpeLYg_5_0_1Ur3tSC3fBjK8h0Fc8wSDqtztpKL_rrWfC"}]}
```

`seals/security/acme/2026/09/17/10.jws`, header and payload decoded:

```json
{"alg": "ES384", "kid": "o2yB9qPIG1yB95LuYok20fSC80yOxCu7VF8rmi5LyGg", "typ": "audit-seal+jws"}
{"count":"3","first":"records/security/acme/2026/09/17/10/01M2QDGD6000000000033XR4JR",
 "hour":"2026-09-17T10:00:00Z","last":"records/security/acme/2026/09/17/10/01M2QEBW3000000000034XR5BY",
 "meters":{"bytes":"1536","objects":"2","records":"3"},"prev":"","profile":"security",
 "root":"8be871f13785b4c81a1700459c76ac2b3ae2caebb7876c376e223c6adff98c47",
 "sealed_at":"2026-09-17T11:17:04Z","tenant":"acme"}
```

`.../11.jws`, whose `prev` is the SHA-256 of the first seal's compact serialisation:

```json
{"count":"0","first":"","hour":"2026-09-17T11:00:00Z","last":"","meters":{},
 "prev":"dc2b3969dd9edd1197a4bf6a8778189508a9104500a2709e14b20bb4c87f9326",
 "profile":"security","root":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
 "sealed_at":"2026-09-17T12:17:03Z","tenant":"acme"}
```

### Inclusion proof

A record's 0-based leaf index and its RFC 6962 audit path (at most ⌈log₂(count)⌉ hashes) recompute the root.

### Trust

| Rule | Detail |
|---|---|
| `keys/roots.jwks` | A JWK Set of P-384 public keys: distribution, not trust |
| Trusted keys | Only roots whose thumbprints the verifier's configuration pins. The verifier computes thumbprints from the key, never from `kid` |
| Valid seal | The signature verifies under the key named by `kid`, and that key is a pinned root or a delegation chains it to one |

### Delegation

A root signs a delegation (`typ: audit-delegation+jws`), payload `audit.v1.Delegation`:

| Field | Meaning |
|---|---|
| `iss` | Thumbprint of the root |
| `sub` | Thumbprint of the delegated key |
| `jwk` | The delegated public key |
| `nbf`, `exp` | Signing window, seconds since the epoch. `exp - nbf` is at most 25 hours |
| `scope` | `profiles` and `tenants`, each a list of names or `*` |

| Rule | Detail |
|---|---|
| Valid delegate seal | `sealed_at` is in the window; profile and tenant are in scope |
| Witness | The bucket's write time, within five minutes of skew. A seal written after the window closed is invalid |
| Over 25 hours | The delegation is invalid |
| Status | Verifying is built; signing is not |

### Revocation

A root signs a revocation (`typ: audit-revocation+jws`), payload `audit.v1.Revocation`:

| Field | Meaning |
|---|---|
| `iss` | The root |
| `revokes` | Thumbprint of the revoked key |
| `revoked_at` | RFC 3339 |
| `reason` | Optional |

| Rule | Detail |
|---|---|
| Invalid seal | Signed by the revoked key, with `sealed_at` or the bucket's write time (same skew) at or after `revoked_at` |
| Root | Revoked only by removing the pin |
| Unpinned signer | A delegation or revocation no pinned root signed is ignored |

## Reading the archive

| Rule | Detail |
|---|---|
| Follow | List `records/<profile>/<tenant>/` after the last handled key, in order, never past a key younger than the settle window |
| Tenants | List `records/<profile>/` with a delimiter |
| Source of truth | The listing. A notification is a hint |
| Outside the contract | The dead-letter prefix, verification marks and index tables. They may change without a new version, and no part reads another's |

## Conformance

`internal/bucketcontract` is a black-box suite for any S3 API.

| Half | Holds a bucket to | Applied by |
|---|---|---|
| Records and catalogue | Key grammar, envelope, metadata, sha256, every record hash, key uniqueness and order, the catalogue a record names | `audit verify` |
| Seals (`CheckSeals`) | Seal key grammar, JWS, payload, signature against pinned roots, delegations and revocations, chain, recomputed count and root, lock, missing seals | `audit verify --root` |

It runs on the in-memory store and LocalStack S3, with a key-file notary and a KMS `ECC_NIST_P384` notary. It rejects:

| Broken case |
|---|
| A key nobody pinned |
| A broken chain |
| A root that is not the hour's |
| An object added to a sealed hour |
| A seal made before its hour ended |
| A stray key |
| A delegation outside its window or scope, or over 25 hours |
| A delegate whose key was revoked |
| A second catalogue put with other bytes |
