# Migrate from the legacy chart

Move a Kubernetes installation from the legacy `access-issuer` chart to the `sluis` chart, keeping every object name, the State and the signing key. The legacy chart and its three images are no longer published.

## Before you start

- Configure the installation by file, not environment variables: [migrate from environment variables](migrate-from-environment-variables.md) first.

- Have `kubectl`, `helm` and the current values.

- Without step 3 the upgrade renames every object, makes a new signing key and starts with an empty store.

- A Deployment's selector is immutable. `nameOverride: access-issuer` keeps it, so the upgrade is a rollout.

- `config.release` must be the full name. The render says what to write.

- Run `helm template` before each upgrade. Expect the two controller Deployments and their objects to be deleted: the controllers run in the one process ([v1.63](../upgrade/v1.63.md)).

## Steps

### 1. Record the names

```sh
kubectl -n <namespace> get deploy
helm get values <release>
```

The old full name is the Deployment's name without a suffix.

### 2. Rename the controllers' values

Delete the two legacy controller blocks (the values keys ending in `Roster`) and their `image` keys. Move their settings into `config.controllers.github` and `config.controllers.slack` ([keys](../../../reference/sluis/configuration.md#controllers-the-github-and-slack-controllers)), and fold their own `config` into the one `config`. Set `image.repository` to `ghcr.io/truvity/sluis/sluis`, or your mirror's name.

### 3. Keep every name

```yaml
nameOverride: access-issuer
fullnameOverride: <the old full name>   # what `kubectl get deploy` showed
config:
  release: <the same>                    # write it even if it was unset
```

```sh
helm template ... | grep -E '^  name: ' | sort -u
```

The list must match step 1 and show no controller Deployment.

### 4. Point at the `sluis` chart

Set the chart reference to `oci://ghcr.io/truvity/charts/sluis` in the Helm release, the Argo CD `Application`'s `chart:` or the `HelmRelease`. A `docker run`, Compose file or systemd unit runs `sluis serve` with the same `--config` file. Run `helm template` again. To undo, restore the reference.

### 5. Admit the one ServiceAccount, then roll out

Ship the policy change first: `exchange` and the audit `workloadIdentity` map must admit the release's ServiceAccount where they admitted the two controller accounts. Then:

```sh
helm upgrade <release> oci://ghcr.io/truvity/charts/sluis --version <version> -f values.yaml
kubectl -n <namespace> rollout status deploy/<old full name>
```

The config mounts at `/etc/sluis/config.yaml`. `/readyz` turns ready once each controller has begun. Check a sign-in and the next controller pass. To undo, `helm rollback`.

## Afterwards

- Dashboards and alerts that select a controller's old service name must select the process's ([v1.63](../upgrade/v1.63.md)).

- Mount paths under `/var/run/access-issuer/`, the recovery ServiceAccount's default name and the legacy chart's labels keep their old spelling.

Decided in: [0032](../../../decisions/0032-one-configuration-file-one-binary-one-chart.md).
