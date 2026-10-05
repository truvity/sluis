# Install sluis with Helm

## Purpose

Install the `sluis` chart on Kubernetes with plain `helm install`, sign in for the first time, and take later releases
without ArgoCD or Kargo. Every name below is a placeholder: `example.com` for your domain, `eu-west-1` for your region.

The chart is one OCI chart, `oci://ghcr.io/truvity/charts/sluis`, and runs one image, `ghcr.io/truvity/sluis/sluis`, as
one Deployment (`sluis serve`; the GitHub and Slack controllers run inside it,
[ADR 0037](../decisions/0037-one-process-everywhere.md)). It is configured by two **rendered documents**, the service
document and the policy document, which `sluisctl render` writes from an installation
([ADR 0038](../decisions/0038-estates-render-through-sluis.md)). Moving from the `access-issuer` chart is
[its own page](migrate-from-the-access-issuer-chart.md). The `access-proxy` chart was removed in v1.32.0
([ADR 0003](../decisions/0003-deprecate-access-proxy.md)).

## Preconditions

| Needed | For | Notes |
|---|---|---|
| Kubernetes | the pod | recovery sign-in asks the API server to review a ServiceAccount token, so sluis runs in the cluster it trusts |
| AWS: a DynamoDB table, an S3 bucket, a KMS key | state, blobs, signing | the `k8s-aws` preset is the one Kubernetes preset that is built. `k8s-minimal` and `k8s-openbao` are unavailable (loading one fails naming the missing adapters): see [adapters](../reference/adapters.md) |
| a pod identity for the AWS role | the above | EKS Pod Identity (renders nothing) or `serviceAccount.awsIdentity: irsa` with `awsRoleArn`; the chart carries no credential |
| SSM parameters, or OpenBao, for the secrets | the OAuth client, the state secret, each confidential client's secret | seeded under `/sluis/<instance>/private/config/`, layout in [configuration](../reference/secrets.md#ssm-layout-v3); with an `openbao` or `ssm` adapter the chart projects no Kubernetes Secret for them |
| Gateway API and a controller serving a `GatewayClass` | the route | the chart renders a `Gateway` (or attaches to `route.parentRefs`) and two `HTTPRoute`s. Without `route.host` it renders no route, for a trial by port-forward |
| cert-manager and a `ClusterIssuer` | the TLS certificate of the Gateway (`route.certificate`) | the signing key is KMS here, so no signing Certificate is rendered |
| a Google Workspace and an OAuth client | people signing in, and the directory | [Google Workspace](connect/google-workspace.md) walks through the client |
| `sluisctl`, `helm`, `kubectl` | rendering and installing | |
| an audit installation ([truvity/audit](https://github.com/truvity/audit)) | the audit trail (optional) | without `audit.writer` nothing is kept beyond the log line each record also is, and the service says so at start |

Replicas above one need `adapters.state` `dynamodb`, so the tick leases are shared; the chart refuses to render
otherwise ([high availability](high-availability.md)). Valkey is optional (the directory cache); see
[configuration](provide-a-valkey.md).

## Before you start

- **Documents mode is the way; values mode is deprecated.** Setting `config` and `policy` as values still works for one
  minor, then goes. `NOTES.txt` says so while it is used. Rendered documents are trusted: the chart does not re-validate
  them, so produce them with `sluisctl render` and never edit them by hand. Looks like: a `helm template` failure naming
  a key to change in the installation, or a service that refuses to start after the rollout.
- **Preview before every apply, and read the preview.** `helm template` and `diff` (step 4 and
  [Afterwards](#afterwards)); a policy that fails to load fails the rollout, not a sign-in, and the old pods keep serving.
- **The release's full name is part of the documents.** The Roles, ConfigMaps and Secrets are named from it. If
  `release` in the installation differs from the Helm release name, the chart fails the render naming both.
- **The Secrets must exist before the pod starts.** A name that is not delivered stops the start and names where it was
  looked for.
- **The audit catalogue needs its schemas.** If you run the audit installation, its writer refuses to start without the
  `.json` schemas; they ship as the release asset `sluis-audit-catalogue_<version>.tar.gz`.
- **Nobody bumps `deploy/pulumi/go.mod` by hand before a tag**: the Pulumi library require is pinned by the release
  (`hack/pin-pulumi-require.sh`, [CONTRIBUTING.md](../../CONTRIBUTING.md)). This matters only to estates that also
  use the Pulumi library.
- **Deleting a key from a document needs state surgery** when it is a persisted setting (a signing key ring, an adapter
  swap): moving state is [migrate state](migrate-state.md), not an edit.

## Steps

### 1. Write the installation and render it

**Run**

```yaml
# installation.yaml
apiVersion: sluis.truvity.github.io/installation/v1
instance: example                 # the SSM root is /sluis/example
shape: kubernetes
preset: k8s-aws
release: sluis                    # the Helm release name
issuer:
  url: https://access.example.com # stable for the life of the installation
aws: {region: eu-west-1, table: sluis, bucket: sluis-blobs}
signingKey:
  kmsWrapped: {keyId: alias/sluis-signing}
recovery: {enabled: true, serviceAccount: sluis-recovery, audience: sluis-recovery}
console: {client: access-console}
oauthClient: {provider: default}
access:
  groups:
    all:access-roster:operator:
      members: [platform-admins@example.com]
      matchers:
        - service_account: {namespace: sluis, name: sluis-recovery}   # how the first operator gets in
    all:access-roster:viewer:
      matchers: [{email_domain: example.com}]
  clients:
    access-console:
      kind: public
      display_name: sluis
      redirects: [https://access.example.com/console/]
      requires: [all:access-roster:operator, all:access-roster:viewer]
```

```sh
sluisctl render --installation installation.yaml --out rendered/
```

**Expect** `rendered/sluis.yaml` (`sluis.truvity.github.io/sluis/v3`) and `rendered/policy.yaml` (`.../policy/v2`), exit 0.
The installation's keys are in [configuration](../reference/installation-document.md), the
policy's in [policy](../reference/policy.md).

**Verify** commit both documents, and run `sluisctl render --installation installation.yaml --out rendered/ --check` in
CI: it exits 1 with a diff when a committed document is not what the installation renders to.

**Rollback** none, because nothing is applied yet; discard the files.

### 2. Create the namespace and seed the secrets

**Run**

```sh
kubectl create namespace sluis
aws ssm put-parameter --type SecureString --name /sluis/example/private/config/providers/google/default/client-id     --value <google client id>
aws ssm put-parameter --type SecureString --name /sluis/example/private/config/providers/google/default/client-secret --value <google client secret>
aws ssm put-parameter --type SecureString --name /sluis/example/private/config/issuer/state-secret --value "$(openssl rand -base64 32)"
# one per confidential client in the policy
aws ssm put-parameter --type SecureString --name /sluis/example/private/config/clients/<client-id>/secret --value <random>
```

**Expect** each call prints a parameter version.

**Verify** `aws ssm get-parameters-by-path --path /sluis/example/private/config/ --recursive --query 'Parameters[].Name'`
lists the four names (names only; never print values).

**Rollback** `aws ssm delete-parameter --name <name>`; nothing reads them yet.

### 3. Write the deployment values

**Run** a `values.yaml` with only deployment-level keys:

```yaml
route:
  host: access.example.com
  rootRedirect: /console/
  gatewayClassName: example-gateway-class   # a GatewayClass your controller serves
  certificate: {issuerName: example-ca, issuerKind: ClusterIssuer}
serviceAccount:
  awsIdentity: pod-identity                  # or irsa, with awsRoleArn
replicaCount: 2
```

**Expect** nothing yet; `config`, `policy` and the exchange lists are refused beside the documents, so nothing is said
twice. Everything else is in the chart's [README](../../charts/sluis/README.md) and `values.schema.json` (strict: an
unknown key fails the render).

**Verify** done in step 4.

**Rollback** none, because it is a local file.

### 4. Preview, then install

**Run**

```sh
helm template sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis \
  -f values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

Read the output, then the same arguments with `helm install ... --namespace sluis`. One tag releases every artifact;
pin the chart at one version.

**Expect** a ServiceAccount, the `sluis-config` and `sluis-policy` ConfigMaps, one Deployment `sluis`, a Service, a
Gateway, two HTTPRoutes, a Certificate. The release reaches Ready: the readiness probe on `/readyz` means the whole
process, controllers included, finished starting.

**Verify** `kubectl -n sluis rollout status deploy/sluis`, then `curl -s https://access.example.com/.well-known/openid-configuration`
returns the issuer's URL.

**Rollback** `helm uninstall sluis -n sluis` removes the objects; the SSM parameters and the DynamoDB state stay (state
holds the directory connections and keys: delete them only on purpose).

### 5. First sign-in

**Run**

```sh
kubectl -n sluis create token sluis-recovery --audience sluis-recovery --duration 10m
```

Open `https://access.example.com/login`, expand **Recovery sign-in** and paste the token. The console's Overview lists
what is left: connect the Google Workspace by admin consent (the OAuth client needs the redirect URIs
`https://access.example.com/connect/google/callback` and `https://access.example.com/login/google/callback`), and
check that your directory group lands you in the operators group. Who may mint a token is the cluster's RBAC on
`serviceaccounts/token` for that one account ([day two](day-one.md)).

**Expect** the Overview page, with this installation's own values to copy.

**Verify** sign out, sign in as yourself, search for yourself: your page shows the membership that granted each group.

**Rollback** none, because a token is short-lived; set `recovery.enabled: false` in the installation once the
directory is connected, render and upgrade.

Controllers: `controllers.github` and `controllers.slack` in the installation turn them on; an organisation or workspace
is a dry run until listed in `enabledOrgs` / `enabledWorkspaces` ([day two](enable-slack-workspace.md)).
Then connect what you need: a [cluster](connect/kubernetes-cluster.md), an [AWS account](connect/aws-account.md),
[GitHub Actions](connect/github-actions.md), a [laptop](../reference/sluisctl.md). A console with no OpenID flow of its
own uses the gateway's OIDC ([console app](connect/console-app.md)); on any other gateway run upstream oauth2-proxy
([recipe](connect/oauth2-proxy.md)).

## Afterwards

Nothing here needs a GitOps controller. ArgoCD and Kargo appear in these docs as **relying parties** ([ArgoCD](connect/argocd.md),
[Kargo](connect/kargo.md)). Taking a release by hand:

- Keep the installation, the rendered documents and `values.yaml` in your own repository. A change of who may do what is a
  reviewed change to the installation, then `sluisctl render`, then `helm upgrade`; the pod rolls on a document change by
  checksum.
- Take a release through the zero-diff gate: render the pinned version and the new one with your values and compare.

  ```sh
  for v in OLD NEW; do helm template sluis oci://ghcr.io/truvity/charts/sluis --version $v --namespace sluis \
    -f values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml > $v.yaml; done
  diff -u OLD.yaml NEW.yaml
  ```

  An image tag and the version labels move on every release; anything else is a line of
  [CHANGELOG.md](../../CHANGELOG.md). Read the release's page under `docs/how-to/upgrade/` first, then `helm upgrade`.
- Back up what the console adds: the Secrets and the state that cannot be minted again
  ([restoring from the Secrets alone](back-up-and-restore.md),
  [Slack state](back-up-and-restore.md#1-know-what-there-is)). On DynamoDB, turn on the table's point-in-time recovery.
- Moving from another identity provider is [migrate from an IdP](migrate-from-an-idp.md); from the `access-issuer` chart, [its migration](migrate-from-the-access-issuer-chart.md).
- Tell the people who use the consoles when the issuer URL or a client's redirects change.
