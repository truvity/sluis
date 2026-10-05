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
      namespace: kernel                       # the OpenBao namespace
      mount: kv                               # the KV v2 mount (default kv)
      root: sluis                             # see below
      auth:
        method: jwt                           # jwt | kubernetes
        mount: jwt-kernel                     # the auth mount (default: the method's name)
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
namespace: namespace `kernel`, mount `kv`, and

```text
kv/sluis/private/config/<name>                       what an operator seeds (read only if used)
kv/sluis/private/credentials/<kind>/<id>/<ref>       what sluis writes and reads back
kv/sluis/export/<path>                               what sluis copies out, for consumers
```

`sluis/<instance>` is the option for an OpenBao without a namespace per installation (`kv/sluis/kernel/export/...`).
Either way `private` and `export` are reserved: a root with such a segment is refused at start. `secrets.root` is not used
by this adapter (with `source: file` it is a directory).

## Values

A secret is one KV key with one field, `value` (text) or `value_b64` (bytes that are not UTF-8), so
`bao kv put kv/sluis/private/config/<name> value=...` seeds one. An export of properties (the JSON object the secrets
export writes) is stored as the properties themselves, one field each, so a consumer's External Secrets reads
`property: botToken` of the key exactly as it does a copy made by `ports.export: openbao`; `Get` puts the object back
together. `PutIfVersion` is KV's check-and-set, atomic on the server. The mount must not set `cas_required`, or
unconditional writes are refused. A value is never logged, and an error names the operation, the path and the status only.

## An export in another namespace

An export entry's `namespace` is honoured on the default destination (`ports.export` unset): the same `export/<path>` of
the same installation is written in that namespace, over the same connection, with a login of its own there. A preview
runner App can thus land in `devel`:

```yaml
exports:
  - source: runner-app
    tier: preview
    org: truvity
    namespace: devel
    path: github-runner-app/preview/truvity   # kv/sluis/export/github-runner-app/preview/truvity in devel
```

A `403` is put down to the token (and costs one new login) only when the token is older than 30 seconds; a fresh
token's `403` is the policy's. After an empty listing or a delete of an absent key the adapter asks the server whether
the mount exists, and fails loudly with the mount and namespace if it does not (a wrong `mount` or `namespace` otherwise
reads as "nothing there"). The role must exist in that namespace too, with the policy below there. The `ssm` adapter has
no namespaces and refuses such an entry.

## The policy it needs

Least privilege, with root `sluis` and mount `kv`, in the namespace and again in every namespace an export names:

```hcl
# what sluis writes and reads back
path "kv/data/sluis/private/credentials/*"     { capabilities = ["create", "read", "update"] }
path "kv/metadata/sluis/private/credentials/*" { capabilities = ["list", "delete"] }
# the exports: written, never read by sluis for any other purpose
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

`read` on the export keys is the idempotence check (an identical write makes no new version); `delete` and `list` on the
metadata are for `Delete` and `List`, and may be left out when nothing deletes or lists. Consumers get `read` on
`kv/data/sluis/export/*` and nothing else.
