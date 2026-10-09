# Move the State with `sluis migrate`

Copy an installation's State, secrets and controller reports between storages, and undo it with the same command. The whole Kubernetes-to-AWS move is the [cutover](cutover.md).

## Before you start

- Write a `serve` file per side. The legacy side needs `store`, `release` and `valkey`. The destination needs a working Secrets adapter, `ssm` or `openbao`.
- For DynamoDB set `ports.adapter: dynamodb`, `ports.dynamodb` (table, region) and `ports.blob` if reports move. Remove `valkey.address`; keep `store`, `release` and `inCluster`.
- From a workstation pass `--kubeconfig`, `--kube-context` and `--namespace`.
- Move SSM config parameters older than layout v3 first: `sluis migrate ssm-layout --to-root /sluis/<instance>` takes `--dry-run`. For v4 see [secrets layout](migrate-secrets-layout.md).
- `--i-have-stopped-writers` is your statement that the source is frozen ([freeze](cutover.md#2-freeze)).
- A differing destination value stops the run and names the key. Pass `--overwrite` only when the source is right.
- `--with-sessions` keeps sessions and `--skip` leaves out domains. For a backup, copy to a second storage.

| Domain | Moves | Lifetime |
|---|---|---|
| `directory` | workspaces and credentials | permanent |
| `github` | organisations, Apps, links with their `Revision`, confirmations, pass requests | permanent; requests keep their timestamp |
| `slack` | workspaces, catalogue Apps, Connect and console channel records, confirmations, pass requests | as `github` |
| `console` | session-signing key | permanent |
| `issuer` | key ring schedule (`issuer:keyring:*`); with `--with-sessions` also sessions, refresh tokens, SSO, minted tokens, requests, codes | the lifetime left on the source |
| `blobs` | controller reports | `--blobs auto` (default) copies only when the two sides keep reports in different places; `copy` always copies, `skip` never |
| never | leases, hub snapshots, `issuer:kms:*`, `share.`, `cache.` and `dedupe.` records | none |

The report prints keys and short hashes such as `issuer:session:#3fa9c1d2`, never values.

## Steps

### 1. Preview

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> --dry-run
```

Run it against the live installation, no freeze. `conflicts`, `unreadable` and `refused` must be empty. A record over 256 KiB, a credential over 8 KiB or an invalid key is `refused` and fails a real run even with `--overwrite`.

### 2. Freeze the writers

Scale the Deployment to 0 until no pod is listed. Sign-in is down.

```sh
kubectl -n <namespace> get pods -l app.kubernetes.io/instance=<release>
```

### 3. Run

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> \
  --i-have-stopped-writers
```

Or run a one-off Job with the chart image and ServiceAccount. One identity must reach DynamoDB and the secrets store.

```yaml
apiVersion: batch/v1
kind: Job
metadata: {name: sluis-migrate}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: <the chart's ServiceAccount>
      containers:
        - name: migrate
          image: <the chart's image, the same tag>
          args: [migrate, --from=/etc/migrate/old.yaml, --to=/etc/migrate/new.yaml,
                 --i-have-stopped-writers, --report-blob=reports/migrate-<date>.json]
          # env, volumeMounts and volumes as the Deployment's, plus the two files (a ConfigMap).
```

`kubectl logs job/sluis-migrate` shows the report. The run is idempotent: after a failure, repeat it.

### 4. Read the report

```json
{ "ok": true, "dryRun": false, "overwrite": false,
  "steps": [{"domain": "github", "kind": "organisations", "source": 3, "new": 3, "present": 0, "copied": 3, "verified": 3}],
  "totals": {"source": 415, "copied": 415, "verified": 415},
  "conflicts": [], "unreadable": [], "mismatches": [], "notes": [] }
```

Expect `ok: true`, empty `mismatches`, `conflicts` and `unreadable`, and `totals.verified` equal to `totals.source`. A source that changed shows as a mismatch. The run skips an `unreadable` record and exits non-zero.

### 5. Switch

Put the new `serve` file into the chart's values and restore the replicas. The log says `keeping state in DynamoDB`; the console lists the same workspaces.

## Roll back

Before the switch, scale up with the old values: nothing on the source was written. After it, freeze and swap the files:

```sh
sluis migrate --from new.yaml --to old.yaml --overwrite --i-have-stopped-writers
```

Then restore `ports.adapter` and `valkey.address` and roll. Keep the old ConfigMaps, Secrets and Valkey while this should stay possible.

## When it says no

| It says | Do |
|---|---|
| `refusing to copy while the source can still change` | Freeze, then pass `--i-have-stopped-writers`. |
| `the destination already holds a different value` | Nothing was written. Inspect the named `<domain>/<kind> <key>`, then `--overwrite` if the destination is stale. |
| `the destination does not match the source after the copy` | A writer still ran. Stop it and run again. |
| `the source holds items its store cannot read` | Fix or delete the named record and run again. |
| `ports.adapter dynamodb: ... secrets adapter` | Add a working Secrets adapter to the destination. |
| `store unavailable` | A side went away. Run the same command again. |

Decided in: [0031](../../../decisions/0031-a-generic-migration-tool.md), [0027](../../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md).
