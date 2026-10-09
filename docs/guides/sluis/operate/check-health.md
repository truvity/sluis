# Check health: what "unhealthy" means and what to do

## Purpose

Read what the console says is wrong with a directory, a workspace or a rollout, and fix it.

## Preconditions

- Operator access to the console ([lost operator access](lost-operator-access.md) if you have none).
- `kubectl` on the namespace, for the rollout and log steps.

## Before you start

- **A non-authoritative answer holds, it never removes.** Every symptom in the table below is therefore safe to
  investigate at leisure: consumers keep what they had.
- **A rotation that has not taken effect looks exactly like one that has.** If the symptom started after somebody wrote a
  Secret, read [rotate keys and credentials](rotate-keys-and-credentials.md) first.
- **On Lambda the directory snapshot was never refreshed after the first one** (v1.60 and v1.61): sign-in failed 30 to 60
  minutes after a cold start with "the directory cannot be vouched for". It is fixed in v1.61.1 (refresh on demand and
  every 15 minutes). On an older release, the symptom is a snapshot (`snapshots/<workspace>` in the blob bucket) that
  never gets newer; deleting it, it is a cache, starts a refresh. Upgrade instead.
- **The one process serves, refreshes and runs the controllers** (since v1.63). A start that fails stops all of it, and a
  bad release shows as a stalled rollout, not an outage (step 2).

## Steps

### 1. Match the console's symptom

**Run** open the directory's page and match the row.

| Symptom (console) | Cause | Action |
|---|---|---|
| Health: error, domains *provisional, probe failed* | token revoked, admin suspended, tenant policy changed, scopes withdrawn | **Reconnect** (consent) or upload a new key. Nothing is lost meanwhile: the last snapshot is served, non-authoritative |
| Health ok, domains *provisional, snapshot stale* | the refresher cannot complete a full read (a page fails, quota, timeouts) | read the logs for the failing page ([read the logs](read-the-logs.md)); **Refresh** to retry now; the snapshot recovers on the next successful pass |
| Health ok, domains *provisional, first snapshot pending* | the workspace was connected moments ago, or its served list was just changed; the first snapshot runs detached | wait: seconds for a small tenant, a minute or two for a large one. Longer than a refresh interval: read the log for the failing page |
| The consent callback shows the CDN's own *Bad gateway* page | the callback never answers 5xx (since 0.7.2), so the request did not reach the service | the gateway, the route or the egress policy; the service's log has nothing because nothing arrived |
| One domain not authoritative on two workspaces, marked *conflict* | both tenants list the domain **and both serve it**: a move in progress, or a misconfiguration | wait for the move to complete, or narrow one of them: *Choose which to serve* on the directory's page |
| A domain shows *not served* | this service was narrowed to a subset of the tenant's domains, so nothing routes to it | intended in most cases; *Choose which to serve* changes it. A declared workspace says so in the values (`directory.workspaces[].serve`) |
| A domain shows *no longer owned* | the served list names a domain the directory no longer lists: it moved to another tenant | the hand-over already happened. Drop the entry so the list matches reality |
| Every domain non-authoritative at once | the snapshot store (the blob and State ports) is unreachable | restore it; the service refills it within one refresh interval |
| A workspace shows *declared* and no Reconnect/Disconnect | it comes from the chart's overlay | change the deployment's values, not the console |
| After a restart, a workspace is unhealthy with "no backend: the credential is not loaded" | its entry in `Secret <release>-workspace-credentials` is gone or unreadable: a restored namespace without that Secret, a hand-edited object | put the Secret back from a backup, then **restart** (the credential is read once), or **Reconnect**, or upload the key again. The record, its served domains and its synced groups are intact; only the credential is missing. The log names the workspace id at start |

**Expect** one row to match.
**Verify** after the action the row's health returns to ok.
**Rollback**: none, because the actions only reconnect or retry.

### 2. A rollout that does not complete

The Deployment rolls (Kubernetes' default `RollingUpdate`) with a readiness probe on `/readyz` (`probes.address`, default
`:7070`). A new pod is Ready only after the process finished starting: the policy loaded, the stores open, the audit
catalogue accepted, each controller begun. A pod that crashes at start never listens, so the rollout stalls with the old
pod still serving.

**Run**

```sh
kubectl -n <namespace> rollout status deploy/<release> --timeout=120s
kubectl -n <namespace> get pods -l app.kubernetes.io/name=<name>
kubectl -n <namespace> logs <the new pod> --previous
```

**Expect** the old pod Running and Ready, the new one `CrashLoopBackOff` or Running and not Ready, and a log line naming
the refusal. Seen so far: `the audit installation refused the catalogue` (the catalogue the binary carries is not one the
installation accepts: [change the audit catalogue](../change-the-audit-catalogue.md)), a policy the binary refuses, or
`enabledOrgs` / `enabledWorkspaces` naming something the policy does not bind.
**Verify** fix the cause and roll again; `rollout status` completes. Argo CD reports Progressing until
`progressDeadlineSeconds` (ten minutes), then Degraded; the old pod runs throughout.
**Rollback**: roll back the release. Prefer not to scale the old Deployment down or delete its pod to "make room": that
turns a stall into an outage.

### 3. Alert on a controller that stopped

**Run** alert on `access_roster.tick.last_success_timestamp` (per `kind`, `target`); the chart's `AccessRosterTickStale`
([AccessRosterTickStale](../../../reference/sluis/telemetry.md#runbook)) ages it. The series is **absent** when a controller has
never ticked and after a day of silence, so an alert on a controller gone for good needs `absent_over_time(...)` beside
it. The series are in [telemetry](../../../reference/sluis/telemetry.md#the-controllers-and-the-rails).
**Expect** the alert to fire within the stale window of a controller stopping.
**Verify** stop a controller in a test installation and see it fire.
**Rollback**: remove the alert rule.

## Afterwards

- A replica's loss pauses reconciling only for the time a new pod needs to start; every pass recomputes from the console
  and the target system, so nothing is missed. More than one replica is [high availability](high-availability.md).
- If a Secret was restored, restart and confirm from the log that the credential was read.
