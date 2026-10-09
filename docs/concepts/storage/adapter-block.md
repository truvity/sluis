# What is the adapter block?

A product's configuration chooses its backends in two blocks, `state` and `keys`. sluis and audit share them because the storage module owns them. A product's own pages link here.

## `keys`

`keys` names one key service and, for each purpose, the key that serves it.

```yaml
keys:
  adapter: kms                # kms | transit | local
  sign: alias/sluis-signing
  seal: {key: alias/audit-seal, context: default}
  conceal: {key: alias/audit-data, context: {app: audit}}
```

A purpose maps to a string or to `{key, context}`. A string is the key's alias, or its transit name, with the default context.

| purpose | product | use |
|---|---|---|
| `sign` | sluis | the key that wraps the signing key ring |
| `seal` | audit | sealing the trail |
| `pseudonym` | audit | per-tenant pseudonyms |
| `conceal` | audit | values that must be recoverable |
| `archive` | audit | long-term storage |

Several purposes may share a key. The context keeps their ciphertexts apart, but disabling a shared key stops every purpose that uses it.

Write names, never ARNs. A file shared between deployments then carries no account or region, and an alias can move to a new key without editing files. An alias grants nothing: the grant on the key decides what a role can do. See [the adapter block reference](../../reference/storage/adapter-block.md).

## `state`

`state` names the backend of the versioned store: AWS SSM parameters, an S3 bucket or OpenBAO KV version 2. `memory` is for tests.

Each shape decides which backends it accepts: [sluis deployment](../../get-started/sluis/deployment/README.md) and [audit deployment](../../get-started/audit/README.md). Products are still moving to these blocks and may read older keys they replace. Check the product's configuration reference for what it accepts today.

Secrets and published values sit at addresses of one layout: `<root>/internal/...` and `<root>/external/<kind>/<id>`. Each address holds one versioned JSON document. See [layout v4](../../reference/sluis/storage-layout.md).

sluis selects the layout with `secrets.layout` (`v3`, `transition`, `v4`). `v3` is the default for now. `sluis migrate secrets-layout` moves an installation.

## Decided in

- [0041 The secret contract](../../decisions/0041-the-secret-contract.md)
