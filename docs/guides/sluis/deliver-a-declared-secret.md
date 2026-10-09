# Deliver a declared Secret with external-secrets

Put a service-account key into the namespace of a [declared workspace](../../reference/sluis/declared-workspaces.md). Any producer that creates the Secret works.

## Before you start

- Install external-secrets with a `ClusterSecretStore` that reaches the store holding the key.

- Declare the workspace with `keySecret: directory/<id>/key`, and map that name in the chart's `secrets` values: `{name: directory/<id>/key, secretName: example-sa-key, key: key.json}`.

- Trust the store with a workspace's service account. A rotated Secret takes effect without a restart.

## 1. Apply the ExternalSecret

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

## 2. Verify

```sh
kubectl -n <namespace> get externalsecret example-sa-key
kubectl -n <namespace> get secret example-sa-key -o jsonpath='{.data.key\.json}' | base64 -d | jq .client_email
```

The first reports `SecretSynced`. The second prints the service account. The console shows the workspace healthy after its first probe.

Do not pull a Secret the service writes itself with an `ExternalSecret` ([back up and restore](operate/back-up-and-restore.md)).

## Roll back

Delete the `ExternalSecret`. With `creationPolicy: Owner` the Secret goes with it.
