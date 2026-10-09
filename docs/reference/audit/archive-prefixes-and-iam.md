# Archive prefixes and IAM

What an installation writes under its prefix, who may put and get what, and what breaks verification. To make a bucket, see [prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md). The key grammar is the [bucket contract](bucket-contract.md).

## Prefixes and retention

Everything one installation writes, beneath its `prefix`:

| prefix | written by | what | retention |
|---|---|---|---|
| `records/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>/<ULID>` | writer | one object per ingest batch, by the hour of ingest ([the contract](bucket-contract.md)) | the profile's, per object at PUT (years after expiry for an `after_expiry` profile) |
| `catalogue/<app>/<version>` | writer | the application's catalogue at that version, written once | the longest profile |
| `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws` | notary | one signed seal per profile, tenant and hour, empty hours too, chained through `prev` | that of the records it covers, per object at PUT |
| `keys/roots.jwks` | the notary, once, if absent; otherwise the operator | the root public keys, as a JWK Set: distribution, not trust | none; the bucket versions it |
| `keys/delegations/<thumbprint>/<ULID>.jws`, `keys/revocations/<ULID>.jws` | a root | statements about keys a verifier checks | none |
| `schema/…` | writer | extension schemas and the record schema the records were written under | the longest profile |
| `dlq/year=/month=/day=/…` | writer | records the writer could not take | the longest profile |
| `holds/<id>/…` | `audit hold` | legal holds placed and released | the longest profile |
| `identity/tenant=<t>/purpose=<p>/<pseudonym>` | writer | the sealed identity behind a pseudonym, for resolve | the longest profile |

| Rule | Detail |
|---|---|
| Placement | A reader finds a record by its ingest hour, not its own date |
| Names | A profile name must not contain `/`. A record whose tenant id contains `/` is dead-lettered |
| Catalogue | Written once. The same bytes again succeed; other bytes under the version stop the writer at start-up, or at the first record of a catalogue it did not start with |
| `identity/` | Exists only when a key provider is configured. `keys.provider: none` (no `keys` block) is the default ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)) |
| v0 archive | `profile=<p>/tenant=<t>/year=...`, `digest/` and `verified/` are read by nothing in v1. The previous release's CLI (v0.6.x) reads them. A bucket holding both needs both lifecycle rule sets |
| Exports | A separate bucket without Object Lock, with a lifecycle rule expiring `export/`. `exports.bucket` of the query configuration takes its own `endpoint`, `pathStyle` and `credentialsSecret`, inheriting none from the archive |
| Lifecycle | The chart creates no buckets ([0065](../../decisions/0065-archive-retention-and-lifecycle.md)). The environment's bucket carries one rule per profile on `<prefix>/records/<profile>/`: Glacier Instant Retrieval at 30 days, Deep Archive at 1 year, expiration only after lock expiry. The profile leads `records/` because a lifecycle filter matches a literal prefix |
| Per-tenant roles | Scope a role to `arn:aws:s3:::<bucket>/<prefix>/records/<profile>/<tenant>/*` |

## IAM per component

Four roles per installation, each bound to a service account (Pod Identity or IRSA); the chart has a `serviceAccount` per component. The receiver, the clock-sync job and the purge job get a service account and no S3 role.

Scope every role under the installation's prefix: the resource is `arn:aws:s3:::<bucket>/<prefix>/*`, and `s3:ListBucket` is conditioned on `s3:prefix` being `<prefix>/*`.

| role | on the archive, under its prefix | elsewhere |
|---|---|---|
| **writer** | `s3:PutObject`, `s3:PutObjectRetention`, `s3:GetObjectRetention`, `s3:PutObjectLegalHold` under `records/`, `catalogue/`, `schema/`, `dlq/`, `holds/` and `identity/` (where there are keys); `s3:GetObject` and `s3:ListBucket` on `records/`, `catalogue/`, `holds/`, `schema/`, and `identity/` where there are keys | `kms:GenerateDataKey`, `kms:Encrypt` on the bucket's key |
| **verify job** | `s3:GetObject`, `s3:ListBucket`; nothing is put | `kms:Decrypt` on the bucket's key |
| **indexer** (`audit-observe`) | `s3:GetObject`, `s3:ListBucket` on `records/`, `catalogue/` and `schema/`; nothing is put | `kms:Decrypt` on the bucket's key |
| **query service** | `s3:GetObject`, `s3:ListBucket` | `s3:PutObject`, `s3:GetObject` on the exports bucket; `kms:Decrypt` |

| Role | On the archive, under the installation's prefix |
|---|---|
| notary | Get and list; put under `seals/` and the object `keys/roots.jwks`; decrypt; use the seal key. No put under `records/` |
| purge job | None; it works on the index database |
| operator running `audit hold` | `s3:PutObjectLegalHold` (placing), `s3:GetObjectLegalHold`, `s3:ListBucket`, `s3:PutObject` on `holds/` |
| break-glass | `s3:PutObjectLegalHold` with `s3:object-lock-legal-hold` = `OFF` (releasing) |
| auditor | Read-only on `records/` and `catalogue/`; reads show in the bucket access log, and through the query service as `audit.get` and `audit.search` |

| Rule | Detail |
|---|---|
| Separation | The writer may not put under `seals/` or `keys/`. Verify job, indexer and query service write nothing into the archive |
| Deletes | No role gets `s3:DeleteObject`, `s3:DeleteObjectVersion` or `s3:BypassGovernanceRetention` |
| Legal hold | Only the writer holds `s3:PutObjectLegalHold`, and a put sends the header only when placing a hold. A refusal on a plain put means a version that sent it as OFF on every put: upgrade, do not grant |
| `schema/` | The writer reads the last composition back on every start. A policy that allows put but not get makes it write one object, take a 403 and die in a loop |

## What breaks verification

| Cause | Effect |
|---|---|
| Moving or renaming objects | The key carries profile, tenant and ingest hour |
| Changing the KMS key without keeping the old one decryptable | Objects cannot be read |
| Re-uploading under the same key | The writer's conditional put refuses it; `audit verify` reports a changed object by its `sha256` |
| Changing the prefix | Moves every key. Choose the prefix once, at creation |
