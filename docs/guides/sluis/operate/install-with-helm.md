# Install sluis with Helm

Install `oci://ghcr.io/truvity/charts/sluis` and sign in. One Deployment runs, configured by two documents from `sluisctl render`. To move from the previous chart, see [its migration](../migrate/migrate-from-the-access-issuer-chart.md).

## Before you start

| Needed | For | Notes |
|---|---|---|
| Kubernetes | the pod | recovery sign-in asks this cluster's API server to review a ServiceAccount token |
| AWS: DynamoDB table, S3 bucket, KMS key | state, blobs, signing | preset `k8s-aws`; `k8s-minimal` and `k8s-openbao` are unavailable ([adapters](../../../reference/sluis/adapters.md)) |
| a pod identity for the AWS role | the above | EKS Pod Identity, or `serviceAccount.awsIdentity: irsa` with `awsRoleArn` |
| SSM parameters or OpenBao | OAuth client, state secret, client secrets | layout in [secrets](../../../reference/sluis/secrets.md#ssm-layout) |
| Gateway API and a `GatewayClass` controller | the route | without `route.host` no route renders: port-forward for a trial |
| cert-manager and a `ClusterIssuer` | the Gateway's TLS certificate | KMS signing renders no signing Certificate |
| a Google Workspace and an OAuth client | sign-in and the directory | [Google Workspace](../connect/google-workspace.md) |
| `sluisctl`, `helm`, `kubectl` | rendering and installing | |
| an [audit installation](../../../concepts/audit/README.md) | the audit trail, optional | without `audit.writer` records stay in the log line |

- Never edit the rendered documents: the chart does not re-validate them.

- `release` in the installation must equal the Helm release name, or the render fails.

- Seed every Secret before the pod starts. A missing name stops the start.

## 1. Write the installation and render it

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
    all:sluis:operator:
      members: [platform-admins@example.com]
      matchers:
        - service_account: {namespace: sluis, name: sluis-recovery}   # how the first operator gets in
    all:sluis:viewer:
      matchers: [{email_domain: example.com}]
  clients:
    access-console:
      kind: public
      display_name: sluis
      redirects: [https://access.example.com/console/]
      requires: [all:sluis:operator, all:sluis:viewer]
```

```sh
sluisctl render --installation installation.yaml --out rendered/
```

The render writes `rendered/sluis.yaml` and `rendered/policy.yaml` ([installation keys](../../../reference/sluis/installation-document.md), [policy keys](../../../reference/sluis/policy.md)). Commit both and run `--check` in CI: it exits 1 with a diff when one is stale.

## 2. Create the namespace and seed the secrets

```sh
kubectl create namespace sluis
aws ssm put-parameter --type SecureString --name /sluis/example/internal/config/providers/google/default/client-id     --value <google client id>
aws ssm put-parameter --type SecureString --name /sluis/example/internal/config/providers/google/default/client-secret --value <google client secret>
aws ssm put-parameter --type SecureString --name /sluis/example/internal/config/issuer/state-secret --value "$(openssl rand -base64 32)"
# one per confidential client in the policy
aws ssm put-parameter --type SecureString --name /sluis/example/internal/config/clients/<client-id>/secret --value <random>
```

List the names, never the values, with `aws ssm get-parameters-by-path --path /sluis/example/internal/config/ --recursive --query 'Parameters[].Name'`.

## 3. Write the deployment values

Put only deployment-level keys in `values.yaml`. The chart refuses `config`, `policy` and the exchange lists beside the documents. The [chart README](../../../../charts/sluis/README.md) lists the keys.

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

## 4. Preview, then install

```sh
helm template sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis \
  -f values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

Read the output, then run the same arguments with `helm install`. Check `kubectl -n sluis rollout status deploy/sluis` and that `curl -s https://access.example.com/.well-known/openid-configuration` returns the issuer's URL. To roll back, run `helm uninstall sluis -n sluis`. The SSM parameters and DynamoDB state stay.

## 5. First sign-in

```sh
kubectl -n sluis create token sluis-recovery --audience sluis-recovery --duration 10m
```

Open `https://access.example.com/login`, expand **Recovery sign-in** and paste the token. Finish as in [day one](day-one.md). The OAuth client needs the redirect URIs `https://access.example.com/connect/google/callback` and `https://access.example.com/login/google/callback`.

Once the directory works, set `recovery.enabled: false`, render and upgrade. To act in GitHub or Slack, set `controllers.github` and `controllers.slack`, then list targets in `enabledOrgs` or `enabledWorkspaces`. Connect a [cluster](../connect/kubernetes-cluster.md) next.

## Afterwards

Keep the installation, rendered documents and `values.yaml` in your repository. To take a release, render and compare the old and new chart. Only the image tag and version labels should move: read other changes in the [CHANGELOG](../../../../CHANGELOG.md). Then `helm upgrade`.

  ```sh
  for v in OLD NEW; do helm template sluis oci://ghcr.io/truvity/charts/sluis --version $v --namespace sluis \
    -f values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml > $v.yaml; done
  diff -u OLD.yaml NEW.yaml
  ```

Back up what the console adds ([back up and restore](back-up-and-restore.md#1-know-what-there-is)) and enable point-in-time recovery on the DynamoDB table.

## Decided in

[ADR 0037](../../../decisions/0037-one-process-everywhere.md), [ADR 0038](../../../decisions/0038-estates-render-through-sluis.md).
