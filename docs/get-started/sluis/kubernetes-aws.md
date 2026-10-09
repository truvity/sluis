# Tutorial: sluis on Kubernetes with AWS storage

You run sluis as one pod on your cluster (preset `k8s-aws`). State is in DynamoDB, blobs in S3 and secrets in SSM or OpenBao. Tokens are signed under a KMS-wrapped key. `sluisctl render` writes the two documents the pod reads from an installation file. CI holds them to it with `--check`. The chart runs in documents mode: it mounts the documents unchanged and keeps only deployment-level values.

The full runbook with a gateway and the first sign-in is [Install with Helm](../../guides/sluis/operate/install-with-helm.md). Every chart value is in [chart values](../../reference/sluis/chart-values.md). Design: [ports](../../concepts/sluis/ports.md).

## What you need

Names below are placeholders; replace them.

| Item | Detail |
|---|---|
| Cluster | EKS, with `kubectl` and `helm` pointed at it. Pod Identity gives the pod its AWS role; `serviceAccount.awsIdentity: irsa` is the alternative. |
| AWS | Account `111122223333`, region `eu-central-1`, credentials that create DynamoDB, S3, KMS, IAM and SSM resources. |
| Tools | Go and `pulumi` for step 1, or the same resources by hand. |
| `sluisctl` | The release you install. Pin the chart and `sluisctl` at one version `X.Y.Z`; documents mode needs v1.64 or later. |

## 1. Create the AWS side

The pod needs a table, a bucket, a symmetric KMS key and a role. The Pulumi library declares the table, bucket and role. You create the key, and its policy reserves the signing context to the pod's role with statements the library returns. In a Pulumi Go project, add the library with `go get github.com/truvity/sluis/deploy/pulumi@vX.Y.Z`:

```go
store, _ := sluispulumi.NewStorage(ctx, "demo", &sluispulumi.StorageArgs{BucketName: "demo-sluis-111122223333"}, withAWS)
state, _ := sluispulumi.NewState(ctx, "demo", &sluispulumi.StateArgs{TableName: "demo-sluis"}, withAWS)

// The role is named <RoleNamePrefix>-sluis, and the prefix defaults to the component's name, "demo".
roleArn := "arn:aws:iam::111122223333:role/demo-sluis"
statements := append([]map[string]any{{
	"Sid": "EnableIAMPolicies", "Effect": "Allow",
	"Principal": map[string]any{"AWS": "arn:aws:iam::111122223333:root"},
	"Action":    "kms:*", "Resource": "*",
}}, sluispulumi.WrappedKeyPolicyStatements([]string{roleArn})...)
policy, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
key, _ := kms.NewKey(ctx, "demo-signing", &kms.KeyArgs{
	EnableKeyRotation: pulumi.Bool(true), Policy: pulumi.String(policy)}, withAWS)
kms.NewAlias(ctx, "demo-signing", &kms.AliasArgs{
	Name: pulumi.String("alias/demo-sluis-signing"), TargetKeyId: key.KeyId}, withAWS)

sluispulumi.NewKubernetesIdentity(ctx, "demo", &sluispulumi.KubernetesIdentityArgs{
	ClusterName: "demo", ClusterArn: "arn:aws:eks:eu-central-1:111122223333:cluster/demo",
	AccountID: "111122223333", Region: "eu-central-1",
	Namespace: "sluis", ServiceAccount: "sluis",
	Instance:             "demo", // the role's SSM grants under /sluis/demo
	Storage:              store.Grant(),
	State:                state.Grant(),
	WrappedSigningKeyArn: key.Arn,
}, withAWS)
```

```sh
pulumi preview    # a bucket, a table, a key and alias, a policy, a role and a pod identity association
pulumi up
```

Expect only creates. The role's grants and the key policy are in the [Pulumi library](../../reference/sluis/pulumi-library.md#kubernetes-identity).

## 2. Write the installation and render it

```yaml
# installation.yaml
apiVersion: sluis.truvity.github.io/installation/v1
instance: demo                    # the SSM root is /sluis/demo
shape: kubernetes
preset: k8s-aws
release: sluis                    # the Helm release name
cluster: demo

issuer:
  url: https://access.example.test
  secureCookies: true

aws:
  account: "111122223333"
  region: eu-central-1
  table: demo-sluis
  bucket: demo-sluis-111122223333

signingKey:
  kmsWrapped:
    keyId: alias/demo-sluis-signing

recovery: {enabled: true, serviceAccount: sluis-recovery, audience: sluis-recovery}
console: {client: access-console}

access:
  groups:
    all:access-roster:operator:
      members: [admin@example.test]
      matchers:
        - service_account: {namespace: sluis, name: sluis-recovery}   # how the first operator gets in
    all:access-roster:viewer:
      matchers: [{email_domain: example.test}]
  clients:
    access-console:
      kind: public
      redirects: [https://access.example.test/console/callback]
      requires: [all:access-roster:viewer]
```

