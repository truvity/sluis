# Getting started with the services in the cluster

From nothing to an installation whose services all run in the cluster: NATS carries the stream, PostgreSQL holds the
deduplication table and the index, and credentials arrive as Kubernetes Secrets. Nothing reads SQS, DynamoDB or SSM
Parameter Store, and there is no Pulumi program and no Lambda. This is one of the two peer
[shapes](../deployment/README.md); the other is [AWS Lambda](aws-lambda.md).

If your bucket, key and workload identity are on AWS and you only want the chart, the shorter path is
[Kubernetes with AWS storage](kubernetes.md).

## What you need

The chart takes references to all of these and refuses to render when one is missing.

| you need | how |
|---|---|
| a NATS server the pods can reach | any cluster NATS with JetStream enabled; the example uses `nats://nats.nats.svc:4222` |
| a PostgreSQL database, an owner and a role for each part | [prepare the database](../how-to/prepare-the-database.md) |
| an S3 bucket and a prefix | AWS S3, or Cloudflare R2 (use AWS S3 where Object Lock may be needed: `attested` is AWS S3 only); [prepare the bucket](../how-to/prepare-the-bucket.md) |
| a key for the seals | an AWS KMS key, or OpenBao transit; <!-- TODO(coordinator): kms provider status from #428 --> see [key custody](../explanation/key-custody.md) |
| the secrets | Kubernetes Secrets named in the values, delivered by an operator such as External Secrets |
| a reference clock | an NTP address the pods can reach, for example `pool.ntp.org` (on AWS, `169.254.169.123` is the instance's own time service) |
| the images | `ghcr.io/truvity/audit/`, one tag that also stamps the chart |

## 1. Add the dependency

```yaml
# the application's Chart.yaml
dependencies:
  - name: audit
    version: <version>
    repository: oci://ghcr.io/truvity/charts
```

## 2. Write the values

The whole file is
[`charts/audit/examples/in-cluster-services.yaml`](../../../charts/audit/examples/in-cluster-services.yaml); the chart's
tests render it. Its shape:

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

Replace the hosts and Secret names with yours. On Cloudflare R2 instead of AWS S3, set the preset's `endpoint` and give
each pod that touches the archive the access key from a Secret; the example file has the variant. With OpenBao transit
instead of KMS, replace the notary's `keys` with a `signer.transit` block (also in the example).

## 3. Install

```sh
helm dependency build ./charts/<application>
helm upgrade --install <application> ./charts/<application> -n <app> -f values.yaml
```

## 4. Check that it works

1. `kubectl -n <app> rollout status deploy/<release>-audit` for the receiver and each writer; a 503 on `/readyz`
   names the failed check.
2. Perform an action the application's catalogue declares ([connect an application](../how-to/connect-an-application.md)).
3. List the archive with a generic S3 client, naming the endpoint when the store is not AWS S3:

   ```sh
   aws s3 ls s3://audit-eu-example-1/audit/ --recursive --endpoint-url https://ACCOUNT.r2.cloudflarestorage.com
   # or, with the MinIO client
   mc ls --recursive archive/audit-archive/audit/
   ```

   On AWS S3 the `--endpoint-url` is omitted.
4. Verify it: `audit verify --profile security --last 24h --bucket audit-eu-example-1 --prefix audit` checks the archive
   from the archive alone.

## Next

[Recover from an outage](../how-to/recover-from-an-outage.md), [verify the trail](../how-to/verify-the-trail.md),
[configure OpenBAO keys](../how-to/configure-openbao-keys.md).
