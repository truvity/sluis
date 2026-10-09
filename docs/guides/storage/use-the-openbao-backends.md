# Use the OpenBao backends

**Purpose.** Give a process OpenBao-backed state and keys with the least privilege that works.

**Preconditions.** An OpenBao (or Vault) with a JWT auth mount that accepts the process's projected ServiceAccount
token, a KV version 2 mount for state, and a transit mount for keys. The policies below were verified against
OpenBao 2.6.2 by the backends' tests, which run under exactly these policies and not under the root token.

## Steps

1. Create a JWT role that binds the ServiceAccount (subject) and audience, and names the policies below.
2. Write the policies. State on KV version 2, with mount `kv` and prefix `sluis/state`:

   ```hcl
   path "kv/config" { capabilities = ["read"] }
   path "kv/data/sluis/state/*" { capabilities = ["create", "update", "read"] }
   path "kv/metadata/sluis/state" { capabilities = ["list"] }
   path "kv/metadata/sluis/state/*" { capabilities = ["read", "list", "delete"] }
   ```

   Keys on transit: give a role only the lines for the operations it performs.

   ```hcl
   path "transit/keys/audit-data" { capabilities = ["read"] }      # the backend reads type, derivation, versions
   path "transit/encrypt/audit-data" { capabilities = ["update"] }
   path "transit/decrypt/audit-data" { capabilities = ["update"] }
   path "transit/datakey/plaintext/audit-data" { capabilities = ["update"] }
   path "transit/hmac/audit-data" { capabilities = ["update"] }
   path "transit/keys/sluis-signing" { capabilities = ["read"] }
   path "transit/sign/sluis-signing" { capabilities = ["update"] }
   ```

3. Pin the encryption context so a compromised role cannot encrypt or decrypt for another instance or purpose. The
   context travels as one base64 string of the canonical JSON of the map (keys sorted, no white space). Add
   `required_parameters`: `allowed_parameters` alone does not require the parameter, and on a key that is not derived a
   request that leaves it out encrypts with no binding at all. Name `plaintext` (encrypt) or `ciphertext` (decrypt) with
   an empty list, or the request is refused. Do not pin integer parameters such as `key_version`; pin the key by the path.
4. Point the product's `state` and `keys` blocks at the backends ([reference](../../reference/storage/adapter-block.md)).

**Verify.** The backends' conformance tests run against a development server:
`bao server -dev -dev-root-token-id=root -dev-listen-address=127.0.0.1:8200`, then `just test-openbao`.

**Rollback.** Keep the previous backend's policies until the new one has served traffic; keys and state are separate
choices and can be moved one at a time.

The same policies, with the reasoning for each line, are in the package documentation, `storage/doc.go`.
