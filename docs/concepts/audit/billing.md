# Billing and metering

Usage billing is a projection of records the installation already keeps. A billable action is an audit record that carries a quantity. Nothing here is in the request path.

## Where a billable record goes

The catalogue declares a billable action with `meter: {name, quantity_path, outcomes: [success]}` and `category: billing`. The per-action `profiles` list is deprecated.

| # | slot | what happens | state |
|---|---|---|---|
| 1 | catalogue | the application declares the meter | built |
| 2 | emit | the record carries `meter{name, quantity, unit}` | built |
| 3 | writer | splits a `billing` copy and dedupes by record id | built |
| 4 | rollups and statement | a rollup row per tenant, meter and hour at index time; a monthly CronJob writes the statement | rollups partly built, statement not built |
| 5 | export | the application pushes the totals or invoices from the statement | the application's |

The indexer reads the meter fields from the object. It does not add anything to the request path.

## What a billing record holds

A billable record carries `meter` with `name`, `quantity` (decimal string), `unit`, `kind` (`count` or `gauge`) and flat `dimensions`.

The billing copy keeps `id`, `occurred_at`, `recorded_at`, `source`, `action`, `tenant_id`, `meter` and `origin_hash`. It omits actor and subject, so the two copies of one record cannot be joined on a person.

Where a deployment runs pseudonymisation keys, the two copies also carry different pseudonyms for one person.

## Which records count

The catalogue lists the outcomes a metered action counts. The default is success only, so a refused call is not billable. An action that bills attempts lists those outcomes. The rollup reads the list from the catalogue and never guesses.

A count is an event. A gauge, such as stored bytes, is an absolute sample emitted hourly and billed as the average over the period. Never emit deltas.

A billable action is `block`. If the record cannot be kept, the operation does not happen.

## Projection and period close

| table | columns |
|---|---|
| `usage_dedup` | `id`, `source`, `seen_at`, kept for the framework profile's dedupe window |
| `usage_hourly` | `tenant_id`, `meter`, `hour`, `quantity`, `event_count`, `last_event_id`, upserted idempotently |
| `usage_statement` | `tenant_id`, `meter`, `period`, `quantity`, `event_id_range`, `seal_key`, `computed_at`, written once |

The period closes `close_after_hours` after its end (default 72). Later events are flagged, not billed. Corrections are credit notes, never rewrites.

The statement is immutable. It names the period, the rollups it summed and the seal of the archive it was computed from. It is written under the metering profile's lock.

Reprocessing replays the period's billing copies into a fresh rollup and diffs it against the statement. The diff is kept as the reconciliation.

## Rating

A rating engine, such as a payment provider's meters or an in-house one, receives rollups in pre-aggregated mode. Use an idempotency key per tenant, meter and hour. The rating engine is never the evidence store.

## What to watch

| signal | meaning |
|---|---|
| `audit.observe.index.deferred` counter and `audit.observe.index.lag` histogram | the indexer is behind, so the rollups undercount |
| a statement run before the last hour is sealed | the statement names a seal that does not cover the period |
| a renamed meter | a new meter: the old name keeps its history and rollups do not migrate |

The metering copy keeps seven years under `billing-nl`. See [which framework profiles to compose](which-profiles-to-compose.md). To switch billing on, see [enable billing](../../guides/audit/operate/enable-billing.md). Quotas count the same meter: [usage quotas](usage-quotas.md).

## Decided in

- [0044 Profiles composed from framework profiles](../../decisions/0044-profiles-composed-from-framework-profiles.md)
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md)
- [0061 Seals](../../decisions/0061-seals.md)
- [0062 Observe follows the bucket](../../decisions/0062-observe-follows-the-bucket.md)
