# Export what the console holds

## Purpose

Write every Secret and ConfigMap the service manages to one file, for an offline copy.

## Preconditions

- `kubectl` access to the service's namespace, on the `legacy` (Kubernetes objects) State. On a State adapter the
  credentials are in the Secrets port ([back up and restore](back-up-and-restore.md#on-a-state-adapter)).

## Before you start

- **The export contains credentials.** Treat the file as one: encrypt it, keep it off shared disks, and delete it when
  its purpose is served.
- Prefer the copies the chart renders ([back up and restore](back-up-and-restore.md)) for a standing backup; this is a
  one-off.

## Steps

### 1. Export

**Run**

```sh
kubectl -n <namespace> get secret,configmap \
  -l app.kubernetes.io/managed-by=directory-roster -o yaml > sluis-export.yaml
```

**Expect** a YAML list of the managed objects.
**Verify** `grep -c '^  kind:' sluis-export.yaml` is the number of objects you expect (the five GitHub and workspace
Secrets, the Slack objects, the session key).
**Rollback**: delete the file.

## Afterwards

- Store it where backups go, encrypted, and record who has it.
