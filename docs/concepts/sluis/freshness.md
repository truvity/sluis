# How fresh is a directory answer?

sluis does not read the directory on the request path. It keeps one **snapshot per workspace**: domains, every group with its flat members, every account with its live flag, and `snapshot_at`.

A background refresher replaces the snapshot every `refresh_interval` (default 15 minutes) under a lease the replicas share, so one replica fetches for all.

## Reads take a freshness

Every read takes an optional `max_age`. Omitted, it serves the current snapshot. A value makes the snapshot fresher first when it is older. Zero fetches now. A failed fetch serves the stale snapshot with `authoritative=false`.

Bulk reads trigger a single-flight full workspace read. Point reads fetch one account and its groups live and patch the snapshot, so a login never waits behind a full read.

A miss on an in-domain address goes live once before answering `found = false`, because not-found is a removal signal. An account created after the last snapshot is never reported absent.

A served domain that is not authoritative is **provisional**. The console says why: *first snapshot pending*, *snapshot stale* or *probe failed*. A domain two workspaces serve is *contested*. On the wire only the `authoritative` boolean exists.

## Nothing slow on the request path

A request never waits on the directory. Adopting a workspace stores it and returns, and the first snapshot runs detached and single-flight. A read with no snapshot answers *first snapshot pending*, provisional and empty.

Narrowing stores the new list, drops what is now excluded at once and refreshes detached. The only request-scoped read is the point lookup of one account.

## Every replica knows every workspace

The store is the truth and the reader map is a cache of it. A replica with no reader for a stored workspace opens one from the stored credential on first use. A workspace adopted on one replica exists on the others without a restart.

## The snapshot is shared

Snapshots, sessions and logins in progress live in the **State port**. On AWS that is DynamoDB, with large snapshots as S3 blobs. On Kubernetes the `legacy` adapter keeps them in ConfigMaps and, optionally, Valkey ([the store](store.md), [ports](ports.md)).

With a shared store, replicas answer from the same snapshot and a restart is warm. The directory is read once per interval whatever the replica count. The snapshot holds no directory credential. Losing it costs one fetch per workspace and, where sessions live in it, one sign-in per person.

Readiness follows the store and liveness does not. A replica that cannot reach the store leaves the gateway's rotation and names the dependency (`internal/health`), and nothing restarts.

With the Valkey-backed legacy adapter, leave cluster mode off unless you run several shards. With one shard, the client learns node addresses from `CLUSTER SLOTS` and bypasses the Kubernetes Service. A moved pod is then dialled at its old address while it reports healthy.

### The refresh lease

The refresh lease is held for most of the interval, not for the work (`leaseFor` in `internal/hub/background.go`). A successful pass leaves its lease to expire. Only a failed pass hands it back, so another replica tries.

Otherwise the replica whose turn came minutes later would read the same directory again, and a directory's API quota is per tenant. A refresh somebody asked for ignores the lease.

To run more than one replica, see [high availability](../../guides/sluis/operate/high-availability.md).

## Decided in

- [ADR 0027: The State port](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md)
