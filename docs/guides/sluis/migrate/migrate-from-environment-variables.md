# Migrate from environment variables

## Purpose

Move a deployment that configures `sluis` with environment variables (and the chart's old flat values) to the
configuration file, the only thing that configures it since v1.52.4.

## Preconditions

- The deployment runs a release that still starts; the binary refuses a retired variable by name, which is the checklist.
- The values or manifests that set the variables, the secrets they carried, and the right to change the policy.

## Before you start

- **A retired variable that is set stops the process at start.** The error names each variable and the key that replaces
  it. Nothing is ignored, so a half-migrated deployment fails loudly rather than running with a default
  (`internal/config/retired.go`).
- **A secret does not go in the file.** `VALKEY_PASSWORD`, `ADMIN_PASSWORD`, `OAUTH_CLIENT_SECRET` and the others become
  the name of a secret (`valkey/password`) that `secrets.source` delivers ([secrets](../../../reference/sluis/secrets.md)).
  A secret written into a document does not match the schema.
- **`OTEL_*` and the pod's own `NAMESPACE` and `POD_NAME` are not retired.** The platform sets them.
- **Preview before every apply, and read the preview.** On Kubernetes `helm template` shows the rendered ConfigMap; a key
  the chart has removed is refused at render, naming it.
- **Policy-like variables moved to the policy document**, not the service document: `GITHUB_OWNERS`,
  `GITHUB_RUNNER_TIERS`, the catalogue files, `ENABLED_ORGS`, `ENABLED_WORKSPACES`, the exchange files.

## Steps

### 1. List what is set

**Run** read the variables from the Deployment, the Compose file or the unit
(`kubectl -n <namespace> get deploy <name> -o jsonpath='{.spec.template.spec.containers[*].env[*].name}'`), or start the
binary once: it prints every retired variable that is set.

**Expect** a list of names such as `ISSUER_URL`, `VALKEY_ADDRESS`, `SIGNING_KEY_FILE`.

**Verify** each name is in [retired environment variables](../../../reference/sluis/configuration.md#retired-environment-variables).

**Rollback**: none, because nothing changed.

### 2. Write the service document

**Run** write `sluis.yaml` with `apiVersion: sluis.truvity.github.io/sluis/v3`, one key per variable, using the tables in
[retired environment variables](../../../reference/sluis/configuration.md#retired-environment-variables): for example `ISSUER_URL` is
`issuerURL`, `PORT` is `listen.address`, `VALKEY_ADDRESS` is `valkey.address`. A variable the table sends to the policy
document (`GITHUB_OWNERS`, `ENABLED_ORGS`, ...) goes into the policy document
([the policy document](../../../reference/sluis/policy-document.md)).

**Expect** a document the schema accepts.

**Verify** `sluis serve --config sluis.yaml` validates before anything starts: an unknown key, a missing required key or a
value of the wrong type refuses to start and names the path.

**Rollback**: none, because the file is not in use yet.

### 3. Name the secrets

**Run** for each secret variable pick its name from [the names](../../../reference/sluis/secrets.md#the-names) and deliver it: on
Kubernetes a `secrets` entry `{name: valkey/password, secretName: <the Secret>, key: <its key>}` with
`config.secrets: {source: file, root: /var/run/sluis/secrets}`; on AWS an SSM parameter under the instance's root.

**Expect** `valkey.passwordSecret: valkey/password` and friends in the document, and no secret value.

**Verify** start; a name that is not delivered stops the start, naming the name and where it was looked for.

**Rollback**: none, because only names and deliveries were added.

### 4. For the chart, move the values

**Run** move each old flat value to its place under `config`, using this table:

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
| `valkey.passwordSecret.name` / `.key` | `config.valkey.passwordSecret: valkey/password` and a `secrets` entry `{name: valkey/password, secretName, key}` |
| `oauthClient.secret.name` / `.keys.*` | `config.oauthClient.provider` and `secrets` entries `providers/google/<provider>/client-id` and `client-secret` (`config.oauthClient.{secretName,idKey,secretKey}` name the Secret the console shows) |
| `github.owners` | `policy.exchange.github.owners` |
| `githubRunnerApps.tiers` | `policy.apps.github.runnerTiers` |
| `console.origin`, `console.client` | `config.console.origin`, `config.console.client` (`console.mount` stays: it is the route's) |
| `signingKey.rotation.*` | `config.signingKey.pollInterval`, `.activationDelay`, `.overlap` |
| `audit.writer`, `.query`, `.audience`, `.forwardedForTrustedHops` | `config.audit.writer`, `.queryURL`, `.audience`, `.forwardedForTrustedHops` (`audit.token.*` stays: it is the chart's) |
| `controllerGithub.interval`, `controllerGithub.actsIn` | `config.controllers.github.interval`, `policy.controllers.github.enabledOrgs` |
| `controllerSlack.interval`, `controllerSlack.actsIn` | `config.controllers.slack.interval`, `policy.controllers.slack.enabledWorkspaces` |
| `controllerGithub.*`, `controllerSlack.*` | removed in v1.63: [`config.controllers`](../../../reference/sluis/configuration.md#controllers-the-github-and-slack-controllers) |
| `telemetry.otlpEndpoint` | `telemetry.otlp.endpoint` ([chart values](../../../reference/sluis/chart-values.md)) |

The old values are removed, not aliased: an old key is refused at render (`values.schema.json` names it), so a values file
that was not migrated fails `helm template` and not a rollout. The paths the config names are where the chart mounts what
it renders, and the chart refuses a config that names another; the defaults in `values.yaml` carry them, so a values file
states only what it changes.

**Expect** `helm template` to render without a refusal.

**Verify** the rendered `<release>-config` ConfigMap holds the document you wrote.

**Rollback**: put the previous values file back.

### 5. Roll out

**Run** `helm upgrade` (or restart the process with `--config <file>`, or set `SLUIS_CONFIG`).

**Expect** the pods start with no refusal in the log.

**Verify** `/readyz` is ready; a sign-in and a token exchange work.

**Rollback**: `helm rollback`.

## Afterwards

- Remove the old variables from every place that set them (a Compose file, a unit, a CI job), or the next start refuses.
- On AWS Lambda the function's own variables (`SLUIS_CONFIG_FILE`, `SLUIS_SECRET_FILES`, `SLUIS_ROLE`) are retired too: the
  Pulumi library of the same version sets `SLUIS_CONFIG` ([AWS Lambda](../../../reference/sluis/lambda.md#package)).
