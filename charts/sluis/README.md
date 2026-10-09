# sluis chart

The chart deploys `sluis serve` as one Deployment from the image `ghcr.io/truvity/sluis/sluis`.
The process holds the issuer, the console and the GitHub and Slack controllers.

The chart is published to `oci://ghcr.io/truvity/charts/sluis` on every `v*` tag. The tag is the chart version.

```sh
helm install sluis oci://ghcr.io/truvity/charts/sluis \
  --namespace sluis --create-namespace \
  --set config.issuerURL=https://issuer.example
```

## Configuration

Pass the documents that `sluisctl render` writes as `documents.service` and `documents.policy`, and run `sluisctl render --check` in CI.
The chart does not re-validate them. Values mode (`config`, `policy`) is deprecated.
`values.schema.json` rejects an unknown top-level key.
See [chart values](../../docs/reference/sluis/chart-values.md) and [configuration](../../docs/reference/sluis/configuration.md).

## Input secrets

With `secrets.source: file` the chart projects each `secrets` entry as the file `<root>/<name>` at mode 0440.
The chart refuses a name the documents use and `secrets` does not declare.

```yaml
secrets:
  - {name: clients/argocd/secret, secretName: sluis-client-argocd, key: client-secret}
  - {name: providers/google/default/client-secret, secretName: sluis-google, key: client-secret}
  - {name: issuer/state-secret, secretName: sluis-inputs, key: state-secret}
```

## More than one replica

The chart refuses `replicaCount` above 1 unless `config.ports.adapter` is `dynamodb`.
See [high availability](../../docs/concepts/sluis/high-availability.md#the-controllers-in-the-one-process).

## Where to go next

- [Install with Helm](../../docs/guides/sluis/operate/install-with-helm.md)
- [Telemetry](../../docs/reference/sluis/telemetry.md#wiring-it-with-the-chart)
- [Secrets](../../docs/reference/sluis/secrets.md#the-external-documents)
- [GitHub Apps example set](../../docs/guides/sluis/connect/github-apps-catalogue.md#a-default-set), shipped as `examples/github-apps.yaml`
- [Slack controller](../../docs/guides/sluis/connect/slack-workspace.md#running-the-controller)
- [Back up and restore](../../docs/guides/sluis/operate/back-up-and-restore.md)
- [CHANGELOG](../../CHANGELOG.md) for upgrades