```sh
sluisctl render --installation installation.yaml --out rendered
```

Expect no output, exit 0, and `rendered/sluis.yaml` and `rendered/policy.yaml`. In `sluis.yaml`, check `preset: k8s-aws`, the adapters `dynamodb`, `s3` and `ssm` with root `/sluis/demo`, and `signingKey.kmsWrapped`. An installation that disagrees with its shape is refused here, naming the key. Commit both documents.

## 3. Seed the secrets

The documents name secrets and hold none. With the SSM adapter each secret is a SecureString under the instance's root:

```sh
kubectl create namespace sluis
aws ssm put-parameter --type SecureString --name /sluis/demo/private/config/issuer/state-secret \
  --value "$(openssl rand -base64 32)"
aws ssm get-parameters-by-path --path /sluis/demo/private/config/ --recursive --query 'Parameters[].Name'
```

Expect a parameter version, then `["/sluis/demo/private/config/issuer/state-secret"]`. Print names only, never values. The state secret keys the sign-in state and must be the same in every replica.

### With OpenBao instead of SSM

An `openbao` block in the installation switches the secrets adapter to `openbao`:

```yaml
openbao:
  address: https://openbao.example.test   # https only
  namespace: demo
  root: sluis
  auth: {method: jwt, mount: jwt-demo, role: sluis, tokenFile: /var/run/openbao/token}
```

Set `exports.openbao.token.audience` to the audience in your role's `bound_audiences`. The chart projects the login token with it. The server's CA is the system's unless you set `exports.openbao.caBundle` (the PEM) and `caFile` in the block; the chart refuses values that disagree. The state secret goes under `<root>/private/config/issuer/state-secret` in that mount, not in SSM. The role's policy is in the [OpenBao secrets adapter](../../reference/sluis/openbao-secrets-adapter.md).

## 4. Install the chart in documents mode

`values.yaml` holds only deployment-level keys. Without `route.host` the chart renders no route, which suits a first run by port-forward. For a gateway, see [Install with Helm](../../guides/sluis/operate/install-with-helm.md).

```yaml
replicaCount: 2        # replicas coordinate through DynamoDB
serviceAccount:
  awsIdentity: pod-identity     # renders nothing: EKS Pod Identity associates the role (step 1)
```

Preview, read it, then install:

```sh
helm template sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis -f values.yaml \
  --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
helm install sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis -f values.yaml \
  --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

Expect a ServiceAccount, the `sluis-config` and `sluis-policy` ConfigMaps, one Deployment `sluis` and a Service. Set both documents or neither. Beside them the chart refuses `config`, `policy` and the exchange lists. It also refuses a document that disagrees with the release name, `policy.file` or the public URLs, and names the installation key to change. Helm cannot run the loader, so the service validates the rest at start, after the rollout.

## 5. Check it

```sh
kubectl -n sluis rollout status deploy/sluis
kubectl -n sluis port-forward svc/sluis 8080:8080 &
curl -s localhost:8080/.well-known/openid-configuration
```

Expect the rollout to finish and JSON whose `issuer` is `https://access.example.test`. If the pod does not start, `kubectl -n sluis logs deploy/sluis` names the key the loader refused or the missing parameter's path.

Then sign in on the console's login page with a recovery token ([first sign-in](../../guides/sluis/operate/install-with-helm.md#5-first-sign-in)):

```sh
kubectl -n sluis create token sluis-recovery --audience sluis-recovery --duration 10m
```

## 6. Hold the documents to the installation in CI

Generate the documents with `sluisctl render`, never by hand. Add one CI step:

```sh
sluisctl render --installation installation.yaml --out rendered --check
```

It writes nothing and exits 0 when the committed files match what the installation renders to. If a file differs or is missing, it prints a line diff, tells you to render again and exits 1.

To change the installation, review the diff of the installation and both documents. Then run `helm upgrade` with the same `--set-file` arguments. The pod rolls on a document change by checksum.

## Next

- A gateway and the first real sign-in: [Install with Helm](../../guides/sluis/operate/install-with-helm.md).

- A directory and each relying party: [how-to index](../../concepts/sluis/README.md).

- A release: [upgrade pages](../../guides/sluis/upgrade/v1.64.md) and the [zero-diff gate](../../guides/sluis/operate/install-with-helm.md#afterwards).

Values mode (`config`, `policy`, `exchange`) is deprecated and works for one more minor.
