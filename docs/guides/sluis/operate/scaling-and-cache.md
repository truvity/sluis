# Scaling and cache

Choose the replica count and size the directory snapshot cache.

## Before you start

- Replicas above one need a shared State: `config.ports.adapter: dynamodb` or the `k8s-aws` preset. The chart refuses a controller with `replicaCount` above 1 on any other adapter.
- A single replica may run on `memory`, or `legacy` with no `valkey.address`. It starts cold every time and logs `keeping snapshots in memory`.

- Every replica serves every workspace and opens a console-connected one from its stored credential on first use.

## Steps

1. Set `replicaCount` (default 2). For more than one replica follow [high availability](high-availability.md). Each pod logs `keeping snapshots and the refresh lease in the state ports`.

2. Estimate the snapshot size as the directories' size. The Blob port holds one gzip object `google/<workspace>` per workspace, of the order of the number of accounts and groups.

## Verify

Read the object sizes in the bucket. After raising the count, watch `access_roster.leases.contended` and `AccessRosterLeaseLost` ([telemetry](../../../reference/sluis/telemetry.md)).

## Roll back

Set `replicaCount` back.

## Decided in

[Why more than one replica is safe](../../../concepts/sluis/high-availability.md).
