# Architecture

```text
product (sluis, audit)
   │  asks by purpose / by path
   ├── storage/keys    Key: Sign, Encrypt, Decrypt, GenerateDataKey, MAC, PublicKey
   │      backends:    kms · transit · local (tests)
   └── storage/state   Store, Value[T]: versioned JSON objects
          backends:    ssm · s3 · openbao (KV v2) · memory (tests)

storage/openbao   one client (JWT login, namespace, CA bundle) shared by state/openbao and keys/transit
```

**state** is a small versioned key-value store for what a service keeps between runs: signing keys, issued credentials,
rotation state. Every value is a JSON object, every write returns a revision, and a write is conditional on the revision
the caller last saw, so a lost race is an error and never a silent overwrite. The store knows nothing about what it
holds; each product puts its own typed layer on top (an internal view for what only the service reads and writes, an
external view for what it publishes or receives).

**keys** gives a caller one way to ask for a key: by short purpose (`sign`, `seal`, `pseudonym`, `conceal`,
`archive`). The caller says what it wants, the deployment's configuration says which key that is, and a backend does the
work. A ciphertext is bound to a context (the instance and the purpose by default), so two deployments or two purposes
that share a key cannot open each other's ciphertexts.

**One OpenBao client.** A process builds one `openbao.Client` and gives it to both backends. It logs in with a JWT auth
mount using a projected ServiceAccount token, re-reads the token file at every login, logs in lazily, and keeps its
token until 80% of the lease has gone.

Each backend must pass a conformance suite (`storage/state/conformance`, `storage/keys/conformance`), so a product's
behaviour does not depend on which backend it was given. The explanation of the block that selects them is
[the adapter block](explanation/adapter-block.md).
