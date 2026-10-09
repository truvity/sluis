# Tutorial: an existing Kubernetes installation on the legacy store

!!! warning "Deprecated"
    The legacy store (`ports.adapter: legacy`, the default) and Valkey are deprecated in v1.74.0 and removed in v1.75.
    Move off them with `sluis migrate`: see [migrate the State](../../guides/sluis/migrate/migrate-state.md).

You have an installation on Kubernetes that keeps its state in Kubernetes objects and Valkey. By the end you can recognise the legacy store and keep it healthy. You also know the path to the DynamoDB state of [Kubernetes with AWS storage](kubernetes-aws.md).

Start a new installation from [Kubernetes with AWS storage](kubernetes-aws.md). The legacy store is not a preset: an installation gets it when it names no other adapter. Lambda refuses it.

## 1. Recognise it

The adapter is `ports.adapter` in the service document, and it defaults to `legacy`. On a chart release the sign is the chart default `config.store: kubernetes` with no adapter named. Read the release's ConfigMap, named `<release>-config` (`sluis` below):

```sh
kubectl -n sluis get configmap sluis-config -o jsonpath='{.data.config\.yaml}' | grep -E 'store:|adapter|preset|valkey'
```

Expect `store: kubernetes`, and a `valkey:` block if sessions are shared across replicas. You see no `adapter: dynamodb`, no `preset` and no `adapters:` table. A DynamoDB installation logs `keeping state in DynamoDB` at start.

The service's own objects are the other sign:

```sh
kubectl -n sluis get configmap,secret -l app.kubernetes.io/managed-by=directory-roster
```

Expect objects named `<release>-<kind>`, such as `sluis-github-orgs`, `sluis-github-apps`, `sluis-github-links`, `sluis-workspace-credentials`, `sluis-slack-credentials` and `sluis-github-status`. A State adapter has none. The full list, with the writer of each, is in [Kubernetes objects](../../reference/sluis/kubernetes-objects.md).

The release uses the chart's values mode (`config`, `policy`, `exchange`), which is deprecated; `NOTES.txt` says so. The installation document cannot describe the legacy store. `sluisctl render` refuses a Kubernetes installation with no AWS resources, because its fallback preset `k8s-minimal` is [unavailable](README.md#availability).

## 2. Keep it healthy

- **Run one replica of anything with a controller.** Leases live in each pod's memory, so a second replica acts on every target twice. The chart refuses `replicaCount` above 1 with a controller unless the adapter is `dynamodb`. A rollout can overlap the old and new pod for a few seconds: [high availability](../../concepts/sluis/high-availability.md#the-controllers-in-the-one-process).

- **Back up the Secrets.** Nothing upstream can re-deliver `<release>-workspace-credentials`, `<release>-github-apps`, `<release>-github-links`, `<release>-github-runner-apps` and `<release>-github-catalogue-apps`. The Slack state is three more. Export them, or copy two of them with External Secrets and the deprecated `directory.push` and `githubApps.push`.

- **Keep the sync off the service's objects.** The service rewrites these ConfigMaps and Secrets at runtime. A GitOps sync that prunes or reverts them causes an outage.

- **Know what Valkey does.** With `valkey.address` set, sessions, refresh tokens and leases live in Valkey, and asynchronous replication means a failover can sign everyone out. Without it they live in pod memory, and a restart signs everyone out.

To export the Secrets and ConfigMaps:

```sh
kubectl -n sluis get secret,configmap -l app.kubernetes.io/managed-by=directory-roster -o yaml > sluis-export.yaml
```

The export holds credentials: store it with the credentials themselves. Restoring is a rotation, so restart the pod afterwards; see [backing up and restoring](../../guides/sluis/operate/back-up-and-restore.md).

## 3. Move off it

Move the data first and the runtime second. Decided in: [0031](../../decisions/0031-a-generic-migration-tool.md).

1. Build the AWS side as in [step 1 of the AWS tutorial](kubernetes-aws.md#1-create-the-aws-side).
2. Write the destination configuration.
3. Dry-run against the live installation. It writes nothing and needs no freeze.
4. Freeze the writers, then run the move from your workstation:

   ```sh
   sluis migrate --from <old serve config> --to <new serve config>
   ```

5. Read the report (`ok: true`) and switch.

The command reads Kubernetes objects and Valkey and writes a DynamoDB table, SSM secrets and S3 blobs. It reads every record back to verify. It never changes the legacy objects, so the rollback is to scale the old Deployment back up. People sign in again unless you pass `--with-sessions`.

The runbook, including the writer freeze, is [Moving the State](../../guides/sluis/migrate/migrate-state.md). Moving the runtime to Lambda is a second step: [Cutover](../../guides/sluis/migrate/cutover.md). After the switch, move the release to [documents mode](kubernetes-aws.md#2-write-the-installation-and-render-it). Delete the legacy objects only when you no longer want a cheap rollback.
