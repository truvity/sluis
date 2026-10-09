# Read the logs

Find what the service did from its stdout, when no audit installation is connected or the console does not say enough.

## Before you start

- You need `kubectl logs` on the namespace (on Lambda, the function's log group) and `jq`.
- The log is JSON. It never holds a credential, a token, a key file or group members.

- Every audit record is also one line with `"audit":true` ([read the audit trail](read-the-audit-trail.md)).
- `log.level` sets the level. Under `enforce` the groups-scoping lines drop from INFO to DEBUG ([turn enforce on](../turn-enforce-on.md)).

## Steps

1. Read warnings and errors from the pod. Add `--previous` for a pod that restarted.

   ```sh
   kubectl -n <namespace> logs deploy/<release> --since=1h | jq -c 'select(.level == "ERROR" or .level == "WARN")'
   ```

2. Follow one workspace: probes, snapshot refreshes and the failing page.

   ```sh
   kubectl -n <namespace> logs deploy/<release> --since=1h | jq -c 'select(.workspace == "<workspace id>")'
   ```

## Verify

Each line has `time`, `level` and `msg`. A message matches a row of [check health](check-health.md). Widen `--since` when a line is missing.

## Roll back

Reading changes nothing. If you raised `log.level`, set it back: a DEBUG installation is noisy.
