# OpenBao Secrets adapter

`adapters.secrets: {adapter: openbao}` keeps the Secrets port in an OpenBao KV version 2 mount, in the layout of [SSM v3](secrets.md#ssm-layout-v3). It needs no `platform.openbao` answer. Source: `internal/port/openbao`. The other adapters are listed in [adapters](adapters.md).

## Settings

```yaml
secrets: {source: file, root: /var/run/sluis/secrets}
adapters:
  secrets:
    adapter: openbao
    settings:
      address: https://openbao.example        # https, no path
      caFile: /var/run/access-issuer/openbao-ca/ca.pem   # the CA the server is verified against
      namespace: staging                       # the OpenBao namespace
      mount: kv                               # the KV v2 mount (default kv)
      root: sluis                             # see below
      auth:
        method: jwt                           # jwt | kubernetes
        mount: jwt-staging                     # the auth mount (default: the method's name)
        role: sluis
        tokenFile: /var/run/openbao/token     # a projected ServiceAccount token, read again at every login
```

| Setting | Meaning |
|---|---|
| `address` | `https://host[:port]`, no path. https only, redirects are not followed |
| `caFile` | PEM authorities that verify the server. TLS verification is always on |
| `namespace` | OpenBao namespace; empty is the root namespace |
| `mount` | KV v2 mount, default `kv` |
| `root` | Required. Key hierarchy of the installation under the mount |
| `auth.method`, `.mount`, `.role` | Login `POST auth/<mount>/login {role, jwt}`. The token is kept per namespace until 80% of its lease has passed |
| `auth.tokenFile` | The JWT. Required for `jwt`; `kubernetes` defaults to the pod's token |

`secrets.source` stays `file`: the source delivers inputs, the adapter holds what sluis writes. `secrets.root` is unused by this adapter.

## Layout

With namespace `staging`, mount `kv` and root `sluis`:

```text
kv/sluis/private/config/<name>                       what an operator seeds (read only if used)
kv/sluis/private/credentials/<kind>/<id>/<ref>       what sluis writes and reads back
kv/sluis/export/<path>                               what sluis copies out, for consumers
```

Use `sluis/<instance>` as the root when the OpenBao has no namespace per installation. A root with a `private` or `export` segment is refused at start. On layout v4 the same grants apply to `internal/` and `external/`.

## Values

- A secret is one KV key with one field: `value` (text) or `value_b64` (bytes that are not UTF-8).
- Seed one with `bao kv put kv/sluis/private/config/<name> value=...`.
- A JSON object under `export/` is stored as one field per property.
- `PutIfVersion` is KV check-and-set. The mount must not set `cas_required`.
- Errors name the operation, path and status. Values are never logged.

## Policy

Least privilege for root `sluis` and mount `kv`, in the namespace. A consumer on layout v4 gets `read` on its own `kv/data/sluis/external/<kind>/<id>` only.

```hcl
# what sluis writes and reads back
path "kv/data/sluis/private/credentials/*"     { capabilities = ["create", "read", "update"] }
path "kv/metadata/sluis/private/credentials/*" { capabilities = ["list", "delete"] }
# layout v3's exports: only read and deleted by a migration
path "kv/data/sluis/export/*"     { capabilities = ["create", "read", "update"] }
path "kv/metadata/sluis/export/*" { capabilities = ["list", "delete"] }
# List("") (every secret) lists the two directories themselves; List of a
# prefix under credentials/ or export/ needs only the lines above
path "kv/metadata/sluis/private" { capabilities = ["list"] }
path "kv/metadata/sluis/private/" { capabilities = ["list"] }
path "kv/metadata/sluis/export"  { capabilities = ["list"] }
path "kv/metadata/sluis/export/" { capabilities = ["list"] }
# only if configuration secrets are read from OpenBao
path "kv/data/sluis/private/config/*" { capabilities = ["read"] }
```

## Example

The `k8s-aws` preset with KMS-wrapped signing and OpenBao secrets. The pod's AWS role comes from EKS Pod Identity or, with `serviceAccount.awsIdentity: irsa`, from `awsRoleArn`.

```yaml
config:
  issuerURL: https://access.example
  preset: k8s-aws
  signingKey:
    file: null                      # no key file: KMS holds the keys
    kmsWrapped:
      keyId: alias/sluis-signing
      region: eu-west-1
      stateSecret: issuer/state-secret      # a name in `secrets` below
      rotateEvery: 24h              # the rotation alert fires at 26h
  ports:
    dynamodb: {table: sluis, region: eu-west-1}
    blob: {adapter: s3, s3: {bucket: sluis-blobs, region: eu-west-1}}
  adapters:
    secrets:
      adapter: openbao
      settings:
        address: https://openbao.example
        caFile: /var/run/access-issuer/openbao-ca/ca.pem     # exports.openbao.caBundle
        namespace: staging
        mount: kv
        root: sluis
        auth:
          method: jwt
          mount: jwt-staging
          role: sluis
          tokenFile: /var/run/openbao/token                  # exports.openbao.token.audience
  audit: {writer: https://audit.example:8443}
secrets:
  - {name: issuer/state-secret, secretName: sluis-inputs, key: state-secret}
serviceAccount:
  awsIdentity: pod-identity          # or irsa, with awsRoleArn
exports:
  openbao:
    caBundle: |
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
    token: {audience: openbao-staging}   # a ServiceAccount token projected for the jwt login
```
