# Provide a Valkey

Give the `legacy` State adapter a shared store for sessions and directory snapshots, so more than one replica works. With `config.ports.adapter: dynamodb` you need no Valkey ([high availability](high-availability.md)).

## Before you start

- You need a Valkey or Redis-protocol server reachable from the namespace. Memory use is the size of the directories, tens of megabytes at most. Persistence is optional: a cold cache costs one fetch per workspace.
- Set `valkey.cluster: false` for a single node. With one shard, cluster mode makes the client bypass the Service and talk to node addresses from `CLUSTER SLOTS`. Turn it on for three shards with a replica each.

- Without a Valkey, `replicaCount` above 1 is wrong under `ports.adapter: legacy`: each replica keeps logins in its own memory.

## Steps

1. With the [valkey-operator](https://github.com/hyperspike/valkey-operator), create a single-node Valkey in the service's namespace.

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

2. Set `config.valkey.address: sluis-cache.<namespace>.svc:6379` and `config.valkey.cluster: false`. If the operator issues a password Secret, set `config.valkey.passwordSecret: valkey/password` and add a `secrets` entry `{name: valkey/password, secretName: <that Secret>, key: <its key>}`.

## Verify

`kubectl -n <namespace> get valkey sluis-cache` is ready. After `helm upgrade`, `/readyz` is ready (readiness follows Valkey) and a sign-in survives a pod restart. A replica that cannot reach Valkey is not ready.

## Roll back

Remove the `valkey` block and delete the Valkey object.
