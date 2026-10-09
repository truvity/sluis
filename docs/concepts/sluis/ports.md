# How are storage, signalling and identity cut into ports?

A port is a small Go interface, a set of semantics every adapter shares and a conformance suite. This page explains the
cut. The contracts are in the [ports reference](../../reference/sluis/ports.md).

| You want | Read |
|---|---|
| the keys sluis stores | [keys](../../reference/sluis/keys.md) |
| where each record lands in DynamoDB, SSM and S3 | [storage layout](../../reference/sluis/storage-layout.md) |
| one adapter's settings, IAM and limits | [adapter details](../../reference/sluis/port-adapters.md) |
| which adapters and presets exist | [adapters](../../reference/sluis/adapters.md) |
| how far each piece has got, per platform | [capabilities](../../reference/sluis/capabilities.md) |
| how the domain records sit on the ports | [the domain stores](domain-stores.md) |

## What is a port?

Business code names a port and never an adapter, and an adapter holds no business rule. The test
`internal/port/guard_test.go` fails if business code imports `internal/kube`, `internal/valkey` or the legacy adapter.
Only the adapters, the factory `internal/store`, the apps that assemble a process, the migration and the commands may.

Two platforms are supported permanently, so every port has an adapter for each:

| Port | What it is for | Kubernetes | AWS Lambda |
|---|---|---|---|
| State | records, sessions, tokens, leases, gates, caches, counters (no credential) | `legacy` (ConfigMaps, Valkey), DynamoDB | DynamoDB |
| Blob | status reports, directory snapshots | S3 | S3 |
| Trigger | a change becomes a tick | DynamoDB (polling watch), in-process | asynchronous `lambda:Invoke` |
| Secrets | dynamic secrets | OpenBao, SSM, memory, `legacy` | SSM, OpenBao |
| Inputs | policy, configuration, operator-managed secrets | mounted ConfigMaps and Secrets | the configuration layer, or a parameter store |
| Identity | proves a workload to the issuer, and the service to the cloud | ServiceAccount token, AWS federation | the same |
| Audit sink | records what the service did | `connect`, `log` | `sqs` |

[Adapters](../../reference/sluis/adapters.md) is the registry's own table and wins on a difference.

## What do the ports promise?

State is single-key with a lifetime. DynamoDB and a ConfigMap or Valkey share no transaction or index. A flow over
several keys is a sequence of idempotent steps with a recovery marker. Every record expires unless the layout says it is
permanent. A revision is opaque and changes on every write.

Reads filter on expiry themselves, because DynamoDB removes expired items late. A credential is never written to State.
The domain stores put it in Secrets under `credentials/<kind>/<id>/<ref>` and leave a marker in the record.

DynamoDB has no cheap change feed, so `Watch` and the Trigger poll the prefix. A notification is a hint. The lease and
the backstop schedule make a duplicate or lost one harmless.

The `legacy` adapter wraps ConfigMaps, Secrets and Valkey. It is deprecated and goes when the
[migration](../../decisions/0031-a-generic-migration-tool.md) has run. One conformance suite gates every adapter and the
migration tool ([run it](../../guides/sluis/run-conformance.md); [what it asserts](../../reference/sluis/ports.md#conformance)).

## Adapters, presets and the platform

You choose an adapter by name per concern: `state`, `secrets`, `blobs`, `signing`, `trigger`, `schedule` and `audit`.
Each adapter registers a descriptor in `internal/port` (`port.Register`) with its name, concern, needs, runtimes
(`kubernetes`, `lambda`, `process`), status (`implemented` or `on-request`) and a factory.

`port.Default.Matrix()` joins the registry and `port.Catalogue`, the planned adapters. [Adapters](../../reference/sluis/adapters.md) is generated from it.

The `aws-hybrid` preset runs sluis on Lambda with DynamoDB state, SSM secrets, KMS token signing, S3 blobs, SQS audit,
EventBridge ticks and an asynchronous invoke for "run a pass now". Every other adapter is on request. It is in the
matrix and start refuses it.

### How do you choose a preset?

Answer four questions in this order:

```text
AWS?        no  -> Kubernetes?   no  -> server
                                 yes -> OpenBao?  yes -> k8s-openbao
                                                  no  -> k8s-minimal
            yes -> Kubernetes?   no  -> aws-serverless
                                 yes -> sluis on Lambda?  yes -> aws-hybrid
                                                          no  -> k8s-aws
```

The answers are the `platform` block (`aws`, `kubernetes`, `openbao`, `runtime`, `replicas`). `preset` names one outright.
A modifier overrides one concern: `adapters.secrets: {adapter: openbao, settings: {...}}` replaces the SSM secrets of
`k8s-aws` with OpenBao, and needs no `platform.openbao`.

`aws-serverless`, `aws-hybrid` and `k8s-aws` are available. `aws-eks` is the deprecated name of `k8s-aws` and logs a
warning. `server`, `k8s-minimal` and `k8s-openbao` are unavailable. Loading one fails and names the missing adapters.
Per-preset adapters are in [adapters](../../reference/sluis/adapters.md#presets).

### What wins when settings disagree?

An explicit override beats the preset, and the preset beats what `platform` derives. The order is `adapters.<concern>`
with its `settings`, then the legacy keys beside a preset (`ports.adapter`, `ports.blob`), then `preset`, then the
preset `platform` leads to.

With none of `platform`, `preset` and `adapters`, the `ports` keys decide. The defaults are `ports.adapter: legacy`,
signing `file` and schedule `ticker`. Audit is `connect` when `audit.writer` is set and `log` otherwise.

### What does start refuse?

Start fails and names every problem when an adapter needs a platform answer that is false. That check runs only when
`platform` or `preset` is given. It also fails for an adapter that cannot run on the runtime, such as `legacy` on `lambda`.

It fails for `memory` while `platform.replicas` is above 1, and for a secrets adapter that is not a secret store while the
platform has one. It fails for an unknown or planned adapter.

The runtime is `platform.runtime`, the preset's, or `lambda` when `AWS_LAMBDA_FUNCTION_NAME` is set. Start logs the resolved
table once (`adapters resolved`) and exposes the gauge `sluis_adapter_info{concern,adapter} 1`.

## Which signing keys can you publish without signing?

The `signing` concern chooses `file`, `kms` or `kms-wrapped`. `signingKey.verifyOnly` lists public keys that are
published and never signed with. Each has an optional `kid` and `alg` and a required `until`. They keep tokens from old
file keys verifying across a cutover to `kmsWrapped`. A private key there stops the start. The keys are in
[configuration](../../reference/sluis/configuration.md).

## Decided in

- [ADR 0024](../../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md): shared pieces, not a framework
- [ADR 0026](../../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md): two platforms permanently
- [ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md): the State port
- [ADR 0028](../../decisions/0028-nothing-writes-configmaps-or-secrets.md): nothing writes ConfigMaps or Secrets
