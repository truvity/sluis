# Migrate from the access-issuer chart

## Purpose

Move a Kubernetes installation from the `access-issuer` chart and its three images (`access-issuer`, `github-roster`,
`slack-roster`) to the `sluis` chart and its one image, without renaming an object, losing State or changing the signing
key. [ADR 0032](../../../decisions/0032-one-configuration-file-one-binary-one-chart.md) is the decision (v1.53.0); nothing is
kept as an alias.

## Preconditions

- The installation is configured by a configuration file: the environment variables went in v1.52.4 and the binaries
  refuse them. If it still sets any, do [migrate from environment variables](migrate-from-environment-variables.md) first.
- `kubectl` and `helm` access, and the installation's current values.
- Ability to change the policy and, if you run one, the audit installation's `workloadIdentity` map before the upgrade
  (the controllers' accounts change: step 5).

## Before you start

- **The chart's name is part of every object's name.** The full name is `<release>-<chart>`, or the release alone when it is
  the chart's name. Moving to the `sluis` chart without a value renames every object: the Deployment, the Service, the
  ServiceAccounts and their Roles, every ConfigMap and the Secrets the chart generates, the cert-manager signing key's
  Secret among them (a new key). `config.release`, the name the service writes its objects under (`<release>-github-orgs`,
  `<release>-github-apps`, `<release>-slack-workspaces`, the status ConfigMaps and the links Secret), must be the full
  name, so it moves too and the service would start with an empty store. Step 3 prevents it.
- **A Deployment's selector is immutable.** `nameOverride: access-issuer` keeps `app.kubernetes.io/name`, so the selector
  does not change and the upgrade is a rollout. Without it Helm cannot upgrade the Deployment in place.
- **`config.release` used to default to `access-issuer` and is now `sluis`.** An installation that relied on the default
  must write `release: <the full name>`, or the render is refused and says what to write.
- **Preview before every apply, and read the preview.** `helm template` first: a misspelt key, a values key the chart has
  removed, or a `release` that is not the full name is refused at render, with the value to write. Expect the controllers'
  Deployments, ServiceAccounts, ConfigMaps and PodDisruptionBudgets (`<release>-github-roster`, `<release>-slack-roster`)
  to be deleted: since v1.63 the controllers run in the one process ([upgrade to v1.63](../upgrade/v1.63.md)).
- **The old images and chart are no longer published**, so a pin on one of them stops resolving at the next pull.

## Steps

### 1. Write down what the names are now

**Run** `kubectl -n <namespace> get deploy` and note the Deployment's name (the old full name); read the current values
(`helm get values <release>`).

**Expect** one Deployment, plus `<name>-github-roster` and `<name>-slack-roster` if the controllers were on.

**Verify** the old full name is the Deployment's name without a suffix.

**Rollback**: none needed, because nothing changed.

### 2. Rename the controllers' values

**Run** in your values, delete `githubRoster.image` and `slackRoster.image` (a controller runs the chart's one `image`);
move each controller's settings into `config.controllers.github` and `config.controllers.slack` (present is on; the keys are
in [configuration](../../../reference/sluis/configuration.md#controllers-the-github-and-slack-controllers)); fold anything the
controllers had under their own `config` (`policy`, `release`, `log`, `ports`, `platform`, `preset`, `adapters`, `audit`,
`probes`) into the one `config`. Delete the `githubRoster` and `slackRoster` blocks: an old value is refused at render. If
the values set `image.repository`, change it to `ghcr.io/truvity/sluis/sluis` (or your mirror's name for it).

**Expect** a values file with one `config` and no controller blocks.

**Verify** `helm template` accepts it.

**Rollback**: keep the old values file.

### 3. Keep every name

**Run** add to the values:

```yaml
nameOverride: access-issuer
fullnameOverride: <the old full name>   # what `kubectl get deploy` showed
config:
  release: <the same>                    # unchanged: it is what it was
```

Write `release` out even if the values left it unset.

**Expect** `helm template` renders every object under its old name, with the old Deployment selector.

**Verify** `helm template ... | grep -E '^  name: ' | sort -u` lists the names you saw in step 1, and no `-github-roster`
or `-slack-roster` Deployment.

**Rollback**: remove the three values; the render is a new installation beside the old, with a new store and a new signing
key, which is what the step prevents.

### 4. Point the chart reference at the `sluis` chart

**Run** change the reference to `oci://ghcr.io/truvity/charts/sluis` at the version you are moving to: the Helm release,
an Argo CD `Application`'s `chart:`, a `HelmRelease`. Anything outside the chart that ran `access-issuer`, `github-roster`
or `slack-roster` by name (a `docker run`, a Compose file, a systemd unit) runs `sluis serve` with the same `--config`
file; `sluis controller github|slack` still run, deprecated.

**Expect** the preview of step 3 plus a new image.

**Verify** `helm template` once more with the final reference.

**Rollback**: put the old reference back.

### 5. Admit the one ServiceAccount, then roll out

**Run** ship the policy change first: the policy's `exchange` and the audit installation's `workloadIdentity` map must
admit the release's ServiceAccount where they admitted `<release>-github-roster` and `<release>-slack-roster`. Then
`helm upgrade`.

**Expect** the pods restart onto the one image; the controllers' objects are deleted; the config file is mounted at
`/etc/sluis/config.yaml`.

**Verify** `kubectl -n <namespace> rollout status deploy/<old full name>`; `/readyz` is ready (it is ready only once each
controller has begun); a sign-in works; the controllers' next pass is not refused at the console.

**Rollback**: `helm rollback`. Nothing was renamed, so the old objects are intact; the controllers' Deployments return with
the old chart.

## Afterwards

- Telemetry service names: the series report as one process; a dashboard or alert that selects `github-roster` or
  `slack-roster` must select the process's name ([upgrade to v1.63](../upgrade/v1.63.md)).
- What the move leaves under the old spelling on purpose: the mount paths under `/var/run/access-issuer/`, the recovery
  ServiceAccount's default name, and the old chart's labels.
- Tell whoever pins an image or a chart by name.
