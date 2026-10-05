# Freshness

The directory is not read on the request path. One **snapshot per workspace** is kept (domains, every group with its
flat members, every account with its live flag, and `snapshot_at`), and a background refresher replaces it every
`refresh_interval` (default 15 minutes) under a lease shared by the replicas, so one replica fetches for all.

Every read takes an optional `max_age`: omitted serves the current snapshot, a value makes it fresher first when it
is older, zero fetches now, and a failed fetch serves the stale snapshot with `authoritative=false`. Freshness is
honoured by the cheapest path that satisfies it. Bulk reads trigger a full workspace read, single-flight. Point reads
fetch one account and its groups live and patch the snapshot, so a login is never held behind a full read. A miss on
an in-domain address always goes live once before answering `found = false`, because not-found is a removal signal:
an account created after the last snapshot is never reported absent.

A served domain that is not authoritative is **provisional**, and the console says why: *first snapshot pending*,
*snapshot stale*, or *probe failed*; a domain two workspaces both serve is *contested*. The wire contract is the
`authoritative` boolean and nothing else; the word is the console's.

## Nothing slow on the request path

A request never waits on the directory. Adopting a workspace stores it and returns; the first snapshot runs detached,
under the process's own context, single-flight. A read with no snapshot answers *first snapshot pending* (provisional
and empty) and lets the refresher fill it. Narrowing stores the new list, drops what is now excluded at once, and
refreshes detached. The only request-scoped read left is the point lookup, which is one account and bounded.

The rule exists because slow reads on the request path met a gateway's fifteen-second timeout, were cancelled
mid-read, restarted from zero on the next request, and made a consent callback report failure on a connect that had
succeeded. Raising the gateway's timeout is not the fix: a longer timeout only hides the next thing that approaches
it.

## Every replica knows every workspace

The store is the truth and the reader map is a cache of it. A replica with no reader for a workspace the store knows
opens one from the stored credential on first use, so a workspace adopted on one replica exists on the others without
a restart.

## Sharing the snapshot

Snapshots, sessions and the logins in progress live in the **State port**, not in the process: on AWS that is
DynamoDB, with the large snapshots as S3 blobs; on Kubernetes the `legacy` adapter keeps them in ConfigMaps and,
optionally, Valkey until a deployment has moved off it ([the store](store.md), [ports](ports.md)). With a shared
store, replicas answer from the same snapshot, a restart is warm, and the directory is read once per interval
regardless of replica count. The store holds no directory credential in the snapshot: losing it costs one fetch per
workspace and, where sessions live in it, one sign-in per person.

Readiness follows the store; liveness deliberately does not. A replica that cannot reach it leaves the gateway's
rotation and answers fast instead of hanging, and nothing is restarted, so one blip cannot restart the fleet. The
refusal names the dependency and the reason (`internal/health`). For the Valkey-backed legacy adapter, leave cluster
mode off unless there are several shards: with one shard it makes the client learn node addresses from
`CLUSTER SLOTS` and bypass the Kubernetes Service, so a moved pod is dialled at its old address while reporting
healthy.

### The refresh lease

The refresh lease is **held for most of the interval, not for the work** (`leaseFor` in `internal/hub/background.go`).
Replicas do not tick together: a lease let go when a refresh finished would be taken by the replica whose turn came
minutes later, which would read the same directory again, and a directory's API quota is per tenant, not per reader.
A successful pass leaves its lease to expire; only a failed pass hands one straight back, because then somebody else
should try. None of it applies to a refresh somebody asked for.

For running more than one replica see [high availability](../how-to/high-availability.md).
