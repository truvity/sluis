# Respond to an audit alert

Find the cause of a firing write-path alert and fix it. Each rule in [telemetry](../../../reference/audit/telemetry.md#alerts) links to its section here through `alerts.runbookBaseUrl`.

## Before you start

- Install and route the alerts: see [telemetry](../../../reference/audit/telemetry.md#installing-them). You need the component logs and read access to the archive.

- `AuditEmitterDroppingRecords` means records are gone, not late.

- An hour with no seal looks like one whose seal was removed. Do not let `AuditSealStale` age.

- On AWS the alarms are CloudWatch's: see [alarms](../../../reference/audit/aws-pulumi-library.md#alarms).

## Steps

Find the alert's section.

### AuditRecordsDeadLettered

Read the writer's `dead letter` log lines for the record id and reason. Usual causes are a catalogue or schema version the writer lacks and a record that fails its catalogue. Give the writer the version first: [change what a source records](../connect/change-what-a-source-records.md). Then [replay dead-lettered records](replay-dead-lettered-records.md).

### AuditEmitterDroppingRecords

Each lost record is in the emitter's log. Check `audit_emit_queue_pending` and why the sink was away. Fix the sink before you deepen the queue.

### AuditSealStale

Read the notary CronJob's last Job log. Causes are the seal key (policy, missing key or role), a refused archive `Put`, and an object that contradicts its metadata (`hour ... is not sealed`). The notary resumes once fixed. Confirm with `audit verify --root ...`.

### AuditIndexLagHigh

Check that `audit-observe` runs, its log, and the database CPU, locks and pool.

### AuditIndexStalled

The log shows `an indexing pass failed`. After an upgrade the cause is usually a reader older than the writer: `does not satisfy catalogue.schema.json`. Upgrade `audit-observe` and `audit-query` to the writer's release or newer. Otherwise check the bucket and database.

### AuditIndexPassesFailing

Read the log as for `AuditIndexStalled`. It fires first when one tenant's object fails and the others succeed.

### AuditIndexRowsDeferred

The log line `an object was not indexed` names each object. A `retry` reason resumes when the cause is fixed. An `unreadable` object was skipped: investigate it. See [repair or rebuild the index](rebuild-the-index.md).

### AuditWriterRejectingRecords

The log names the record and reason. Find the producer by the logged observer.

### AuditQueueConsumerFailing

Read the writer log for `the writer refused a batch from the stream`. Check the archive and database first.

## Verify

The alert resolves. For a dead letter or a drop, tell the application's owner which records were affected.
