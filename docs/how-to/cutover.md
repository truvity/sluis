# Cutover: move an installation from Kubernetes to AWS

## Purpose

Move a running installation from Kubernetes (the `legacy` adapter: ConfigMaps, Secrets and Valkey) to the AWS hybrid
preset (DynamoDB for State, SSM for secrets, S3 for the controllers' reports), with sign-in down for minutes.

## Preconditions

- The destination exists and is configured ([AWS Lambda](../reference/lambda.md), [Pulumi library](../reference/pulumi-library.md)).
- An operator workstation with AWS credentials and kube access. The command is [`sluis migrate`](migrate-state.md); the
  decision is [0031](../decisions/0031-a-generic-migration-tool.md). Its flags, report and refusals live in that page and
  are not repeated here.
- **Credentials that do not depend on the installation you are about to stop.** Where kube and AWS access are issued
  through this installation, both are down during the window: have a break-glass path (a kubeconfig from the cluster
  itself, a temporary AWS key) written down before you start.
- `old.yaml`, the legacy file as the Deployment reads it, with `valkey.address` pointing at a `kubectl port-forward` to
  the Valkey, so that the key ring can be read; `new.yaml`, the destination `serve` file: `ports.adapter: dynamodb` with
  the table and region, the `ssm` secrets adapter with its root, `ports.blob` with the bucket.

## Before you start

- **What moves.** The State records (every key, value and lifetime; an expired record is not copied); the secrets the
  domain stores keep (workspace credentials, App keys, link token pairs, the console's session key), written to the
  Secrets port; the controllers' last reports, to S3; and the issuer's key ring schedule, so the old file key's public
  half stays published through its overlap and a token issued before the cutover keeps verifying.
- **What does not move.** The issuer's sessions, refresh tokens, codes in flight and Index sets: people sign in again,
  the one visible effect (`--with-sessions` is for a move that must keep them). And `issuer:kms:state-secret-fingerprint`:
  the new installation's secret differs, and a stale fingerprint would stop it from starting.
- **Preview before every apply, and read the preview.** The dry run (step 2) and the Pulumi preview of the destination
  are both read in full; a preview that deletes a KMS key, a table or a bucket is not what a cutover does.
- **The freeze is the Deployment at 0, held there.** The chart refuses `replicaCount: 0` (a schema minimum of 1), and a
  GitOps controller scales a Deployment back up. Under Argo CD, set `argocd.argoproj.io/skip-reconcile=true` on the
  Application, then `kubectl scale --replicas=0`. Not freezing is the failure that turns a copy into a mismatch.
- **The signing key.** The old file key's signer is not on Lambda, so the new KMS key signs only after
  `signingKey.activationDelay`; token requests fail closed until then. For the cutover's Lambda configuration set it to
  its minimum, equal to the poll interval (for example `30s`). Expect about 30 to 60 seconds with no new tokens after the
  switch.
- **A first smoke start of the new installation writes `console/session-key`.** The real run then needs `--overwrite`.
- **The Pulumi library's require pin is automatic.** Nobody bumps `deploy/pulumi/go.mod` by hand before a tag
  ([CONTRIBUTING](../../CONTRIBUTING.md), `hack/pin-pulumi-require.sh`); do not do it for a cutover either.
- **Deleting keys needs state surgery.** Leaving remote signing for KMS-wrapped signing drops the two old keys from the
  Pulumi state before the apply: `pulumi state delete` refuses while role policies depend on the keys, so export the
  state, remove the key resources and the dependency edges that name them, import it back, then apply; disable the old
  keys and schedule their deletion only after the token lifetime and the JWKS cache have passed. The sequence and its
  rollback are in [AWS Lambda](../how-to/upgrade/v1.62.md#6-retire-the-asymmetric-signing-keys-only-when-moving-from-kms-to-wrappedsigning). A protected resource first
  needs `pulumi state unprotect <urn>`.
- **On Lambda the directory snapshot refresh** is fixed in v1.61.1. On an older release the symptom is sign-in failing
  30 to 60 minutes after the switch with "the directory cannot be vouched for"; upgrade before the cutover.
- **Audit.** The audit writer refuses to start without the catalogue's `.json` schemas. They ship as the release asset
  `sluis-audit-catalogue_<version>.tar.gz`; a Lambda that carries only `roster.yaml` fails every cold start
  ([change the audit catalogue](change-the-audit-catalogue.md)).

## Steps

### 1. Prepare

**Run** write `new.yaml` and `old.yaml` as above.
**Expect** both load: `sluis migrate --version` and a dry run in step 2 parse them.
**Verify** the Pulumi preview of the destination, read in full.
**Rollback**: none, because nothing has changed.

### 2. Dry-run against the live installation

**Run**

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> --dry-run
```

**Expect** a summary on stderr counting items and bytes per concern, and what the destination would refuse.
**Verify** exit status 0. Anything refused (a record over 256 KiB, a credential over 8 KiB, an invalid key) must be fixed
or, for a dead record, deleted; dry-run again until it exits 0.
**Rollback**: none, because it writes nothing and needs no freeze.

### 3. Freeze (in the window)

**Run** pause the GitOps controller's reconciliation (above), then scale the one Deployment (issuer, console and
controllers) to 0 and wait until the pods are gone.
**Expect** sign-in is down from here until the switch.
**Verify** no pod of the release is left.
**Rollback**: scale back up and remove the pause.

### 4. Run

**Run**

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> \
  --i-have-stopped-writers
```

Add `--overwrite` only when the destination holds a value the source's must replace (the smoke start's session key).
**Expect** it is idempotent: after a failure the same command completes the copy.
**Verify** the next step.
**Rollback**: the legacy side was not written; scale it back up.

### 5. Verify counts

**Run** read the report, and run `cmd/acceptance` against the API Gateway URL.
**Expect** `ok: true`, `totals.verified` equal to `totals.source`, the `concerns` counts equal to the dry run's.
**Verify** acceptance passes.
**Rollback**: scale the old Deployment up.

### 6. Switch

**Run** point DNS at the new origin.
**Expect** people sign in again; a token issued before the cutover still verifies; the first new tokens come after the
activation delay.
**Verify** sign in, `accessctl login` or your own client, and the [health page](check-health.md).
**Rollback**: point DNS back and scale the old Deployment up. The legacy data does not have what the new installation
wrote since the switch; to carry it back, freeze the new writers and run `sluis migrate` the other way round with
`--overwrite` ([rollback](migrate-state.md#rollback)).

## Afterwards

- Leave the old Deployment at 0, and the ConfigMaps, Secrets and Valkey as they are, until the new installation has been
  healthy for as long as you want a rollback to stay cheap. An orphaned Application with `skip-reconcile` is still there;
  remove it when you retire the old one.
- Turn off the recovery password once a directory group grants operator ([recover on Lambda](recover-on-lambda.md)).
- Tell the people who use sign-in that they signed in again, and the owners of relying parties the activation-delay gap.
