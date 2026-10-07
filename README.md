# sluis

[![CI](https://github.com/truvity/sluis/actions/workflows/ci.yaml/badge.svg)](https://github.com/truvity/sluis/actions/workflows/ci.yaml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

> Formerly **access-roster** (CLI `accessctl`, now `sluisctl`). What changed
> and what deliberately did not: [ADR 0035](docs/decisions/0035-renamed-to-sluis.md).

**The policy is the product.** sluis is one small OpenID provider and one policy file in git. The file turns the groups
your people already have in the corporate directory, and the identities your machines already hold (a GitHub Actions job,
a Kubernetes ServiceAccount), into one vocabulary of internal groups, gated per audience.

Everything that can read a claim gets that vocabulary minted into a token. Everything that cannot (GitHub teams, Slack
channels) gets it reconciled into a membership by a controller that reads the same file. Nothing here authenticates
anyone: sign-in, passwords and MFA stay with the corporate directory. sluis has no database of record and no users of its
own. It runs as one process ([ADR 0037](docs/decisions/0037-one-process-everywhere.md)).
Why it exists and how it differs from an identity product: [why sluis](docs/explanation/why.md).

## Who it is for

Platform teams that run a corporate directory and want one reviewed policy file to drive tokens, GitHub teams and Slack
channels. It assumes AWS (Lambda, or Kubernetes with AWS storage) or an older Kubernetes install on the legacy store.
It installs no identity store, no passwords and no MFA: those stay with the directory.

## The model

- **Directory**: where people and their groups come from.
- **Policy**: the file in git that maps directory groups and machine identities to internal groups, per audience.
- **Issuer and controllers**: one process that mints tokens and reconciles GitHub and Slack from the same file.

| Shape | What runs | Start with |
|---|---|---|
| AWS Lambda | one function, one role; DynamoDB state, SSM or OpenBao secrets, S3 blobs, KMS-wrapped signing | [AWS Lambda](docs/getting-started/aws-lambda.md) |
| Kubernetes on AWS (`k8s-aws`) | one Deployment, one Pod Identity role; the same AWS adapters | [Kubernetes on AWS](docs/getting-started/kubernetes-aws.md) |
| Kubernetes, legacy store | one Deployment; state in Kubernetes objects and Valkey (older installations) | [legacy store](docs/getting-started/kubernetes-legacy-store.md) |

The presets `server`, `k8s-minimal` and `k8s-openbao` are unavailable: they name adapters that are planned and not built
([adapters](docs/reference/adapters.md)). To choose between the shapes, read [getting started](docs/getting-started/README.md).

## Install and a worked example

Render the two documents from an installation file, then install the chart with them ([tutorial](docs/getting-started/kubernetes-aws.md)):

```sh
sluisctl render --installation installation.yaml --out rendered/
helm install sluis oci://ghcr.io/truvity/charts/sluis \
  --version X.Y.Z --namespace sluis --create-namespace \
  --values values.yaml \
  --set-file documents.service=rendered/sluis.yaml --set-file documents.policy=rendered/policy.yaml
```

A minimal `installation.yaml` and every key are in [the installation document](docs/reference/installation-document.md).

## Consumers

The `sluis` chart installs from an estate's GitOps repository and a second, non-AWS estate. The Go module is imported by an estate's GitOps repository
(`policy`, in its render tests) and by `truvity/gemaal` (`identity`). CI workflows use `sluisctl` and the GitHub Action
`truvity/sluis`; developers use `sluisctl` to mint credentials locally. The sluis service is a token audience for
`truvity/cloudflare` (r2broker) and `truvity/observability` (vmauth).

## Neighbours

- **openbao**: sluis mints tokens; openbao trusts them and issues certificates
  ([integration](https://github.com/truvity/openbao/blob/master/docs/integrations/sluis.md)).
- **audit**: every decision, sign-in, refusal and console action is one record, written by the issuer and the controllers.
- **workstation**: `sluisctl` and `awsctl` both mint AWS credentials on a laptop; `sluisctl` is the estate path.

## Documentation

[docs/index.md](docs/index.md) is the one entry point, organised by what you are trying to do: tutorials
(`docs/getting-started/`), tasks (`docs/how-to/`), reference (`docs/reference/`), explanation (`docs/explanation/`) and
decisions (`docs/decisions/`). Upgrading is `docs/how-to/upgrade/`: [v1.62](docs/how-to/upgrade/v1.62.md),
[v1.63](docs/how-to/upgrade/v1.63.md), [v1.64](docs/how-to/upgrade/v1.64.md). [CHANGELOG.md](CHANGELOG.md) says what
changed, per version; the steps are in the upgrade pages. What is refused and why: [safety](docs/explanation/safety.md).
OpenID conformance results: [conformance](docs/explanation/conformance-findings.md).

## What ships

Every artifact is stamped by one tag, `vX.Y.Z`; pin one version of this repository.

| Artifact | Published at | For |
|---|---|---|
| `sluis` chart and image | `oci://ghcr.io/truvity/charts/sluis`, `ghcr.io/truvity/sluis/sluis` | the installation on Kubernetes: one image, one chart, one Deployment (`sluis serve`, with the GitHub and Slack controllers as loops inside it, named in `config.controllers`). Chart values: [chart README](charts/sluis/README.md) |
| `resource-proxy` image | `ghcr.io/truvity/sluis/resource-proxy` | the resource-server sidecar a consumer's chart names beside the container it fronts |
| `sluis-lambda_<version>_linux_arm64.zip` | the release's assets | the AWS Lambda function (arm64): the release zip, deployed unchanged by the Pulumi library |
| `sluis_<version>_<os>_<arch>` archives | the release's assets | the `sluis` binary (`serve`, `migrate`), the `resource-proxy` binary and the `sluis-acceptance` test binary |
| `sluisctl` | the release's archives (`sluisctl_<version>_<os>_<arch>`), and a Nix flake on every release | people on laptops and CI jobs: sign in, then kubeconfigs, AWS credentials, tokens for any audience, `render` and `policy render` |
| `accessctl_<version>_<os>_<arch>` | the release's assets | `sluisctl` under its old name, deprecated |
| `sluis-config-schemas_<version>.tar.gz` | the release's assets | the JSON Schemas of the service, policy and installation documents |
| `sluis-audit-catalogue_<version>.tar.gz` | the release's assets | the audit catalogue `roster.yaml` with every schema it references: the audit writer refuses to start without them |
| `checksums.txt` | the release's assets | SHA-256 of every archive above |
| Pulumi library | `github.com/truvity/sluis/deploy/pulumi` (tag `deploy/pulumi/vX.Y.Z`) | AWS infrastructure and the Lambda: [Pulumi library](docs/reference/pulumi-library.md) |
| Go module | `github.com/truvity/sluis` | services and consoles in Go: verify a bearer, read the caller's groups ([Go module](docs/reference/go-module.md)) |
| TypeScript package | `@truvity/sluis` on GitHub Packages | console UIs: `useIdentity()` over `/.access/whoami`; Node services: verify a bearer |
| GitHub Action | `truvity/sluis@<commit>` | workflows: one exchange, then a kubeconfig, AWS profiles, or a GitHub App token |
| an Entra directory backend | none | planned: a second corporate directory behind the same workspace record |

## The rule that makes this repository public

**Mechanism only.** Nothing here names an account, a zone, a hostname, a cluster, an issuer or a secret path of any
installation: every such thing is a value with a neutral example (`example.com`, `acme`, `globex`), and the installation
supplies it from its own repository. The rule covers code, docs, the CHANGELOG, tests, commit messages and pull request
text, and [`hack/leak-canary.sh`](hack/leak-canary.sh) enforces it in `just check` and in CI. This repository follows the
shared [component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md).

## Status

Used in production by its maintainers; [releases](https://github.com/truvity/sluis/releases). Presets `server`, `k8s-minimal`
and `k8s-openbao` are unavailable.

## Development

```sh
devbox shell        # or direnv: Go, buf, golangci-lint, helm, just, lefthook
just check          # build, test, lint, chart-lint, telemetry, archive-check, docs-check, leak-canary, audit-catalogue, ts
just vuln           # govulncheck; separate from check, a new CVE must not turn it red
just generate       # proto to gen/ after a contract change; the generated code is committed
```

[CONTRIBUTING.md](CONTRIBUTING.md) has the conventions and the console's build order.

## Releasing

Push a tag `vX.Y.Z`: the release workflow publishes the images, the chart, `sluisctl` and its Nix flake, the TypeScript
package, the Go module and the Action at that version. The Pulumi library's tag is cut by the release itself, with its
`require` pinned to the release. Auto-release cuts patch tags when changes merge to master; a minor needs its
`## vX.Y.0` CHANGELOG heading and is tagged by hand.

## Licence

MIT, see [LICENSE](LICENSE).
