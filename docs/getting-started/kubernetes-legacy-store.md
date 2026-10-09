# Tutorial: an existing Kubernetes installation on the legacy store

!!! warning "Deprecated"
    The legacy store (`ports.adapter: legacy`, the default) and Valkey are deprecated in v1.74.0 and removed in v1.75.
    Move off them with `sluis migrate`: see [migrate the State](../how-to/migrate-state.md) and
    [migrate the secrets layout](../how-to/migrate-secrets-layout.md).

This is for an installation that already runs on Kubernetes and keeps its state in Kubernetes objects (and Valkey):
the `legacy` adapter, the storage sluis has always had. It is the oldest shape and it is on its way out: the
destination is the DynamoDB state of [Kubernetes with AWS storage](kubernetes-aws.md), reached with `sluis migrate`.
By the end you can tell that you are on it, keep it healthy while you stay, and you know the path off it.

You do not start a new installation here. A new one on Kubernetes starts from
[Kubernetes with AWS storage](kubernetes-aws.md). The legacy store is not a preset: it is what an installation gets when
it names no other adapter, and it is refused on Lambda.

## 1. Recognise it

The adapter is `ports.adapter` in the service document, and its default is `legacy` (`internal/store`). On a chart
release the sign is the chart's own default, `config.store: kubernetes`, with no adapter named. In the release's
ConfigMap (named `<release>-config`; `sluis` below):

```sh
kubectl -n sluis get configmap sluis-config -o jsonpath='{.data.config\.yaml}' | grep -E 'store:|adapter|preset|valkey'
```

Expect `store: kubernetes` and, if you keep sessions across replicas, a `valkey:` block. On the legacy store you will
see no `adapter: dynamodb`, no `preset` and no `adapters:` table. The service's own objects are the other sign:

```sh
kubectl -n sluis get configmap,secret -l app.kubernetes.io/managed-by=directory-roster
```

Expect the objects the service writes as people connect things, each named `<release>-<kind>`: for example
`sluis-github-orgs`, `sluis-github-apps`, `sluis-github-links`, `sluis-workspace-credentials`, `sluis-slack-credentials`,
`sluis-github-status`. On a State adapter there are none: the records are in the table and the credentials in the
secrets adapter. The full list, with who writes each, is in
[configuration](../reference/kubernetes-objects.md). A DynamoDB installation logs
`keeping state in DynamoDB` at start; look for that line too.

The release is configured in the chart's values-mode (`config`, `policy`, `exchange`), which is deprecated: it keeps
working for one minor and `NOTES.txt` says so while it is used. The installation document cannot describe the legacy
store: `sluisctl render` refuses a Kubernetes installation with no AWS resources, because the preset it falls back to,
`k8s-minimal`, is unavailable ([availability](README.md#availability)).

## 2. Keep it healthy

- **Run one replica of anything that has a controller.** Leases live in each pod's own memory on the legacy store, so
  a second replica would act on every target twice; the chart refuses `replicaCount` above 1 with a controller unless
  the adapter is `dynamodb`. A rollout can overlap the old and new pod for a few seconds
  ([high availability](../explanation/high-availability.md#the-controllers-in-the-one-process)).
- **Back up the Secrets, because nothing upstream can re-deliver them.** `<release>-workspace-credentials`,
  `<release>-github-apps`, `<release>-github-links`, `<release>-github-runner-apps` and
  `<release>-github-catalogue-apps` hold what the console added, and the Slack state is three more. Export them, or have
  External Secrets copy two of them with the deprecated `directory.push` and `githubApps.push`:

  ```sh
  kubectl -n sluis get secret,configmap -l app.kubernetes.io/managed-by=directory-roster -o yaml > sluis-export.yaml
  ```

  The export holds credentials: keep it where you keep the credentials themselves. Restoring is a rotation, so restart
  the pod afterwards ([backing up and restoring](../how-to/back-up-and-restore.md)).
- **Keep the sync off the service's objects.** The service rewrites these ConfigMaps and Secrets at runtime. A GitOps
  sync that prunes or reverts them is an outage; the objects belong to the service, not to the repository.
- **Know what Valkey is for.** With `valkey.address` set, sessions, refresh tokens and leases are in Valkey, and its
  replication is asynchronous: a failover can sign everyone out. Without it they are in the pod's memory, and a restart
  signs everyone out.

## 3. The path off it

Moving off is two decisions taken apart: the data, then the runtime
([0031](../decisions/0031-a-generic-migration-tool.md): data and runtime never move in the same step). The data move is
one command, `sluis migrate --from <old serve config> --to <new serve config>`, from your workstation: Kubernetes objects
and Valkey in, a DynamoDB table, SSM secrets and S3 blobs out, every record read and written through the ports and read
back to verify. Build the AWS side as in [step 1 of the AWS tutorial](kubernetes-aws.md#1-create-the-aws-side), write the
destination configuration, dry-run against the live installation (it writes nothing and needs no freeze), then freeze
the writers, run it, read the report (`ok: true`) and switch. The legacy objects are never changed, so the rollback is
to scale the old Deployment back up. People sign in again unless you pass `--with-sessions`.

The runbook is [Moving the State](../how-to/migrate-state.md), including the freeze of the writers. If the runtime moves
to Lambda as well, that is a second step, with its own runbook: [Cutover](../how-to/cutover.md). After the switch, move the release to documents mode
([the AWS tutorial](kubernetes-aws.md#2-write-the-installation-and-render-it)) and delete the legacy objects only when
you no longer want a cheap rollback.

## You now have

- a way to tell a legacy installation from a State-backed one, from its configuration and from the objects it keeps;
- the habits that keep it safe until it moves: one replica, a backup of the five Secrets, no sync over the service's
  objects;
- the route off it: [Moving the State](../how-to/migrate-state.md), then [Kubernetes with AWS storage](kubernetes-aws.md).
