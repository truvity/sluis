# Configuration: observe and query

The keys of the indexer (`audit-observe`), the query service (`audit-query`) and its grants file. Shared blocks are in [configuration](configuration.md#shared-blocks).

## Indexer

`audit-observe` follows the archive and writes the index ([0062](../../decisions/0062-observe-follows-the-bucket.md), [0066](../../decisions/0066-indexer-and-query-are-separate-processes.md)). It reads the archive, never writes it, and serves only `/healthz` and `/readyz`.

| Step | Behaviour |
|---|---|
| Pass | Lists `records/<profile>/<tenant>/` from a cursor kept in Postgres. Indexes objects older than `settle`. Moves the cursor in the transaction that writes their rows |
| Wake-up | Set one of `wake.nats` and `wake.sqs`, or neither. A lost, repeated or reordered notification costs latency only |
| Latency without wake | Between `settle` and `interval`, at worst `interval` (five minutes with defaults) |
| Latency with wake | About `settle`, whatever `interval` is. Lower `settle` only as far as a put reliably finishes |
| Undecodable object | Skipped and counted (`reason=unreadable`) |
| Fetch or catalogue failure | Stops that tenant's cursor, retried by the next pass (`reason=retry`). Other tenants continue |
| Reset | `audit reindex --reset-cursor` rereads a profile from the start and changes nothing already indexed |
| Release | Run the same audit release as the writer or newer ([upgrade to v1.74](../../guides/audit/upgrade/v1.74.md)) |

<!-- generated: config-audit-observe -->
| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address `/healthz` and `/readyz` are served on. `/healthz` says the process is up (the liveness probe); `/readyz` says it can work now (the readiness probe): the index database answers and the archive's catalogues can be listed. A failing check is named in the 503, never its error |
| `archive` | `archive`, required | | the archive to follow (`bucket`, `prefix`). It has no `lockMode`: this process reads. The catalogues and their extension schemas are read from it too |
| `database` | `database`, required | | the index, as the indexer's **own** role (`audit migrate --observe`): read and write on the index and its cursors, nothing of the deduplication table, not the owner, and not the writer's or the query service's |
| `settle` | duration | `2m` | how far behind now the cursor stays. An object's key is fixed when its put starts and it is visible when it ends, so it must be longer than a put can take and than the clocks of the writers and of this process can disagree. It is the least time between a record's acknowledgement and its appearance in search |
| `interval` | duration | `5m` | the poll: how often a pass runs when nothing woke it. Without a `wake` an object is searchable after the longer of `settle` and the time to the next pass, so at worst after `interval`; with one the woken pass reschedules itself for when the object is old enough, the delay is about `settle` whatever `interval` is, and `interval` only bounds a lost notification |
| `readiness.failedPasses` | integer, at least 1 | `3` | `/readyz` fails once this many passes in a row have failed |
| `readiness.staleIntervals` | integer, at least 1 | `3` | `/readyz` fails once this many `interval`s have passed without a successful pass |
| `batch` | integer, at least 1 | `500` | rows written in one transaction; a transaction ends at an object's end |
| `profiles` | list of strings, at least one, unique | every profile the archive has | the profiles to follow. Profiles and tenants are discovered by listing |
| `wake.nats.nats`, `wake.nats.subject` | `nats` (url, `tokenFile`), string | | a subject carrying the bucket's notifications. Their content is never read |
| `wake.sqs` | `sqs` | | a queue of the bucket's notifications that is the indexer's own: each message wakes a pass and is deleted. Credentials are the SDK's ambient ones |
<!-- /generated -->

A failed pass is retried at the next poll. `/readyz` fails after `readiness.failedPasses` failed passes in a row, or `readiness.staleIntervals` intervals without a success. `/healthz` is unaffected, so a failing indexer does not crash-loop.

| Signal | Use |
|---|---|
| `audit_observe_passes_total`, `audit_observe_pass_since_success_seconds` | Back the chart alerts `AuditIndexStalled` and `AuditIndexPassesFailing` |
| Log line | Once per distinct error with the object and reason, then every half hour |

## Query service

`audit-query` serves search, facets, get, export, tail and resolve behind the grants. The writer records every read it serves.

<!-- generated: config-audit-query -->
| key | type | default | meaning |
|---|---|---|---|
| `listen` | `listen` | `:8080` | the address it is served on |
| `grants` | path, required | | the grants file, below |
| `sink` | `sink`, required | | the writer every read is recorded through: its `url`, or the ingest `sqs` queue of a writer that runs elsewhere ([AWS](../../guides/audit/operate/aws-run-readers-in-kubernetes.md)) |
| `require` | `logged`, `queued` or `archived` | none: checks nothing | the weakest durability the writer's acknowledgements may carry. Needs `sink.expect` at least as strong ([durability](configuration-writer.md#durability-require-forward-consume)) |
| `deployment` | path | none | the profile configuration. A grants preset needs it, because it turns roles into the deployment's own profiles |
| `searcher` | `postgres` or `s3scan` | `postgres` | `postgres` is the index; `s3scan` is the archive, within a budget, for a deployment with no database. The scan orders by `occurred_at` only and refuses `recorded_at`, so a deployment on it can search the trail but cannot follow it: there is no live tail ([search](../../concepts/audit/search.md#tail)) |
| `database` | `database` | | the index, as the query service's **own** role (`audit migrate --reader`): `usage` on the schema, `select` on the index's tables, not the owner. Tenant row-level security binds only a non-owner. Required unless `searcher` is `s3scan` |
| `archive.bucket`, `archive.prefix` | `bucket`, string | | what `s3scan` reads, and where Get finds a record's object. Required with `s3scan`. Without it Get still answers, with where the copy is and nothing about whether it has been verified (nothing yet sets that: it is for seals). The service's region is `archive.bucket.region` |
| `exports.bucket` | `bucket`, required with `exports` | | a separate bucket with no Object Lock, which clears it. Without `exports` the export operation is refused. It inherits nothing from the archive: name its endpoint, path style and `credentialsSecret` here |
| `exports.expiry` | duration | `168h` | how long an export is kept before the bucket clears it |
| `exports.linkValid` | duration | `1h` | how long a download link works |
| `keys` | `keys` | none | the writer's key provider, which turns resolve on. With provider `none`, or no `keys`, there is nothing to resolve and the RPC is `unimplemented`. `local` reads the writer's key directory, which must be shared; `transit` signs in as the query service's own identity, never the writer's |
<!-- /generated -->

The service's limits are fixed, not configured: `filter` 4 terms, `sort` 4, `in` 100 values, `limit` 1000. See the [API reference](api.md).

## Grants file

`grants` names the file (in the chart `/etc/audit/grants.yaml`, rendered from `query.grants`). It says who may authenticate and what each caller sees.

```yaml
issuers:
  - url: https://id.example.com        # exactly as the tokens' iss claim says it
    audience: audit                    # required
  - url: https://customers.example.com
    audience: audit
rules:                                 # first match wins
  - name: auditors
    issuer: https://id.example.com     # required once more than one issuer is trusted
    claim: groups
    value: all:audit:auditor
    grant:
      all_tenants: true
      profiles: [security, operational]
      operations: [search, facets, get, export]
  - name: assessor-2026-q3             # an external assessor sees one period, not the archive
    issuer: https://id.example.com
    claim: groups
    value: all:audit:assessor
    grant:
      all_tenants: true
      profiles: [security]
      operations: [search, get]
      from: 2026-07-01T00:00:00Z         # by when records happened; end exclusive
      until: 2026-10-01T00:00:00Z
  - name: acme-viewers
    issuer: https://customers.example.com
    claim: groups
    value: acme:audit:viewer
    grant:
      tenants: [acme]
      profiles: [security]
      operations: [search, get]
```

A grants preset replaces a rule per group. It needs `deployment` in the query configuration, because it turns roles into the deployment's profiles.

```yaml
presets:
  - name: access-roster
    issuer: https://id.example.com       # required once more than one issuer is trusted
    claim: groups                        # the default
```

The `access-roster` grants preset reads groups named `<scope>:audit:<role>`, the grant grammar from sluis. The name is a legacy identifier, renamed in v1.75–v1.76. The scope is `all` or an audit tenant id, byte for byte.

| Role | Profiles built from | Operations | `all` allowed |
|---|---|---|---|
| `viewer` | `history` | search, facets, get | no: `all:audit:viewer` grants nothing |
| `security` | `security`, `dora`, `pci-dss`, `nen-7513` | search, facets, get, tail, export | yes |
| `auditor` | every profile built from no `billing-*` framework profile | search, facets, get, export | yes |
| `billing` | `billing-*` | search, facets, get, export | yes |
| `evidence` | `evidence-etsi` | search, get, export | yes |

`resolve` comes from no group name: write an explicit rule naming the person.

| Rule | Behaviour |
|---|---|
| Union | Every matching rule and audit group counts, per request. On the profile asked for, the tenants of the covering grants are unioned. The read's record names all of them (`acme:audit:viewer,all:audit:security`) |
| Profiles | A union never crosses profiles |
| Windows | An unbounded grant on a profile makes the answer unbounded. Two different windows on one profile are refused |

The service refuses to start in these cases:

| Case | Reason |
|---|---|
| No issuer | Nobody could sign in |
| An issuer without an audience | A token minted for another service could be replayed |
| Several issuers and a rule naming none | A rule would match a group from the least-trusted issuer |
| A rule names an unlisted issuer or an unknown operation | Invalid reference |
| A preset without `deployment`, or a nonexistent one | The preset cannot resolve profiles |

It accepts a bearer token in `Authorization` and the access token the fleet gateway forwards. [gateway-auth](https://github.com/truvity/gateway-auth) verifies it with one verifier per issuer. It checks signature, issuer, audience and expiry, refreshes keys in the background, and allows no clock skew.
