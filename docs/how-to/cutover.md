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
- **Tokens the old issuer signed with a key that does not move.** `sluis migrate` carries a file key ring's schedule, so
  its public half stays published. A key it does not carry (an issuer whose keys stay where they are, a key ring the
  destination does not read) is published with `LambdaArgs.VerifyOnly` instead: the old public keys, each with the
  `kid` its tokens carry and an `Until` at least the switch plus the longest token lifetime plus a verifier's JWKS cache.
  The library puts them in the configuration layer and names them in `signingKey.verifyOnly`, as the chart does
  ([the Pulumi library](../reference/pulumi-library.md#inputs-lambdaargs)). Only public keys: a private one is refused.
  Remove them after `Until`; past it they are not published anyway.
- **Declare the schedules paused.** Until the switch the old installation's controllers are the ones that
  run. Deploy the destination with `Schedule.Paused` and `DirectoryRefresh.Paused` set: every
  schedule exists, disabled, with the scheduler's role and the function's grants, and
  turning them on in step 6 is one setting whose preview changes only each schedule's state.
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

**Run** point DNS at the new origin, then unset the schedules' `Paused` and apply.
**Expect** people sign in again; a token issued before the cutover still verifies; the first new tokens come after the
activation delay.
**Verify** sign in, `accessctl login` or your own client, and the [health page](check-health.md); the preview of the
schedules' apply changes their state and nothing else.
**Rollback**: set `Paused` again and apply, point DNS back and scale the old Deployment up. The legacy data does not have what the new installation
wrote since the switch; to carry it back, freeze the new writers and run `sluis migrate` the other way round with
`--overwrite` ([rollback](migrate-state.md#rollback)).

## Afterwards

- Leave the old Deployment at 0, and the ConfigMaps, Secrets and Valkey as they are, until the new installation has been
  healthy for as long as you want a rollback to stay cheap. An orphaned Application with `skip-reconcile` is still there;
  remove it when you retire the old one.
- Turn off the recovery password once a directory group grants operator ([recover on Lambda](recover-on-lambda.md)).
- Tell the people who use sign-in that they signed in again, and the owners of relying parties the activation-delay gap.

## Moving a stack from the core library's domain to the edge module

Before the edge modules, `NewLambda` built the custom domain, its mutual TLS and a truststore bucket of its own from
`API.DomainName`, `API.CertificateArn`, `API.TruststorePEM` and `API.TruststoreBucketName`. Those four are deprecated and
still work for one release (with a warning); the domain, the certificate and the truststore are now
[the edge module's](../reference/pulumi-library.md#the-edge-modules). A stack moves **without replacing the custom
domain**: the edge's domain and mapping are aliased to the ones the core created (under the Lambda component, by the
name `Lambda.FrontDoor()` carries), so Pulumi sees one resource moved and not one deleted and one created. The domain
name, certificate and `DomainTarget` do not change, so DNS is not touched and nothing is down.

**Preconditions.** The stack is on a release that has the edge module, with its four inputs set. You know the roles that
may write the truststore from now on: the role the stack is applied with (CD), the operators' admin role and a
break-glass role. An apply by any other principal, the functions' role included, is denied.

1. **Guard the blob bucket.** In the program, give the storage `Versioning: true` and
   `ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edgecloudflare.Guard(cdRole, adminRole, breakglassRole)}`.
   (Blobs on R2: skip this and give the edge a `TruststoreBucket` instead.) Versioning the blob bucket keeps old
   versions of every object it holds: set a lifecycle rule if that matters. `pulumi up` this alone first: the preview
   shows the bucket's versioning and its policy changing in place and nothing else. The deny does not touch what the
   functions already do under other prefixes.
2. **Add the edge and drop the four inputs.** Remove `API.DomainName`, `API.CertificateArn`, `API.TruststorePEM` and
   `API.TruststoreBucketName`; keep `API.KeepDefaultEndpoint` (it is the API's). Add `edgecloudflare.NewEdge` with the
   same domain, `CertificateArn`, `TruststorePEM` and `Storage`.
3. **Unprotect the old truststore bucket.** It was created with `Protect(true)`, so Pulumi refuses to plan the move
   until it is not. With the Lambda component's name `<l>` (for example `access`), read the URN from `pulumi stack
   --show-urns` and run:

   ```sh
   pulumi state unprotect 'urn:pulumi:<stack>::<project>::sluis:aws:Lambda$aws:s3/bucket:Bucket::<l>-truststore'
   ```

4. **Preview, and read it.** Expect: the new object `<edge>-truststore-pem` created in the blob bucket; the domain
   `<l>-domain` **updated in place** (`truststoreUri` and `truststoreVersion`), and the mapping unchanged; the old
   object, policy, public-access block, versioning, encryption configuration and bucket deleted. A **replace** of the
   domain or the mapping is wrong: stop (the alias did not match, which is a different Lambda component name or a
   different stack), and do not apply.
5. **Apply.** `pulumi up`. The old bucket still holds the old object's versions, so its deletion fails with
   `BucketNotEmpty`; everything else completes, and the domain is already on the new truststore. That one error is
   expected.
6. **Empty the old bucket and finish.** Delete every version and delete marker in it
   (`aws s3api list-object-versions` and `aws s3api delete-objects`, repeated until the listing is empty), then
   `pulumi up` again: it deletes the bucket. Check the domain
   (`aws apigatewayv2 get-domain-name --domain-name <domain>`: `MutualTlsAuthentication.TruststoreUri` names the blob
   bucket and `TruststoreVersion` the new object's version) and ask the issuer with a client certificate as in
   [the tutorial](../getting-started/aws-lambda.md#6-point-dns-at-it-and-ask-the-issuer).

To keep the old bucket and manage it by hand instead, `pulumi state delete --force` its six resources after step 3 (the
bucket, `-encryption`, `-versioning`, `-public-access`, `-policy` and the object `<l>-truststore-pem`) and skip step 6's
emptying; the domain moves in step 5 as before.

**Rollback.** Before step 5, revert the program. After it, restore the four inputs and apply: the library builds its
own truststore bucket again (the old bucket's name must be free, so do not delete it first) and the domain is updated
back in place.

## Moving a stack from library-created keys to supplied ones

A stack whose `NewLambda` created its own signing keys (`SigningKeyAlias`, `SigningKeyRS256Alias`, `WrappedSigning`) moves to
keys the estate supplies (`LambdaArgs.Keys`) without the next apply deleting a key. The aliases the library created keep
pointing at the same keys, so supplying those aliases adopts them.

1. **Decide the aliases.** `Keys.Sign` is a symmetric key's alias. To keep an existing symmetric key, use the alias of the
   wrapped-signing key (`alias/sluis-signing-wrapped` unless you changed it). The asymmetric keys of remote signing do not
   fit: the runtime wraps locally, so supply a symmetric key. The key policy must admit the function's role, and, for the
   older ring entries, carry `WrappedKeyPolicyStatements` while `LegacySigningContext` is true.
2. **Unprotect the keys.** They were created with `Protect(true)`. With the Lambda component's name `<l>` (read the URNs with
   `pulumi stack --show-urns`):

   ```sh
   pulumi state unprotect 'urn:pulumi:<stack>::<project>::sluis:aws:Lambda$aws:kms/key:Key::<l>-signing-key-wrapped'
   ```

   Repeat for each library-created key you keep (`-signing-key`, `-signing-key-rs256`, `-signing-key-wrapped`).
3. **Drop them from state without deleting them.** For each key and its alias resource (`<l>-signing-alias`,
   `-signing-alias-rs256`, `-signing-alias-wrapped`):

   ```sh
   pulumi state delete --target-dependents 'urn:…::aws:kms/alias:Alias::<l>-signing-alias-wrapped'
   pulumi state delete 'urn:…::aws:kms/key:Key::<l>-signing-key-wrapped'
   ```

   The key and alias stay in AWS. A role policy that depends on the key blocks the delete; add `--force` or follow the state
   surgery in [AWS Lambda](upgrade/v1.62.md#6-retire-the-asymmetric-signing-keys-only-when-moving-from-kms-to-wrappedsigning).
4. **Set `Keys`** and remove `WrappedSigning`, `SigningKeyAlias`, `SigningKeyRS256Alias` and `DisableSigningKeyRS256`.
5. **Preview, and read it.** Expect the role policy updated (the supplied-key grants), the configuration layer
   republished (`instance` and `keys:`), and **no key or alias deleted or created**. A `delete` of a `kms:Key` means a key
   is still in state: stop.
6. **Apply**, then once the ring has rotated past the entries written before the runtime wrapped locally, set
   `LegacySigningContext` to false.
