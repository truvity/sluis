# Replay dead-lettered records

Read what the writer set aside under `dlq/` in the archive, fix the cause, and send the records through again.

## Before you start

- You need read access to the archive bucket and a writer to send to (`--sink`).

- `audit.writer.dead_lettered` names the reason: unknown catalogue version, schema violation or oversized.

- Replay one fixed cause at a time. A record that fails its schema is refused again, and should be.

- An unknown catalogue version is dead-lettered and acknowledged, so no queue alarm sees it. Look for `event=unknown_catalogue`, and give the writer the version first. See [change what a source records](../connect/change-what-a-source-records.md).

- This is the archive's `dlq/`, not the SQS dead-letter queue. For that, see [redrive the ingest DLQ](redrive-the-ingest-dlq.md).

## Steps

1. Read what is waiting. This sends nothing and groups the reasons.

   ```
   audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17
   ```

2. Fix the cause: register the catalogue version, configure the missing profile, or correct the emitter and deploy it.

3. Replay the one cause.

   ```
   audit replay --dlq --bucket <b> --from 2026-09-17 --to 2026-09-17 \
     --reason "no catalogue" --sink https://audit-writer:8080
   ```

   The command reports what came back under `dlq/` and exits non-zero if anything did.

## Verify

Run `audit verify` on the replayed range. Records keep their identifiers, so replaying one that got through is deduplicated.

## Roll back

None. A replay is idempotent. Report any record left under `dlq/` to the emitter's owner with its reason: the writer will keep refusing it.
