# What does the writer do with a record?

The writer is the single trusted consumer of the wide record. It is a service, never a library in the application. The archive, index and stream credentials live in its pods only.

In [stream mode](stream-mode.md) it is `audit-writer` in consumer mode: N pods sharing one durable pull consumer. In [direct mode](direct-mode.md) the receiver is the writer, and records arrive from the request. The steps are the same in both.

The deployment creates the stream, and the writer refuses to start without it. Set the acknowledgement wait above the longest write, or a second replica receives records the first is still writing.

## Per record

1. **Ask** the deduplication table whether the `id` was written within the window (`pipeline.dedupe_window_days`). Asking marks nothing.

2. **Resolve** the catalogue by `source` and `catalogue_version`. An unknown version is dead-lettered with an alert, never dropped.

3. **Validate** against the composed schema. A violation is dead-lettered.

4. **Stamp** `recorded_at`, `observer` from the publisher's verified identity, and `origin_hash` as the SHA-256 of the canonical wide record.

5. **Split** into one copy per profile the action belongs to, with the profile's allowed fields and classes.

6. **Treat identities** per profile: clear, pseudonym (HMAC under the tenant-and-purpose key), scoped or omit. Apply `x-audit-sensitive`. With `keys.provider: none`, the default, there is no pseudonym treatment. Declare `external_identifiers_are_opaque` instead.

7. **Buffer** per profile and tenant. Roll on an interval of one to five minutes or on size before compression. A roll is one ingest batch keyed by the hour it was taken. Retention is fixed when an object opens, so every copy keeps at least the oldest's period.

8. **PUT** each object conditionally (`If-None-Match: *`) with `ObjectLockMode=COMPLIANCE`, `RetainUntilDate` from the profile, SSE-KMS, a checksum, `Content-Encoding: zstd` and the metadata `format`, `sha256` and `count`. See the [bucket contract](../../reference/audit/bucket-contract.md).

9. **Copy schemas** on first use of a catalogue version. The catalogue goes to `catalogue/<app>/<version>`, written once: the same bytes again succeed, other bytes stop the writer. Extension schemas go to the schema prefix, locked for the longest profile involved. A new record major copies the record's JSON Schema and proto there too.

10. **Index** the object's rows. A failure does not fail the write: `audit reindex` rebuilds the index, and the deployment is alerted.

11. **Mark** the identifiers as written, now that the copies are durable.

12. **Acknowledge** after the PUT: the stream message in stream mode, the caller's batch in direct mode. An acknowledgement means the records are in the archive.

## Asking and marking are two calls

The writer marks after the PUT, not before. With a shared table, a writer that claims an identifier and dies before its PUT leaves the record nowhere. The redelivery that would save it looks like a duplicate.

A crash between PUT and mark writes the redelivery again. The archive holds a second object, the index keeps one row per identifier, and a reader sees the record once. A duplicate costs an object. A loss cannot be repaired.

The writer keeps its own account within a batch. It absorbs a redelivery that sits beside its original there.

## Idempotency

An object key ends in a ULID made when the put starts. The put is conditional, so a key is never written twice. Index inserts key on `(profile, id)`. A crash between PUT and index leaves an object without rows, and the nightly reindex of the ingest day repairs it.

## Failure

| failure | stream mode | direct mode |
|---|---|---|
| object storage unavailable | the buffer holds to the stream horizon, then the writer stops consuming and alerts; discard-new surfaces publish failures to emitters | nothing is acknowledged; a `block` call fails and an `async` record waits in the application's queue |
| Postgres unavailable | the PUT proceeds, the index defers to reindex, the ack waits for a configurable grace, then dead-letters | the same |
| writer restart | unacknowledged messages redeliver and dedupe absorbs them | the emitter's queue retries |

## Meta-events

The writer records `audit.writer.started`, `audit.writer.stopped`, `audit.writer.dead_lettered`, `audit.catalogue.registered` and `audit.retention.extended`. It holds an emitter bound to the common catalogue whose sink is the writer itself, over the in-process transport.

The emitter uses `async` delivery, because a `block` write inside the writer's own batch would wait on itself. A dead letter caused by a meta-record is logged and dead-lettered, never emitted about.

## Replay

A dead letter carries the reason and the full record. After you fix the cause, `audit replay --dlq --from --to` hands the range back to the writer as a batch. Deduplication makes replaying a record that got through harmless.

Without a sink, replay groups the reasons and sends nothing. Narrow with `--reason` and `--action`. Replay reports what failed again by listing the prefix before and after.

## Decided in

- [0053 One installation per service or product](../../decisions/0053-one-installation-per-service-or-product.md).

- [0054 Two deliveries and a durable ack](../../decisions/0054-two-deliveries-and-a-durable-ack.md).

- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md).

- [0062 Observe follows the bucket](../../decisions/0062-observe-follows-the-bucket.md).
