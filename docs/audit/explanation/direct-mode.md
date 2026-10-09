# Direct mode

The receiver is the writer. A record arrives over Connect, is validated,
split per profile, rolled into an object and put into the bucket, and only
then acknowledged. There is no stream and nothing to consume.

This is the shape for an internal service — an identity service, an
operations console, anything whose record rate is tens or hundreds a minute
rather than thousands a second — and for any cluster that has no message
stream to use.

## What it looks like

**Write path.** Every workload of the application emits to the same writer, which puts the object in the bucket and marks the id in the database.

```mermaid
flowchart TB
  E["application pods<br/>emit"]
  E2["a second workload<br/>emit"]
  RW["audit-writer ×2<br/>receiver = writer"]
  S3[("the environment's bucket<br/>audit/app/")]
  PG[("database")]
  E --> RW
  E2 --> RW
  RW --> S3
  RW -- "dedupe, registry" --> PG
```

**Read path and jobs.** The indexer fills the index; the query service answers the console's Audit page; the CronJobs verify and purge the bucket.

```mermaid
flowchart TB
  CON["the console's<br/>Audit page"]
  Q["audit-query ×1"]
  OB["audit-observe ×1<br/>the indexer"]
  CJ["CronJobs<br/>verify, purge, clock-sync"]
  PG[("database")]
  S3[("the environment's bucket")]
  CON --> Q
  Q -- "reads" --> PG
  Q --> S3
  OB -- "index, cursors" --> PG
  OB -- "lists, reads" --> S3
  CJ --> S3
```

Two receiver replicas are safe: the deduplication table is in Postgres, so
two pods cannot write the same record twice, and the application's client
follows the Service to whichever is ready.

## One record

A privileged sign-in, declared `block` in the catalogue:

```mermaid
sequenceDiagram
  participant P as person
  participant A as application
  participant R as receiver = writer
  participant S3 as bucket
  participant PG as database
  P->>A: sign in, good proof
  A->>A: validate against the catalogue
  A->>R: Record, block
  R->>R: verify, stamp, split
  R->>S3: put object
  S3-->>R: stored
  R->>PG: mark the id
  R-->>A: durable
  A-->>P: signed in
  Note over A,S3: the bucket refuses, so the sign-in is refused
```

An `async` record takes the same path, except that the application does not
wait: it is queued, batched with whatever else is queued, and the batch is put
and acknowledged as one. The application's queue holds a record from the
moment it is recorded until that acknowledgement — one flush interval plus a
round trip, and that is the loss window if the pod dies. The emitter's `Flush`
and `Batch` are the knobs.

## Running it

The values, the index's database and the checks are in the
[Kubernetes tutorial](../getting-started/kubernetes.md) (the whole of
[`charts/audit/examples/direct.yaml`](../../../charts/audit/examples/direct.yaml) is rendered by the
chart's own tests) and [prepare the database](../how-to/prepare-the-database.md). The index
is behind the archive by the indexer's settle window (two minutes by default,
[0062](../../decisions/0062-observe-follows-the-bucket.md)); anything that must see a record
sooner reads the sink's acknowledgement, not the index. What to do when something fails is
in [recover from an outage](../how-to/recover-from-an-outage.md).
