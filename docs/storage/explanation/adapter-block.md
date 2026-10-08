# The adapter block

A product's configuration chooses its backends in two blocks, `state` and `keys`. They are the same in sluis and audit
because the storage module owns them; a product's own pages link here instead of restating them.

## `keys`

`keys` names one key service and, for each purpose the product uses, which key serves it:

```yaml
keys:
  adapter: kms                # kms | transit | local
  sign: alias/sluis-signing
  seal: {key: alias/audit-seal, context: default}
  conceal: {key: alias/audit-data, context: {app: audit}}
```

A purpose maps to a string (the key's alias, or its transit name, with the default context) or to `{key, context}`.
The purposes are `sign` (sluis: token signing, asymmetric), `seal` (audit: sealing the trail), `pseudonym` (audit:
per-tenant pseudonyms), `conceal` (audit: values that must be recoverable) and `archive` (audit: long-term storage).
Several purposes may share a key; the context keeps their ciphertexts apart, though not their fates (disabling a shared
key stops every purpose that uses it).

Names only, never ARNs: a file shared between deployments must carry neither an account nor a region, and an alias can
be re-pointed at a new key without editing every file. An alias is not a permission; the grant on the key decides what
a role can do. The reference is [the adapter block reference](../reference/adapter-block.md).

## `state`

`state` names the backend of the versioned store: AWS SSM parameters, an S3 bucket, or OpenBao KV version 2 (memory
is for tests). Which of them a product accepts in a given shape is that shape's decision
([sluis deployment](../../sluis/deployment/README.md), [audit deployment](../../audit/deployment/README.md)); that the
products take these blocks directly is decided in [0041](../../decisions/0041-the-secret-contract.md) and is being
carried out, so a product may still read the older keys that the block replaces. Check the product's own configuration
reference for what it accepts today.

## Why a block and not an adapter per call site

Each product used to decide where each secret and key lived, so moving to a different store meant touching every
caller. With a block, a caller asks by purpose or by path, a deployment says once where those live, and the conformance
suites hold every backend to the same behaviour.
