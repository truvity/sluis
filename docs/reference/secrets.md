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
  layout: v3           # ssm only: v3 (default) | transition | v4, see "SSM layout v4"
  grace: 24h           # ssm, transition or v4: how long a rotated client secret's previous value is accepted
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

## SSM layout v4

Layout v4 (ADR 0041, the secret contract) keeps every value once, in one of two namespaces of the
same root:

```text
/sluis/<instance>/internal/config/<name>                   what an operator seeds: the names above (was private/config/)
/sluis/<instance>/internal/credentials/<kind>/<id>/<ref>   what sluis writes (was private/credentials/)
/sluis/<instance>/external/<kind>/<id>                     one typed document per address: the public contract
```

`internal/` is read by sluis only and is never granted to anyone. `external/` holds what something outside sluis reads,
whoever wrote it, and is granted to each consumer on its exact addresses. `internal` and `external` join `private` and
`export` as names an instance may not take. The exports copies of layout v3 do not exist in v4: sluis reads an external
value from its external address, so what a consumer reads is what sluis uses.

`secrets.layout` says which layout an installation is on: `v3` (the default), `transition` (read v4 first and fall back to
v3; every write goes to v4 and then to v3) or `v4`. It is changed by `sluis migrate secrets-layout` ([the steps](../how-to/migrate-secrets-layout.md)), never by a start-time
upgrade. The key is accepted by the `ssm` source only.

### The external documents

Every field is a JSON string, so both backends hand a consumer the same text. `schema` names the kind and version; a
breaking change is a new address, `external/<kind>.v2/<id>`, written beside the old one. Adding a field is not breaking.
Each kind has a JSON Schema under `schemas/external/` and a golden document in
`internal/secretstore/testdata/`; changing a field fails the test unless the schema version moves.

| Kind | Address | Fields | Schema |
|---|---|---|---|
| `oidc/v1` | `external/oidc/<client>` | `schema`, `client-id`, `client-secret` | [`oidc.v1.schema.json`](../../schemas/external/oidc.v1.schema.json) |
| `github/v1` | `external/github/<app>`; a runner App is `external/github/runner-<tier>-<org>` | `schema`, `app_id`, `installation_id`, `private_key` | [`github.v1.schema.json`](../../schemas/external/github.v1.schema.json) |
| `slack/v1` | `external/slack/<app>` | `schema`, `bot_token` | [`slack.v1.schema.json`](../../schemas/external/slack.v1.schema.json) |

```json
{"schema":"oidc/v1","client-id":"example-rp","client-secret":"example-secret-value"}
```

A catalogue GitHub App's id may not begin `runner-`: that prefix names a runner App's document. A consumer reads one
field with External Secrets' `remoteRef: {key: <address>, property: <field>}` and names the key of its own Secret
itself.

The Go side is `internal/secretstore`: `Internal` and `External` over a `state.Store`, with a typed `state.Value` for each
address. The token check of a rotating client secret reads `External.OIDC(client).Rotating(grace)`.

### What follows from the layout

With `secrets.layout: transition` or `v4`, the service reads and writes through the layout; its callers keep their paths.

| Value | `v3` | `v4` |
|---|---|---|
| The names above (`secrets.source: ssm`) | `<root>/private/config/<name>` | `<root>/internal/config/<name>`; `transition` reads v4 and falls back to v3 |
| The console session key, directory credentials, links, organisations' keys, the link App, Slack workspaces | `<root>/private/credentials/<kind>/<id>/<ref>` | `<root>/internal/credentials/<kind>/<id>/<ref>`, a `{"value": "<base64>"}` document |
| A generated client's secret | `private/credentials/oidc-client/<id>/secret`, a record with `previous` and `previous_valid_until` | the `oidc/v1` document `external/oidc/<id>`; a rotation is one write, and the previous secret is the document's previous revision while the current one is younger than `secrets.grace` |
| A confidential client's secret an operator seeded | `private/config/clients/<id>/secret` | the same `oidc/v1` document |
| An installed runner App, or a catalogue App with `export: true` | `private/credentials/github-…/<ref>`, copied to `export/…` by the exports controller | the `github/v1` document `external/github/<app>`, ids from the App's record; a pending App's key stays internal until it is installed |
| A catalogue Slack App | `private/credentials/slack-app/<id>/<ref>`, client secret and bot token | the bot token is the `slack/v1` document `external/slack/<id>`; the client secret stays internal |

In `transition` every write goes to v4 and then to v3, and a read tries v4 first. In `v4` the exports controller's copies
under `export/` are still written until the exports are retired. The token endpoint checks a client's secret against the
cached pair; a secret that matches neither is checked once more against a fresh read before it is refused (at most once
every five seconds per client), so a replica that cached the pair before a rotation does not refuse the old secret during
the overlap. A rotation with no overlap writes the new secret twice, so the previous revision is the current secret
itself and the old one is refused at once. An orphaned client's mark is derived from the policy and is not stored.
