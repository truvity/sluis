# Ports and adapters

The shape of sluis's storage, signalling and identity edges, and why they are cut this way. The reasoning behind the
cut is in the records [0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md) to
[0034](../decisions/0034-exports-go-to-openbao-directly.md). This page is the explanation; the pages below are the
reference.

| You want | Read |
|---|---|
| the contract each port promises (operations, errors, conformance) | [ports](../reference/ports.md) |
| the logical keys sluis stores | [keys](../reference/keys.md) |
| where each record lands in DynamoDB, SSM and S3 | [storage layout](../reference/storage-layout.md) |
| what one adapter does beyond the contract (settings, IAM, limits) | [adapter details](../reference/port-adapters.md) |
| which adapters and presets exist, and which are built | [adapters](../reference/adapters.md) |
| how far each piece has got, per platform | [capabilities](../reference/capabilities.md) |
| how the domain records sit on the ports | [the domain stores](domain-stores.md) |

## The shape

A port is a small Go interface in the service, a set of semantics every adapter shares, and a conformance suite. It is
**not a framework** ([0024](../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md)): business code
names a port and never an adapter, and an adapter contains no business rule. A test (`internal/port/guard_test.go`)
fails if business code imports `internal/kube`, `internal/valkey` or the legacy adapter; only the adapters, the one
factory that chooses them (`internal/store`), the apps that assemble a process, the migration and the commands may.

Two platforms are supported permanently, so every port has an adapter for each:

| Port | What it is for | Kubernetes | AWS Lambda |
|---|---|---|---|
| State | records, sessions, tokens, leases, gates, caches, counters (no credential) | `legacy` (ConfigMaps, Valkey), DynamoDB | DynamoDB |
| Blob | status reports, directory snapshots | S3 | S3 |
| Trigger | a change becomes a tick | DynamoDB (polling watch), in-process | asynchronous `lambda:Invoke` |
| Secrets | dynamic secrets (the credentials of the domain stores) | OpenBao, SSM, memory, `legacy` | SSM, OpenBao |
| Inputs | policy, configuration, operator-managed secrets | mounted ConfigMaps and Secrets | the configuration layer, or a parameter store |
| Identity | proves a workload to the issuer, and the service to the cloud | ServiceAccount token, AWS federation | the same |
| Audit sink | records what the service did | `connect`, `log` | `sqs` |

The table is a summary; [adapters](../reference/adapters.md) is the registry's own, and wins on a difference.

### Choices that follow from the cut

- **State is single-key, with a lifetime.** The two engines (DynamoDB, a ConfigMap or Valkey) share no transaction or
  index, so a flow that needs several keys is a sequence of idempotent steps with a recovery marker, and every record
  expires unless the layout says it is permanent. A revision is opaque and changes on every write.
- **Reads filter on expiry themselves.** DynamoDB removes expired items late, so no caller relies on the engine's sweep.
- **A credential is never written to State.** The domain stores put it in Secrets under
  `credentials/<kind>/<id>/<ref>` and leave a marker in the record. The earlier sealing step (an envelope under a KMS
  key) is retired ([0027](../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md) is superseded in part).
- **DynamoDB has no cheap change feed**, so `Watch` and the Trigger poll the prefix: a stream needs a consumer, a second
  IAM surface and shard handling, and the port's contract (at-least-once, reconcile by listing) allows it.
- **A notification is a hint.** The lease and the backstop schedule make a duplicate or a lost one harmless.
- **The `legacy` adapter is temporary.** It wraps today's ConfigMaps, Secrets and Valkey and is deleted when the
  migration of [0031](../decisions/0031-a-generic-migration-tool.md) has run.
