# sluis

OpenID issuer and audit trail in one repository. sluis turns the proofs you already have into short-lived tokens and credentials under one policy file. audit keeps a tamper-evident record of what happened. One tag, one version.

[![CI](https://github.com/truvity/sluis/actions/workflows/ci.yaml/badge.svg)](https://github.com/truvity/sluis/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/truvity/sluis?include_prereleases)](https://github.com/truvity/sluis/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

| Deliverable | Published at |
|---|---|
| sluis and audit images | `ghcr.io/truvity/sluis/*`, `ghcr.io/truvity/audit/*` |
| Helm charts `sluis`, `audit` | `oci://ghcr.io/truvity/charts/sluis`, `oci://ghcr.io/truvity/charts/audit` |
| `sluisctl`, `audit` CLI, Lambda zips | the [GitHub release](https://github.com/truvity/sluis/releases) |
| Go modules (root, `storage`, `audit`, Pulumi libraries) | `github.com/truvity/sluis[/<dir>]`, tagged with the release |
| `@truvity/sluis`, `@truvity/audit`, `@truvity/audit-react` | GitHub Packages |
| GitHub Action | `truvity/sluis@<commit>` |

Every artifact and its location: [artifacts](docs/reference/sluis/artifacts.md).

## Try it without a credential

```sh
cat > /tmp/demo.yaml <<'YAML'
demo: true
store: memory
allowInsecure: true
issuerURL: http://localhost:8099
publicURL: http://localhost:8099/console
listen: {address: ":8099"}
probes: {address: ":7099"}
YAML
go run ./cmd/sluis serve --config /tmp/demo.yaml
```

Open `http://localhost:8099/console/` and choose *Continue with the demonstration directory*.

## Install

```sh
sluisctl render --installation installation.yaml --out rendered/
helm install sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis --create-namespace \
  --values values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

Tutorials: [sluis on Kubernetes](docs/get-started/sluis/kubernetes-aws.md), [sluis on AWS Lambda](docs/get-started/sluis/aws-lambda.md), [audit on Kubernetes](docs/get-started/audit/kubernetes.md) and [audit on AWS Lambda](docs/get-started/audit/aws-lambda.md).

## Documentation

The site is <https://truvity.github.io/sluis/>. The entry point in the repository is [docs/README.md](docs/README.md).
The [CHANGELOG](CHANGELOG.md) links an upgrade page from each breaking entry.

## Status

The maintainers use sluis in production. audit is stabilizing: a minor may break with a **Breaking:** entry, and a patch never does.
Report vulnerabilities as [SECURITY.md](SECURITY.md) describes.

## Contributing

Run `devbox shell`, then `just check`. See [CONTRIBUTING](CONTRIBUTING.md).
This repository is public and holds mechanism only: [`hack/leak-canary.sh`](hack/leak-canary.sh) enforces it.

## Licence

MIT, see [LICENSE](LICENSE).
