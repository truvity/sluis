# Export what the console holds

Write every Secret and ConfigMap the service manages to one file, for an offline copy.

## Before you start

- You need `kubectl` access to the service's namespace on the `legacy` State. A State adapter keeps credentials in the Secrets port: see [back up and restore](back-up-and-restore.md#on-a-state-adapter).
- The export contains credentials. Encrypt the file and delete it when done.
- For a standing backup, prefer the copies the chart renders ([back up and restore](back-up-and-restore.md)).

## Steps

1. Export the managed objects:

   ```sh
   kubectl -n <namespace> get secret,configmap \
     -l app.kubernetes.io/managed-by=directory-roster -o yaml > sluis-export.yaml
   ```

   `directory-roster` is a legacy identifier, renamed in v1.75–v1.76: the chart still labels objects with it.

## Verify

`grep -c '^  kind:' sluis-export.yaml` equals the objects you expect. Those are the five GitHub and workspace Secrets, the Slack objects and the session key.

## Roll back

Delete the file.
