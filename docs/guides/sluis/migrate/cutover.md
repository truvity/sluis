# Cut over from Kubernetes to AWS

Move a running installation from the `legacy` adapter to the AWS hybrid preset (DynamoDB, SSM, S3), with sign-in down for minutes.

## Before you start

- Configure the destination ([AWS Lambda](../../../reference/sluis/lambda.md), [Pulumi library](../../../reference/sluis/pulumi-library.md)). A preview that deletes a KMS key, table or bucket is wrong.

- Keep break-glass kube and AWS access independent of this installation.

- Write `old.yaml` with `valkey.address` at a `kubectl port-forward`.

- Write `new.yaml` with `ports.adapter: dynamodb`, the `ssm` secrets adapter and `ports.blob` ([`sluis migrate`](migrate-state.md)).

- Deploy the destination with `Schedule.Paused` and `DirectoryRefresh.Paused` set.

- Set `signingKey.activationDelay` to the poll interval, for example `30s`. New tokens pause that long after the switch.

- Run v1.61.1 or later: earlier releases fail sign-in after 30 to 60 minutes.

- Ship the [audit catalogue](../change-the-audit-catalogue.md) `.json` schemas.

- Publish old public keys the copy does not carry with `LambdaArgs.VerifyOnly` ([inputs](../../../reference/sluis/pulumi-library.md#inputs-lambdaargs)).

| Moves | Stays behind |
|---|---|
| State records, except expired ones | Sessions, refresh tokens, codes in flight, Index sets: people sign in again, or pass `--with-sessions` |
| Stored secrets, to the Secrets port | `issuer:kms:state-secret-fingerprint`: a stale value stops the start |
| Controller reports, to S3 | |
| Key ring schedule: old tokens keep verifying | |

A first smoke start writes `console/session-key`, so the real run needs `--overwrite`.

## Steps

### 1. Dry-run against the live installation

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> --dry-run
```

It writes nothing and needs no freeze. Fix what it refuses until it exits 0.

### 2. Freeze

```sh
kubectl -n argocd annotate application <app> argocd.argoproj.io/skip-reconcile=true
kubectl -n <namespace> scale deploy/<release> --replicas=0
```

The chart refuses `replicaCount: 0` and a GitOps controller scales back, so pause it first. Wait until no pod is left. Sign-in is down from here.

### 3. Copy

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> \
  --i-have-stopped-writers
```

Add `--overwrite` only when the source value must replace the destination's. After a failure, repeat the command.

### 4. Verify the counts

Run `cmd/acceptance` against the API Gateway URL. The report shows `ok: true`, `totals.verified` equal to `totals.source`, and `concerns` equal to the dry run.

### 5. Switch

Point DNS at the new origin, unset `Paused` and apply. The preview changes only schedule state.

Sign in with `sluisctl login` and check [health](../operate/check-health.md).

## Roll back

Set `Paused` again, apply, point DNS back and scale the old Deployment up. To keep writes made since the switch, copy back ([rollback](migrate-state.md#roll-back)).

## Afterwards

- Keep the old Deployment at 0 and the old ConfigMaps, Secrets and Valkey while a rollback should stay cheap. Then remove the orphaned Application.

- Turn off the recovery password once a directory group grants operator ([recover on Lambda](../operate/recover-on-lambda.md)).

See also [move the domain to the edge module](move-the-domain-to-the-edge-module.md) and [supply your own signing keys](supply-your-own-signing-keys.md).

Decided in: [0031](../../../decisions/0031-a-generic-migration-tool.md).
