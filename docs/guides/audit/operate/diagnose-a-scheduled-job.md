# Diagnose a scheduled job that is failing or silent

Find out why a CronJob (verify, purge, clock-sync or the notary) fails or stopped running, and get its error back.

## Before you start

- You need `kubectl` access to the installation's namespace. For clock-sync, you need the reference clock's address (`ntp` in the job's configuration).

- A failing CronJob says nothing: its pods are deleted with the Job, so no log is left. The tell is `lastSuccessfulTime`.

- The causes are configuration: a bucket or prefix the process never received, a role missing one verb, or a signing key it may not use. Each is one log line, so a re-run shows it.

- The clock-sync job never sets the clock. The offset it reports is the correction this clock needs: positive means it is behind.

## Steps

1. Look for a job that never succeeded. A `lastScheduleTime` with no or an old `lastSuccessfulTime` means it fails on every run.

   ```
   kubectl -n <ns> get cronjobs -o custom-columns=\
   NAME:.metadata.name,LAST:.status.lastScheduleTime,SUCCESS:.status.lastSuccessfulTime
   ```

2. Get the error back by running the job again and keeping the pod. The log names the bad key, bucket or role.

   ```
   kubectl -n <ns> create job --from=cronjob/<name> <name>-probe
   kubectl -n <ns> logs job/<name>-probe
   ```

3. For clock-sync, run the command by hand.

   ```
   audit clock-sync --ntp <server> --ntp <server> --sink <url> [--max-offset 1s]
   ```

   It records `audit.clock.synchronised` whenever a reference answered, whether or not the offset exceeds `--max-offset`. It records nothing when no reference answered, because the clock was not checked. The report says which failure you have.

## Roll back

Delete the probe: it is in nobody's git.

```
kubectl -n <ns> delete job <name>-probe
```

## See also

Alert on the CronJob, not the archive: a verification that never ran leaves nothing in the archive.
