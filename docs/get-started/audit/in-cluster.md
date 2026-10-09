# Getting started with the services in the cluster

Install audit with every service in the cluster: NATS carries the stream, PostgreSQL holds the deduplication table and the index,
and credentials arrive as Kubernetes Secrets. There is no Pulumi program and no Lambda. To keep the bucket, key and identity on AWS and only run the chart, use [Kubernetes with AWS storage](kubernetes.md).
The other shape is [AWS Lambda](aws-lambda.md).

## What you need

The chart takes references to all of these and refuses to render when one is missing.

| You need | How |
|---|---|
| NATS the pods can reach | JetStream enabled; the example uses `nats://nats.nats.svc:4222` |
| PostgreSQL, an owner and a role per part | [prepare the database](../../guides/audit/operate/prepare-the-database.md) |
| An S3 bucket and a prefix | AWS S3 or Cloudflare R2; `attested` needs AWS S3; [prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md) |
| A key for the seals | AWS KMS or OpenBao transit ([key custody](../../concepts/audit/key-custody.md)); pseudonymising under KMS also needs `keys.conceal`, a symmetric KMS key |
| The secrets | Kubernetes Secrets named in the values, delivered by an operator such as External Secrets |
| A reference clock | an NTP address, for example `pool.ntp.org`, or `169.254.169.123` on AWS |
| The images | `ghcr.io/truvity/audit/`, one tag that also stamps the chart |

## 1. Add the dependency

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: <version>
    repository: oci://ghcr.io/truvity/charts
```

## 2. Write the values

The chart's tests render [`charts/audit/examples/in-cluster-services.yaml`](../../../charts/audit/examples/in-cluster-services.yaml). Its shape:

```yaml
audit:
  mode: stream
  replicas: 2
  presets:
    standard: {bucket: audit-eu-example-1, region: eu-example-1, prefix: audit/}
  receiver:
    config:
      mode: receiver
      stream: {nats: {url: "nats://nats.nats.svc:4222"}, name: AUDIT}
  writer:
    consumers: 3
  jobs:
    notary:
      enabled: true
      config:
        keys: {adapter: kms, seal: {key: alias/audit-seal}}
    clockSync:
      enabled: true
      config:
        ntp: ["pool.ntp.org"]
```

Replace the hosts and Secret names. For R2, set the preset's `endpoint` and give each pod that touches the archive its access key from a Secret.
For OpenBao transit, replace the notary's `keys` with a `signer.transit` block. The example file shows both.

## 3. Install

```sh
helm dependency build ./charts/<application>
helm upgrade --install <application> ./charts/<application> -n <app> -f values.yaml
```

## 4. Check it works

1. Run `kubectl -n <app> rollout status deploy/<release>-audit`. A 503 on `/readyz` names the failed check.
2. Perform an action the catalogue declares ([connect an application](../../guides/audit/connect/connect-an-application.md)).
3. List the archive. Omit `--endpoint-url` on AWS S3:

   ```sh
      aws s3 ls s3://audit-eu-example-1/audit/ --recursive --endpoint-url https://ACCOUNT.r2.cloudflarestorage.com
      # or, with the MinIO client
      mc ls --recursive archive/audit-archive/audit/
      ```

4. Run `audit verify --profile security --last 24h --bucket audit-eu-example-1 --prefix audit`.

## Next

[Recover from an outage](../../guides/audit/operate/recover-from-an-outage.md), [verify the trail](../../guides/audit/operate/verify-the-trail.md),
[configure OpenBAO keys](../../guides/audit/operate/configure-openbao-keys.md).
