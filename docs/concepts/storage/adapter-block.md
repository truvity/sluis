# What is the adapter block?

A product's configuration names its key service in the `keys` block. sluis and audit share it because the storage module owns it.

## `keys`

`keys` names one key service and, for each purpose, the key that serves it.

```yaml
keys:
  adapter: kms                # kms | transit | local
  sign: alias/sluis-example-sign
  seal: {key: alias/sluis-example-audit-seal, context: default}
  conceal: {key: alias/audit-data, context: {app: audit}}
```

A purpose maps to a string or to `{key, context}`. A string is the key's alias, or its transit name, with the default context.

| purpose | product | use |
|---|---|---|
| `sign` | sluis | the key that wraps the signing key ring |
| `seal` | audit | sealing the trail |
| `pseudonym` | audit | per-tenant pseudonyms |
| `conceal` | audit | values that must be recoverable |
| `archive` | audit | the archive's SSE-KMS alias; sluis ignores it |

Several purposes may share a key. The context keeps their ciphertexts apart, but disabling a shared key stops every purpose that uses it.

Write names, never ARNs. A file shared between deployments then carries no account or region, and an alias can move to a new key without editing files. An alias grants nothing: the grant on the key decides what a role can do. See [the adapter block reference](../../reference/storage/adapter-block.md).

## `state`

The versioned store has four backends: AWS SSM parameters, an S3 bucket, OpenBAO KV version 2 and `memory` for tests. No product has a `state` block. sluis reads SSM through `secrets`, and audit through `archive.stateRoot` and `keys.state`. Neither reads OpenBAO KV yet.

Backends per shape: [sluis deployment](../../get-started/sluis/deployment/README.md) and [audit deployment](../../get-started/audit/README.md).

Secrets and published values sit at addresses of one layout, module first: `<root>/internal/<module>/...` and `<root>/external/<module>/<id>`. Each address holds one versioned JSON document. See [storage layout](../../reference/sluis/storage-layout.md).

sluis keeps its secrets in layout v5; the `ssm` source requires `secrets.layout: v5`.

## Decided in

- [0041 The secret contract](../../decisions/0041-the-secret-contract.md)
- [0072 Storage layout v5, module first](../../decisions/0072-storage-layout-v5-module-first.md)
