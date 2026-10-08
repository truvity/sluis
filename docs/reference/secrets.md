# Secrets: names, sources and the SSM layout

A document never holds a secret. It gives a secret's **name**, and the service document's `secrets` says how a name is
delivered. A key ending in `Secret` holds a name (`valkey.passwordSecret: valkey/password`); a key ending in `File` or
`Dir` holds a path to something that is not a secret, or to a key the platform mounts. A secret written in a document is
refused: there is no key to put it in, and a URL or address with a password in it does not match the schema. Why:
[Configuration: the model](../explanation/configuration.md#a-secret-is-named-never-written). Source:
`schemas/config/sluis.schema.json`.

## The sources

A name is a path of segments of letters, digits, `.`, `_` and `-`, separated by `/`.

```yaml
secrets:
  source: ssm          # env | file | ssm
  root: /sluis/example    # file: a directory; ssm: the installation's root
  region: eu-west-1    # ssm only
  refresh: 5m          # ssm only
  kmsKeyId: alias/example   # ssm only: the key the service's own writes are encrypted with
```

| `source` | A name is delivered as | Read |
|---|---|---|
| `env` (the default) | the variable `SLUIS_SECRET_<NAME>`: the name upper-cased, every character that is not a letter or a digit an underscore (`valkey/password` is `SLUIS_SECRET_VALKEY_PASSWORD`). Two names may not share a variable | once, at start; for a local run |
| `file` | the file `<root>/<name>` | on every use, so a rotated Secret the platform mounts takes effect without a restart. The chart's `secrets` value projects each name as a file under `/var/run/sluis/secrets` |
| `ssm` | the SecureString `<root>/private/config/<name>` of AWS SSM Parameter Store | every parameter under `<root>/private/config/` at once (decrypted, paged), and again once `refresh` (`5m`) has passed |


A name that is not delivered stops the start, naming the name and where it was looked for; a value is never in an error
or a log line. On Kubernetes the usual source is `file`, the chart projecting each Secret key a `secrets` entry names (the
chart's `config` defaults to it); on AWS Lambda it is `ssm`. A secret held in OpenBao instead is the
[OpenBao Secrets adapter](openbao-secrets-adapter.md), which holds what sluis writes while `secrets.source` delivers the inputs.

## The names

| Name | Held by | What it is |
|---|---|---|
| `clients/<client-id>/secret` | a confidential client of the policy | the client's secret. The service reads one per confidential client of the policy it loaded |
| `providers/google/<provider>/client-id`, `.../client-secret` | `oauthClient.provider` | the Google OAuth client registered for the directory |
| `issuer/state-secret` | `signingKey.kms.stateSecret`, `signingKey.kmsWrapped.stateSecret` | the sign-in state secret |
| `recovery/password` | `recovery.passwordSecret` | the recovery password |
| `directory/<id>/key` | `directory.workspaces[].keySecret` | a declared workspace's service-account key |
| `valkey/password` | `valkey.passwordSecret` | the shared store's password |

## SSM layout v3

An installation has one root, `secrets.root`, which is `/sluis/<instance>` with `<instance>` the installation's name, so
two installations share an account without colliding. Under it:

```text
/sluis/<instance>/private/config/<name>                    what an operator seeds: the names above
/sluis/<instance>/private/credentials/<kind>/<id>/<ref>    what sluis writes: the credentials of the Secrets port
/sluis/<instance>/export/<path>                            what sluis copies out, for consumers
```

The `ssm` Secrets adapter takes its root from the same `secrets.root` (`source: ssm`): naming another root under
`adapters.secrets.settings` is refused, and with neither the start is refused. A v1 document that names no root keeps
`/sluis` (layout v2) until it moves.

`private` and `export` are reserved: an instance may not be named either, since `/sluis/private/...` would then be the
tree of another installation, and a root with such a segment is refused at start. An instance is lower-case letters,
digits and dashes.

The IAM boundary follows the tree. A function that only runs a controller needs `private/credentials` and `export` and
never `private/config`; the one that signs tokens reads `private/config` as well ([AWS Lambda](lambda.md#iam-one-role)).
The paths in [storage layout](storage-layout.md) are v2's; v3 puts the instance between `/sluis` and `private`.

Moving an installation from v2 to v3 is `sluis migrate ssm-layout --to-root /sluis/<instance>` (add `--dry-run` first; the
report is JSON and names parameters, never a value), then `sluis migrate` for the credentials. It writes where the
destination is absent, refuses one that holds another value unless `--overwrite`, deletes nothing, and encrypts each copy
with the source parameter's own KMS key unless `--kms-key` names one. The steps, with their checks and rollbacks:
[upgrade to v1.62](../how-to/upgrade/v1.62.md).
