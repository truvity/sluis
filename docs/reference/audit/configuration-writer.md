# Configuration: the writer

The keys of `audit-writer` (receiver and writer), the durability knobs, the writer Lambda's file and the workload identity file. Shared blocks (`database`, `bucket`, `archive`, `sink`, `openbao`, `keys`) are in [configuration](configuration.md#shared-blocks).

## audit-writer

One binary, two roles chosen by `mode`.

| Mode | Behaviour |
|---|---|
| `writer` (default) | Serves the sink, writes the archive, consumes a stream when `stream` is set |
| `receiver` | Serves the sink and publishes to the stream. Holds no archive and no key provider |
| Direct mode | One process in `writer` mode does both |
| Stream mode | The receiver acknowledges the replicated JetStream publish; the same image runs again as the writer |
| Both roles | Serve `RegistryService`, where the application registers its catalogue |

<!-- generated: config-audit-writer -->
| key | type | default | meaning |
|---|---|---|---|
| `mode` | `writer` or `receiver` | `writer` | the role above |
| `listen` | `listen` | `:8080` | the address the sink is served on |
| `deployment` | path, required | | the profile configuration |
| `workloads` | path | | the file naming the issuers trusted to say which workload is publishing ([Workload identity](#workload-identity)). Exactly one of `workloads` and `anonymousWrites` |
| `anonymousWrites` | `true` | | accept writes from callers nobody verified, stamped with no observer. For a trial install only |
| `catalogues` | path | none | a directory of catalogues registered at start-up. An application that registers its own over `RegisterCatalogue` needs none. Not in a receiver |
| `archive` | `archive` | | required in `writer` mode, refused in `receiver` mode |
| `database` | `database` | none | the shared deduplication table and the catalogue registry, in the application's Postgres, as the writer's **own** role (`audit migrate --writer`): it holds those tables and none of the index, which `audit-observe` writes. Without it the writer deduplicates in process and may run one replica only. The writer refuses to start on a schema version it does not know and never migrates itself |
| `replicas` | integer, at least 1 | 1 | how many writers share this stream: the number of writer pods, which the writer cannot see for itself |
| `keys` | `keys` | none, which is provider `none` | pseudonymisation keys. Not in a receiver |
| `forgetIdentities` | boolean | false | do not keep the identity behind each pseudonym, sealed under its key. By default it is kept, so that resolve can find it |
| `require` | `logged`, `queued` or `archived` | `archived` for a writer, `queued` for a receiver | the weakest durability the chain may give ([0059](../../decisions/0059-sink-durability-and-transports.md)). At start-up the process wraps its chain in `sink.Guard` and refuses to run if the chain can never give it; a write acknowledged weaker fails. See [Durability](#durability-require-forward-consume) |
| `forward.nats`, `.sqs`, `.log` | exactly one; `nats` and `sqs` as below | | a receiver's onward transport. Required in `receiver` mode (or `stream`, its NATS shorthand); refused in `writer` mode. `forward.nats` takes the keys of `stream` |
| `forward.sqs.queueUrl` | string, required with `forward.sqs` | | the queue the receiver publishes to |
| `forward.sqs.region` | string | the SDK's (`AWS_REGION`) | the queue's region |
| `forward.sqs.fifo` | boolean | derived from the URL | the queue is FIFO. It must agree with the URL, which ends in `.fifo` for one |
| `forward.log` | `{}` | | the `log` sink: one JSON line per record on standard output, which survives nothing but the log pipeline. Allowed only with `require: logged` |
| `consume.nats`, `.sqs` | exactly one | | what a writer reads its records from, beside its own sink. Not in a receiver. `consume.nats` takes the keys of `stream` |
| `consume.sqs.queueUrl`, `.region`, `.fifo` | as `forward.sqs` | | the queue the writer consumes |
| `consume.sqs.batch` | integer, 1 to 10 | 10 | how many messages are received at once |
| `consume.sqs.visibility` | duration | `1m` | how long a received message is hidden from other consumers while the writer writes it. It must outlast a write to the bucket, or the message is delivered twice |
| `stream.nats.url` | string, required with `stream` | | the JetStream server, for example `nats://nats:4222` |
| `stream.nats.tokenFile` | path | none: no credentials | the token presented to a broker that verifies who connects, read afresh on every connect ([stream](../../guides/audit/operate/run-stream-mode.md)) |
| `stream.name` | string | `AUDIT` | the stream |
| `stream.consumer` | string | `audit-writer` | the durable consumer this installation's writers share |
| `stream.batch` | integer, at least 1 | 100 | how many records are taken at once |
| `stream.ackWait` | duration | `2m` | how long the stream waits for a batch to be taken before offering it again. Records are acknowledged only once they are in the archive, so it must exceed `roll.interval` plus the longest a put can take |
| `roll.interval` | duration | `30s` | how long gathered records wait before they are written. In direct mode there is no stream to gather from, and it is only how long an object may stay open inside one write |
| `roll.maxRecords` | integer, at least 1 | 5000 | how many gathered records are written at once. The roll ends at whichever of the two is reached first, or at the roller's byte limit |
<!-- /generated -->

`stream` is the NATS shorthand for `forward.nats` in a receiver and `consume.nats` in a writer. Give it or the longhand, not both. A receiver requires one of them.

### Durability: `require`, `forward`, `consume`

Every acknowledgement carries a durability ([0059](../../decisions/0059-sink-durability-and-transports.md)).

| Value | Meaning |
|---|---|
| `archived` | The object is in the bucket |
| `queued` | A replicated queue holds it and will deliver it |
| `logged` | A log line |

`require` is the floor a process holds its chain to. At start-up the process computes the best its chain can give. A chain below `require` is a start-up error.

| Process | `require` default | Notes |
|---|---|---|
| Writer | `archived` | Puts the object itself |
| Receiver | `queued` | `archived` is refused, because a receiver holds no archive. The writers behind it reach `archived` |
| `forward.log` | `logged` only | Refused with any other `require` by the schema, the loader and the guard |

A process that records through a writer (`audit-query` and every job with a `sink`) takes `require` too. `sink.expect` states what that writer gives. Set `require` needs an `expect` at least as strong, checked at start-up and on every acknowledgement. Unset checks nothing.

| SQS item | Detail |
|---|---|
| Credentials | The AWS SDK's ambient ones, such as EKS Pod Identity or IRSA. Never in the file |
| Receiver role | `sqs:SendMessage` |
| Writer role | `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:ChangeMessageVisibility` |
| Example | `charts/audit/examples/sqs.yaml`. Tested against a fake and LocalStack, not live AWS |

| Caller verification | Behaviour |
|---|---|
| `workloads` set | The receiver verifies who writes and stamps the caller's service account as the record's observer |
| Neither `workloads` nor `anonymousWrites: true` | The receiver refuses to start |
| Records over the stream | No verified observer; the stream's authentication admits the publisher |
| Observer version | The build's version, not configurable |

### audit-writer-lambda

The write path as an AWS Lambda function behind an SQS event source mapping ([AWS](aws-pulumi-library.md)). It runs the writer under the same `require: archived` guard. It has no listener, registry, stream, database or HTTP front door. DynamoDB does the deduplication that Postgres does for `audit-writer`.

| Config file lookup, first that exists |
|---|
| `--config` |
| `AUDIT_CONFIG` |
| `/opt/audit/audit.yaml` |
| `/var/task/audit.yaml` |

The Pulumi library puts the file in the function's configuration layer at `/opt/audit/audit.yaml`, named by `AUDIT_CONFIG` ([AWS](../../concepts/audit/aws-lambda.md#configuration-as-a-layer)). The writer's start-up record names the file read and its digest ([evidence](configuration.md#evidence-the-writers-start-up-record)).

<!-- generated: config-audit-writer-lambda -->
| key | type | default | meaning |
|---|---|---|---|
| `deployment` | path, required | | the profile configuration, in the layer at `/opt/audit/deployment.yaml` |
| `catalogues` | path | none | a directory of catalogues registered at start-up, `/opt/audit/catalogues` in the layer. There is no registry service to register one with |
| `archive` | `archive` | | `stateRoot`, `sluisRoot`, `ca` and `kmsKey`; the stores are the deployment's `presets` |
| `keys` | `keys` | none | pseudonymisation keys. Only a provider a function outside a VPC can reach is usable: `transit` over a public address |
| `forgetIdentities` | boolean | false | as in `audit-writer` |
| `dedupe.dynamodb.table` | string, required | | the table: a string hash key `pk`, TTL on `expires_at` |
| `dedupe.dynamodb.region` | string | the SDK's (`AWS_REGION`) | the table's region |
| `dedupe.dynamodb.window` | duration | the widest window any profile asks for | how long a written record's id is remembered. It wants to be at least as long as the queue keeps a message (SQS: 14 days at most) |
| `require` | `logged`, `queued` or `archived` | `archived` | the weakest durability the chain may give; the writer gives `archived` at best |
<!-- /generated -->

The notary Lambda reads `audit-notary`'s file ([audit-notary](configuration-jobs.md#audit-notary)) from the same place. Its `signer.kms` names the seal key by alias.

## Workload identity

The receiver reads the file `workloads` names (in the chart `/etc/audit/workloads.yaml`, rendered from `workloadIdentity`).

```yaml
issuers:
  - url: https://oidc.example.com/id/CLUSTER   # the cluster's service-account issuer
    audience: audit
```

| Item | Behaviour |
|---|---|
| Caller token | A projected service-account token as a bearer |
| Jobs and query service | Read the file `sink.tokenFile` names on every request, because the kubelet replaces it |
| `audit` commands | Read `--token-file` or `AUDIT_TOKEN_FILE` the same way |
| Catalogue ownership | The verified identity decides it, never the document. The file's `workloads` list maps service account to source |
| Caller missing from `workloads` | Registers as nobody; the registration is refused |
| Index plus verified callers | The chart refuses to render without the `workloads` list |
| Issuer discovery | Fetched at start-up, so it must be reachable over HTTPS from the pods. The API server's in-cluster issuer usually is not, as the file takes no CA or credential for it |
