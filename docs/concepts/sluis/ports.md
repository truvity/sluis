# How are storage, signalling and identity cut into ports?

A port is a small Go interface, a set of semantics every adapter shares and a conformance suite. The contracts are in the [ports reference](../../reference/sluis/ports.md).

| You want | Read |
|---|---|
| the keys sluis stores | [keys](../../reference/sluis/keys.md) |
| where each record lands in DynamoDB, SSM and S3 | [storage layout](../../reference/sluis/storage-layout.md) |
| one adapter's settings, IAM and limits | [adapter details](../../reference/sluis/port-adapters.md) |
| which adapters and presets exist | [adapters](../../reference/sluis/adapters.md) |
| how far each piece has got, per platform | [capabilities](../../reference/sluis/capabilities.md) |
| how the domain records sit on the ports | [the domain stores](domain-stores.md) |

## What is a port?

Business code names a port and never an adapter, and an adapter holds no business rule. The test `internal/port/guard_test.go`
fails on a business import of `internal/kube`, `internal/valkey` or the legacy adapter. Only the adapters, the factory
`internal/store`, the apps that assemble a process, the migration and the commands may.

Every port has an adapter for each permanent platform:

| Port | What it is for | Kubernetes | AWS Lambda |
|---|---|---|---|
| State | records, sessions, tokens, leases, gates, caches, counters (no credential) | `legacy` (ConfigMaps, Valkey), DynamoDB | DynamoDB |
| Blob | status reports, directory snapshots | S3 | S3 |
| Trigger | a change becomes a tick | DynamoDB (polling watch), in-process | asynchronous `lambda:Invoke` |
| Secrets | dynamic secrets | OpenBao, SSM, memory, `legacy` | SSM, OpenBao |
| Inputs | policy, configuration, operator-managed secrets | mounted ConfigMaps and Secrets | the configuration layer, or a parameter store |
| Identity | proves a workload to the issuer, and the service to the cloud | ServiceAccount token, AWS federation | the same |
| Audit sink | records what the service did | `connect`, `log` | `sqs` |

[Adapters](../../reference/sluis/adapters.md) is generated from the registry (`port.Default.Matrix()`) and wins on a difference.

## What do the ports promise?

State is single-key with a lifetime. A flow over several keys is a sequence of idempotent steps with a recovery
marker. Every record expires unless the layout says it is permanent. A revision is opaque and changes on every write.

Reads filter on expiry because DynamoDB removes items late. A credential is never written to State. The
domain stores put it in Secrets and leave a marker in the record.

## Who owns a record, and who may read it?

On layout v5 each module owns one table ([ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md)): `oidc`,
`github`, `slack`, `cloudflare`, `google` and `backup`. A process writes its own module's table, and another module's
key fails with `ErrNotOwner` before any request.

A peer is a read-only view of another module's table. The issuer holds `google`, `github` and `slack`.
Any other module's read fails.

A listing across modules, such as `ws.`, is `ErrUnsupported` on the router: list `ws.dir.`. Layout v5 pairs
`secrets.layout: v5` with `ports.dynamodb.tables`, and start refuses a mix.

DynamoDB has no cheap change feed, so `Watch` and the Trigger poll the prefix. A notification is a hint, and the lease and
the backstop schedule make a duplicate or lost one harmless.

The `legacy` adapter wraps ConfigMaps, Secrets and Valkey, and goes when the
[migration](../../decisions/0031-a-generic-migration-tool.md) has run. One conformance suite gates every adapter and the
migration tool ([run it](../../guides/sluis/run-conformance.md); [assertions](../../reference/sluis/ports.md#conformance)).

## Adapters, presets and the platform

You choose an adapter per concern: `state`, `secrets`, `blobs`, `signing`, `trigger`, `schedule` and `audit`.
Each adapter registers a descriptor in `internal/port` (`port.Register`) with its name, concern, needs, runtimes
(`kubernetes`, `lambda`, `process`), status (`implemented`, `on-request`) and factory.

The `aws-hybrid` preset runs sluis on Lambda with DynamoDB state, SSM secrets, KMS token signing, S3 blobs, SQS audit,
EventBridge ticks and an asynchronous invoke for "run a pass now". Every other adapter is on request: in the matrix and
refused at start.

### How do you choose a preset?

Answer four questions in order:

```text
AWS?        no  -> Kubernetes?   no  -> server
                                 yes -> OpenBao?  yes -> k8s-openbao
                                                  no  -> k8s-minimal
            yes -> Kubernetes?   no  -> aws-serverless
                                 yes -> sluis on Lambda?  yes -> aws-hybrid
                                                          no  -> k8s-aws
```

The answers are the `platform` block (`aws`, `kubernetes`, `openbao`, `runtime`, `replicas`), or `preset` names one.
A modifier overrides one concern: `adapters.secrets: {adapter: openbao, settings: {...}}` replaces the SSM secrets of
`k8s-aws` with OpenBao, with no `platform.openbao`.

Available presets: `aws-serverless`, `aws-hybrid` and `k8s-aws` (`aws-eks` is its deprecated name). Loading
`server`, `k8s-minimal` or `k8s-openbao` fails and names the missing adapters. The
[adapters](../../reference/sluis/adapters.md#presets) page lists each preset's adapters.

### What wins when settings disagree?

From first to last: `adapters.<concern>` with its `settings`, the legacy keys beside a preset (`ports.adapter`,
`ports.blob`), `preset`, then the preset `platform` leads to.

With none of `platform`, `preset` and `adapters`, the `ports` keys decide: `ports.adapter: legacy`, signing `file`,
schedule `ticker`, and audit `connect` when `audit.writer` is set, else `log`.

### What does start refuse?

When `platform` or `preset` is given, start fails and names every problem. It refuses an adapter that needs a false
platform answer or cannot run on the runtime (`legacy` on `lambda`). It also refuses `memory` above one replica, a
secrets adapter that is not a secret store while the platform has one, and an unknown or planned adapter.

The runtime is `platform.runtime`, the preset's, or `lambda` when `AWS_LAMBDA_FUNCTION_NAME` is set. Start logs the
resolved table and exposes the gauge `sluis_adapter_info{concern,adapter} 1`.

## Which signing keys can you publish without signing?

The `signing` concern chooses `file`, `kms` or `kms-wrapped`. The list `signingKey.verifyOnly` holds public keys that are
published and never signed with, so tokens from old file keys verify across a cutover to `kmsWrapped`. A private key
there stops the start. The keys are in [configuration](../../reference/sluis/configuration.md).

## Decided in

- [ADR 0026](../../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md): two platforms permanently
- [ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md): the State port
- [ADR 0028](../../decisions/0028-nothing-writes-configmaps-or-secrets.md): nothing writes ConfigMaps or Secrets
- [ADR 0029](../../decisions/0029-ticks-per-target-under-a-lease.md): the Trigger port
- [ADR 0030](../../decisions/0030-workload-identity-on-both-platforms.md): the Identity port
- [ADR 0031](../../decisions/0031-a-generic-migration-tool.md): the migration
