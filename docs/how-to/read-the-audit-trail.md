# Read the audit trail: what happened lately

## Purpose

Find who did what in an installation: through the console's Audit page, or from the log lines every record also is.

## Preconditions

- For the Audit page: an audit installation connected ([connect an audit installation](connect-audit-installation.md))
  and a person the installation's grants admit.
- For the log lines: `kubectl logs` on the namespace and `jq`.

## Before you start

- **sluis keeps no audit trail of its own.** It records into an **audit installation**
  ([truvity/audit](https://github.com/truvity/audit)) rendered beside it in the same namespace (`audit.writer` and
  `audit.query` in the chart). What it records is its catalogue ([audit actions](../reference/audit-actions.md)). The
  installation locks, indexes and signs; retention is its profile's (`security`, every action, people in clear), not a
  setting here. Its own documentation covers the archive, verification and legal holds.
- **The Audit page is the installation's view.** The console forwards the page's calls to the query service with a token
  it mints for the person signed in (audience `audit.audience`), so what anyone sees is decided by the installation's
  grants (with the `access-roster` grants preset, groups named `<scope>:audit:<role>`), and every read is itself
  recorded there. Somebody the policy does not admit is told the page is not theirs. **A recovery sign-in has no address
  and cannot read the page.**
- **With no installation connected, the log lines are all there is.** They are validated against the catalogue and logged,
  and kept nowhere else.
- **The address in a record's context** is the connection's peer unless `audit.forwardedForTrustedHops` is set; then it is
  the `X-Forwarded-For` entry just left of that many of the deployment's own proxies, read from the right. Count the
  proxies that append: behind an edge that appends the client and a gateway that appends the edge's connector, it is 1.
- **The controllers record as the one process**, with its service account's token; the installation stamps it as those
  records' observer. The one service account (`<release>`) must be mapped to the source `roster` in the installation's
  `workloadIdentity.workloads`; the `<release>-github-roster` and `<release>-slack-roster` entries of v1.62 are dead and
  may be removed (on Lambda the one role is `<FunctionName>`).

## Steps

### 1. On the Audit page

**Run** open the console's Audit page.
**Expect** the records you are granted, newest first.
**Verify** your own read appears as a record.
**Rollback**: none, because it only reads.

### 2. From the log

Every record is one log line with `"audit":true`: `audit.id` (the record's id, which finds it in the installation),
`audit.action`, `audit.outcome`, `audit.actor.kind`, `audit.actor.id`, `audit.subject.id`, `audit.targets.N` and
`audit.reason`.

**Run**

```sh
kubectl -n <namespace> logs deploy/<release> --since=24h | jq 'select(.audit == true)'
kubectl -n <namespace> logs deploy/<release> --since=24h \
  | jq -c 'select(.audit == true and ."audit.outcome" == "denied") | {time, action: ."audit.action", who: ."audit.actor.id", why: ."audit.reason"}'
```

**Expect** one object per record; the second command lists the refusals.
**Verify** a record's `audit.id` finds the same record in the installation.
**Rollback**: none, because it only reads.

### 3. When the installation cannot be reached

Read this before an incident, not during it.

- **Nothing but a recovery sign-in waits for the installation.** Every other record goes on a bounded queue inside the
  process and is retried with backoff until the writer takes it: an outage is a delay, and the queue's depth is the
  signal. The queue is in memory, so a pod deleted while the writer is down loses what it held, and past the bound the
  oldest are dropped and counted. The log line every record also is remains either way. This is fail-open by decision: an
  audit outage must not become an access outage.
- **A recovery sign-in fails closed.** Its catalogue entry says `block`: the installation keeps it before the sign-in
  succeeds, and when it cannot, the sign-in is refused (a page at the issuer, a 503 at the console's own door), and the
  refusal is recorded the ordinary way. The proof was good: bring the writer back and recover again. There is no
  override. With no installation connected at all, recovery is not refused.
- **An installation unreachable at start** does not stop the start: records wait in the queue, and the registration of the
  catalogue is retried until it answers. **An installation that refuses the catalogue stops the process**, whether at
  start or when it first answers after an unreachable start ([change the audit catalogue](change-the-audit-catalogue.md)).
  **A token the installation does not trust** is said at Error, naming the token file, and retried; until it is fixed the
  queue fills and past its bound drops.

**The signals**, in the log:

| Line | Means |
|---|---|
| `audit installation connected` (Info) | the address answered |
| `audit catalogue registered` (Info) | it accepted this service's catalogue; records are kept |
| `the audit installation could not be reached; records wait in the emitter's queue, and registration is retried` (Warn) | started unregistered |
| `the audit installation refused the catalogue; records will not be kept until it is fixed` (Error) | the catalogue and the installation disagree; the process ends |
| `the audit installation does not trust this workload's token; ...` (Error) | the writer answered 401 or 403: the token file, its audience, or the installation's `workloadIdentity` |
| `audit records could not be delivered yet` (Warn) | the writer refused or could not be reached; the queue holds them |
| `audit record not kept` (Warn) | one record: a `block` record the writer would not take, and the request it was for was refused |
| `audit record dropped and is not in the trail` (Error) | the queue gave one up; its `audit.id` names the Info line that reads as kept |
| `an audit record does not satisfy the catalogue` (Error) | a bug: the code built a record its catalogue refuses |
| `no audit installation is connected: records are validated and logged, and kept nowhere else` (Warn, at start) | the deployment keeps no trail |

and as metrics, from the emitter, pushed over OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set on the pod:
**`audit.emit.records.dropped` is the one to alert on**: the queue gave up and those records are gone.
`audit.emit.queue.pending` climbing and not falling is a writer gone too long; `audit.emit.batches.failed`,
`audit.emit.records.refused` (a bug) and `audit.emit.records.written` are the rest.

**Run** alert on the dropped counter.
**Expect** it to stay at zero.
**Verify** stop the writer in a test installation and see `queue.pending` climb.
**Rollback**: remove the alert.

## Afterwards

- An `audit record dropped` line is an incident: the trail has a hole, and the log line is the only copy.
