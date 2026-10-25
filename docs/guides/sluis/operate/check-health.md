# Check health

Read what the console says is wrong with a directory, a workspace or a rollout, and fix it.

## Before you start

- You need operator access to the console ([lost operator access](lost-operator-access.md)) and `kubectl` on the namespace.
- A non-authoritative answer holds and never removes: consumers keep what they had, so every symptom below can wait.
- A rotation that has not taken effect looks like one that has. If the symptom followed a Secret write, read [rotate keys and credentials](rotate-keys-and-credentials.md).

## 1. Match the symptom

| Symptom (console) | Cause | Action |
|---|---|---|
| Health: error, domains *provisional, probe failed* | token revoked, admin suspended, tenant policy changed, scopes withdrawn | **Reconnect** or upload a new key. The last snapshot keeps being served, non-authoritative |
| Health ok, domains *provisional, snapshot stale* | the refresher cannot complete a full read: a failing page, quota, timeouts | find the page in [the logs](read-the-logs.md), then **Refresh** |
| Health ok, domains *provisional, first snapshot pending* | the workspace was just connected or its served list just changed | wait: seconds for a small tenant, minutes for a large one. Longer than a refresh interval: read the log |
| The consent callback shows the CDN's *Bad gateway* page | the callback never answers 5xx, so the request did not reach the service | check the gateway, the route and the egress policy. The service log is empty |
| One domain *conflict* on two workspaces | both tenants list and serve the domain: a move in progress, or a misconfiguration | wait for the move, or narrow one with *Choose which to serve* |
| A domain shows *not served* | this service was narrowed to a subset of the tenant's domains | change it with *Choose which to serve*, or `directory.workspaces[].serve` for a declared workspace |
| A domain shows *no longer owned* | the served list names a domain the directory no longer lists | drop the entry |
| Every domain non-authoritative | the snapshot store (Blob and State ports) is unreachable | restore it. The service refills it within one refresh interval |
| A workspace shows *declared* with no Reconnect or Disconnect | it comes from the chart's overlay | change the deployment's values |
| A Cloudflare token request takes seconds (`route` `token_mint`) | a mint is synchronous: the token endpoint calls the Cloudflare module, which calls Cloudflare | expected for now. `SluisTokenEndpointSlow` leaves mints out; `SluisTokenMintSlow` fires on a p95 over 10 s |
| After a restart: "no backend: the credential is not loaded" | its entry in `Secret <release>-workspace-credentials` is gone or unreadable | restore the Secret from a backup and restart, or **Reconnect**, or upload the key again. The record is intact |

After the action the row's health returns to ok. The actions only reconnect or retry.

## 2. A rollout that does not complete

The Deployment uses `RollingUpdate` with a readiness probe on `/readyz` (`probes.address`, default `:7070`). A new pod is Ready only after the policy loads, the stores open, the audit catalogue is accepted and each controller begins. A pod that crashes at start never listens, so the old pod keeps serving.

```sh
kubectl -n <namespace> rollout status deploy/<release> --timeout=120s
kubectl -n <namespace> get pods -l app.kubernetes.io/name=<name>
kubectl -n <namespace> logs <the new pod> --previous
```

The log names the refusal. Known ones are `the audit installation refused the catalogue` ([change the audit catalogue](../change-the-audit-catalogue.md)), a policy the binary refuses, and `enabledOrgs` or `enabledWorkspaces` naming something the policy does not bind. Fix the cause and roll again. Argo CD reports Progressing until `progressDeadlineSeconds` (ten minutes), then Degraded.

To roll back, roll back the release. Do not scale the old Deployment down or delete its pod: that turns a stall into an outage.

## Alert on a stopped controller

Alert on the ok ticks of `sluis.ticks` per `kind` and `target`; the last-success gauge is per process. The chart's [SluisTickStale](../../../reference/sluis/telemetry.md) counts them and `SluisTickSeriesStale` flags a stopped series. After two hours of silence the series is absent, so add `absent(...)` for a gone controller. The series are listed in [telemetry](../../../reference/sluis/telemetry.md#the-controllers-and-the-rails). Stop a test controller to see it fire.

More than one replica is [high availability](high-availability.md): losing one pauses reconciling only while a new pod starts.
