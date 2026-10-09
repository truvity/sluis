# Repair or rebuild the index

Bring the search index back in step with the archive when `audit-observe` is behind, wrong or gone, and keep the index and deduplication table bounded.

## Before you start

- Check that `audit verify` passes. It reads the archive alone, so it runs during a rebuild.

- The index has no backup. A rebuild costs search, facets and tail while it runs. A redelivery in the gap is written twice, but the index keeps one row per identifier.

- The index is always `settle` behind, two minutes by default. A lag far above that is a fault.

- A catalogue is read from `catalogue/<app>/<version>` unless you pass a file. A missing one is an error.

- Every repair is safe over a range already indexed.

## Steps

1. Tell whether the index is behind. With `OTEL_EXPORTER_OTLP_ENDPOINT` set, the indexer emits `audit.observe.index.lag` and `audit.observe.index.deferred`. `AuditIndexLagHigh` fires at a p99 above ten minutes.

   ```
   increase(audit_observe_index_deferred_total[15m]) > 0
   ```

   Read the `audit-observe` log and check `/readyz`.

   - `reason=retry`: the archive was unreadable, the database refused a write, or a catalogue is missing. The cursor stays. Fix the cause and the next pass resumes.

   - `reason=unreadable`: an object does not decode. It is skipped and the log names its key. Treat it as a finding.

2. Read a profile again from the start.

   ```sh
   audit reindex --profile <p> [--tenant <t>] --reset-cursor --database <url>
   ```

3. Or read a range of ingest days.

   ```sh
   audit reindex --profile <p> --from <day> --to <day> \
       --database <url> --bucket <b> [--catalogue <file>...]
   ```

4. If the index is wrong or gone, drop what is left, recreate the schema and roles, and start `audit-observe`. It reads every profile and tenant from the first key. Start writers once `audit migrate` has run.

   ```sh
   audit migrate --database "$OWNER_URL" --writer audit_writer \
       --observe audit_observe --reader audit_query --purge audit_purge
   ```

5. Bound the index and deduplication table. `audit purge` removes index rows past each profile's retention and identifiers past the deduplication window. It never touches the archive. `--identifying-after <duration>` also clears actor and subject columns. It has no default.

   ```sh
   audit purge --deployment <file> --database <url> [--dry-run]
   ```

## Verify

The lag histogram falls and a search returns the range. Pollers catch up on their own.

## Roll back

None. The archive is unaffected.

## Retention not extended

The counter `audit.writer.retention.not_extended` means an addendum could not lengthen an earlier record's lock. The failed `audit.retention.extended` record names the record and why: usually it is older than the scan horizon, or the role lacks `s3:PutObjectRetention`. Fix the cause, then lengthen it by hand.

```sh
aws s3api put-object-retention --bucket <b> --key <object> \
    --retention Mode=COMPLIANCE,RetainUntilDate=<retain_until from the record>
```

## Decided in

[0062 Observe follows the bucket](../../../decisions/0062-observe-follows-the-bucket.md).
