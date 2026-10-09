# Secrets

A document names a secret; `secrets` says how the name is delivered. Keys ending in `Secret` hold a name (`valkey.passwordSecret: valkey/password`), `File` and `Dir` keys a path. No key takes a value ([model](../../concepts/sluis/configuration.md#a-secret-is-named-never-written)).

## The sources

A name is `/`-separated segments of letters, digits, `.`, `_` and `-`.

```yaml
secrets:
  source: ssm          # env | file | ssm
  root: /sluis/example    # file: a directory; ssm: the installation's root
  region: eu-west-1    # ssm only
  refresh: 1m          # ssm only
  kmsKeyId: alias/example   # ssm only: the key the service's own writes are encrypted with
  layout: v4           # ssm only: v4 (the only value and the default), see "SSM layout"
  grace: 24h           # ssm only: how long a rotated client secret's previous value is accepted
```

| `source` | A name is delivered as | Read |
|---|---|---|
| `env` (the default) | the variable `SLUIS_SECRET_<NAME>`: the name upper-cased, every character that is not a letter or a digit an underscore (`valkey/password` is `SLUIS_SECRET_VALKEY_PASSWORD`). Two names may not share a variable | once, at start; for a local run |
| `file` | the file `<root>/<name>` | on every use, so a rotated Secret the platform mounts takes effect without a restart. The chart's `secrets` value projects each name as a file under `/var/run/sluis/secrets` |
| `ssm` | the SecureString `<root>/internal/config/<name>` of AWS SSM Parameter Store | that one parameter, decrypted, on first use and again once `refresh` (`1m`) has passed. Nothing is read at start |


An undelivered name stops the start and appears in the error, never its value. Kubernetes defaults to `file`, Lambda to `ssm`. See [OpenBao](openbao-secrets-adapter.md).

## The names

| Name | Held by | What it is |
|---|---|---|
| `clients/<client-id>/secret` | a confidential client of the policy | the client's secret. The service reads one per confidential client of the policy it loaded |
| `providers/google/<provider>/client-id`, `.../client-secret` | `oauthClient.provider` | the Google OAuth client registered for the directory |
| `issuer/state-secret` | `signingKey.kms.stateSecret`, `signingKey.kmsWrapped.stateSecret` | the sign-in state secret |
| `recovery/password` | `recovery.passwordSecret` | the recovery password |
| `directory/<id>/key` | `directory.workspaces[].keySecret` | a declared workspace's service-account key |
| `valkey/password` | `valkey.passwordSecret` | the shared store's password |

## SSM layout

Layout v4 ([ADR 0041](../../decisions/0041-the-secret-contract.md)) keeps each value once, in two namespaces of the root. `secrets.root` is `/sluis/<instance>` (lower-case letters, digits, dashes):

```text
/sluis/<instance>/internal/config/<name>                   what an operator seeds: the names above
/sluis/<instance>/internal/credentials/<kind>/<id>/<ref>   what sluis writes: the credentials of the Secrets port
/sluis/<instance>/external/<kind>/<id>                     one typed document per address: the public contract
```

Only sluis reads `internal/`. `external/` is granted per consumer on exact addresses. The `ssm` Secrets adapter takes the same root; another is refused. `private`, `export`, `internal` and `external` are reserved names.

IAM per function: [AWS Lambda](lambda.md#iam-one-role).

`secrets.layout` (`ssm` only) is `v4`, which is also the default. Layouts v3 (`private/` and `export/`) and `transition` were removed in v1.75: an installation still on one runs v1.74.x and `sluis migrate secrets-layout` first, then upgrades.

### The external documents

Every field is a JSON string. A breaking change is a new address, `external/<kind>.v2/<id>`. Schemas: `schemas/external/`; goldens: `internal/secretstore/testdata/`.

| Kind | Address | Fields | Schema |
|---|---|---|---|
| `oidc/v1` | `external/oidc/<client>` | `schema`, `client-id`, `client-secret` | [`oidc.v1.schema.json`](../../../schemas/external/oidc.v1.schema.json) |
| `github/v1` | `external/github/<app>`; a runner App is `external/github/runner-<tier>-<org>` | `schema`, `app_id`, `installation_id`, `private_key` | [`github.v1.schema.json`](../../../schemas/external/github.v1.schema.json) |
| `slack/v1` | `external/slack/<app>` | `schema`, `bot_token` | [`slack.v1.schema.json`](../../../schemas/external/slack.v1.schema.json) |
| `cloudflare/v1` | `external/cloudflare/<preset>` | `schema`, `expires_on`, and `token` or `access_key_id`, `secret_access_key`, `endpoint`. For R2 the access key id is the token's id and the secret is the hex SHA-256 of its value | [`cloudflare.v1.schema.json`](../../../schemas/external/cloudflare.v1.schema.json) |

```json
{"schema":"oidc/v1","client-id":"example-rp","client-secret":"example-secret-value"}
```

A catalogue GitHub App's id may not begin `runner-`. A consumer reads a field with `remoteRef: {key: <address>, property: <field>}`.

### What each value is

| Value | Address |
|---|---|
| The names above (`secrets.source: ssm`) | `<root>/internal/config/<name>` |
| The console session key, directory credentials, links, organisations' keys, the link App, Slack workspaces | `<root>/internal/credentials/<kind>/<id>/<ref>`, a `{"value": "<base64>"}` document |
| A generated client's secret | the `oidc/v1` document `external/oidc/<id>`; a rotation is one write, and the previous secret is the document's previous revision while the current one is younger than `secrets.grace`. A rotation with no overlap refuses the old secret at once |
| A confidential client's secret an operator seeded | the same `oidc/v1` document |
| An installed runner App, or a catalogue App with `export: true` | the `github/v1` document `external/github/<app>`, ids from the App's record; a pending App's key stays internal until it is installed |
| A catalogue Slack App | the bot token is the `slack/v1` document `external/slack/<id>`; the client secret stays internal |

A failing client secret is rechecked once against a fresh read (once per five seconds per client).
