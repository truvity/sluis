# Billing and metering

Usage billing is a projection of records the installation already keeps. A billable action is an audit record that carries a quantity. The metering copy is built. The rollups and the statement are designed, not built.

## Where a billable record goes

The catalogue declares a billable action with `meter: {name, quantity_path, outcomes: [success]}` and `category: billing`. The per-action `profiles` list is deprecated.

| # | slot | what happens | state |
|---|---|---|---|
| 1 | catalogue | the application declares the meter | built |
| 2 | emit | the record carries `meter{name, quantity, unit}` | built |
| 3 | writer | splits a `billing` copy and dedupes by record id | built |
| 4 | rollups and statement | a rollup row per tenant, meter and hour at index time; a monthly CronJob writes the statement | designed, not built |
| 5 | export | the application pushes the totals or invoices from the statement | the application's |

## What a billing record holds

A billable record carries `meter` with `name`, `quantity` (decimal string), `unit`, `kind` (`count` or `gauge`) and flat `dimensions`.

The billing copy keeps `id`, `occurred_at`, `recorded_at`, `source`, `action`, `outcome`, `tenant_id`, `meter` and `origin_hash`. It omits actor and subject, so the two copies of one record cannot be joined on a person.

Where a deployment runs pseudonymisation keys, the two copies also carry different pseudonyms for one person.

## Which records count

The catalogue lists the outcomes a metered action counts. The default is success only, so a refused call is not billable. An action that bills attempts lists those outcomes. The rollup reads the list from the catalogue and never guesses.

A count is an event. A gauge, such as stored bytes, is an absolute sample emitted hourly and billed as the average over the period. Never emit deltas.

A billable action is `block`. If the record cannot be kept, the operation does not happen.

## Projection and period close

This section is the design. No code creates the tables.

| table | columns |
|---|---|
| `usage_dedup` | `id`, `source`, `seen_at`, kept for the framework profile's dedupe window |
| `usage_hourly` | `tenant_id`, `meter`, `hour`, `quantity`, `event_count`, `last_event_id`, upserted idempotently |
| `usage_statement` | `tenant_id`, `meter`, `period`, `quantity`, `event_id_range`, `seal_key`, `computed_at`, written once |

The period closes `close_after_hours` after its end (default 72). Later events are flagged, not billed. Corrections are credit notes, never rewrites.

The statement is immutable. It names the period, the rollups it summed and the seal of the archive it was computed from. It is written under the metering profile's lock.

## What to watch

The signals apply once rollups exist.

| signal | meaning |
|---|---|
| `audit.observe.index.deferred` counter and `audit.observe.index.lag` histogram | the indexer is behind, so the rollups undercount |
| a statement run before the last hour is sealed | the statement names a seal that does not cover the period |
| a renamed meter | a new meter: the old name keeps its history and rollups do not migrate |

The metering copy keeps seven years under `billing-nl`. See [which framework profiles to compose](which-profiles-to-compose.md). To switch billing on, see [enable billing](../../guides/audit/operate/enable-billing.md). Quotas count the same meter: [usage quotas](usage-quotas.md).

## Decided in

- [0044 Profiles composed from framework profiles](../../decisions/0044-profiles-composed-from-framework-profiles.md)
- [0045 S3 Object Lock as the record](../../decisions/0045-s3-object-lock-as-the-record.md)
- [0047 Identity tiers and pseudonymisation](../../decisions/0047-identity-tiers-and-pseudonymisation.md)
- [0061 Seals](../../decisions/0061-seals.md)
