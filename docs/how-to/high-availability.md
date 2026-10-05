# Run more than one replica

## Purpose

Run `sluis serve` at two or more replicas so a node loss or a rollout does not stop sign-in or pause reconciling.

## Preconditions

- A State every replica shares: `config.ports.adapter: dynamodb` (or the `k8s-aws` preset). A cluster still on `legacy`
  moves first ([migrate the State](migrate-state.md), [cutover](cutover.md)).
- A signing key every replica reads (`signingKey.existingSecret` or the chart's `Certificate`; KMS-wrapped on AWS).

## Before you start

- **`replicaCount` above 1 with a controller is refused at render** unless the adapter is `dynamodb`: on `legacy` or
  `memory` a lease lives in each pod's own memory, so every replica would act on every target and make each change twice.
  Without a controller, `legacy` with `valkey.address` shares the issuer's state. The reasons are in
  [why more than one replica is safe](../explanation/high-availability.md).
- **A replica on a process-local store looks like an intermittent failure.** The log says `keeping logins in progress in
  memory: correct for one replica`. If you see it with two replicas, the State is not shared.
- **The chart renders no PodDisruptionBudget, no anti-affinity and no topology-spread rule** (checked against
  `charts/sluis/templates/`). `values.schema.json` has `"additionalProperties": false` and no `affinity` or
  `topologySpreadConstraints`, so a stray `affinity:` key is refused at render, not ignored. You add those yourself.
- **A rollout is the default `RollingUpdate`.** A pod that crashes at start never becomes Ready and the old pods keep
  serving ([check health](check-health.md#2-a-rollout-that-does-not-complete)). Prefer not to set `Recreate`: it deletes
  the old pod first.
- **`/keys` must reach verifiers with no caching header added in front of it.** A proxy or CDN that adds one brings back
  the stale-JWKS window the key ring's `activationDelay` exists to avoid.
- **A client that changes its own metadata document** may be seen with old values by one replica and new by another for
  up to 10 minutes.

## Steps

### 1. Set the count

**Run** set `replicaCount: 2` (or more) in the values and render.
**Expect** the render accepts it; with `legacy` and a controller it fails naming `config.ports.adapter`.
**Verify** the render output has `replicas: 2` on the Deployment.
**Rollback**: set `replicaCount: 1`.

### 2. Add a disruption budget and spread

**Run** apply a PodDisruptionBudget of your own, and a Helm post-renderer or `kustomize` patch over `helm template` for
anti-affinity if you want one:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: sluis
spec:
  minAvailable: 1
  selector:
    matchLabels:
      # the chart's selector labels (`sluis.selectorLabels` in _helpers.tpl)
      app.kubernetes.io/name: sluis
      app.kubernetes.io/instance: <the release name>
```

**Expect** `kubectl get pdb` shows `ALLOWED DISRUPTIONS` of 1.
**Verify** drain a node in a test cluster; one replica stays Ready.
**Rollback**: delete the PodDisruptionBudget.

### 3. Roll out and check

**Run** `kubectl -n <namespace> rollout status deploy/<release>`.
**Expect** both pods Ready; probes are `/healthz` (liveness, follows nothing outside the process) and `/readyz`
(readiness, follows the State; `timeoutSeconds: 3`, `failureThreshold: 3`, `periodSeconds: 10`, so a replica leaves
rotation within about thirty seconds of real trouble).
**Verify** each pod's log says `keeping state in DynamoDB`, and `access_roster.leases.contended` is non-zero while
`AccessRosterLeaseLost` stays quiet ([telemetry](../reference/telemetry.md)).
**Rollback**: set `replicaCount: 1` and roll out.

### 4. One tick by hand

**Run** `sluis tick <github|slack> <target> --config <file>` runs one target's tick once under its lease. With the
`legacy` adapter it refuses unless the controller is scaled to 0 and `--unsafe-local-lease` is passed, because the
controller's lease does not exclude it.
**Expect** one pass and its report.
**Verify** the report's `tick.outcome`.
**Rollback**: none, because a tick acts only where the target is enabled ([enable an organisation](enable-github-organisation.md)).

## Afterwards

- Each replica sweeps every interval, so expect about twice the GitHub and Slack API calls.
