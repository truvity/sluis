# Recover from a receiver, writer or storage outage

Bring the write path back after the receiver, writer or object store was down, and learn what was lost.

## Before you start

- Know the mode, [direct](../../../concepts/audit/direct-mode.md) or [stream](../../../concepts/audit/stream-mode.md). You need the pods' logs and the application's metrics.

- `block` actions fail while the receiver is down. `async` records wait in the emitter's bounded in-memory queue with backoff, and are dropped only on overflow. Each drop is logged and counted.

- There is no outbox file. A pod's queue dies with it: one flush interval of `async` records plus the batch in flight. A `block` record is never lost.

- Nothing the receiver acknowledged is lost: it acknowledges only what it stored ([0059](../../../decisions/0059-sink-durability-and-transports.md)).

| | direct | stream |
|---|---|---|
| receiver | is the writer: one process validates and puts the object | publishes to the stream and acknowledges when replicated |
| writer | the receiver's own pods | `audit-writer` in consumer mode, N pods, scaled apart |
| "behind" looks like | `audit.emit.queue.pending` climbing in the application | the stream consumer's pending count |

## Steps

1. Tell what was lost from the application's own metrics. `audit.emit.queue.pending` rising means the receiver is slow or down. `audit.emit.records.dropped` is the incident. `audit.emit.records.refused` is an emitter bug.

   ```
   increase(audit_emit_records_dropped_total[15m]) > 0
   ```

2. Restore the part that is down.

   - Receiver: restore it. `block` requests fail meanwhile.

   - Writer in direct mode: a rollout is a pause, because the receiver is the writer. With two replicas and a rolling update it lasts one pod's readiness time. The deduplication table is in Postgres, not a pod.

   - Writer in stream mode: a rollout is a backlog. The application notices nothing and the consumer's pending count rises then falls. A count that does not fall means the writers cannot keep up or write: restore them. A writer resumes from its consumer position and the dedupe table absorbs redelivered batches.

   - Stream past its horizon: the discard-new policy refuses publishes instead of dropping. Refusals show as failed `block` calls and a filling queue.

   - Object storage: in direct mode the receiver acknowledges nothing, so `block` fails and `async` queues. In stream mode the stream holds batches up to its horizon.

## Verify

`/readyz` on the receiver and writer answers 200 and `pending` falls. Records written after a storage outage carry their original `occurred_at`, and `recorded_at` shows the delay. Run `audit verify` over the window: see [verify the trail](verify-the-trail.md).

## Roll back

If a rollout caused the outage, run `kubectl rollout undo`.

## After a drop

If `audit.emit.records.dropped` moved, the records are in the application's log (`Options.Logger`) and nowhere else. Tell the application's owner.
