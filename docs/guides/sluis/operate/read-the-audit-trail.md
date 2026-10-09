# Read the audit trail

Find who did what in an installation, through the console's Audit page or the log lines every record also is.

## Before you start

- sluis records into an [audit installation](../../../concepts/audit/README.md) (`audit.writer` and `audit.query` in the chart) and keeps no trail of its own ([audit actions](../../../reference/sluis/audit-actions.md)).

- The Audit page needs a [connected installation](../connect-audit-installation.md) and a person its grants admit, with groups `<scope>:audit:<role>` in the `access-roster` grants preset. A recovery sign-in cannot read it.

- The log lines need `kubectl logs` and `jq`. With no installation connected they are the only copy.

- A record's address is the connection peer unless `audit.forwardedForTrustedHops` is set: then it is the `X-Forwarded-For` entry that many appending proxies from the right.

- The controllers record as the one process. Map its service account `<release>` to the source `roster` in the installation's `workloadIdentity.workloads` (on Lambda, the role `<FunctionName>`).

## 1. On the Audit page

Open the console's Audit page. It lists your granted records, newest first. Your own read appears as a record.

## 2. From the log

Each record is one line with `"audit":true` and the fields `audit.id`, `audit.action`, `audit.outcome`, `audit.actor.*`, `audit.subject.id`, `audit.targets.N` and `audit.reason`.

```sh
kubectl -n <namespace> logs deploy/<release> --since=24h | jq 'select(.audit == true)'
kubectl -n <namespace> logs deploy/<release> --since=24h \
  | jq -c 'select(.audit == true and ."audit.outcome" == "denied") | {time, action: ."audit.action", who: ."audit.actor.id", why: ."audit.reason"}'
```

The second command lists refusals. A record's `audit.id` finds the same record in the installation.

## 3. When the installation cannot be reached

Read this before an incident.

- Every record except a recovery sign-in goes on a bounded in-memory queue and is retried with backoff. A pod deleted during an outage loses its queue, and past the bound the oldest records are dropped and counted. The log line remains.

- A recovery sign-in fails closed (`block`): without the installation it is refused. Bring the writer back and recover again. With no installation connected, recovery is not refused.

- An installation unreachable at start does not stop it; registration retries. One that refuses the catalogue stops the process ([change the audit catalogue](../change-the-audit-catalogue.md)).

| Log line | Means |
|---|---|
| `audit installation connected` (Info) | the address answered |
| `audit catalogue registered` (Info) | the installation accepted the catalogue; records are kept |
| `the audit installation could not be reached; ...` (Warn) | started unregistered; records wait |
| `the audit installation refused the catalogue; ...` (Error) | catalogue and installation disagree; the process ends |
| `the audit installation does not trust this workload's token; ...` (Error) | the writer answered 401 or 403: check the token file, its audience, or `workloadIdentity` |
| `audit records could not be delivered yet` (Warn) | the writer refused or is unreachable; the queue holds them |
| `audit record not kept` (Warn) | a `block` record the writer refused; its request was refused |
| `audit record dropped and is not in the trail` (Error) | the queue gave one up; `audit.id` names it |
| `an audit record does not satisfy the catalogue` (Error) | a bug |
| `no audit installation is connected: ...` (Warn, at start) | the deployment keeps no trail |

Alert on `audit.emit.records.dropped`, pushed over OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. A climbing `audit.emit.queue.pending` means a writer gone too long.

## Verify

Stop the writer in a test installation and watch `audit.emit.queue.pending` climb. A dropped record is an incident: the trail has a hole and the log line is the only copy.
