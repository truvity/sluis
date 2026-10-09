# How is storage structured?

```text
product (sluis, audit)
   │  asks by purpose / by path
   ├── storage/keys    Key: Encrypt, Decrypt, GenerateDataKey, MAC, PublicKey, Sign (direct signing is deprecated)
   │      backends:    kms · transit · local (tests)
   └── storage/state   Store, Value[T]: versioned JSON objects
          backends:    ssm · s3 · openbao (KV v2) · memory (tests)

storage/openbao   one client (JWT login, namespace, CA bundle) shared by state/openbao and keys/transit
```

**state** is a small versioned key-value store for what a service keeps between runs: signing keys, issued credentials and rotation state.

A `Value[T]` is a typed handle on one key. `Rotating(grace)` also returns the value it replaced for a grace period. Every value is a JSON object and every write returns a revision. A write is conditional on the revision the caller last saw, so a lost race is an error and never a silent overwrite.

The store knows nothing about what it holds. Each product adds a typed layer: an internal view for what only the service reads and writes, and an external view for what it publishes or receives.

**keys** gives a caller one way to ask for a key: by purpose (`sign`, `seal`, `pseudonym`, `conceal`, `archive`). The configuration names the key, and a backend does the work. A ciphertext is bound to a context, by default the instance and the purpose. Two deployments or purposes that share a key cannot open each other's ciphertexts.

**One OpenBAO client.** A process builds one `openbao.Client` and gives it to both backends. It logs in lazily with a JWT auth mount and a projected ServiceAccount token, and re-reads the token file at every login. It keeps its token until 80% of the lease has gone.

Each backend passes a conformance suite, `storage/state/conformance` or `storage/keys/conformance`. A product behaves the same whichever backend it gets. The block that selects them is [the adapter block](adapter-block.md).

## Decided in

- [0041 The secret contract](../../decisions/0041-the-secret-contract.md)
