# sluis

sluis turns the proofs you already have into short-lived tokens under one policy file. audit keeps a tamper-evident record. One tag, one version.

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

## Who it is for

Platform teams that drive tokens, GitHub teams and Slack channels from one reviewed policy file. Applications that need an audit trail an auditor can verify.

## The model

A policy file maps directory groups and machine identities to groups. sluis mints tokens and reconciles GitHub and Slack from it; audit records what it did. See [concepts](docs/concepts/sluis/concepts.md).

## Install and a worked example

```sh
sluisctl render --installation installation.yaml --out rendered/
helm install sluis oci://ghcr.io/truvity/charts/sluis --version X.Y.Z --namespace sluis --create-namespace \
  --values values.yaml --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

### Try it without a credential

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

## Consumers

| Consumer | Uses |
|---|---|
| Estates | the Helm charts or the Pulumi libraries |
| Services | the Go module or `@truvity/sluis` |
| CI jobs | the GitHub Action and `sluisctl` |
| People | `sluisctl` |

## Neighbours

| Neighbour | Role |
|---|---|
| OpenBao | trusts sluis tokens and issues certificates; can be audit's key provider |
| Cloudflare | fronts the Lambda shape; can hold audit's archive on R2 |

## Documentation

| Topic | Where |
|---|---|
| The site | <https://truvity.github.io/sluis/> |
| Tutorials | [sluis](docs/get-started/sluis/README.md), [audit](docs/get-started/audit/README.md) |
| Every deliverable | [artifacts](docs/reference/sluis/artifacts.md) |
| Upgrades | the [CHANGELOG](CHANGELOG.md) links an upgrade page from each breaking entry |

## The rule that makes this repository public

Mechanism only: no account, hostname or secret path of an installation. [`hack/leak-canary.sh`](hack/leak-canary.sh) enforces it.

## Status

Used in production by its maintainers. audit may break in a minor, with a **Breaking:** entry. Report vulnerabilities as [SECURITY.md](SECURITY.md) says.

## Development

Run `devbox shell`, then `just check`. See [CONTRIBUTING](CONTRIBUTING.md).

## Releasing

By hand, as a pushed tag `vX.Y.Z`. See [CONTRIBUTING](CONTRIBUTING.md#releasing).

## Licence

MIT, see [LICENSE](LICENSE).
