# Scaling and cache

## Purpose

Decide how many replicas to run and what the directory snapshot cache costs.

## Preconditions

- A State every replica shares: `config.ports.adapter: dynamodb` (or the `k8s-aws` preset). Without it, one replica.

## Before you start

- **Replicas coordinate through the State port, not through each other.** With DynamoDB, every replica reads and writes
  the same table, so one refresher runs for all of them (a lease), and the snapshots are in the Blob port. The why is in
  [high availability](../explanation/high-availability.md).
- **A single replica may run with no shared State** (`memory`, or `legacy` with no `valkey.address`), at the cost of a
  cold cache on every restart. At two or more replicas that is a mistake: each replica refreshes the same directory and
  the directory's API quota is per tenant, not per reader. The service logs `keeping snapshots in memory` at start.
- **`replicaCount` above 1 with a controller is refused at render** unless the adapter is `dynamodb`.
- **Every replica serves every workspace**: one connected through the console on another replica is opened from its
  stored credential on first use.

## Steps

### 1. Choose the count

**Run** set `replicaCount` (the default is 2) and, for more than one replica, follow [high availability](high-availability.md).
**Expect** the render to accept it.
**Verify** the log at each pod: `keeping snapshots and the refresh lease in the state ports`.
**Rollback**: set the count back.

### 2. Size the snapshot store

**Run** estimate the snapshot size as the directories' size: one snapshot per workspace (`snapshots/<workspace>` in the
Blob port, gzip-encoded).
**Expect** bytes of the order of the number of accounts and groups.
**Verify** read the objects' sizes in the bucket.
**Rollback**: none, because it reads.

## Afterwards

- Watch `access_roster.leases.contended` and `AccessRosterLeaseLost` ([telemetry](../operations/telemetry.md)) after raising the
  count.
