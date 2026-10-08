# The OpenBao Secrets adapter

`adapters.secrets: {adapter: openbao}` keeps the Secrets port (the credentials sluis writes and reads back, and the
exports) in an OpenBao KV version 2 mount, in the layout of [SSM v3](secrets.md#ssm-layout-v3). It needs no
`platform.openbao` answer. The configuration secrets can stay where they are (`secrets.source: file`, the chart
projecting them): the source delivers the inputs, the adapter holds what sluis writes. The adapter's place among the
others is [adapters](adapters.md); the policy it needs is at the end of this page. Source: `internal/port/openbao`.

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
| `address` | the OpenBao, `https://host[:port]`, no path. **https only**: a token and a login JWT cross the connection, and a redirect is never followed |
| `caFile` | PEM authorities the server's certificate is verified against, instead of the system's. TLS verification is always on: there is no insecure flag |
| `namespace` | the OpenBao namespace the secrets live in; empty is the root namespace |
| `mount` | the KV version 2 mount; `kv` |
| `root` | required, no default: the installation's key hierarchy under the mount |
| `auth.method`, `.mount`, `.role` | the login: `POST auth/<mount>/login {role, jwt}`; the token it returns is kept until 80% of its lease has passed, per namespace |
| `auth.tokenFile` | the JWT; `jwt` requires it, `kubernetes` defaults to the pod's own token |

## The root and the layout

OpenBao namespaces already separate installations, so the recommendation is root `sluis` in the installation's own
namespace: namespace `staging`, mount `kv`, and

```text
kv/sluis/private/config/<name>                       what an operator seeds (read only if used)
kv/sluis/private/credentials/<kind>/<id>/<ref>       what sluis writes and reads back
kv/sluis/export/<path>                               what sluis copies out, for consumers
```

`sluis/<instance>` is the option for an OpenBao without a namespace per installation (`kv/sluis/acme/export/...`).
Either way `private` and `export` are reserved: a root with such a segment is refused at start. `secrets.root` is not used
by this adapter (with `source: file` it is a directory).

## Values

A secret is one KV key with one field, `value` (text) or `value_b64` (bytes that are not UTF-8), so
`bao kv put kv/sluis/private/config/<name> value=...` seeds one. A JSON object under `export/` (what layout v3's exports wrote) is stored as the properties themselves, one field
each; `Get` puts the object back together. `PutIfVersion` is KV's check-and-set, atomic on the server. The mount must not set `cas_required`, or
unconditional writes are refused. A value is never logged, and an error names the operation, the path and the status only.

## The policy it needs

Least privilege, with root `sluis` and mount `kv`, in the namespace:

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

`delete` and `list` on the
metadata are for `Delete` and `List`, and may be left out when nothing deletes or lists. On layout v4 the same grants are on `internal/` and `external/`, and a consumer gets `read` on the exact
`kv/data/sluis/external/<kind>/<id>` it needs and nothing else.
