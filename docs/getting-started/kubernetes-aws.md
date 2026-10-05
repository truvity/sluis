# Tutorial: sluis on Kubernetes with AWS storage

By the end you have sluis running as one pod on your cluster (preset `k8s-aws`): state in DynamoDB, blobs in S3,
tokens signed under a KMS-wrapped key, secrets in SSM (or OpenBao). The two documents the pod reads are rendered from
an installation file by `sluisctl render`, and CI holds them to it with `--check`. The chart is in documents mode: it
mounts the documents unchanged and keeps only the deployment-level values.

Not here: the full runbook with a gateway and the first sign-in ([Install with Helm](../how-to/install-with-helm.md)),
every chart value ([configuration](../reference/chart-values.md)), why ([ports](../explanation/ports.md)).

## What you need

- A Kubernetes cluster on EKS, and `kubectl` and `helm` pointed at it. Pod Identity gives the pod its AWS role;
  `serviceAccount.awsIdentity: irsa` is the alternative.
- An AWS account (`111122223333`) and a region (`eu-central-1`), with credentials that can create DynamoDB, S3, KMS,
  IAM and SSM resources.
- Go and `pulumi` for the AWS side below, or the equivalent resources by hand.
- `sluisctl` of the release you install. Pin the chart and `sluisctl` at the same version (`X.Y.Z`; documents mode
  needs v1.64 or later).

Names below are placeholders; replace them.

## 1. Create the AWS side

The pod needs a table, a bucket, a symmetric KMS key to wrap its signing keys under, and a role. The Pulumi library
declares the first, second and fourth; the key you create, because its policy must reserve the signing context to the
pod's role (the library returns the statements). Use the library at the release's tag
(`go get github.com/truvity/sluis/deploy/pulumi@vX.Y.Z`), in a Pulumi Go project:

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
pulumi preview    # read it: a bucket, a table, a key and alias, a policy, a role and a pod identity association
pulumi up
```

Expect the creates and nothing else. The role's grants and the key policy are in the
[Pulumi library](../reference/pulumi-library.md#kubernetes-identity); sealing is retired, so there is no sealer key.

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

Expect no output, exit 0, and `rendered/sluis.yaml` and `rendered/policy.yaml`. In `sluis.yaml` check `preset: k8s-aws`,
the three adapters (`dynamodb`, `s3`, and `ssm` with root `/sluis/demo`) and `signingKey.kmsWrapped`. An installation
that disagrees with its shape is refused here, naming the key. Commit both documents.

## 3. Seed the secrets

A document names its secrets and holds none. With the SSM adapter each is a SecureString under the instance's root:

```sh
kubectl create namespace sluis
aws ssm put-parameter --type SecureString --name /sluis/demo/private/config/issuer/state-secret \
  --value "$(openssl rand -base64 32)"
aws ssm get-parameters-by-path --path /sluis/demo/private/config/ --recursive --query 'Parameters[].Name'
```

Expect a parameter version, then `["/sluis/demo/private/config/issuer/state-secret"]`. Print names only, never values.
The state secret is the sign-in state's key: it must be the same in every replica.

### With OpenBao instead of SSM

Add an `openbao` block to the installation, and the preset's secrets adapter becomes `openbao`:

```yaml
openbao:
  address: https://openbao.example.test   # https only
  namespace: demo
  root: sluis
  auth: {method: jwt, mount: jwt-demo, role: sluis, tokenFile: /var/run/openbao/token}
```

The chart projects the token the login presents from values: `exports.openbao.token.audience` is the audience your
role's `bound_audiences` names. The server's CA is the system's unless you give one: `exports.openbao.caBundle` (the
PEM) and `caFile` in the block, which the chart refuses to disagree about. The state secret is then written
under `<root>/private/config/issuer/state-secret` in that mount, not in SSM. The policy the role needs is in
[configuration](../reference/openbao-secrets-adapter.md).

## 4. Install the chart in documents mode

`values.yaml` holds only deployment-level keys. Without `route.host` no route is rendered, which suits a first run by
port-forward; a gateway is in [Install with Helm](../how-to/install-with-helm.md).

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

Expect a ServiceAccount, the `sluis-config` and `sluis-policy` ConfigMaps, one Deployment `sluis` and a Service. Set
both documents or neither: beside them the chart refuses `config`, `policy` and the exchange lists, so nothing is said
twice. It also refuses a document that disagrees with what it mounts (the release name, `policy.file`, the public URLs)
and names the key to change in the installation. Helm cannot run sluis's loader, so the service validates the rest at
start, after the rollout.

## 5. Check it

```sh
kubectl -n sluis rollout status deploy/sluis
kubectl -n sluis port-forward svc/sluis 8080:8080 &
curl -s localhost:8080/.well-known/openid-configuration
```

Expect the rollout to finish (Ready means the whole process finished starting) and JSON whose `issuer` is
`https://access.example.test`. If the pod does not start, `kubectl -n sluis logs deploy/sluis` names the key the loader
refused, or the missing parameter's path (never its value).

Then sign in with a recovery token, `kubectl -n sluis create token sluis-recovery --audience sluis-recovery
--duration 10m`, on the console's login page ([first sign-in](../how-to/install-with-helm.md#5-first-sign-in)).

## 6. Hold the documents to the installation in CI

The documents are trusted as they are, so they must come from `sluisctl render`, never by hand. Add one CI step:

```sh
sluisctl render --installation installation.yaml --out rendered --check
```

It writes nothing and exits 0 when the committed files are what the installation renders to. Edit a line of
`rendered/sluis.yaml` and run it again to see the other case: a line diff, then a message telling you to render again,
and exit 1 (a missing file counts as a difference). A change to the installation is therefore a reviewed diff of the
installation and of both documents, then `helm upgrade` with the same `--set-file` arguments; the pod rolls on a
document change by checksum.

## You now have

- one Deployment `sluis` whose documents are exactly what `sluisctl render` writes for your installation;
- DynamoDB state shared by the replicas, S3 blobs, tokens signed under a KMS-wrapped key, and secrets in SSM (or
  OpenBao);
- a CI check that fails when a committed document and its installation drift apart.

Next: a gateway and the first real sign-in ([Install with Helm](../how-to/install-with-helm.md)), connecting a
directory and each relying party ([how-to index](../index.md)), taking a release
([upgrade pages](../how-to/upgrade/v1.64.md), including the zero-diff gate in
[Install with Helm](../how-to/install-with-helm.md#afterwards)). The values-mode (`config`, `policy`, `exchange`) is
deprecated and works for one more minor.
