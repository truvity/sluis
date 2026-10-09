# Migrate from environment variables

Move a deployment that configures `sluis` with environment variables, or the chart's old flat values, to the configuration file. The file is the only configuration since v1.52.4.

## Before you start

- Run a release that still starts. The binary refuses a retired variable by name, and the error names the key that replaces it.

- Secrets do not go in the file. Each becomes a name, such as `valkey/password`, that `secrets.source` delivers ([secrets](../../../reference/sluis/secrets.md)).

- `OTEL_*`, `NAMESPACE` and `POD_NAME` are not retired.

- Policy-like variables go in the policy document: `GITHUB_OWNERS`, `GITHUB_RUNNER_TIERS`, the catalogue files, `ENABLED_ORGS`, `ENABLED_WORKSPACES`, the exchange files.

- On Kubernetes, `helm template` refuses a removed key at render and names it.

## Steps

### 1. List what is set

```sh
kubectl -n <namespace> get deploy <name> -o jsonpath='{.spec.template.spec.containers[*].env[*].name}'
```

Read the variables from the Deployment, Compose file or unit, or start the binary once: it prints every retired variable that is set. Match each name against [retired environment variables](../../../reference/sluis/configuration.md#retired-environment-variables).

### 2. Write the service document

Write `sluis.yaml` with `apiVersion: sluis.truvity.github.io/sluis/v3`, one key per variable. `ISSUER_URL` is `issuerURL`, `PORT` is `listen.address`, `VALKEY_ADDRESS` is `valkey.address`. Variables the table sends to the policy go in the [policy document](../../../reference/sluis/policy-document.md).

`sluis serve --config sluis.yaml` validates before it starts and names the path of any bad key.

### 3. Name the secrets

Pick each name from [the names](../../../reference/sluis/secrets.md#the-names). On Kubernetes:

```yaml
secrets:
  - {name: valkey/password, secretName: <the Secret>, key: <its key>}
config:
  secrets: {source: file, root: /var/run/sluis/secrets}
  valkey: {passwordSecret: valkey/password}
```

On AWS use an SSM parameter under the instance's root. A name that is not delivered stops the start and says where it looked.

### 4. Move the chart values

| Old value | Now |
|---|---|
| `issuerURL` | `config.issuerURL` |
| `listeners.port`, `listeners.healthPort` | `config.listen.address`, `config.probes.address` |
| `logLevel` | `config.log.level` |
| `groupsScoping`, `cluster` | `config.groupsScoping`, `config.cluster` |
| `lifetimes.*` | `config.lifetimes.*` |
| `directory.store` | `config.store` |
| `directory.freshness.*`, `directory.sessionLifetime`, `directory.login` | `config.freshness.*`, `config.lifetimes.session`, `config.login.directory` |
| `recovery.enabled`, `.serviceAccountName`, `.audience` | `config.recovery.enabled`, `.serviceAccount`, `.audience`, with `config.inCluster: true` |
| `exchange.audience` | `config.exchange.audience` |
| `valkey.address`, `.tls`, `.cluster` | `config.valkey.address`, `.tls`, `.cluster` |
| `valkey.passwordSecret.name` / `.key` | `config.valkey.passwordSecret: valkey/password` and a `secrets` entry |
| `oauthClient.secret.name` / `.keys.*` | `config.oauthClient.provider` and `secrets` entries `providers/google/<provider>/client-id` and `client-secret`; `config.oauthClient.{secretName,idKey,secretKey}` name the Secret the console shows |
| `github.owners` | `policy.exchange.github.owners` |
| `githubRunnerApps.tiers` | `policy.apps.github.runnerTiers` |
| `console.origin`, `console.client` | `config.console.origin`, `config.console.client`; `console.mount` stays |
| `signingKey.rotation.*` | `config.signingKey.pollInterval`, `.activationDelay`, `.overlap` |
| `audit.writer`, `.query`, `.audience`, `.forwardedForTrustedHops` | `config.audit.writer`, `.queryURL`, `.audience`, `.forwardedForTrustedHops`; `audit.token.*` stays |
| `controllerGithub.interval`, `.actsIn` | `config.controllers.github.interval`, `policy.controllers.github.enabledOrgs` |
| `controllerSlack.interval`, `.actsIn` | `config.controllers.slack.interval`, `policy.controllers.slack.enabledWorkspaces` |
| other `controllerGithub.*`, `controllerSlack.*` | removed in v1.63: [`config.controllers`](../../../reference/sluis/configuration.md#controllers-the-github-and-slack-controllers) |
| `telemetry.otlpEndpoint` | `telemetry.otlp.endpoint` ([chart values](../../../reference/sluis/chart-values.md)) |

The chart refuses an old key at render, and `values.schema.json` names it. `values.yaml` carries the default mount paths, so your file states only what it changes. Check the rendered `<release>-config` ConfigMap holds your document.

### 5. Roll out

Run `helm upgrade`, or restart with `--config <file>` or `SLUIS_CONFIG`. Check `/readyz` and one sign-in and token exchange. To undo, `helm rollback`.

## Afterwards

- Remove the old variables from every Compose file, unit and CI job, or the next start refuses.

- On AWS Lambda the Pulumi library of the same version replaces `SLUIS_CONFIG_FILE`, `SLUIS_SECRET_FILES` and `SLUIS_ROLE` with `SLUIS_CONFIG` ([AWS Lambda](../../../reference/sluis/lambda.md#version-coupling)).
