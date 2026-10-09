# Run the chart in stream mode

Switch an installation to the stream shape (receivers in front, JetStream, N writers behind), or install it that way.

## Before you start

- Start from the [Kubernetes tutorial](../../../get-started/audit/kubernetes.md) or have its prerequisites. You also need a NATS account with JetStream and a broker policy. See [stream mode](../../../concepts/audit/stream-mode.md).

- `stream.ackWait` must exceed `roll.interval` plus the longest put. The writer refuses to start otherwise, or the day's objects double.

- `writer.config.replicas` must equal `writer.consumers`, and more than one writer needs a `database`. The chart refuses a mismatch, a missing database, and a receiver and writer that share a ServiceAccount name.

- Keep the stream policy `DiscardNew`: a full stream refuses a publish instead of dropping evidence. Size it for your longest writer outage.

- `extensions.billing` and `extensions.quotas` render nothing for now.

## Steps

1. Set the mode and the two config blocks. The receiver is `audit-writer` with `mode: receiver` and holds neither the archive nor a key. The full file is [`charts/audit/examples/stream.yaml`](../../../../charts/audit/examples/stream.yaml). The stream parts are:

   ```yaml
   audit:
     mode: stream
     replicas: 2                       # receivers
     receiver:
       config:
         apiVersion: audit.truvity.github.io/audit-writer/v2
         secrets: {source: file, root: /etc/audit/secrets}
         mode: receiver
         deployment: /etc/audit/deployment.yaml
         workloads: /etc/audit/workloads.yaml
         database:
           url: postgres://audit_writer@db.example.com:5432/audit?sslmode=verify-full
           passwordSecret: database-password
         stream:
           nats:
             url: nats://nats.app.svc:4222
             tokenFile: /var/run/audit/stream/token   # the broker verifies who connects
           name: AUDIT
       secretFiles:
         - {name: database-password, secretName: audit-db, key: password}
       tokens:
         - {audience: nats, mountPath: /var/run/audit/stream}
     writer:
       consumers: 3
       config:
         replicas: 3                     # must equal writer.consumers
         stream:
           nats: {url: nats://nats.app.svc:4222, tokenFile: /var/run/audit/stream/token}
           name: AUDIT
           consumer: audit-writer
           batch: 100
           ackWait: 2m
         roll: {interval: 30s, maxRecords: 5000}
         # archive, database, apiVersion, secrets: as in the example file
       tokens:
         - {audience: nats, mountPath: /var/run/audit/stream}
   ```

   `helm template` renders a receiver and `writer.consumers` writers. It refuses `mode: stream` without `receiver.config` or `writer.config.stream`.

2. Authenticate to the stream with the projected token, not a stored credential. Give the receiver and writers a `tokens` entry of the broker's audience (`nats`) and name the file in `stream.nats.tokenFile`. The file is read afresh on every connect. The broker needs an auth callout that reviews the token with a `TokenReview`. Each pod reconnects thirty seconds before its token expires, and a publish in flight is resent under the same message id. A token refused at start stops the pod.

3. Point the query service at the grants that name your console's issuer. See [who the query service trusts](../../../concepts/audit/stream-mode.md#who-the-query-service-trusts).

4. Scale receivers with the request rate and writers with the record rate.

## Verify

```console
$ audit conformance --query https://audit-query.app.svc:8080 \
      --profile security --token-file ./token
$ audit verify --profile security --last 24h \
      --bucket audit-eu-example-1 --prefix audit/app
```

Pods connect and the receiver's `/readyz` answers. The consumer's pending count must not grow. A writer rollout raises it briefly. A count that never returns to zero means the writers cannot keep up or write: see [recover from an outage](recover-from-an-outage.md).

## Roll back

Set `mode: direct`. The switch changes the receiver's configuration, not any record. Remove `tokenFile` and the `tokens` entry on a broker that verifies nobody.
