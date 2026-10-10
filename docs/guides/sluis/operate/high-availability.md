# Run more than one replica

Run `sluis serve` at two or more replicas so a node loss or a rollout stops neither sign-in nor reconciling.

## Before you start

- Every replica needs a shared State: `config.ports.adapter: dynamodb` or the `k8s-aws` preset. A `legacy` cluster moves first ([migrate the State](../migrate/migrate-state.md), [cutover](../migrate/cutover.md)).

- Every replica needs a signing key: `signingKey.existingSecret`, the chart's `Certificate`, or a KMS-wrapped key on AWS.

- The chart refuses `replicaCount` above 1 with a controller unless the adapter is `dynamodb`. Without a controller, `legacy` with `valkey.address` shares the issuer's state.

- A replica on a process-local store looks like an intermittent failure. Its log says `keeping logins in progress in memory: correct for one replica`.

- The chart renders no PodDisruptionBudget, anti-affinity or topology-spread rule, and `values.schema.json` refuses an `affinity` key. Add them yourself.

- Keep the default `RollingUpdate`. `Recreate` deletes the old pod first, and a pod that crashes at start never becomes Ready ([check health](check-health.md#2-a-rollout-that-does-not-complete)).

- Add no caching header to `/keys` in front of the service.

- A changed client metadata document may show old values on a replica for 10 minutes.

## Steps

1. Set `replicaCount: 2` or more in the values and render. With `legacy` and a controller the render fails naming `config.ports.adapter`.

2. Apply a PodDisruptionBudget of your own. For anti-affinity, use a Helm post-renderer or a `kustomize` patch over `helm template`.

   ```yaml
   apiVersion: policy/v1
   kind: PodDisruptionBudget
   metadata:
     name: sluis
   spec:
     minAvailable: 1
     selector:
       matchLabels:
         app.kubernetes.io/name: sluis
         app.kubernetes.io/instance: <the release name>
   ```

3. Roll out.

   ```sh
   kubectl -n <namespace> rollout status deploy/<release>
   ```

4. To run one tick by hand, use `sluis tick <github|slack> <target> --config <file>`. It runs once under the target's lease and acts only where the target is enabled ([enable an organisation](../enable-github-organisation.md)). With `legacy` it refuses unless the controller is scaled to 0 and you pass `--unsafe-local-lease`.

## Verify

`kubectl get pdb` shows `ALLOWED DISRUPTIONS` of 1, and draining a node in a test cluster leaves one replica Ready. Both pods log `keeping state in DynamoDB`. `sluis.leases.contended` is non-zero while `SluisLeaseLost` stays quiet ([telemetry](../../../reference/sluis/telemetry.md)).

`/healthz` is liveness and checks nothing outside the process. `/readyz` follows the State: `periodSeconds: 10`, `timeoutSeconds: 3`, `failureThreshold: 3`. A replica leaves rotation within about thirty seconds.

## Roll back

Set `replicaCount: 1` and roll out. Each replica sweeps every interval, so two replicas make about twice the GitHub and Slack API calls.

## Decided in

[Why more than one replica is safe](../../../concepts/sluis/high-availability.md).
