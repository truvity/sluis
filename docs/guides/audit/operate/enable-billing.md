# Enable usage billing

Keep a metering copy of each billable record, so rollups and a monthly statement come from the same records that make the trail evidence. How it works: [billing](../../../concepts/audit/billing.md).

## Before you start

- Compose a metering framework profile into a profile of the installation. `billing-nl` ships: see [which profiles to compose](../../../concepts/audit/which-profiles-to-compose.md). The chart refuses `extensions.billing.enabled` when none does.

- Run the notary. The statement names the seal of the archive it was computed from.

- `extensions.billing` takes `enabled` only and renders nothing yet. Any other key fails the values schema.

- The metering copy keeps seven years under `billing-nl`. Under compliance Object Lock you cannot shorten that later.

- A billable action must be `block`: if the record cannot be kept, the operation does not happen.

- A renamed meter is a new meter. The old name keeps its history and rollups do not migrate.

## Steps

1. Compose a metering profile and enable the extension.

   ```yaml
   audit:
     profiles:
       security:
         frameworks: [security]
       billing:
         frameworks: [billing-nl]
     extensions:
       billing:
         enabled: true
   ```

2. Declare the meter in the application's catalogue and deploy the writer before the emitter. See [change what a source records](../connect/change-what-a-source-records.md).

   ```yaml
   actions:
     app.credential.verified:
       summary: A credential was verified for a tenant.
       operation: execute
       categories: [data_access]
       category: billing                     # the destination that takes `billing` keeps the copy
       delivery: block
       meter:
         name: verifications
         outcomes: [success]
   ```

## Verify

`helm template` renders, and refuses without the `billing` profile. A record of the action lands under `records/billing/<tenant>/` as well as `records/security/`.

## Roll back

Set `enabled: false`. Copies already written stay.

## Watch

`audit-observe` writes rollups at index time, so a lagging indexer is a count that is not counting. See `audit.observe.index.deferred`, `audit.observe.index.lag` and [repair or rebuild the index](rebuild-the-index.md). Write a statement after the period ends and its last hour is sealed. An earlier run names a seal that does not cover the period.
