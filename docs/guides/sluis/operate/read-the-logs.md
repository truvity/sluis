# Read the logs

## Purpose

Find what the service did, from its stdout, when no audit installation is connected or the console does not say enough.

## Preconditions

- `kubectl logs` on the namespace (on Lambda, the function's log group), and `jq`.

## Before you start

- **Structured JSON on stdout.** The service never logs a credential, a token or a key file, and never logs the members of
  a group. It logs workspace ids, domains, counts, durations and errors.
- **Every audit record is also one log line** with `"audit":true`. With no installation connected, those lines are all
  there is ([read the audit trail](read-the-audit-trail.md)).
- **The level is `log.level`.** The groups-scoping lines drop from INFO to DEBUG under `enforce`
  ([turn enforce on](../turn-enforce-on.md)).

## Steps

### 1. Read the pod's log

**Run**

```sh
kubectl -n <namespace> logs deploy/<release> --since=1h | jq -c 'select(.level == "ERROR" or .level == "WARN")'
```

**Expect** one JSON object per line, with `time`, `level` and `msg`.
**Verify** the lines you need appear; widen `--since` if not. Add `--previous` for a pod that restarted.
**Rollback**: none, because it only reads.

### 2. Follow one workspace

**Run** `kubectl -n <namespace> logs deploy/<release> --since=1h | jq -c 'select(.workspace == "<workspace id>")'`.
**Expect** the probes, snapshot refreshes and the failing page, if any.
**Verify** the message matches a row of [check health](check-health.md).
**Rollback**: none, because it only reads.

## Afterwards

- If you changed `log.level` to read a line, set it back: a DEBUG installation is noisy.
