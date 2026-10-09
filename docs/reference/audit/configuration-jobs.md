# Configuration: the jobs

The notary and the four scheduled commands of `audit`, each reading one `--config` file. Shared blocks are in [configuration](configuration.md#shared-blocks).

## Scheduled jobs

Each job runs `audit <command> --config <file>` to completion. The notary is its own binary: `audit-notary --config <file>`. A job records what it did through the writer's sink, so it needs `sink` and, where the writer verifies callers, a `tokenFile`. Each job has its own identity.

### audit-notary

Seals the archive ([0061](../../decisions/0061-seals.md)). Hourly in the chart.

| Behaviour | Detail |
|---|---|
| Seal | One signed seal per profile, tenant and ended, settled hour, chained to the previous one |
| Path | `seals/<profile>/<tenant>/<yyyy>/<mm>/<dd>/<hh>.jws` ([bucket contract](bucket-contract.md#seals)) |
| Signer | A managed key the writer's identity cannot use ([key custody](../../concepts/audit/key-custody.md#signing-key)); the chart enforces a separate identity |
| Rerun | Writes nothing new |
| Mismatch | An hour whose objects do not match their metadata is not sealed, nor is any later hour |
| First seal | The hour of the tenant's first object; empty hours are sealed from then on |

<!-- generated: config-audit-notary -->
| key | type | default | meaning |
|---|---|---|---|
| `archive` | `archive`, required | | the archive to read and to put seals in (`bucket`, `prefix`, `lockMode`, `kmsKey`); `lockMode` defaults to `compliance`, and a seal is locked as long as the records it covers |
| `signer` | object, required | | exactly one of `kms`, `transit` and `file` |
| `signer.kms` | `{key, region}` | | an AWS KMS key, `ECC_NIST_P384` `SIGN_VERIFY`, by ARN, ID or alias; the credentials are the SDK's ambient ones, the notary's role |
| `signer.transit` | `{key, openbao}` | | an OpenBAO transit key of type `ecdsa-p384`; `openbao` is the [shared block](configuration.md#shared-blocks) |
| `signer.file` | `{path}` | | a P-384 private key in PEM (PKCS#8 or SEC 1), for development |
| `profiles` | list of strings, at least one, unique | every profile the archive has records for | the profiles to seal |
| `settle` | duration | `10m` | how long after an hour has ended it is sealed, so that a batch put late in the hour it is keyed by is in the seal |
| `sink` | `sink` | none | the writer the job records each seal through (`audit.seal.written`) |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` |
<!-- /generated -->

It reports `audit.seal.age` (newest sealed hour per profile, of the tenant furthest behind), `audit.seal.written` and `audit.seal.failures` over OTLP ([telemetry](telemetry.md)).

### audit verify

Checks the record objects of a range of ingest time against the [bucket contract](bucket-contract.md) ([verification](../../guides/audit/operate/verify-the-trail.md)). It reads the archive only, so `archive` has no `lockMode` and no key is needed. Nightly in the chart, over the last 24 hours, for each profile it names or the deployment composes.

<!-- generated: config-audit-verify -->
| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration; each object's lock is held to what its profile demands |
| `archive` | `archive`, required | | the archive to read (`bucket`, `prefix`) |
| `profiles` | list of strings, at least one, unique | every profile the deployment composes | the profiles whose objects to check |
| `last` | duration | `24h` | check the objects ingested in the last this long, ending at the hour that has closed |
| `seals` | object | none | also check the seals of the range: `roots` (list of thumbprints, at least one, required) are the only keys trusted; `settle` (default `10m`, keep it equal to the notary's) and `grace` (default `1h`) say when a seal is due. Without it no seal is checked |
| `sink` | `sink` | none | the writer the job records what it checked through (`audit.seal.verified` or `audit.seal.failed` per ingest hour, recorded only with `seals`) |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](configuration-writer.md#durability-require-forward-consume)) |
<!-- /generated -->

The v0 keys `publicKeyFile`, `lookback` and `record` are gone.

### audit purge

Brings the index and the deduplication table within the profiles. Daily in the chart. It never touches the archive.

<!-- generated: config-audit-purge -->
| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration |
| `database` | `database`, required | | the index and the deduplication table, as the purge job's **own** role (`audit migrate --purge`): delete from both, and add nothing to either |
| `identifyingAfter` | duration | unset: nothing is forgotten early | how long the index keeps who an event happened to. No shipped framework profile states one, so it is the deployment's own policy |
| `dedupeWindow` | duration | the widest window the profiles ask for | how long a written identifier is remembered |
<!-- /generated -->

### audit clock-sync

Compares the clock with UTC and records the answer. Daily in the chart.

<!-- generated: config-audit-clock-sync -->
| key | type | default | meaning |
|---|---|---|---|
| `ntp` | list of strings, at least one, required | | time references, host or host:port; the quickest to answer is believed, and one being unreachable is survivable |
| `sink` | `sink` | none | the writer the reading is recorded through |
| `require` | `logged`, `queued` or `archived` | none | the weakest durability the writer's acknowledgements may carry; needs `sink` and `sink.expect` ([durability](configuration-writer.md#durability-require-forward-consume)) |
| `maxOffset` | duration | `1s` | the offset beyond which the run fails. `0s` records any offset and never fails |
| `timeout` | duration | `5s` | how long to wait for a reference |
<!-- /generated -->

### audit migrate

Applies the schema, grants each part's database role what it needs, and takes back the rest. In the chart it is a pre-install and pre-upgrade hook Job; otherwise run it by hand from one place.

<!-- generated: config-audit-migrate -->
| key | type | default | meaning |
|---|---|---|---|
| `database` | `database`, required | | the database, as the **owner** of the tables. No part connects as the owner: an owner is bound by no grant and no row-level security, so the migration refuses to grant a role that is the owner |
| `writer` | string | none | the write path's role: the deduplication table, the catalogue registry and the key directory, and none of the index |
| `observe` | string | none | the indexer's role: read and write on the index and its cursors, and `execute` on `audit_ensure_month(date)`, the function that creates a month's partition as the owner |
| `reader` | string | none | the query service's role: `select` on the index's tables, bound by row-level security to the tenants of each request, and nothing else |
| `purge` | string | none | the purge job's role: delete from the index and the deduplication table |
<!-- /generated -->

Each role must already exist, and no role may serve two parts. A part left unnamed gets no grant. A part that connects as the owner has no separation.
