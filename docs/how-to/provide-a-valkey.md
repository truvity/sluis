# Provide a Valkey

## Purpose

Give the `legacy` State adapter a shared store for sessions and directory snapshots, so more than one replica works. The
other way to run replicas is `config.ports.adapter: dynamodb`, which needs no Valkey ([high availability](high-availability.md)).

## Preconditions

- A Valkey or Redis-protocol server reachable from the namespace. The service stores one key set per workspace (the
  snapshot, its timestamp, a short negative cache) and a lock per workspace: memory use is the size of the directories,
  tens of megabytes at most. Persistence is not required; a cold cache costs one fetch per workspace.
- With the [valkey-operator](https://github.com/hyperspike/valkey-operator), a single-node cluster in the service's
  namespace is enough.

## Before you start

- **`valkey.cluster: false` for a single node.** With one shard the cluster protocol makes the client learn node addresses
  from `CLUSTER SLOTS` and talk to those, bypassing the Service, the one mechanism whose job is to survive a pod moving.
  Turn it on when the store has three shards with a replica each.
- **Without a Valkey, `replicaCount` above 1 is wrong** under `ports.adapter: legacy`: each replica keeps logins in its own
  memory ([high availability](high-availability.md)).

## Steps

### 1. Create the Valkey

**Run**

```yaml
apiVersion: hyperspike.io/v1
kind: Valkey
metadata:
  name: sluis-cache
  namespace: <namespace>
spec:
  nodes: 1
  replicas: 0
  tls: false
```

**Expect** a Service for the cluster, and a password Secret if the operator issues one.

**Verify** `kubectl -n <namespace> get valkey sluis-cache` is ready.

**Rollback**: delete the object.

### 2. Point the chart at it

**Run** set `config.valkey.address: sluis-cache.<namespace>.svc:6379`, `config.valkey.cluster: false`, and, if the operator
issues a password Secret, `config.valkey.passwordSecret: valkey/password` with a `secrets` entry
`{name: valkey/password, secretName: <that Secret>, key: <its key>}`.

**Expect** `helm template` renders the address in `<release>-config`.

**Verify** after the upgrade `/readyz` is ready (readiness follows Valkey) and a sign-in survives a pod restart.

**Rollback**: remove the `valkey` block.

## Afterwards

- Watch readiness: a replica that cannot reach Valkey is not ready, by design ([high availability](high-availability.md)).
