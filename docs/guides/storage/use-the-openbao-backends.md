# Use the OpenBao backends

Give a process OpenBao-backed state and keys with the least privilege that works.

## Before you start

- You need an OpenBao or Vault with a JWT auth mount that accepts the process's projected ServiceAccount token, a KV version 2 mount for state and a transit mount for keys.

- The policies below were verified against OpenBao 2.6.2. The backends' tests run under them, not under the root token.

## Steps

### 1. Create the role

Create a JWT role that binds the ServiceAccount subject and audience and names the policies below.

### 2. Write the policies

State on KV version 2, with mount `kv` and prefix `sluis/state`:

```hcl
path "kv/config" { capabilities = ["read"] }
path "kv/data/sluis/state/*" { capabilities = ["create", "update", "read"] }
path "kv/metadata/sluis/state" { capabilities = ["list"] }
path "kv/metadata/sluis/state/*" { capabilities = ["read", "list", "delete"] }
```

Keys on transit. Give a role only the lines for the operations it performs.

```hcl
path "transit/keys/audit-data" { capabilities = ["read"] }      # the backend reads type, derivation, versions
path "transit/encrypt/audit-data" { capabilities = ["update"] }
path "transit/decrypt/audit-data" { capabilities = ["update"] }
path "transit/datakey/plaintext/audit-data" { capabilities = ["update"] }
path "transit/hmac/audit-data" { capabilities = ["update"] }
path "transit/keys/sluis-signing" { capabilities = ["read"] }
path "transit/sign/sluis-signing" { capabilities = ["update"] }
```

### 3. Pin the encryption context

The context travels as one base64 string of the canonical JSON of the map, with keys sorted and no white space. Pin it so a compromised role cannot encrypt or decrypt for another instance or purpose.

```hcl
path "transit/encrypt/audit-data" {
  capabilities        = ["update"]
  required_parameters = ["associated_data"]
  allowed_parameters  = {
    "plaintext"       = []
    "associated_data" = ["eyJpbnN0YW5jZSI6InByb2QiLCJwdXJwb3NlIjoiY29uY2VhbCJ9"]
  }
}
```

The value above is `{"instance":"prod","purpose":"conceal"}`. For a derived key, use `context` in both places. Follow these rules:

- Add `required_parameters`. `allowed_parameters` alone allows a request that omits the parameter, and on a key that is not derived that request encrypts with no binding.

- Name `plaintext` for encrypt, or `ciphertext` for decrypt, with an empty list. Otherwise the request is refused.

- Do not pin integer parameters such as `key_version`. Pin the key by the path.

### 4. Point the product at the backends

Set the product's `state` and `keys` blocks ([adapter block reference](../../reference/storage/adapter-block.md)).

## Verify

Start a development server and run the backends' conformance tests:

```sh
bao server -dev -dev-root-token-id=root -dev-listen-address=127.0.0.1:8200
just test-openbao
```

## Roll back

Keep the previous backend's policies until the new one has served traffic. State and keys are separate choices, so move them one at a time.

The policies, line by line, are in the package documentation `storage/doc.go`.
