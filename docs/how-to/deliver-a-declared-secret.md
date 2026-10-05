# Deliver a declared Secret with external-secrets

## Purpose

Put a service-account key into the namespace for a [declared workspace](../reference/declared-workspaces.md) with
external-secrets. It is one way to do it, not a dependency of the service: any producer that creates the Secret works,
because the chart lets you name both the Secret and its keys.

## Preconditions

- external-secrets is installed and a `ClusterSecretStore` reaches the store that holds the key.
- The workspace is declared with `keySecret: directory/<id>/key`, and a `secrets` entry in the chart's values maps that name
  to the Secret and key below (`{name: directory/<id>/key, secretName: example-sa-key, key: key.json}`).

## Before you start

- **The key is a credential.** The store that holds it must be trusted with a workspace's service account.
- **A restored or rotated key needs the service to read it again.** The chart's `secrets` projection is read on every use,
  so a rotated Secret takes effect without a restart.
- **Preview before every apply, and read the preview.**

## Steps

### 1. Apply the ExternalSecret

**Run**

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: example-sa-key
  namespace: <namespace>
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: your-store
  target:
    name: example-sa-key
    creationPolicy: Owner
  data:
    - secretKey: key.json
      remoteRef:
        key: /path/in/your/store/example-sa-key-json
```

**Expect** `kubectl -n <namespace> get externalsecret example-sa-key` reports `SecretSynced`.

**Verify** `kubectl -n <namespace> get secret example-sa-key -o jsonpath='{.data.key\.json}' | base64 -d | jq .client_email`
prints the service account.

**Rollback**: delete the `ExternalSecret`; with `creationPolicy: Owner` the Secret goes with it.

## Afterwards

- The console shows the declared workspace healthy after its first probe.
- Do not use a pulling `ExternalSecret` for a Secret the service writes itself ([back up and
  restore](back-up-and-restore.md)): the service is the writer.