- **One suite, every adapter.** The conformance suite is the gate for adding or changing an adapter and for the
  migration tool, where each adapter is a source and a destination
  ([run it](../how-to/run-conformance.md); [what it asserts](../reference/ports.md#conformance)).

## Adapters, presets and the platform

An adapter is chosen by name, **per concern**. The concerns are `state` (sessions are State under `ses.`, with a
lifetime), `secrets` (dynamic secrets), `blobs`, `signing`, `trigger`, `schedule` and
`audit`. Each adapter registers a descriptor in `internal/port` (`port.Register`): its name and concern, what it needs
(AWS, Kubernetes, OpenBao), the runtimes it works on (`kubernetes`, `lambda`, `process`), its status (`implemented` or
`on-request`) and a factory from its settings. `port.Catalogue` lists the adapters that are planned and not built;
`port.Default.Matrix()` is the registry plus the catalogue, which [adapters](../reference/adapters.md) is generated from.

**Only what the current estates need is built.** Both run the `aws-hybrid` preset: sluis on Lambda, DynamoDB state, SSM
secrets, KMS token signing, S3 blobs, SQS audit, EventBridge ticks and an asynchronous invoke for "run a pass now".
Every other adapter is *on request*: it is in the matrix, and start refuses it.

### Choosing a preset

Four questions, in this order:

```text
AWS?        no  -> Kubernetes?   no  -> server
                                 yes -> OpenBao?  yes -> k8s-openbao
                                                  no  -> k8s-minimal
            yes -> Kubernetes?   no  -> aws-serverless
                                 yes -> sluis on Lambda?  yes -> aws-hybrid
                                                          no  -> k8s-aws
```

The answers are the `platform` block (`aws`, `kubernetes`, `openbao`, `runtime`, `replicas`); `preset` names one
outright. A **modifier** overrides one concern: `adapters.secrets: {adapter: openbao, settings: {...}}` replaces the
SSM secrets of `k8s-aws` with OpenBao, and naming the adapter is the answer, so no `platform.openbao` is needed.

Available presets: `aws-serverless`, `aws-hybrid` and `k8s-aws` (sluis as a pod on Kubernetes with AWS storage; every
adapter it names is built). `aws-eks` is the deprecated name of `k8s-aws`: it resolves to it, and start logs a warning.
The presets `server`, `k8s-minimal` and `k8s-openbao` are **unavailable**: each names adapters that are not built, and
loading one fails naming them. What each preset names per concern, and which adapters are missing, is in
[adapters](../reference/adapters.md#presets).

### Resolution

An explicit override wins over the preset, and the preset over what the decision tree derives from the `platform`
answers: `adapters.<concern>` (with its `settings`), then the legacy keys written beside a preset (`ports.adapter`,
`ports.blob`), then `preset`, then the preset the `platform` leads to. **With none of `platform`, `preset` and
`adapters`, nothing changes:** the `ports` keys decide, `ports.adapter: legacy` is the default, and the table is the
legacy one (state `legacy` or the named adapter, signing `file`, schedule `ticker`, audit `connect` when `audit.writer`
is set and `log` otherwise).

### Start-up validation

Start is refused, naming every problem, when an adapter needs a platform answer that is false (only checked when
`platform` or `preset` is given); cannot run on the current runtime (`legacy` on `lambda`; the runtime is
`platform.runtime`, the preset's, or `lambda` when `AWS_LAMBDA_FUNCTION_NAME` is set); is process-local (`memory`) while
`platform.replicas` is above 1; is a secrets adapter that is not a secret store while the platform has one; is unknown;
or is planned. The resolved table is then logged once (`adapters resolved`, one attribute per concern) and exposed as the
gauge `sluis_adapter_info{concern,adapter} 1`.

### Signing keys

The `signing` concern chooses how tokens are signed (`file`, `kms`, `kms-wrapped`). Public keys that are published and
never signed with are `signingKey.verifyOnly` (each with an optional `kid`, `alg` and a required `until`): they keep
tokens issued by the old file keys verifying across a cutover to `kmsWrapped`. A private key there stops the start
(`internal/issuerapp`, `loadVerifyOnly`). The keys are in [configuration](../reference/configuration.md).
